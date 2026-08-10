package main

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// Reserva de cota e pacing (BAN-06, BAN-07, BAN-10).
//
// Esta é a seção crítica do governor: o ponto onde "a instância pode enviar
// agora?" vira uma decisão que duas requisições concorrentes não conseguem
// responder "sim" as duas. A prova disso é o UPDATE condicional — não um mutex
// em Go, que não vale entre réplicas do container.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func saoPauloOrSkip(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("fuso America/Sao_Paulo indisponível: %v", err)
	}
	return loc
}

// newTestGovernor devolve um governor cujo relógio é a variável apontada por at:
// mover o tempo num teste é reatribuir *at, nunca dormir.
func newTestGovernor(db *sqlx.DB, at *time.Time) *SendGovernor {
	g := NewSendGovernor(db, GovernorDefaults{})
	g.now = func() time.Time { return *at }
	return g
}

func insertTestUser(t *testing.T, db *sqlx.DB, userID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO users (id, name, token) VALUES (?, ?, ?)`,
		userID, "instancia", "tok-"+userID); err != nil {
		t.Fatalf("insert de usuário falhou: %v", err)
	}
}

// setReserveState prepara o estado de cota/pacing de uma instância. Timestamps
// nil viram NULL (nunca enviou / nunca teve janela agendada).
func setReserveState(t *testing.T, db *sqlx.DB, userID string, sentToday int, quotaResetAt, lastSendAt *time.Time) {
	t.Helper()
	var reset, last interface{}
	if quotaResetAt != nil {
		reset = quotaResetAt.UTC()
	}
	if lastSendAt != nil {
		last = lastSendAt.UTC()
	}
	if _, err := db.Exec(
		`UPDATE users SET sent_today = ?, quota_reset_at = ?, last_send_at = ? WHERE id = ?`,
		sentToday, reset, last, userID); err != nil {
		t.Fatalf("preparação do estado falhou: %v", err)
	}
}

func readReserveState(t *testing.T, db *sqlx.DB, userID string) (sentToday int, quotaResetAt, lastSendAt *time.Time) {
	t.Helper()
	var row struct {
		SentToday    int        `db:"sent_today"`
		QuotaResetAt *time.Time `db:"quota_reset_at"`
		LastSendAt   *time.Time `db:"last_send_at"`
	}
	if err := db.Get(&row,
		`SELECT sent_today, quota_reset_at, last_send_at FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("leitura do estado falhou: %v", err)
	}
	return row.SentToday, row.QuotaResetAt, row.LastSendAt
}

// ---------------------------------------------------------------------------
// Caminho feliz
// ---------------------------------------------------------------------------

// Primeiro envio de uma instância: o contador sobe, o instante é carimbado e a
// virada do dia é agendada para a meia-noite do FUSO DA INSTÂNCIA. Sem esse
// agendamento inicial (quota_reset_at nasce NULL) a cota nunca viraria.
func TestReserveFirstSendStampsCounterAndSchedulesReset(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC) // 11:00 em São Paulo
	g := newTestGovernor(s.db, &now)

	if gerr := g.reserve("u1", 10, 45*time.Second, sp); gerr != nil {
		t.Fatalf("primeira reserva recusada: %v", gerr)
	}

	sent, reset, last := readReserveState(t, s.db, "u1")
	if sent != 1 {
		t.Errorf("sent_today = %d; quero 1", sent)
	}
	if last == nil || !last.Equal(now) {
		t.Errorf("last_send_at = %v; quero %v (carimbado na mesma instrução do incremento)", last, now)
	}
	// Meia-noite de 11/08 em São Paulo (UTC-3) = 03:00Z do dia 11.
	wantReset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	if reset == nil || !reset.Equal(wantReset) {
		t.Errorf("quota_reset_at = %v; quero %v (próxima meia-noite no fuso da instância)", reset, wantReset)
	}
}

// Envio seguinte, já respeitado o intervalo: incrementa sem remarcar a virada.
// Remarcar aqui empurraria a virada para sempre e a cota nunca zeraria.
func TestReserveSecondSendIncrementsWithoutReschedulingReset(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)

	if gerr := g.reserve("u1", 10, 45*time.Second, sp); gerr != nil {
		t.Fatalf("primeira reserva recusada: %v", gerr)
	}
	_, firstReset, _ := readReserveState(t, s.db, "u1")

	now = now.Add(60 * time.Second) // além do intervalo mínimo
	if gerr := g.reserve("u1", 10, 45*time.Second, sp); gerr != nil {
		t.Fatalf("segunda reserva recusada: %v", gerr)
	}

	sent, reset, last := readReserveState(t, s.db, "u1")
	if sent != 2 {
		t.Errorf("sent_today = %d; quero 2", sent)
	}
	if reset == nil || firstReset == nil || !reset.Equal(*firstReset) {
		t.Errorf("quota_reset_at = %v; quero %v inalterado", reset, firstReset)
	}
	if last == nil || !last.Equal(now) {
		t.Errorf("last_send_at = %v; quero %v", last, now)
	}
}

// Edge case da spec: "o relógio vira o dia no meio de uma campanha". A cota
// zera e a campanha continua — sem isso a instância ficaria travada até alguém
// mexer no banco.
func TestReserveExpiredWindowResetsCounterAndReschedules(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	expired := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC) // meia-noite de SP já passada
	setReserveState(t, s.db, "u1", 10, &expired, nil)
	g := newTestGovernor(s.db, &now)

	if gerr := g.reserve("u1", 10, 45*time.Second, sp); gerr != nil {
		t.Fatalf("reserva recusada com a janela de cota vencida: %v", gerr)
	}

	sent, reset, _ := readReserveState(t, s.db, "u1")
	// 1, não 0: esta requisição já consumiu a primeira unidade do novo dia.
	if sent != 1 {
		t.Errorf("sent_today = %d após a virada; quero 1", sent)
	}
	wantReset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	if reset == nil || !reset.Equal(wantReset) {
		t.Errorf("quota_reset_at = %v; quero %v (reagendado para a próxima virada)", reset, wantReset)
	}
}

// ---------------------------------------------------------------------------
// Recusas
// ---------------------------------------------------------------------------

// BAN-06 / AC-1: cota esgotada devolve 429 com Retry-After até a virada do dia.
// O cliente usa esse número para reagendar a campanha — se viesse o intervalo de
// pacing, ele voltaria em 45 s e tomaria 429 até meia-noite.
func TestReserveQuotaExceededReturns429WithRetryAfterUntilMidnight(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC) // 13h à frente
	old := now.Add(-10 * time.Minute)                     // pacing folgado: só a cota barra
	setReserveState(t, s.db, "u1", 10, &reset, &old)
	g := newTestGovernor(s.db, &now)

	gerr := g.reserve("u1", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita com a cota esgotada; quero 429")
	}
	if gerr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; quero 429", gerr.Status)
	}
	if gerr.Code != CodeQuotaExceeded {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeQuotaExceeded)
	}
	if want := 13 * time.Hour; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (segundos até a virada)", gerr.RetryAfter, want)
	}
	// Uma recusa não pode consumir cota nem mover o relógio de pacing.
	sent, _, last := readReserveState(t, s.db, "u1")
	if sent != 10 {
		t.Errorf("sent_today = %d após a recusa; quero 10 inalterado", sent)
	}
	if last == nil || !last.Equal(old) {
		t.Errorf("last_send_at = %v após a recusa; quero %v inalterado", last, old)
	}
}

// BAN-07 / AC-2: envio rápido demais devolve 429 com o que falta do intervalo.
func TestReservePacingViolatedReturns429WithRemainingInterval(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	last := now.Add(-20 * time.Second) // faltam 25s dos 45s
	setReserveState(t, s.db, "u1", 1, &reset, &last)
	g := newTestGovernor(s.db, &now)

	gerr := g.reserve("u1", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita 20s após o envio anterior; quero 429")
	}
	if gerr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; quero 429", gerr.Status)
	}
	if gerr.Code != CodePacingViolated {
		t.Errorf("code = %q; quero %q", gerr.Code, CodePacingViolated)
	}
	if want := 25 * time.Second; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (o que falta do intervalo)", gerr.RetryAfter, want)
	}
	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 1 {
		t.Errorf("sent_today = %d após a recusa; quero 1 inalterado", sent)
	}
}

// Quando cota e pacing barram juntos, a resposta é a cota: esperar 45 s não
// resolve um limite que só vira à meia-noite, e o cliente reagendaria errado.
func TestReserveQuotaOutranksPacingWhenBothBlock(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	last := now.Add(-1 * time.Second)
	setReserveState(t, s.db, "u1", 10, &reset, &last)
	g := newTestGovernor(s.db, &now)

	gerr := g.reserve("u1", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita com cota e pacing violados; quero 429")
	}
	if gerr.Code != CodeQuotaExceeded {
		t.Errorf("code = %q; quero %q (a cota é o limite mais distante)", gerr.Code, CodeQuotaExceeded)
	}
	if want := 13 * time.Hour; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v", gerr.RetryAfter, want)
	}
}

// AD-001: todo 429 carrega Retry-After utilizável. Um resto de intervalo de
// 100 ms não pode virar "0" — isso convidaria o cliente a repetir na hora e
// tomar outro 429, em loop.
func TestReserveRetryAfterNeverDropsBelowOneSecond(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	last := now.Add(-44900 * time.Millisecond) // faltam 100ms
	setReserveState(t, s.db, "u1", 1, &reset, &last)
	g := newTestGovernor(s.db, &now)

	gerr := g.reserve("u1", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita 100ms antes do intervalo; quero 429")
	}
	if gerr.RetryAfter != time.Second {
		t.Errorf("RetryAfter = %v para 100ms restantes; quero 1s", gerr.RetryAfter)
	}
}

// ---------------------------------------------------------------------------
// Fuso horário (Risks & Concerns: "relógio do servidor")
// ---------------------------------------------------------------------------

// O SQLite compara TIMESTAMP como texto e o driver serializa time.Time
// preservando o offset. A virada agendada é calculada no fuso da instância, então
// se ela fosse gravada como "2026-08-11T00:00:00-03:00" compararia MENOR que
// "2026-08-11T02:00:00Z" (23:00 em SP) e a cota zeraria três horas cedo — mais um
// dia inteiro de envios, silenciosamente.
//
// O agendamento aqui vem do próprio reserve, não de estado montado à mão: é o
// valor que produção grava que precisa ser comparável.
func TestReserveDoesNotResetQuotaBeforeLocalMidnight(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC) // 11:00 em São Paulo
	g := newTestGovernor(s.db, &now)

	// Caminho real: esta reserva agenda a virada para a meia-noite de SP.
	if gerr := g.reserve("u1", 10, 0, sp); gerr != nil {
		t.Fatalf("primeira reserva recusada: %v", gerr)
	}
	// Esgota a cota sem tocar no agendamento gravado acima.
	if _, err := s.db.Exec(`UPDATE users SET sent_today = 10 WHERE id = ?`, "u1"); err != nil {
		t.Fatalf("preparação da cota esgotada falhou: %v", err)
	}

	// 23:00 em São Paulo: falta 1h para a virada.
	now = time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	gerr := g.reserve("u1", 10, 0, sp)

	if gerr == nil {
		t.Fatal("reserva aceita às 23:00 de SP com a cota esgotada; a virada só acontece à meia-noite local")
	}
	if gerr.Code != CodeQuotaExceeded {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeQuotaExceeded)
	}
	if want := time.Hour; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (falta 1h para a meia-noite local)", gerr.RetryAfter, want)
	}
	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 10 {
		t.Errorf("sent_today = %d; quero 10 — a cota não pode zerar antes da virada local", sent)
	}
}

// O outro lado do mesmo risco: em produção o relógio é local (UTC-3), enquanto
// last_send_at pode ter sido gravado em UTC por outro caminho. Se o governor
// comparasse com o horário de parede, "2026-08-10T14:00:00Z" nunca seria menor
// que um prazo escrito como "-03:00" e o pacing recusaria para sempre — a
// instância travaria sem nenhum limite ter sido atingido.
func TestReserveHonorsElapsedIntervalWhenClockIsNotUTC(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	instant := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	lastSend := instant.Add(-10 * time.Minute) // muito além dos 45s
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	setReserveState(t, s.db, "u1", 1, &reset, &lastSend)

	now := instant.In(sp) // mesmo instante, offset -03:00
	g := newTestGovernor(s.db, &now)

	if gerr := g.reserve("u1", 10, 45*time.Second, sp); gerr != nil {
		t.Fatalf("reserva recusada 10 min após o envio anterior: %v — o intervalo já passou", gerr)
	}

	sent, _, last := readReserveState(t, s.db, "u1")
	if sent != 2 {
		t.Errorf("sent_today = %d; quero 2", sent)
	}
	if last == nil || !last.Equal(instant) {
		t.Errorf("last_send_at = %v; quero %v (mesmo instante, gravado em UTC)", last, instant)
	}
}

// ---------------------------------------------------------------------------
// AD-002: fail-closed
// ---------------------------------------------------------------------------

// Banco fora não pode virar "pode enviar". Fail-open aqui converteria uma falha
// de infra numa rajada sem limite — o cenário que o governor existe para impedir.
func TestReserveFailsClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)
	if err := s.db.Close(); err != nil {
		t.Fatalf("não consegui fechar o banco: %v", err)
	}

	gerr := g.reserve("u1", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita com o banco fechado; quero 503 (fail-closed)")
	}
	if gerr.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d; quero 503", gerr.Status)
	}
	if gerr.Code != CodeGovernorUnavailable {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeGovernorUnavailable)
	}
}

// Instância que não existe é estado que o governor não consegue avaliar — a
// resposta segura é recusar, não deixar passar por falta de linha.
func TestReserveFailsClosedForUnknownInstance(t *testing.T) {
	s := makeTestServer(t)
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)

	gerr := g.reserve("nao-existe", 10, 45*time.Second, sp)

	if gerr == nil {
		t.Fatal("reserva aceita para instância inexistente; quero 503 (fail-closed)")
	}
	if gerr.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d; quero 503", gerr.Status)
	}
	if gerr.Code != CodeGovernorUnavailable {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeGovernorUnavailable)
	}
}

// O UPDATE pode falhar por uma corrida que o SELECT de diagnóstico não vê mais
// (outra requisição já mudou o estado). Ainda assim a recusa precisa ser
// acionável: 429 com Retry-After, nunca nil (que liberaria o envio).
func TestDiagnoseReserveAlwaysReturnsRetryableRefusal(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	// Estado que NÃO explica a recusa: cota sobrando e pacing folgado.
	setReserveState(t, s.db, "u1", 1, &reset, &old)
	g := newTestGovernor(s.db, &now)

	gerr := g.diagnoseReserve("u1", 10, 45*time.Second, now)

	if gerr == nil {
		t.Fatal("diagnóstico devolveu nil; quero uma recusa — nil aqui liberaria um envio que o UPDATE barrou")
	}
	if gerr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; quero 429", gerr.Status)
	}
	if gerr.RetryAfter < time.Second {
		t.Errorf("RetryAfter = %v; quero pelo menos 1s", gerr.RetryAfter)
	}
}

// ---------------------------------------------------------------------------
// BAN-10 / AC-5: atomicidade
// ---------------------------------------------------------------------------

// makeSharedMemoryDB abre um SQLite em memória com cache compartilhado. O
// ":memory:" puro do makeTestServer dá um banco VAZIO a cada conexão nova do
// pool, então não serve para concorrência real — aqui as goroutines precisam
// disputar a MESMA linha por conexões distintas, como réplicas do container.
func makeSharedMemoryDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("abertura do banco compartilhado falhou: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initializeSchema(db); err != nil {
		t.Fatalf("schema falhou: %v", err)
	}
	return db
}

// O AC que justifica o feature inteiro: 50 requisições simultâneas contra uma
// cota de 10 deixam passar exatamente 10. Um read-modify-write em duas
// instruções passaria mais — é isso que o UPDATE condicional impede.
func TestReserveConcurrentCallersNeverExceedQuota(t *testing.T) {
	db := makeSharedMemoryDB(t)
	insertTestUser(t, db, "u1")
	sp := saoPauloOrSkip(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(db, &now)

	const callers, quota = 50, 10
	// minInterval zero isola a cota: com pacing ativo, só uma passaria por
	// definição e o teste não provaria nada sobre o contador.
	results := make(chan *GateError, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- g.reserve("u1", quota, 0, sp)
		}()
	}
	wg.Wait()
	close(results)

	granted, refused := 0, 0
	for gerr := range results {
		if gerr == nil {
			granted++
			continue
		}
		refused++
		if gerr.Status != http.StatusTooManyRequests {
			t.Errorf("recusa com status %d; quero 429", gerr.Status)
		}
		if gerr.Code != CodeQuotaExceeded {
			t.Errorf("recusa com code %q; quero %q", gerr.Code, CodeQuotaExceeded)
		}
	}

	if granted != quota {
		t.Errorf("%d reservas aceitas; quero exatamente %d", granted, quota)
	}
	if refused != callers-quota {
		t.Errorf("%d reservas recusadas; quero %d", refused, callers-quota)
	}
	sent, _, _ := readReserveState(t, db, "u1")
	if sent != quota {
		t.Errorf("sent_today = %d; quero %d — o contador não pode passar da cota", sent, quota)
	}
}
