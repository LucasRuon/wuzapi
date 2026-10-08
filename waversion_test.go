package main

import (
	"testing"

	"go.mau.fi/whatsmeow/store"
)

// A versão lida do site só pode subir a versão em uso, nunca rebaixar.
func TestApplyWAVersionOnlyUpgrades(t *testing.T) {
	embedded := store.GetWAVersion()
	t.Cleanup(func() { store.SetWAVersion(embedded) })

	older := store.WAVersionContainer{embedded[0], embedded[1], embedded[2] - 1}
	if applyWAVersion(older) {
		t.Fatalf("versão mais antiga %s foi aplicada sobre %s", older, embedded)
	}
	if applyWAVersion(embedded) {
		t.Fatalf("versão igual %s foi tratada como atualização", embedded)
	}
	if got := store.GetWAVersion(); got != embedded {
		t.Fatalf("versão em uso mudou para %s sem atualização", got)
	}

	newer := store.WAVersionContainer{embedded[0], embedded[1], embedded[2] + 1}
	if !applyWAVersion(newer) {
		t.Fatalf("versão mais nova %s não foi aplicada sobre %s", newer, embedded)
	}
	if got := store.GetWAVersion(); got != newer {
		t.Fatalf("versão em uso = %s, esperado %s", got, newer)
	}
}
