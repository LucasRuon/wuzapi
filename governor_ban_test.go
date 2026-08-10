package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"go.mau.fi/whatsmeow/types"
)

// Circuit breaker de banimento (BAN-02, BAN-05) e composição do Acquire.
//
// Hoje um TemporaryBan é só uma linha de log e o gateway continua disparando —
// queimando um número já marcado. O estado do ban precisa ser o PRIMEIRO gate:
// é o mais grave e o mais barato de avaliar.

// newAcquireGovernor usa os defaults reais (janela, cota, intervalo), porque o
// Acquire compõe todos eles.
func newAcquireGovernor(t *testing.T, db *sqlx.DB, at *time.Time) *SendGovernor {
	t.Helper()
	withGovernorFlags(t, 200, 45000)
	g := NewSendGovernor(db, NewGovernorDefaults())
	g.now = func() time.Time { return *at }
	return g
}

// insideWindow é uma terça-feira às 14:00 em São Paulo: janela aberta, dia útil.
func insideWindow(t *testing.T) time.Time {
	t.Helper()
	return atSaoPaulo(t, 2026, time.August, 11, 14, 0)
}

func testRecipient() types.JID {
	return types.NewJID("5541999998888", types.DefaultUserServer)
}

func readBanState(t *testing.T, db *sqlx.DB, userID string) (state string, code int, reason string, until *time.Time) {
	t.Helper()
	var row struct {
		State  string     `db:"ban_state"`
		Code   int        `db:"ban_code"`
		Reason string     `db:"ban_reason"`
		Until  *time.Time `db:"ban_until"`
	}
	if err := db.Get(&row,
		`SELECT ban_state, ban_code, ban_reason, ban_until FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("leitura do estado de ban falhou: %v", err)
	}
	return row.State, row.Code, row.Reason, row.Until
}

// ---------------------------------------------------------------------------
// MarkBanned / ClearBan
// ---------------------------------------------------------------------------

// O estado precisa sobreviver a restart: é no banco, não em memória.
func TestMarkBannedPersistsEveryField(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	until := now.Add(24 * time.Hour)

	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", until); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	state, code, reason, gotUntil := readBanState(t, s.db, "u1")
	if state != banStateTemp {
		t.Errorf("ban_state = %q; quero %q", state, banStateTemp)
	}
	if code != 101 {
		t.Errorf("ban_code = %d; quero 101", code)
	}
	if reason != "SentToTooManyPeople" {
		t.Errorf("ban_reason = %q; quero %q", reason, "SentToTooManyPeople")
	}
	if gotUntil == nil || !gotUntil.Equal(until) {
		t.Errorf("ban_until = %v; quero %v", gotUntil, until)
	}
}

// Ban permanente não tem prazo: gravar um "until" qualquer faria a limpeza
// preguiçosa liberar sozinha o que só o admin pode liberar.
func TestMarkBannedPermanentLeavesUntilNull(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)

	if err := g.MarkBanned("u1", banStatePerm, 406, "UnknownLogout", time.Time{}); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	state, _, _, until := readBanState(t, s.db, "u1")
	if state != banStatePerm {
		t.Errorf("ban_state = %q; quero %q", state, banStatePerm)
	}
	if until != nil {
		t.Errorf("ban_until = %v; quero NULL num ban permanente", until)
	}
}

// BAN-05: o clear do admin zera tudo, não só o estado — um ban_code órfão
// apareceria no health como se a instância ainda estivesse marcada.
func TestClearBanResetsEveryField(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", now.Add(time.Hour)); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	if err := g.ClearBan("u1"); err != nil {
		t.Fatalf("ClearBan falhou: %v", err)
	}

	state, code, reason, until := readBanState(t, s.db, "u1")
	if state != banStateOK {
		t.Errorf("ban_state = %q; quero %q", state, banStateOK)
	}
	if code != 0 {
		t.Errorf("ban_code = %d; quero 0", code)
	}
	if reason != "" {
		t.Errorf("ban_reason = %q; quero vazio", reason)
	}
	if until != nil {
		t.Errorf("ban_until = %v; quero NULL", until)
	}
}

// ---------------------------------------------------------------------------
// banGate via Acquire
// ---------------------------------------------------------------------------

// BAN-02 / AC-2: instância banida recusa com 423 e o corpo que o cliente usa
// para reagendar — sem code/reason/until ele só sabe que "deu erro".
func TestAcquireRefusesTemporarilyBannedInstance(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	until := now.Add(6 * time.Hour)
	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", until); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado numa instância banida; quero 423")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d; quero 423", gerr.Status)
	}
	if gerr.Code != CodeInstanceBanned {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeInstanceBanned)
	}
	if gerr.BanCode != 101 {
		t.Errorf("BanCode = %d; quero 101", gerr.BanCode)
	}
	if gerr.Reason != "SentToTooManyPeople" {
		t.Errorf("Reason = %q; quero %q", gerr.Reason, "SentToTooManyPeople")
	}
	if gerr.Until == nil || !gerr.Until.Equal(until) {
		t.Errorf("Until = %v; quero %v", gerr.Until, until)
	}
	// Uma instância banida não pode nem consumir cota do dia.
	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 0 {
		t.Errorf("sent_today = %d; quero 0 — o gate de ban vem antes da reserva", sent)
	}
}

// BAN-02 / AC-6: ban permanente recusa indefinidamente, mesmo com um ban_until
// vencido no banco. Só o clear explícito do admin destrava.
func TestAcquireRefusesPermanentBanRegardlessOfUntil(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	past := now.Add(-100 * time.Hour)
	if _, err := s.db.Exec(
		`UPDATE users SET ban_state = ?, ban_code = ?, ban_reason = ?, ban_until = ? WHERE id = ?`,
		banStatePerm, 406, "UnknownLogout", past.UTC(), "u1"); err != nil {
		t.Fatalf("preparação do ban permanente falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado num ban permanente com until vencido; quero 423")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d; quero 423", gerr.Status)
	}
	state, _, _, _ := readBanState(t, s.db, "u1")
	if state != banStatePerm {
		t.Errorf("ban_state = %q; quero %q intacto — permanente não expira sozinho", state, banStatePerm)
	}
}

// A auto-pausa usa o mesmo 423, e o cliente precisa distinguir "o WhatsApp me
// baniu" de "eu me pausei sozinho" — são reações operacionais diferentes.
func TestAcquireRefusesSelfPausedWithDistinguishableReason(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if err := g.MarkBanned("u1", banStateSelfPaused, 0, "block_rate", now.Add(24*time.Hour)); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado numa instância auto-pausada; quero 423")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d; quero 423", gerr.Status)
	}
	if gerr.Reason != "block_rate" {
		t.Errorf("Reason = %q; quero %q (o motivo registrado da pausa)", gerr.Reason, "block_rate")
	}
}

// BAN-05 / AC-5: prazo vencido destrava no próprio Acquire — sem goroutine de
// varredura, o estado só importa quando alguém tenta enviar.
func TestAcquireClearsExpiredBanLazilyAndProceeds(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", now.Add(-time.Minute)); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	if gerr := g.Acquire("u1", testRecipient(), KindOutbound); gerr != nil {
		t.Fatalf("envio recusado com o ban já vencido: %v", gerr)
	}

	state, code, reason, until := readBanState(t, s.db, "u1")
	if state != banStateOK {
		t.Errorf("ban_state = %q; quero %q (limpeza preguiçosa)", state, banStateOK)
	}
	if code != 0 || reason != "" || until != nil {
		t.Errorf("resíduo do ban vencido: code=%d reason=%q until=%v; quero tudo zerado", code, reason, until)
	}
	// E o envio realmente prosseguiu: a cota foi reservada.
	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 1 {
		t.Errorf("sent_today = %d; quero 1 — o envio deveria ter passado", sent)
	}
}

// Ban temporário sem prazo registrado não expira sozinho: liberar por falta de
// dado é exatamente o fail-open que AD-002 proíbe.
func TestAcquireKeepsTemporaryBanWithoutDeadlineLocked(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if _, err := s.db.Exec(
		`UPDATE users SET ban_state = ?, ban_code = ? WHERE id = ?`, banStateTemp, 101, "u1"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado num ban temporário sem prazo; quero 423")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d; quero 423", gerr.Status)
	}
}

// Estado desconhecido no banco (deploy antigo, escrita manual) é motivo para
// recusar, não para liberar.
func TestAcquireFailsClosedOnUnknownBanState(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if _, err := s.db.Exec(`UPDATE users SET ban_state = ? WHERE id = ?`, "sei_la", "u1"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado com ban_state desconhecido; quero recusa")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d; quero 423", gerr.Status)
	}
}

// ---------------------------------------------------------------------------
// Ordem dos gates
// ---------------------------------------------------------------------------

// O ban vence a janela: uma instância banida fora do horário recebe 423 (pause a
// campanha), não 429 (volte às 09:00) — as reações do cliente são opostas.
func TestAcquireBanOutranksWindow(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := atSaoPaulo(t, 2026, time.August, 11, 3, 0) // madrugada: janela fechada
	g := newAcquireGovernor(t, s.db, &now)
	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", now.Add(6*time.Hour)); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado; quero 423")
	}
	if gerr.Status != http.StatusLocked {
		t.Errorf("status = %d (code %q); quero 423 — o ban é avaliado antes da janela", gerr.Status, gerr.Code)
	}
}

// A janela vence a cota: fora do horário a resposta é "volte às 09:00", não
// "volte à meia-noite". Ordem errada manda o cliente esperar demais.
func TestAcquireWindowOutranksQuota(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := atSaoPaulo(t, 2026, time.August, 11, 3, 0) // madrugada
	g := newAcquireGovernor(t, s.db, &now)
	future := now.Add(20 * time.Hour)
	setReserveState(t, s.db, "u1", 999, &future, nil) // cota estourada também

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado fora da janela; quero 429")
	}
	if gerr.Code != CodeWindowClosed {
		t.Errorf("code = %q; quero %q — a janela é avaliada antes da cota", gerr.Code, CodeWindowClosed)
	}
}

// ---------------------------------------------------------------------------
// SendKind
// ---------------------------------------------------------------------------

// Editar uma mensagem já enviada não é contato novo: não consome cota nem
// esbarra na janela. Se consumisse, corrigir um typo custaria um destinatário.
func TestAcquireEditDoesNotConsumeQuotaOrWindow(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := atSaoPaulo(t, 2026, time.August, 11, 3, 0) // fora da janela
	g := newAcquireGovernor(t, s.db, &now)
	future := now.Add(20 * time.Hour)
	setReserveState(t, s.db, "u1", 999, &future, nil) // cota estourada

	if gerr := g.Acquire("u1", testRecipient(), KindEdit); gerr != nil {
		t.Fatalf("edição recusada: %v; quero passar — edição não é contato novo", gerr)
	}

	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 999 {
		t.Errorf("sent_today = %d; quero 999 inalterado — edição não consome cota", sent)
	}
}

// Mas edição ainda passa pelo ban: uma instância travada não fala com o
// WhatsApp de jeito nenhum.
func TestAcquireEditStillRespectsBan(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if err := g.MarkBanned("u1", banStateTemp, 101, "SentToTooManyPeople", now.Add(time.Hour)); err != nil {
		t.Fatalf("MarkBanned falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindEdit)

	if gerr == nil || gerr.Status != http.StatusLocked {
		t.Fatalf("edição = %v; quero 423 numa instância banida", gerr)
	}
}

// Grupo não tem opt-out individual, mas cota e ritmo continuam valendo: 200
// mensagens de grupo por dia queimam a mesma reputação.
func TestAcquireGroupConsumesQuota(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)

	if gerr := g.Acquire("u1", types.NewJID("120363000000000000", types.GroupServer), KindGroup); gerr != nil {
		t.Fatalf("envio a grupo recusado: %v", gerr)
	}

	sent, _, _ := readReserveState(t, s.db, "u1")
	if sent != 1 {
		t.Errorf("sent_today = %d; quero 1 — grupo consome cota", sent)
	}
}

// ---------------------------------------------------------------------------
// Configuração por instância
// ---------------------------------------------------------------------------

// AD-003 vale também para a coluna da instância: configurar 5s não afrouxa o
// piso de 45s. Um limite que a config relaxa não é limite.
func TestAcquireRaisesPerInstanceIntervalToFloor(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if _, err := s.db.Exec(`UPDATE users SET min_interval_ms = ? WHERE id = ?`, 5000, "u1"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	if gerr := g.Acquire("u1", testRecipient(), KindOutbound); gerr != nil {
		t.Fatalf("primeiro envio recusado: %v", gerr)
	}
	// 10s depois: passaria com os 5s configurados, mas o piso é 45s.
	now = now.Add(10 * time.Second)
	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("segundo envio 10s depois foi aceito; o piso de 45s não pode ser afrouxado pela instância")
	}
	if gerr.Code != CodePacingViolated {
		t.Errorf("code = %q; quero %q", gerr.Code, CodePacingViolated)
	}
}

// Intervalo maior configurado na instância é aceito: a config só pode ser mais
// restritiva, e essa direção é a segura.
func TestAcquireHonorsPerInstanceIntervalAboveFloor(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if _, err := s.db.Exec(`UPDATE users SET min_interval_ms = ? WHERE id = ?`, 120000, "u1"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	if gerr := g.Acquire("u1", testRecipient(), KindOutbound); gerr != nil {
		t.Fatalf("primeiro envio recusado: %v", gerr)
	}
	now = now.Add(60 * time.Second) // além do piso, aquém dos 120s da instância
	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio aceito 60s depois; a instância pediu 120s")
	}
	if want := 60 * time.Second; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v", gerr.RetryAfter, want)
	}
}

// A cota do dia sai da rampa de warmup: uma instância pareada hoje para no 31º
// envio, não no 201º.
func TestAcquireAppliesWarmupRampToDailyQuota(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	paired := now.Add(-2 * time.Hour) // pareada hoje: degrau de 30
	if _, err := s.db.Exec(
		`UPDATE users SET warmup_started_at = ?, sent_today = ?, quota_reset_at = ? WHERE id = ?`,
		paired.UTC(), 30, now.Add(6*time.Hour).UTC(), "u1"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("31º envio aceito no dia do pareamento; a rampa permite 30")
	}
	if gerr.Code != CodeQuotaExceeded {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeQuotaExceeded)
	}
}

// AD-002: sem banco não dá para saber se a instância está banida — recusa.
func TestAcquireFailsClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := makeTestServer(t)
	insertTestUser(t, s.db, "u1")
	now := insideWindow(t)
	g := newAcquireGovernor(t, s.db, &now)
	if err := s.db.Close(); err != nil {
		t.Fatalf("não consegui fechar o banco: %v", err)
	}

	gerr := g.Acquire("u1", testRecipient(), KindOutbound)

	if gerr == nil {
		t.Fatal("envio liberado com o banco fora; quero 503")
	}
	if gerr.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d; quero 503", gerr.Status)
	}
	if gerr.Code != CodeGovernorUnavailable {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeGovernorUnavailable)
	}
}
