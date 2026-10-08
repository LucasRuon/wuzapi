package main

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// Versão de cliente anunciada ao WhatsApp.
//
// O whatsmeow embute a versão no build e o WhatsApp recusa com 405 (Client
// outdated) quando ela sai da janela aceita — foi o que derrubou todas as
// sessões em 05/10/2026 com um whatsmeow de quatro meses. Aqui a versão vigente
// é lida de web.whatsapp.com na subida e a cada 6h, para a imagem não vencer
// sozinha. Isso cobre só o número de versão: mudança de protocolo continua
// exigindo atualizar o whatsmeow.

const waVersionRefreshInterval = 6 * time.Hour

// applyWAVersion adota latest só se for mais nova que a em uso. Nunca rebaixa:
// uma resposta estranha do site não pode deixar o cliente pior que o embutido.
func applyWAVersion(latest store.WAVersionContainer) bool {
	current := store.GetWAVersion()
	if !current.LessThan(latest) {
		return false
	}
	store.SetWAVersion(latest)
	log.Info().Str("from", current.String()).Str("to", latest.String()).Msg("WhatsApp client version updated")
	return true
}

// refreshWAVersion falha em silêncio de propósito: sem resposta do site, segue
// valendo a versão que já estava em uso.
func refreshWAVersion() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	latest, err := whatsmeow.GetLatestVersion(ctx, nil)
	if err != nil {
		log.Warn().Err(err).Str("current", store.GetWAVersion().String()).Msg("Could not fetch latest WhatsApp client version")
		return
	}
	applyWAVersion(*latest)
}

// startWAVersionRefresher faz a primeira leitura de forma síncrona, para que as
// sessões reconectadas na subida já saiam com a versão vigente.
func startWAVersionRefresher() {
	refreshWAVersion()
	go func() {
		ticker := time.NewTicker(waVersionRefreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			refreshWAVersion()
		}
	}()
}
