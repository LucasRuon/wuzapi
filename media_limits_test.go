package main

import (
	"net/http"
	"testing"
)

func TestIsRetryableWebhookStatus(t *testing.T) {
	cases := map[int]bool{
		http.StatusBadRequest:            false,
		http.StatusUnauthorized:          false,
		http.StatusNotFound:              false,
		http.StatusRequestEntityTooLarge: false,
		http.StatusRequestTimeout:        true,
		http.StatusTooManyRequests:       true,
		http.StatusInternalServerError:   true,
		http.StatusBadGateway:            true,
		http.StatusServiceUnavailable:    true,
	}
	for status, want := range cases {
		if got := isRetryableWebhookStatus(status); got != want {
			t.Errorf("status %d: got retryable=%v, want %v", status, got, want)
		}
	}
}

func TestExceedsBase64Limit(t *testing.T) {
	orig := maxBase64MediaBytes
	t.Cleanup(func() { maxBase64MediaBytes = orig })

	maxBase64MediaBytes = 10 * 1024 * 1024
	if exceedsBase64Limit(10 * 1024 * 1024) {
		t.Error("tamanho igual ao limite nao deve exceder")
	}
	if !exceedsBase64Limit(10*1024*1024 + 1) {
		t.Error("tamanho acima do limite deve exceder")
	}
	if exceedsBase64Limit(0) {
		t.Error("tamanho desconhecido (0) nao deve exceder")
	}

	maxBase64MediaBytes = 0 // limite desativado
	if exceedsBase64Limit(1 << 40) {
		t.Error("com limite 0 nada deve exceder")
	}
}

func TestLoadMaxBase64MediaBytes(t *testing.T) {
	t.Setenv("WUZAPI_MAX_BASE64_MEDIA_MB", "25")
	if got := loadMaxBase64MediaBytes(); got != 25*1024*1024 {
		t.Errorf("got %d, want %d", got, 25*1024*1024)
	}
	t.Setenv("WUZAPI_MAX_BASE64_MEDIA_MB", "abc")
	if got := loadMaxBase64MediaBytes(); got != defaultMaxBase64MediaMB*1024*1024 {
		t.Errorf("valor invalido deve cair no padrao, got %d", got)
	}
	t.Setenv("WUZAPI_MAX_BASE64_MEDIA_MB", "0")
	if got := loadMaxBase64MediaBytes(); got != 0 {
		t.Errorf("0 deve desativar o limite, got %d", got)
	}
}
