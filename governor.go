package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// Governor: guardrails anti-ban por instância.
//
// O gateway é o único ponto por onde todo consumidor passa, então é aqui que o
// limite vira garantia em vez de convenção. Este arquivo define os tipos base —
// contrato de erro, tipo de envio e configuração. A decisão em si (Acquire) vem
// nas tarefas seguintes.

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
