package main

import (
	"testing"
	"time"
)

// Rampa de warmup (BAN-08).
//
// Número novo que dispara 200 mensagens no primeiro dia é o padrão que o
// WhatsApp caça. A rampa é o teto que cresce com a idade do pareamento — e ela
// nunca sobe acima da configuração, só a limita ainda mais.

// newWarmupGovernor devolve um governor com os defaults reais (rampa incluída) e
// sem banco: effectiveQuota é decisão pura sobre o relógio.
func newWarmupGovernor(t *testing.T) *SendGovernor {
	t.Helper()
	withGovernorFlags(t, 200, 45000)
	return NewSendGovernor(nil, NewGovernorDefaults())
}

// pairedDaysAgo devolve o instante de pareamento para "d dias completos atrás".
func pairedDaysAgo(now time.Time, d int) *time.Time {
	paired := now.Add(-time.Duration(d) * 24 * time.Hour)
	return &paired
}

// Cada degrau da rampa, incluindo as bordas: o último dia de um degrau e o
// primeiro do seguinte. Errar a borda por um dia é o bug clássico aqui.
func TestEffectiveQuotaFollowsWarmupRamp(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		days int
		want int
	}{
		{0, 30}, {1, 30},
		{2, 60}, {3, 60},
		{4, 100}, {6, 100},
		{7, 150}, {13, 150},
		{14, 200}, {60, 200},
	}
	for _, tc := range cases {
		got := g.effectiveQuota(pairedDaysAgo(now, tc.days), 0, now)
		if got != tc.want {
			t.Errorf("dia %d: cota = %d; quero %d", tc.days, got, tc.want)
		}
	}
}

// Instância que nunca pareou não tem histórico nenhum — trata como dia 0, o
// degrau mais conservador. Liberar 200 por falta de dado seria o pior default.
func TestEffectiveQuotaTreatsUnpairedInstanceAsDayZero(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	if got := g.effectiveQuota(nil, 0, now); got != 30 {
		t.Errorf("cota sem warmup_started_at = %d; quero 30 (dia 0)", got)
	}
}

// Edge case da spec: despareamento + repareamento reinicia warmup_started_at.
// Um número que voltou do zero é tratado como novo, não como veterano.
func TestEffectiveQuotaRestartsRampAfterRepairing(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	if got := g.effectiveQuota(pairedDaysAgo(now, 30), 0, now); got != 200 {
		t.Fatalf("instância veterana = %d; quero 200", got)
	}
	// Repareada hoje: o relógio da rampa volta ao começo.
	if got := g.effectiveQuota(pairedDaysAgo(now, 0), 0, now); got != 30 {
		t.Errorf("cota após repareamento = %d; quero 30 (a rampa reinicia)", got)
	}
}

// A configuração da instância limita a rampa; nunca a ultrapassa. Uma instância
// com teto de 50 continua em 50 no dia 14, quando a rampa já permitiria 200.
func TestEffectiveQuotaNeverExceedsInstanceLimit(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	if got := g.effectiveQuota(pairedDaysAgo(now, 14), 50, now); got != 50 {
		t.Errorf("cota = %d com limite de instância 50; quero 50", got)
	}
}

// E o inverso: config mais alta que a rampa não antecipa o aquecimento. No dia 0
// a instância envia 30, mesmo configurada para 200 — a rampa é o teto real.
func TestEffectiveQuotaKeepsRampBelowConfiguredLimit(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	if got := g.effectiveQuota(pairedDaysAgo(now, 0), 200, now); got != 30 {
		t.Errorf("cota = %d no dia 0 com limite 200; quero 30 (a rampa manda)", got)
	}
}

// BAN-11 / AC-7: 0 na coluna é o sentinela de "não configurado" — cai no padrão
// global, não em "cota zero" (que travaria a instância) nem em "sem limite".
func TestEffectiveQuotaFallsBackToGlobalDefaultWhenUnset(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

	if got := g.effectiveQuota(pairedDaysAgo(now, 14), 0, now); got != g.defaults.MaxDailyQuota {
		t.Errorf("cota = %d com o sentinela 0; quero o padrão global %d", got, g.defaults.MaxDailyQuota)
	}
}

// Dias são contados por períodos de 24h completos: faltando um minuto para
// fechar o segundo dia, a instância ainda está no primeiro degrau.
func TestEffectiveQuotaCountsOnlyCompletedDays(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	almostTwoDays := now.Add(-48*time.Hour + time.Minute)

	if got := g.effectiveQuota(&almostTwoDays, 0, now); got != 30 {
		t.Errorf("cota a 1min de completar 2 dias = %d; quero 30 (o degrau só vira no dia cheio)", got)
	}
}

// Pareamento no futuro só acontece com relógio desalinhado. A resposta segura é
// o degrau mais baixo — nunca liberar o teto por causa de uma data estranha.
func TestEffectiveQuotaTreatsFuturePairingAsDayZero(t *testing.T) {
	g := newWarmupGovernor(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)

	if got := g.effectiveQuota(&future, 0, now); got != 30 {
		t.Errorf("cota com pareamento no futuro = %d; quero 30", got)
	}
}
