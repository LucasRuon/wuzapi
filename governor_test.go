package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GateError.WriteTo é o contrato público de recusa (AD-001). Cada status pede uma
// reação diferente do cliente — 423 pausa a campanha, 429 repete o MESMO
// destinatário, 422 pula — então o corpo e os headers são API, não detalhe.

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("corpo não é JSON válido: %v", err)
	}
	return body
}

// 423: instância travada. O cliente precisa de "até quando" para reagendar.
func TestGateErrorWriteToLockedCarriesBanCodeAndUntil(t *testing.T) {
	until := time.Date(2026, 8, 10, 15, 4, 5, 0, time.UTC)
	e := &GateError{
		Status:  http.StatusLocked,
		Code:    CodeInstanceBanned,
		Reason:  "SentToTooManyPeople",
		Until:   &until,
		BanCode: 101,
	}

	rec := httptest.NewRecorder()
	e.WriteTo(rec)

	if rec.Code != http.StatusLocked {
		t.Errorf("status = %d; quero 423", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != CodeInstanceBanned {
		t.Errorf("error = %v; quero %q", body["error"], CodeInstanceBanned)
	}
	if body["success"] != false {
		t.Errorf("success = %v; quero false (clientes existentes checam esse campo)", body["success"])
	}
	// "code" é o status HTTP em todo o envelope da API; o código do ban vai em
	// "banCode" (ver SPEC_DEVIATION em governor.go).
	if body["code"] != float64(http.StatusLocked) {
		t.Errorf("code = %v; quero 423 (status HTTP, invariante do envelope)", body["code"])
	}
	if body["banCode"] != float64(101) {
		t.Errorf("banCode = %v; quero 101", body["banCode"])
	}
	if body["reason"] != "SentToTooManyPeople" {
		t.Errorf("reason = %v; quero %q", body["reason"], "SentToTooManyPeople")
	}
	if body["until"] != "2026-08-10T15:04:05Z" {
		t.Errorf("until = %v; quero RFC3339 em UTC", body["until"])
	}
	// 423 não é "tente de novo em X" — é "pause". Nada de Retry-After.
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q em 423; quero ausente", got)
	}
}

// 429: rápido/volumoso demais. O Retry-After é o que evita o cliente voltar
// imediatamente e levar outro 429.
func TestGateErrorWriteToTooManyRequestsSetsRetryAfter(t *testing.T) {
	e := &GateError{
		Status:     http.StatusTooManyRequests,
		Code:       CodePacingViolated,
		Reason:     "aguarde o intervalo mínimo entre envios",
		RetryAfter: 30 * time.Second,
	}

	rec := httptest.NewRecorder()
	e.WriteTo(rec)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d; quero 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q; quero \"30\"", got)
	}
	body := decodeBody(t, rec)
	if body["error"] != CodePacingViolated {
		t.Errorf("error = %v; quero %q", body["error"], CodePacingViolated)
	}
	if body["retryAfter"] != float64(30) {
		t.Errorf("retryAfter = %v; quero 30", body["retryAfter"])
	}
	if _, ok := body["until"]; ok {
		t.Errorf("until presente em 429; quero ausente (until é de 423)")
	}
}

// Retry-After fracionário arredonda PARA CIMA: devolver 0 mandaria o cliente
// repetir na hora e tomar outro 429.
func TestGateErrorWriteToRoundsRetryAfterUp(t *testing.T) {
	e := &GateError{
		Status:     http.StatusTooManyRequests,
		Code:       CodeQuotaExceeded,
		RetryAfter: 100 * time.Millisecond,
	}

	rec := httptest.NewRecorder()
	e.WriteTo(rec)

	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q para 100ms; quero \"1\" (arredonda para cima)", got)
	}
}

// 422: o destinatário é que é inválido, não a instância. Sem Retry-After, sem
// until — o cliente pula e segue.
func TestGateErrorWriteToUnprocessableHasNoRetryHints(t *testing.T) {
	e := &GateError{
		Status: http.StatusUnprocessableEntity,
		Code:   CodeRecipientSuppressed,
		Reason: "opt_out",
	}

	rec := httptest.NewRecorder()
	e.WriteTo(rec)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; quero 422", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q em 422; quero ausente", got)
	}
	body := decodeBody(t, rec)
	if body["error"] != CodeRecipientSuppressed {
		t.Errorf("error = %v; quero %q", body["error"], CodeRecipientSuppressed)
	}
	if body["reason"] != "opt_out" {
		t.Errorf("reason = %v; quero %q", body["reason"], "opt_out")
	}
	if _, ok := body["retryAfter"]; ok {
		t.Errorf("retryAfter presente em 422; quero ausente")
	}
}

func TestGateErrorMessage(t *testing.T) {
	withReason := &GateError{Code: CodeQuotaExceeded, Reason: "cota diária esgotada"}
	if got := withReason.Error(); got != "quota_exceeded: cota diária esgotada" {
		t.Errorf("Error() = %q; quero %q", got, "quota_exceeded: cota diária esgotada")
	}
	bare := &GateError{Code: CodeWindowClosed}
	if got := bare.Error(); got != "window_closed" {
		t.Errorf("Error() sem reason = %q; quero %q", got, "window_closed")
	}
}

// ---------------------------------------------------------------------------
// AD-003: configuração só pode ficar MAIS restritiva
// ---------------------------------------------------------------------------

// withGovernorFlags define as flags do governor para um teste e restaura no fim.
func withGovernorFlags(t *testing.T, quota, intervalMs int) {
	t.Helper()
	prevQuota, prevInterval := *maxDailyQuota, *minIntervalMs
	*maxDailyQuota, *minIntervalMs = quota, intervalMs
	t.Cleanup(func() { *maxDailyQuota, *minIntervalMs = prevQuota, prevInterval })
}

// snapshotGovernorFlags restaura TODAS as flags do governor no fim do teste.
// Necessário para os testes de env override, que escrevem em janela e timezone.
func snapshotGovernorFlags(t *testing.T) {
	t.Helper()
	q, i, ws, we, tz := *maxDailyQuota, *minIntervalMs, *windowStart, *windowEnd, *sendTimezone
	t.Cleanup(func() {
		*maxDailyQuota, *minIntervalMs = q, i
		*windowStart, *windowEnd, *sendTimezone = ws, we, tz
	})
}

// Um deploy não pode afrouxar o piso de 45s. Se pudesse, o guardrail seria
// opcional — e a regra que ele existe para impor viraria sugestão.
func TestNewGovernorDefaultsRaisesIntervalBelowFloor(t *testing.T) {
	withGovernorFlags(t, 200, 5000) // 5s: bem abaixo do piso

	d := NewGovernorDefaults()

	if d.MinInterval != minIntervalFloor {
		t.Errorf("MinInterval = %v para 5000ms configurados; quero o piso %v", d.MinInterval, minIntervalFloor)
	}
}

// O contrário é permitido: um operador mais conservador pode espaçar mais.
func TestNewGovernorDefaultsKeepsIntervalAboveFloor(t *testing.T) {
	withGovernorFlags(t, 200, 90000) // 90s

	d := NewGovernorDefaults()

	if d.MinInterval != 90*time.Second {
		t.Errorf("MinInterval = %v; quero 90s (config mais restritiva é aceita)", d.MinInterval)
	}
}

// Cota acima do teto é rebaixada: 200/dia é invariante do código, não do deploy.
func TestNewGovernorDefaultsCapsQuotaAboveCeiling(t *testing.T) {
	withGovernorFlags(t, 5000, 45000)

	d := NewGovernorDefaults()

	if d.MaxDailyQuota != maxDailyQuotaCeiling {
		t.Errorf("MaxDailyQuota = %d para 5000 configurados; quero o teto %d", d.MaxDailyQuota, maxDailyQuotaCeiling)
	}
}

// Abaixar a cota é a direção segura e precisa ser respeitada.
func TestNewGovernorDefaultsKeepsQuotaBelowCeiling(t *testing.T) {
	withGovernorFlags(t, 50, 45000)

	d := NewGovernorDefaults()

	if d.MaxDailyQuota != 50 {
		t.Errorf("MaxDailyQuota = %d; quero 50 (config mais restritiva é aceita)", d.MaxDailyQuota)
	}
}

// Cota zero ou negativa é configuração inválida, não "sem limite": cai no teto.
func TestNewGovernorDefaultsTreatsNonPositiveQuotaAsCeiling(t *testing.T) {
	for _, quota := range []int{0, -1} {
		withGovernorFlags(t, quota, 45000)

		d := NewGovernorDefaults()

		if d.MaxDailyQuota != maxDailyQuotaCeiling {
			t.Errorf("MaxDailyQuota = %d para %d configurado; quero o teto %d (nunca ilimitado)",
				d.MaxDailyQuota, quota, maxDailyQuotaCeiling)
		}
	}
}

// ---------------------------------------------------------------------------
// Override por ambiente
// ---------------------------------------------------------------------------

// Deploys em container configuram por env, não por flag. Se o override não
// funcionasse, o operador acharia que ajustou a cota e continuaria no default.
func TestApplyGovernorEnvOverridesSetsEveryField(t *testing.T) {
	snapshotGovernorFlags(t)
	t.Setenv("GOVERNOR_MAX_DAILY_QUOTA", "120")
	t.Setenv("GOVERNOR_MIN_INTERVAL_MS", "60000")
	t.Setenv("GOVERNOR_WINDOW_START", "08:30")
	t.Setenv("GOVERNOR_WINDOW_END", "19:00")
	t.Setenv("GOVERNOR_TIMEZONE", "America/Manaus")

	applyGovernorEnvOverrides()

	if *maxDailyQuota != 120 {
		t.Errorf("maxDailyQuota = %d; quero 120", *maxDailyQuota)
	}
	if *minIntervalMs != 60000 {
		t.Errorf("minIntervalMs = %d; quero 60000", *minIntervalMs)
	}
	if *windowStart != "08:30" {
		t.Errorf("windowStart = %q; quero %q", *windowStart, "08:30")
	}
	if *windowEnd != "19:00" {
		t.Errorf("windowEnd = %q; quero %q", *windowEnd, "19:00")
	}
	if *sendTimezone != "America/Manaus" {
		t.Errorf("sendTimezone = %q; quero %q", *sendTimezone, "America/Manaus")
	}
}

// Env vazio não é "zere isso": a flag (ou seu default) prevalece. Mesmo
// comportamento de SESSION_DEVICE_NAME em main.go.
func TestApplyGovernorEnvOverridesIgnoresUnsetVars(t *testing.T) {
	snapshotGovernorFlags(t)
	*maxDailyQuota, *minIntervalMs = 77, 50000
	*windowStart, *windowEnd, *sendTimezone = "10:00", "18:00", "America/Bahia"
	t.Setenv("GOVERNOR_MAX_DAILY_QUOTA", "")
	t.Setenv("GOVERNOR_MIN_INTERVAL_MS", "")
	t.Setenv("GOVERNOR_WINDOW_START", "")

	applyGovernorEnvOverrides()

	if *maxDailyQuota != 77 {
		t.Errorf("maxDailyQuota = %d; quero 77 preservado", *maxDailyQuota)
	}
	if *minIntervalMs != 50000 {
		t.Errorf("minIntervalMs = %d; quero 50000 preservado", *minIntervalMs)
	}
	if *windowStart != "10:00" {
		t.Errorf("windowStart = %q; quero %q preservado", *windowStart, "10:00")
	}
}

// Env numérico inválido é ignorado com warning, não vira zero. Zero em
// minIntervalMs significaria "sem intervalo" — o oposto de um guardrail.
func TestApplyGovernorEnvOverridesIgnoresMalformedNumbers(t *testing.T) {
	snapshotGovernorFlags(t)
	*maxDailyQuota, *minIntervalMs = 150, 45000
	t.Setenv("GOVERNOR_MAX_DAILY_QUOTA", "muitas")
	t.Setenv("GOVERNOR_MIN_INTERVAL_MS", "45s")

	applyGovernorEnvOverrides()

	if *maxDailyQuota != 150 {
		t.Errorf("maxDailyQuota = %d após env inválido; quero 150 preservado", *maxDailyQuota)
	}
	if *minIntervalMs != 45000 {
		t.Errorf("minIntervalMs = %d após env inválido; quero 45000 preservado", *minIntervalMs)
	}
}

// Env também passa pelos clamps de AD-003: não existe rota de configuração que
// contorne o piso e o teto.
func TestGovernorDefaultsClampValuesComingFromEnv(t *testing.T) {
	snapshotGovernorFlags(t)
	t.Setenv("GOVERNOR_MAX_DAILY_QUOTA", "10000")
	t.Setenv("GOVERNOR_MIN_INTERVAL_MS", "1000")

	applyGovernorEnvOverrides()
	d := NewGovernorDefaults()

	if d.MaxDailyQuota != maxDailyQuotaCeiling {
		t.Errorf("MaxDailyQuota = %d vindo do env; quero o teto %d", d.MaxDailyQuota, maxDailyQuotaCeiling)
	}
	if d.MinInterval != minIntervalFloor {
		t.Errorf("MinInterval = %v vindo do env; quero o piso %v", d.MinInterval, minIntervalFloor)
	}
}

// A rampa de warmup precisa bater com a calibração para listas opt-in.
func TestNewGovernorDefaultsWarmupRamp(t *testing.T) {
	withGovernorFlags(t, 200, 45000)

	d := NewGovernorDefaults()

	want := []RampStep{{0, 30}, {2, 60}, {4, 100}, {7, 150}, {14, 200}}
	if len(d.WarmupRamp) != len(want) {
		t.Fatalf("rampa com %d degraus; quero %d", len(d.WarmupRamp), len(want))
	}
	for i, step := range want {
		if d.WarmupRamp[i] != step {
			t.Errorf("degrau %d = %+v; quero %+v", i, d.WarmupRamp[i], step)
		}
	}
	// O último degrau nunca pode passar do teto — senão a rampa contornaria AD-003.
	if last := d.WarmupRamp[len(d.WarmupRamp)-1].Quota; last > maxDailyQuotaCeiling {
		t.Errorf("último degrau da rampa = %d; quero <= %d", last, maxDailyQuotaCeiling)
	}
}
