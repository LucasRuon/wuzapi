package main

import (
	"net/http"
	"testing"
	"time"
)

// Janela horária de envio (BAN-09) e poda de send_events (BAN-11).
//
// Disparo às 3 da manhã é assinatura de robô, e o destinatário que acorda com a
// mensagem é o que denuncia. A janela é sobre o horário LOCAL da instância —
// por isso todo cálculo passa por time.LoadLocation explícito, nunca time.Local,
// que seria o fuso do container.

func newWindowGovernor(t *testing.T) *SendGovernor {
	t.Helper()
	withGovernorFlags(t, 200, 45000)
	return NewSendGovernor(nil, NewGovernorDefaults())
}

// atSaoPaulo monta um instante no horário de parede de São Paulo.
func atSaoPaulo(t *testing.T, y int, m time.Month, d, h, min int) time.Time {
	t.Helper()
	return time.Date(y, m, d, h, min, 0, 0, saoPauloOrSkip(t))
}

// Terça às 14:00: dentro de 09:00–20:00, nada a barrar.
func TestWindowGateOpenInsideWindow(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 11, 14, 0) // terça

	if gerr := g.windowGate(now, "", "", "", true); gerr != nil {
		t.Errorf("janela fechada às 14:00 de uma terça: %v", gerr)
	}
}

// As bordas são inclusivas: 09:00 em ponto já vale, 20:00 em ponto ainda vale.
func TestWindowGateBoundariesAreInclusive(t *testing.T) {
	g := newWindowGovernor(t)

	if gerr := g.windowGate(atSaoPaulo(t, 2026, time.August, 11, 9, 0), "", "", "", true); gerr != nil {
		t.Errorf("09:00 em ponto recusado: %v", gerr)
	}
	if gerr := g.windowGate(atSaoPaulo(t, 2026, time.August, 11, 20, 0), "", "", "", true); gerr != nil {
		t.Errorf("20:00 em ponto recusado: %v", gerr)
	}
}

// Antes da abertura: recusa com o tempo que falta para as 09:00 do mesmo dia.
func TestWindowGateBeforeOpeningRetriesSameDay(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 11, 7, 0) // terça, 2h antes

	gerr := g.windowGate(now, "", "", "", true)

	if gerr == nil {
		t.Fatal("janela aberta às 07:00; quero 429")
	}
	if gerr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d; quero 429", gerr.Status)
	}
	if gerr.Code != CodeWindowClosed {
		t.Errorf("code = %q; quero %q", gerr.Code, CodeWindowClosed)
	}
	if want := 2 * time.Hour; gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (até as 09:00 de hoje)", gerr.RetryAfter, want)
	}
}

// Depois do fechamento: a próxima abertura é amanhã, não hoje.
func TestWindowGateAfterClosingRetriesNextDay(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 11, 21, 0) // terça, 1h depois do fim

	gerr := g.windowGate(now, "", "", "", true)

	if gerr == nil {
		t.Fatal("janela aberta às 21:00; quero 429")
	}
	want := atSaoPaulo(t, 2026, time.August, 12, 9, 0).Sub(now) // quarta 09:00
	if gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (até a abertura de amanhã)", gerr.RetryAfter, want)
	}
}

// Domingo é fechado o dia inteiro, mesmo no meio do horário comercial.
func TestWindowGateClosedOnSunday(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 16, 14, 0) // domingo
	if now.Weekday() != time.Sunday {
		t.Fatalf("data de teste não é domingo: %v", now.Weekday())
	}

	gerr := g.windowGate(now, "", "", "", true)

	if gerr == nil {
		t.Fatal("janela aberta num domingo; quero 429")
	}
	want := atSaoPaulo(t, 2026, time.August, 17, 9, 0).Sub(now) // segunda 09:00
	if gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (até segunda)", gerr.RetryAfter, want)
	}
}

// Sábado à noite: a próxima abertura pula o domingo inteiro e cai na segunda.
// Mandar o cliente voltar em 12h o faria bater num domingo fechado.
func TestWindowGateSaturdayNightSkipsOverSunday(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 15, 22, 0) // sábado
	if now.Weekday() != time.Saturday {
		t.Fatalf("data de teste não é sábado: %v", now.Weekday())
	}

	gerr := g.windowGate(now, "", "", "", true)

	if gerr == nil {
		t.Fatal("janela aberta às 22:00 de sábado; quero 429")
	}
	want := atSaoPaulo(t, 2026, time.August, 17, 9, 0).Sub(now) // segunda 09:00
	if gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (pulando o domingo)", gerr.RetryAfter, want)
	}
}

// Com skipSunday desligado, domingo é um dia como outro qualquer.
func TestWindowGateAllowsSundayWhenSkipDisabled(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 16, 14, 0) // domingo

	if gerr := g.windowGate(now, "", "", "", false); gerr != nil {
		t.Errorf("domingo recusado com skipSunday=false: %v", gerr)
	}
}

// A janela por instância sobrescreve o padrão global.
func TestWindowGateHonorsPerInstanceWindow(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 11, 8, 0) // terça 08:00

	// Fora do padrão global (09–20), dentro da janela da instância (07–12).
	if gerr := g.windowGate(now, "America/Sao_Paulo", "07:00", "12:00", true); gerr != nil {
		t.Errorf("janela 07:00–12:00 recusou 08:00: %v", gerr)
	}
	// E o fechamento da instância vale mesmo dentro do padrão global.
	gerr := g.windowGate(atSaoPaulo(t, 2026, time.August, 11, 13, 0), "America/Sao_Paulo", "07:00", "12:00", true)
	if gerr == nil {
		t.Error("janela 07:00–12:00 aceitou 13:00; quero 429")
	}
}

// Fuso inválido na instância não pode derrubar o envio nem o processo: cai no
// padrão global. Uma string errada no banco viraria pânico se o código confiasse
// no retorno de LoadLocation.
func TestWindowGateFallsBackToDefaultTimezoneWhenInvalid(t *testing.T) {
	g := newWindowGovernor(t)
	// 14:00 em São Paulo — dentro da janela quando avaliado no fuso padrão.
	now := atSaoPaulo(t, 2026, time.August, 11, 14, 0)

	if gerr := g.windowGate(now, "Nao/Existe", "", "", true); gerr != nil {
		t.Errorf("fuso inválido derrubou o envio: %v; quero cair no padrão global", gerr)
	}
	// E às 03:00 continua fechado — o fallback avalia de verdade, não libera tudo.
	if gerr := g.windowGate(atSaoPaulo(t, 2026, time.August, 11, 3, 0), "Nao/Existe", "", "", true); gerr == nil {
		t.Error("fuso inválido liberou 03:00; o fallback precisa continuar avaliando a janela")
	}
}

// Horário mal formado na coluna também cai no padrão, em vez de virar 00:00 —
// que abriria a madrugada inteira, o oposto do que a janela existe para fazer.
func TestWindowGateFallsBackToDefaultWindowWhenMalformed(t *testing.T) {
	g := newWindowGovernor(t)
	now := atSaoPaulo(t, 2026, time.August, 11, 3, 0) // 03:00

	if gerr := g.windowGate(now, "", "manhã", "noite", true); gerr == nil {
		t.Error("horário mal formado liberou 03:00; quero o padrão global 09:00–20:00")
	}
}

// O horário de verão muda o offset no meio do intervalo: de sábado 3/11/2018
// (UTC-3) até segunda 5/11 (UTC-2, DST começou no domingo) são 35 horas de
// relógio real, não 36. Aritmética de "+24h" erraria por uma hora e o cliente
// voltaria antes da abertura.
func TestWindowGateCrossesDaylightSavingCorrectly(t *testing.T) {
	g := newWindowGovernor(t)
	sp := saoPauloOrSkip(t)
	now := time.Date(2018, time.November, 3, 22, 0, 0, 0, sp) // sábado
	nextOpen := time.Date(2018, time.November, 5, 9, 0, 0, 0, sp)
	_, offBefore := now.Zone()
	_, offAfter := nextOpen.Zone()
	if offBefore == offAfter {
		t.Fatalf("o intervalo do teste não atravessa o horário de verão (offset %d nos dois lados); "+
			"a base de fusos mudou e o caso precisa de outra data", offBefore)
	}

	gerr := g.windowGate(now, "", "", "", true)

	if gerr == nil {
		t.Fatal("janela aberta às 22:00 de sábado; quero 429")
	}
	want := nextOpen.Sub(now)
	if gerr.RetryAfter != want {
		t.Errorf("RetryAfter = %v; quero %v (segunda 09:00 no relógio local)", gerr.RetryAfter, want)
	}
	if gerr.RetryAfter == 35*time.Hour+time.Hour {
		t.Errorf("RetryAfter = %v; a virada do horário de verão foi ignorada", gerr.RetryAfter)
	}
}

// ---------------------------------------------------------------------------
// Poda de send_events
// ---------------------------------------------------------------------------

func insertSendEvent(t *testing.T, s *server, userID, kind string, at time.Time) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO send_events (user_id, kind, created_at) VALUES (?, ?, ?)`,
		userID, kind, at.UTC()); err != nil {
		t.Fatalf("insert em send_events falhou: %v", err)
	}
}

func countSendEvents(t *testing.T, s *server) int {
	t.Helper()
	var n int
	if err := s.db.Get(&n, `SELECT COUNT(*) FROM send_events`); err != nil {
		t.Fatalf("contagem de send_events falhou: %v", err)
	}
	return n
}

// send_events alimenta janelas de 24h e a de repetição de conteúdo: nada com
// mais de 7 dias tem uso, e sem poda a tabela cresce para sempre.
func TestPruneSendEventsRemovesEntriesOlderThanSevenDays(t *testing.T) {
	s := makeTestServer(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)

	insertSendEvent(t, s, "u1", "sent", now.Add(-8*24*time.Hour))
	insertSendEvent(t, s, "u1", "block", now.Add(-30*24*time.Hour))

	if err := g.pruneSendEvents(now); err != nil {
		t.Fatalf("poda falhou: %v", err)
	}

	if got := countSendEvents(t, s); got != 0 {
		t.Errorf("%d eventos antigos sobreviveram à poda; quero 0", got)
	}
}

// A poda não pode levar junto o que ainda está em uso — inclusive o evento que
// acabou de completar exatamente 7 dias, que ainda entra na janela.
func TestPruneSendEventsKeepsRecentEntries(t *testing.T) {
	s := makeTestServer(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)

	insertSendEvent(t, s, "u1", "sent", now.Add(-time.Hour))
	insertSendEvent(t, s, "u1", "sent", now.Add(-6*24*time.Hour))
	insertSendEvent(t, s, "u1", "block", now.Add(-7*24*time.Hour))

	if err := g.pruneSendEvents(now); err != nil {
		t.Fatalf("poda falhou: %v", err)
	}

	if got := countSendEvents(t, s); got != 3 {
		t.Errorf("sobraram %d eventos recentes; quero 3", got)
	}
}

// A poda roda no boot: um gateway que ficou meses parado não pode voltar
// carregando a tabela inteira até o primeiro tick de 6h.
func TestStartSendEventPrunerPrunesOnBoot(t *testing.T) {
	s := makeTestServer(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	g := newTestGovernor(s.db, &now)

	insertSendEvent(t, s, "u1", "sent", now.Add(-10*24*time.Hour))
	insertSendEvent(t, s, "u1", "sent", now.Add(-time.Hour))

	g.startSendEventPruner()

	if got := countSendEvents(t, s); got != 1 {
		t.Errorf("%d eventos após o boot; quero 1 (só o recente)", got)
	}
}
