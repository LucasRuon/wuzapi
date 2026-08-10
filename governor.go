package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

// Governor: guardrails anti-ban por instância.
//
// O gateway é o único ponto por onde todo consumidor passa, então é aqui que o
// limite vira garantia em vez de convenção. Este arquivo define os tipos base —
// contrato de erro, tipo de envio e configuração — e a reserva de cota, que é a
// seção crítica do feature. A composição dos gates (Acquire) vem nas tarefas
// seguintes.

// ---------------------------------------------------------------------------
// Tipo de envio
// ---------------------------------------------------------------------------

// SendKind determina quais gates se aplicam a uma requisição de envio.
type SendKind int

const (
	// KindOutbound é uma mensagem nova para um contato: passa por todos os gates.
	KindOutbound SendKind = iota
	// KindEdit edita uma mensagem já enviada. Não é contato novo, então não
	// consome cota nem passa por supressão ou pré-flight — só pelo estado de ban.
	KindEdit
	// KindGroup tem um grupo como alvo. Não há opt-out individual nem
	// IsOnWhatsApp para um grupo, mas cota e ritmo continuam valendo.
	KindGroup
)

// ---------------------------------------------------------------------------
// Contrato de erro (AD-001)
// ---------------------------------------------------------------------------

// Códigos de recusa. O consumidor reage a cada um de forma diferente, então eles
// são parte do contrato público da API — não mude sem versionar.
const (
	// 423 — a instância está travada; o cliente deve pausar a campanha.
	CodeInstanceBanned = "instance_banned"
	// 429 — rápido/volumoso demais agora; o cliente deve esperar e repetir o
	// MESMO destinatário.
	CodeQuotaExceeded  = "quota_exceeded"
	CodePacingViolated = "pacing_violated"
	CodeWindowClosed   = "window_closed"
	// 422 — este destinatário é inválido; o cliente deve pular e seguir.
	CodeRecipientSuppressed    = "recipient_suppressed"
	CodeRecipientNotOnWhatsApp = "recipient_not_on_whatsapp"
	// 503 — o guardrail não conseguiu decidir. Fail-closed (AD-002).
	CodeGovernorUnavailable = "governor_unavailable"
)

// GateError é a recusa de um gate, com tudo que o cliente precisa para reagir.
type GateError struct {
	Status     int           // 423 | 429 | 422 | 503
	Code       string        // um dos Code* acima
	Reason     string        // legível, para log e UI
	RetryAfter time.Duration // > 0 apenas em 429
	Until      *time.Time    // preenchido apenas em 423
	BanCode    int           // TempBanReason/ConnectFailureReason, apenas em 423
}

func (e *GateError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Reason)
	}
	return e.Code
}

// WriteTo emite a recusa como JSON.
//
// SPEC_DEVIATION: a spec (BAN-02 AC-2) pedia o código do ban no campo "code" do
// corpo. O envelope de s.Respond já usa "code" para o status HTTP em TODA
// resposta da API, então o ban vai em "banCode". Manter "code" = status HTTP
// preserva a compatibilidade com clientes existentes (o broadcast-app lê
// payload.error e payload.success), que quebrariam se o significado do campo
// mudasse só nestas rotas.
// Reason: colisão de nome com um invariante pré-existente do envelope da API.
func (e *GateError) WriteTo(w http.ResponseWriter) {
	if e.Status == http.StatusTooManyRequests && e.RetryAfter > 0 {
		// RFC 9110: Retry-After em segundos. Arredonda para cima — devolver 0
		// convidaria o cliente a repetir imediatamente e levar outro 429.
		secs := int(math.Ceil(e.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)

	body := map[string]interface{}{
		"code":    e.Status,
		"success": false,
		"error":   e.Code,
		"reason":  e.Reason,
	}
	if e.RetryAfter > 0 {
		body["retryAfter"] = int(math.Ceil(e.RetryAfter.Seconds()))
	}
	if e.Until != nil {
		body["until"] = e.Until.UTC().Format(time.RFC3339)
	}
	if e.BanCode != 0 {
		body["banCode"] = e.BanCode
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("governor: falha ao escrever resposta de recusa")
	}
}

// ---------------------------------------------------------------------------
// Configuração
// ---------------------------------------------------------------------------

// Invariantes de segurança (AD-003). Configuração só pode ficar MAIS restritiva:
// o intervalo mínimo pode subir, a cota pode descer. O contrário não é
// configuração, é desligar o guardrail.
const (
	minIntervalFloor     = 45 * time.Second
	maxDailyQuotaCeiling = 200
)

// RampStep é um degrau da rampa de aquecimento: a partir de FromDay dias desde o
// pareamento, a instância pode enviar Quota mensagens por dia.
type RampStep struct {
	FromDay int
	Quota   int
}

// GovernorDefaults é a configuração global do governor: o que vale para uma
// instância que não sobrescreveu nada.
type GovernorDefaults struct {
	MaxDailyQuota int
	MinInterval   time.Duration
	WindowStart   string // "09:00"
	WindowEnd     string // "20:00"
	Timezone      string // "America/Sao_Paulo"
	SkipSunday    bool
	WarmupRamp    []RampStep
}

// NewGovernorDefaults monta a configuração a partir das flags e do ambiente,
// aplicando os clamps de AD-003. Os clamps ficam AQUI, no construtor, e não no
// ponto de uso: assim não existe caminho de código que produza defaults fora do
// invariante.
func NewGovernorDefaults() GovernorDefaults {
	d := GovernorDefaults{
		MaxDailyQuota: *maxDailyQuota,
		MinInterval:   time.Duration(*minIntervalMs) * time.Millisecond,
		WindowStart:   *windowStart,
		WindowEnd:     *windowEnd,
		Timezone:      *sendTimezone,
		SkipSunday:    true,
		WarmupRamp: []RampStep{
			{FromDay: 0, Quota: 30},
			{FromDay: 2, Quota: 60},
			{FromDay: 4, Quota: 100},
			{FromDay: 7, Quota: 150},
			{FromDay: 14, Quota: 200},
		},
	}

	if d.MaxDailyQuota <= 0 || d.MaxDailyQuota > maxDailyQuotaCeiling {
		d.MaxDailyQuota = maxDailyQuotaCeiling
	}
	if d.MinInterval < minIntervalFloor {
		d.MinInterval = minIntervalFloor
	}
	if d.WindowStart == "" {
		d.WindowStart = "09:00"
	}
	if d.WindowEnd == "" {
		d.WindowEnd = "20:00"
	}
	if d.Timezone == "" {
		d.Timezone = "America/Sao_Paulo"
	}
	return d
}

// ---------------------------------------------------------------------------
// SendGovernor
// ---------------------------------------------------------------------------

// SendGovernor decide se um envio pode sair agora e reserva a cota quando pode.
type SendGovernor struct {
	db       *sqlx.DB
	defaults GovernorDefaults
	// now é injetável: a janela horária e a virada de cota são decisões sobre o
	// relógio, e teste que depende do relógio de parede não é teste.
	now func() time.Time
}

func NewSendGovernor(db *sqlx.DB, defaults GovernorDefaults) *SendGovernor {
	return &SendGovernor{db: db, defaults: defaults, now: time.Now}
}

// reserve consome uma unidade da cota diária de userID, se houver — e devolve
// nil quando o envio está liberado.
//
// O caminho feliz é um único UPDATE condicional. Mutex em Go não serviria: não
// protege entre réplicas do container, e a corrida que importa é justamente a de
// duas requisições concorrentes verem o mesmo sent_today.
//
// Todo timestamp vai e volta em UTC. O SQLite compara TIMESTAMP como texto e o
// driver serializa time.Time preservando o offset, então misturar fusos faria
// "2026-08-11T00:00-03:00" comparar menor que "2026-08-11T02:00Z" — a mesma
// virada de dia acontecendo três horas cedo.
func (g *SendGovernor) reserve(userID string, quota int, minInterval time.Duration, loc *time.Location) *GateError {
	now := g.now().UTC()
	pacingDeadline := now.Add(-minInterval)
	nextReset := nextMidnight(g.now().In(loc)).UTC()

	// quota_reset_at IS NULL é a instância que nunca enviou: conta como janela
	// vencida, senão a virada nunca seria agendada e a cota nunca zeraria.
	query := g.db.Rebind(`
        UPDATE users SET
            sent_today     = CASE WHEN quota_reset_at IS NULL OR quota_reset_at <= ? THEN 1 ELSE sent_today + 1 END,
            quota_reset_at = CASE WHEN quota_reset_at IS NULL OR quota_reset_at <= ? THEN ? ELSE quota_reset_at END,
            last_send_at   = ?
          WHERE id = ?
            AND (quota_reset_at IS NULL OR quota_reset_at <= ? OR sent_today < ?)
            AND (last_send_at IS NULL OR last_send_at <= ?)`)

	res, err := g.db.Exec(query, now, now, nextReset, now, userID, now, quota, pacingDeadline)
	if err != nil {
		return governorUnavailable(userID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return governorUnavailable(userID, err)
	}
	if affected > 0 {
		return nil
	}
	// Cota ou pacing barrou. Só agora vale um SELECT para descobrir qual — o
	// caminho feliz não paga por essa consulta.
	return g.diagnoseReserve(userID, quota, minInterval, now)
}

// diagnoseReserve descobre por que o UPDATE não afetou nenhuma linha e monta a
// recusa com o Retry-After que o cliente precisa. Nunca devolve nil: o UPDATE já
// barrou o envio, e devolver nil aqui o liberaria.
func (g *SendGovernor) diagnoseReserve(userID string, quota int, minInterval time.Duration, now time.Time) *GateError {
	var row struct {
		SentToday    int        `db:"sent_today"`
		QuotaResetAt *time.Time `db:"quota_reset_at"`
		LastSendAt   *time.Time `db:"last_send_at"`
	}
	query := g.db.Rebind(`SELECT sent_today, quota_reset_at, last_send_at FROM users WHERE id = ?`)
	if err := g.db.Get(&row, query, userID); err != nil {
		// Inclui a instância inexistente: estado que não dá para avaliar é
		// recusa, não liberação (AD-002).
		return governorUnavailable(userID, err)
	}

	// Cota antes de pacing: quando os dois barram, esperar 45 s não resolve um
	// limite que só vira à meia-noite — e o cliente reagendaria errado.
	if row.QuotaResetAt != nil && row.QuotaResetAt.After(now) && row.SentToday >= quota {
		return &GateError{
			Status:     http.StatusTooManyRequests,
			Code:       CodeQuotaExceeded,
			Reason:     "cota diária de envios esgotada",
			RetryAfter: clampRetryAfter(row.QuotaResetAt.Sub(now)),
		}
	}
	if row.LastSendAt != nil {
		if remaining := minInterval - now.Sub(row.LastSendAt.UTC()); remaining > 0 {
			return &GateError{
				Status:     http.StatusTooManyRequests,
				Code:       CodePacingViolated,
				Reason:     "aguarde o intervalo mínimo entre envios",
				RetryAfter: clampRetryAfter(remaining),
			}
		}
	}
	// O estado mudou entre o UPDATE e o SELECT: outra requisição concorrente
	// consumiu a vaga. A recusa continua valendo — o cliente repete em 1 s.
	return &GateError{
		Status:     http.StatusTooManyRequests,
		Code:       CodePacingViolated,
		Reason:     "envio concorrente em andamento",
		RetryAfter: time.Second,
	}
}

// windowGate recusa envios fora do horário local da instância — nil quando a
// janela está aberta.
//
// Disparo de madrugada é assinatura de robô, e o destinatário que acorda com a
// mensagem é o que denuncia. tz, start e end vazios caem nos padrões globais
// (BAN-11); valores inválidos também, com warning: string errada no banco não
// pode derrubar envio nem processo.
func (g *SendGovernor) windowGate(now time.Time, tz, start, end string, skipSunday bool) *GateError {
	loc := g.location(tz)
	startH, startM := g.clockOrDefault(start, g.defaults.WindowStart, "09:00")
	endH, endM := g.clockOrDefault(end, g.defaults.WindowEnd, "20:00")

	local := now.In(loc)
	opensAt := time.Date(local.Year(), local.Month(), local.Day(), startH, startM, 0, 0, loc)
	closesAt := time.Date(local.Year(), local.Month(), local.Day(), endH, endM, 0, 0, loc)

	sunday := skipSunday && local.Weekday() == time.Sunday
	if !sunday && !local.Before(opensAt) && !local.After(closesAt) {
		return nil
	}

	next := nextWindowOpen(local, startH, startM, skipSunday)
	return &GateError{
		Status:     http.StatusTooManyRequests,
		Code:       CodeWindowClosed,
		Reason:     "fora da janela de envio da instância",
		RetryAfter: clampRetryAfter(next.Sub(local)),
	}
}

// nextWindowOpen é a próxima abertura a partir de local, pulando domingos.
//
// Cada candidato é construído com time.Date no fuso da instância, não somando
// 24h: numa virada de horário de verão o dia tem 23 ou 25 horas, e a aritmética
// de duração mandaria o cliente voltar antes da abertura.
func nextWindowOpen(local time.Time, startH, startM int, skipSunday bool) time.Time {
	loc := local.Location()
	for d := 0; d <= 8; d++ {
		day := local.AddDate(0, 0, d)
		candidate := time.Date(day.Year(), day.Month(), day.Day(), startH, startM, 0, 0, loc)
		if skipSunday && candidate.Weekday() == time.Sunday {
			continue
		}
		if !candidate.Before(local) {
			return candidate
		}
	}
	return local
}

// location resolve o fuso da instância. Nunca time.Local: esse seria o fuso do
// container, que não tem relação com o horário de quem recebe a mensagem.
func (g *SendGovernor) location(tz string) *time.Location {
	for _, name := range []string{tz, g.defaults.Timezone} {
		if name == "" {
			continue
		}
		loc, err := time.LoadLocation(name)
		if err == nil {
			return loc
		}
		log.Warn().Str("timezone", name).Err(err).Msg("governor: fuso inválido, caindo no padrão")
	}
	return time.UTC
}

// clockOrDefault interpreta "HH:MM", caindo no valor da instância, depois no
// global, depois no embutido. Um horário mal formado NÃO pode virar 00:00 —
// isso abriria a madrugada inteira, o oposto do que a janela existe para fazer.
func (g *SendGovernor) clockOrDefault(values ...string) (hour, minute int) {
	for _, v := range values {
		if v == "" {
			continue
		}
		h, m, err := parseClock(v)
		if err == nil {
			return h, m
		}
		log.Warn().Str("clock", v).Msg("governor: horário de janela inválido, caindo no padrão")
	}
	return 0, 0
}

func parseClock(v string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// sendEventRetention é por quanto tempo send_events serve para alguma coisa: as
// janelas que a consultam são de 24h. Sem poda a tabela cresce para sempre.
const sendEventRetention = 7 * 24 * time.Hour

func (g *SendGovernor) pruneSendEvents(now time.Time) error {
	query := g.db.Rebind(`DELETE FROM send_events WHERE created_at < ?`)
	_, err := g.db.Exec(query, now.UTC().Add(-sendEventRetention))
	return err
}

// startSendEventPruner poda no boot e a cada 6h. A poda do boot é síncrona: um
// gateway que ficou meses parado não pode voltar carregando a tabela inteira até
// o primeiro tick.
func (g *SendGovernor) startSendEventPruner() {
	if err := g.pruneSendEvents(g.now()); err != nil {
		log.Error().Err(err).Msg("governor: poda de send_events no boot falhou")
	}
	safeGo("send-events-pruner", func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := g.pruneSendEvents(g.now()); err != nil {
				log.Error().Err(err).Msg("governor: poda periódica de send_events falhou")
			}
		}
	})
}

// effectiveQuota é a cota do dia: o menor valor entre o limite configurado e o
// degrau da rampa de aquecimento correspondente à idade do pareamento.
//
// A rampa existe porque número novo disparando volume de número velho é o padrão
// que o WhatsApp caça. Ela nunca ELEVA a cota — só limita mais, então configurar
// 200 num pareamento de ontem continua rendendo 30.
//
// instanceQuota <= 0 é o sentinela de "não configurado" da coluna max_daily_quota
// (BAN-11): cai no padrão global, nunca em zero (que travaria a instância).
func (g *SendGovernor) effectiveQuota(warmupStartedAt *time.Time, instanceQuota int, now time.Time) int {
	limit := instanceQuota
	if limit <= 0 {
		limit = g.defaults.MaxDailyQuota
	}

	// Sem pareamento registrado — ou com data no futuro, que só acontece com
	// relógio desalinhado — a instância vale como recém-pareada.
	days := 0
	if warmupStartedAt != nil {
		if elapsed := now.Sub(*warmupStartedAt); elapsed > 0 {
			days = int(elapsed / (24 * time.Hour))
		}
	}

	ramp := 0
	for _, step := range g.defaults.WarmupRamp {
		if days >= step.FromDay {
			ramp = step.Quota
		}
	}

	if ramp < limit {
		return ramp
	}
	return limit
}

// nextMidnight é a próxima meia-noite no fuso de local.
func nextMidnight(local time.Time) time.Time {
	y, m, d := local.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, local.Location())
}

// clampRetryAfter garante um Retry-After utilizável. Zero convidaria o cliente a
// repetir imediatamente e tomar outra recusa, em loop.
func clampRetryAfter(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	return d
}

// governorUnavailable é a recusa de AD-002: quando a verificação que protege não
// consegue rodar, a resposta é não enviar.
func governorUnavailable(userID string, err error) *GateError {
	log.Error().Err(err).Str("userid", userID).Msg("governor: não foi possível avaliar os limites de envio")
	return &GateError{
		Status: http.StatusServiceUnavailable,
		Code:   CodeGovernorUnavailable,
		Reason: "não foi possível verificar os limites de envio",
	}
}

// applyGovernorEnvOverrides sobrescreve as flags do governor pelo ambiente,
// seguindo o mesmo padrão de SESSION_DEVICE_NAME/SESSION_PLATFORM_TYPE.
func applyGovernorEnvOverrides() {
	if v := os.Getenv("GOVERNOR_MAX_DAILY_QUOTA"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*maxDailyQuota = n
		} else {
			log.Warn().Str("value", v).Msg("GOVERNOR_MAX_DAILY_QUOTA inválido, ignorando")
		}
	}
	if v := os.Getenv("GOVERNOR_MIN_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*minIntervalMs = n
		} else {
			log.Warn().Str("value", v).Msg("GOVERNOR_MIN_INTERVAL_MS inválido, ignorando")
		}
	}
	if v := os.Getenv("GOVERNOR_WINDOW_START"); v != "" {
		*windowStart = v
	}
	if v := os.Getenv("GOVERNOR_WINDOW_END"); v != "" {
		*windowEnd = v
	}
	if v := os.Getenv("GOVERNOR_TIMEZONE"); v != "" {
		*sendTimezone = v
	}
}
