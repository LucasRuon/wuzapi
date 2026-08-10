package main

import (
	"testing"
)

// Migration 10 (add_anti_ban_state) cria o estado anti-ban por instância: as
// colunas de governor/ban em users e as tabelas suppression e send_events.
//
// Por que importa: todo o feature anti-ban lê e escreve nesse estado, e ele
// precisa sobreviver a restart. Uma coluna sem default faria o governor ler NULL
// numa instância pré-existente e barrar (ou liberar) envios por engano.

// As colunas de users criadas pela migration 10, com o default que o governor
// espera encontrar numa instância que nunca enviou nada.
func TestMigration10CreatesUserColumnsWithDefaults(t *testing.T) {
	s := makeTestServer(t)

	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token) VALUES (?, ?, ?)`,
		"user-1", "instancia", "tok-1"); err != nil {
		t.Fatalf("insert de usuário falhou: %v", err)
	}

	var got struct {
		BanState            string `db:"ban_state"`
		BanCode             int    `db:"ban_code"`
		BanReason           string `db:"ban_reason"`
		SentToday           int    `db:"sent_today"`
		MaxDailyQuota       int    `db:"max_daily_quota"`
		MinIntervalMs       int    `db:"min_interval_ms"`
		WindowStart         string `db:"window_start"`
		WindowEnd           string `db:"window_end"`
		SendTimezone        string `db:"send_timezone"`
		ConsecutiveFailures int    `db:"consecutive_failures"`
		SimulateTyping      int    `db:"simulate_typing"`
		DeviceOS            string `db:"device_os"`
		DevicePlatform      string `db:"device_platform"`
	}
	if err := s.db.Get(&got, `
        SELECT ban_state, ban_code, ban_reason, sent_today, max_daily_quota,
               min_interval_ms, window_start, window_end, send_timezone,
               consecutive_failures, simulate_typing, device_os, device_platform
          FROM users WHERE id = ?`, "user-1"); err != nil {
		t.Fatalf("select das colunas anti-ban falhou: %v", err)
	}

	// 'ok' é o único estado que libera envio; qualquer outro default travaria
	// toda instância existente no primeiro deploy da migration.
	if got.BanState != "ok" {
		t.Errorf("ban_state = %q; quero %q", got.BanState, "ok")
	}
	if got.BanCode != 0 {
		t.Errorf("ban_code = %d; quero 0", got.BanCode)
	}
	if got.BanReason != "" {
		t.Errorf("ban_reason = %q; quero vazio", got.BanReason)
	}
	if got.SentToday != 0 {
		t.Errorf("sent_today = %d; quero 0", got.SentToday)
	}
	// 0 é o sentinela de "usa o padrão global" — ver design.md, GovernorDefaults.
	if got.MaxDailyQuota != 0 {
		t.Errorf("max_daily_quota = %d; quero 0 (sentinela de padrão global)", got.MaxDailyQuota)
	}
	if got.MinIntervalMs != 0 {
		t.Errorf("min_interval_ms = %d; quero 0 (sentinela de padrão global)", got.MinIntervalMs)
	}
	if got.WindowStart != "" || got.WindowEnd != "" || got.SendTimezone != "" {
		t.Errorf("janela = (%q, %q, %q); quero vazios (sentinela de padrão global)",
			got.WindowStart, got.WindowEnd, got.SendTimezone)
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures = %d; quero 0", got.ConsecutiveFailures)
	}
	// Simulação de digitação é opt-out, não opt-in: instância nova já nasce com ela.
	if got.SimulateTyping != 1 {
		t.Errorf("simulate_typing = %d; quero 1", got.SimulateTyping)
	}
	if got.DeviceOS != "" || got.DevicePlatform != "" {
		t.Errorf("fingerprint = (%q, %q); quero vazios (derivado do userID)",
			got.DeviceOS, got.DevicePlatform)
	}
}

// As colunas de timestamp nascem NULL (nunca pareou, nunca enviou) e precisam ser
// escanáveis como NULL — o governor distingue "nunca enviou" de "enviou às 00:00".
func TestMigration10TimestampColumnsAreNullable(t *testing.T) {
	s := makeTestServer(t)

	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token) VALUES (?, ?, ?)`,
		"user-1", "instancia", "tok-1"); err != nil {
		t.Fatalf("insert de usuário falhou: %v", err)
	}

	var got struct {
		BanUntil        *string `db:"ban_until"`
		QuotaResetAt    *string `db:"quota_reset_at"`
		LastSendAt      *string `db:"last_send_at"`
		WarmupStartedAt *string `db:"warmup_started_at"`
	}
	if err := s.db.Get(&got, `
        SELECT ban_until, quota_reset_at, last_send_at, warmup_started_at
          FROM users WHERE id = ?`, "user-1"); err != nil {
		t.Fatalf("select dos timestamps falhou: %v", err)
	}

	if got.BanUntil != nil {
		t.Errorf("ban_until = %v; quero NULL", *got.BanUntil)
	}
	if got.QuotaResetAt != nil {
		t.Errorf("quota_reset_at = %v; quero NULL", *got.QuotaResetAt)
	}
	if got.LastSendAt != nil {
		t.Errorf("last_send_at = %v; quero NULL", *got.LastSendAt)
	}
	if got.WarmupStartedAt != nil {
		t.Errorf("warmup_started_at = %v; quero NULL", *got.WarmupStartedAt)
	}
}

// suppression tem PK composta (user_id, jid): a mesma supressão registrada duas
// vezes é conflito, e o mesmo JID suprimido em duas instâncias é permitido.
func TestMigration10SuppressionPrimaryKeyIsPerInstance(t *testing.T) {
	s := makeTestServer(t)

	insert := func(userID, jid string) error {
		_, err := s.db.Exec(
			`INSERT INTO suppression (user_id, jid, reason, created_at)
             VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, userID, jid, "opt_out")
		return err
	}

	if err := insert("user-1", "5541999998888@s.whatsapp.net"); err != nil {
		t.Fatalf("primeira supressão falhou: %v", err)
	}
	// Mesmo (user_id, jid): a PK composta precisa recusar, senão o store de
	// supressão duplicaria linhas em vez de fazer upsert.
	if err := insert("user-1", "5541999998888@s.whatsapp.net"); err == nil {
		t.Fatal("supressão duplicada foi aceita; a PK composta (user_id, jid) não está valendo")
	}
	// Mesmo JID, outra instância: supressão é por instância, precisa passar.
	if err := insert("user-2", "5541999998888@s.whatsapp.net"); err != nil {
		t.Fatalf("supressão do mesmo JID em outra instância falhou: %v", err)
	}
}

// send_events alimenta as janelas de 24h (taxa de bloqueio) e a janela deslizante
// de repetição de conteúdo, com índice em (user_id, created_at).
func TestMigration10SendEventsTableAndIndex(t *testing.T) {
	s := makeTestServer(t)

	if _, err := s.db.Exec(
		`INSERT INTO send_events (user_id, kind, body_hash, created_at)
         VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, "user-1", "sent", "abc123"); err != nil {
		t.Fatalf("insert em send_events falhou: %v", err)
	}

	// body_hash tem default: eventos de bloqueio não carregam hash de corpo.
	if _, err := s.db.Exec(
		`INSERT INTO send_events (user_id, kind, created_at)
         VALUES (?, ?, CURRENT_TIMESTAMP)`, "user-1", "block"); err != nil {
		t.Fatalf("insert sem body_hash falhou: %v", err)
	}

	var hash string
	if err := s.db.Get(&hash,
		`SELECT body_hash FROM send_events WHERE kind = ?`, "block"); err != nil {
		t.Fatalf("select de body_hash falhou: %v", err)
	}
	if hash != "" {
		t.Errorf("body_hash = %q; quero vazio por default", hash)
	}

	var indexCount int
	if err := s.db.Get(&indexCount, `
        SELECT COUNT(*) FROM sqlite_master
         WHERE type = 'index' AND name = 'send_events_user_time_idx'`); err != nil {
		t.Fatalf("consulta do índice falhou: %v", err)
	}
	if indexCount != 1 {
		t.Errorf("send_events_user_time_idx encontrado %d vez(es); quero 1", indexCount)
	}
}

// Reaplicar a migration 10 sobre um schema que já a tem não pode falhar: é o que
// acontece se o registro em migrations se perder, ou num rollback parcial.
func TestMigration10IsIdempotent(t *testing.T) {
	s := makeTestServer(t)

	// Apaga só o registro da migration 10, forçando initializeSchema a reaplicá-la
	// sobre um schema que já tem todas as colunas e tabelas.
	if _, err := s.db.Exec(`DELETE FROM migrations WHERE id = 10`); err != nil {
		t.Fatalf("não consegui remover o registro da migration 10: %v", err)
	}

	if err := initializeSchema(s.db); err != nil {
		t.Fatalf("reaplicar a migration 10 falhou: %v", err)
	}

	// O estado precisa continuar utilizável depois da reaplicação.
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token) VALUES (?, ?, ?)`,
		"user-1", "instancia", "tok-1"); err != nil {
		t.Fatalf("insert após reaplicação falhou: %v", err)
	}
	var banState string
	if err := s.db.Get(&banState, `SELECT ban_state FROM users WHERE id = ?`, "user-1"); err != nil {
		t.Fatalf("select após reaplicação falhou: %v", err)
	}
	if banState != "ok" {
		t.Errorf("ban_state = %q após reaplicação; quero %q", banState, "ok")
	}
}
