package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testEncKey is a 32-byte AES-256 key for the encrypt/decrypt round-trips.
const testEncKey = "01234567890123456789012345678901"

// withEncryptionKey sets the package-level globalEncryptionKey for a test and
// restores it on cleanup.
func withEncryptionKey(t *testing.T) {
	t.Helper()
	prev := *globalEncryptionKey
	*globalEncryptionKey = testEncKey
	t.Cleanup(func() { *globalEncryptionKey = prev })
}

func hasEvent(events []string, e string) bool { return Find(events, e) }

// TestParseWebhooks covers R1: the column parser is the single source of truth,
// retro-compatible, and never panics on bad input.
func TestParseWebhooks(t *testing.T) {
	t.Run("empty yields no webhooks", func(t *testing.T) {
		if got := parseWebhooks("", "Message", nil); len(got) != 0 {
			t.Fatalf("len = %d; want 0", len(got))
		}
		if got := parseWebhooks("   ", "Message", nil); len(got) != 0 {
			t.Fatalf("blank len = %d; want 0", len(got))
		}
	})

	t.Run("legacy URL inherits events and hmac", func(t *testing.T) {
		key := []byte("enc-key-bytes")
		got := parseWebhooks("https://legacy.example/hook", "Message,ReadReceipt", key)
		if len(got) != 1 {
			t.Fatalf("len = %d; want 1", len(got))
		}
		tg := got[0]
		if tg.URL != "https://legacy.example/hook" {
			t.Errorf("URL = %q", tg.URL)
		}
		if !tg.Active {
			t.Errorf("legacy webhook should be active")
		}
		if !hasEvent(tg.Events, "Message") || !hasEvent(tg.Events, "ReadReceipt") {
			t.Errorf("Events = %v; want inherited Message,ReadReceipt", tg.Events)
		}
		if string(tg.EncryptedHmacKey) != string(key) {
			t.Errorf("EncryptedHmacKey = %q; want inherited %q", tg.EncryptedHmacKey, key)
		}
	})

	t.Run("array yields per-webhook config", func(t *testing.T) {
		b64 := base64.StdEncoding.EncodeToString([]byte("cipher-a"))
		raw := `[
			{"url":"https://a.example/hook","events":["Message","ReadReceipt"],"hmac_key":"` + b64 + `","active":true},
			{"url":"https://b.example/hook","events":["Message"]}
		]`
		got := parseWebhooks(raw, "Picture", nil)
		if len(got) != 2 {
			t.Fatalf("len = %d; want 2", len(got))
		}
		if got[0].URL != "https://a.example/hook" || string(got[0].EncryptedHmacKey) != "cipher-a" {
			t.Errorf("webhook[0] = %+v", got[0])
		}
		if !hasEvent(got[0].Events, "Message") || !hasEvent(got[0].Events, "ReadReceipt") {
			t.Errorf("webhook[0].Events = %v", got[0].Events)
		}
		if got[1].EncryptedHmacKey != nil {
			t.Errorf("webhook[1] should have no hmac key, got %v", got[1].EncryptedHmacKey)
		}
		if !got[1].Active {
			t.Errorf("webhook[1] should default to active")
		}
	})

	t.Run("empty or All events inherit instance events", func(t *testing.T) {
		raw := `[
			{"url":"https://a.example","events":[]},
			{"url":"https://b.example","events":["All"]}
		]`
		got := parseWebhooks(raw, "Message,Picture", nil)
		if len(got) != 2 {
			t.Fatalf("len = %d; want 2", len(got))
		}
		for i, tg := range got {
			if !hasEvent(tg.Events, "Message") || !hasEvent(tg.Events, "Picture") {
				t.Errorf("webhook[%d].Events = %v; want inherited Message,Picture", i, tg.Events)
			}
		}
	})

	t.Run("active false is preserved", func(t *testing.T) {
		raw := `[{"url":"https://a.example","events":["Message"],"active":false}]`
		got := parseWebhooks(raw, "", nil)
		if len(got) != 1 || got[0].Active {
			t.Fatalf("got %+v; want one inactive target", got)
		}
	})

	t.Run("malformed array does not panic and yields none", func(t *testing.T) {
		got := parseWebhooks(`[{"url": broken`, "Message", nil)
		if len(got) != 0 {
			t.Fatalf("len = %d; want 0 on malformed JSON", len(got))
		}
	})

	t.Run("entries without url are skipped", func(t *testing.T) {
		raw := `[{"url":"","events":["Message"]},{"url":"https://ok.example"}]`
		got := parseWebhooks(raw, "Message", nil)
		if len(got) != 1 || got[0].URL != "https://ok.example" {
			t.Fatalf("got %+v; want only the valid entry", got)
		}
	})
}

// TestWebhooksForEvent covers R2/R3: per-webhook gating + active filter, each
// target keeping its own HMAC key.
func TestWebhooksForEvent(t *testing.T) {
	targets := []WebhookTarget{
		{URL: "a", Events: []string{"Message"}, EncryptedHmacKey: []byte("k1"), Active: true},
		{URL: "b", Events: []string{"Message", "ReadReceipt"}, EncryptedHmacKey: []byte("k2"), Active: true},
		{URL: "c", Events: []string{"Message"}, Active: false},
	}

	t.Run("event matching two webhooks fans out to two", func(t *testing.T) {
		got := webhooksForEvent(targets, "Message", "user")
		if len(got) != 2 {
			t.Fatalf("len = %d; want 2 (c is inactive)", len(got))
		}
		// HMAC key per webhook is preserved.
		if string(got[0].EncryptedHmacKey) != "k1" || string(got[1].EncryptedHmacKey) != "k2" {
			t.Errorf("keys not preserved: %q, %q", got[0].EncryptedHmacKey, got[1].EncryptedHmacKey)
		}
	})

	t.Run("event matching one webhook fans out to one", func(t *testing.T) {
		got := webhooksForEvent(targets, "ReadReceipt", "user")
		if len(got) != 1 || got[0].URL != "b" {
			t.Fatalf("got %+v; want only b", got)
		}
	})

	t.Run("inactive webhook never fires", func(t *testing.T) {
		only := []WebhookTarget{{URL: "c", Events: []string{"Message"}, Active: false}}
		if got := webhooksForEvent(only, "Message", "user"); len(got) != 0 {
			t.Fatalf("len = %d; want 0", len(got))
		}
	})
}

// TestComputeUnion covers R4/R7: users.events stays coherent with the fan-out.
func TestComputeUnion(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	cases := []struct {
		name   string
		inputs []webhookInput
		want   string
	}{
		{"distinct events unite and dedup",
			[]webhookInput{{URL: "a", Events: []string{"Message"}}, {URL: "b", Events: []string{"Message", "ReadReceipt"}}},
			"Message,ReadReceipt"},
		{"any inheriting webhook collapses to All",
			[]webhookInput{{URL: "a", Events: []string{"Message"}}, {URL: "b", Events: nil}},
			"All"},
		{"explicit All collapses to All",
			[]webhookInput{{URL: "a", Events: []string{"All"}, Active: bp(true)}},
			"All"},
		{"unsupported types are dropped",
			[]webhookInput{{URL: "a", Events: []string{"Message", "Bogus"}}},
			"Message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeUnion(tc.inputs); got != tc.want {
				t.Fatalf("computeUnion = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestEncodeWebhooks covers R4/R8: limits, validation, HMAC encryption at rest.
func TestEncodeWebhooks(t *testing.T) {
	withEncryptionKey(t)

	t.Run("cap exceeded is rejected", func(t *testing.T) {
		inputs := make([]webhookInput, maxWebhooksPerInstance+1)
		for i := range inputs {
			inputs[i] = webhookInput{URL: "https://x.example"}
		}
		if _, _, err := encodeWebhooks(inputs); err != errTooManyWebhooks {
			t.Fatalf("err = %v; want errTooManyWebhooks", err)
		}
	})

	t.Run("empty url is rejected", func(t *testing.T) {
		if _, _, err := encodeWebhooks([]webhookInput{{URL: "  "}}); err != errEmptyWebhookURL {
			t.Fatalf("err = %v; want errEmptyWebhookURL", err)
		}
	})

	t.Run("hmac key is encrypted at rest and round-trips", func(t *testing.T) {
		col, union, err := encodeWebhooks([]webhookInput{
			{URL: "https://a.example", Events: []string{"Message"}, HmacKey: "super-secret"},
			{URL: "https://b.example", Events: []string{"ReadReceipt"}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if union != "Message,ReadReceipt" {
			t.Errorf("union = %q", union)
		}
		if strings.Contains(col, "super-secret") {
			t.Fatalf("plaintext key leaked into column: %s", col)
		}
		// Re-parse and decrypt the first webhook's key back to plaintext.
		targets := parseWebhooks(col, union, nil)
		if len(targets) != 2 || len(targets[0].EncryptedHmacKey) == 0 {
			t.Fatalf("targets = %+v", targets)
		}
		plain, err := decryptHMACKey(targets[0].EncryptedHmacKey)
		if err != nil || plain != "super-secret" {
			t.Fatalf("decrypt = (%q, %v); want super-secret", plain, err)
		}
		if len(targets[1].EncryptedHmacKey) != 0 {
			t.Errorf("webhook[1] should be unsigned")
		}
	})
}

// envelopeData decodes the {code,success,data} envelope and returns data.
func envelopeData(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var env map[string]interface{}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	if env["success"] != true {
		t.Fatalf("success = %v (body=%s)", env["success"], body)
	}
	data, ok := env["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data is not an object (body=%s)", body)
	}
	return data
}

// TestWebhookArrayEndpoints covers R4/R5 end-to-end through the real router:
// PUT writes the array (encrypting keys, union events), GET reads it back with
// hmac_configured and without ever leaking the key.
func TestWebhookArrayEndpoints(t *testing.T) {
	withEncryptionKey(t)
	s := makeTestServer(t)
	const token = "tok-multi-webhook"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, connected) VALUES ($1,$2,$3,0)`,
		"u-mw", "tester", token); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// PUT the new array form with two webhooks (first signed, second not).
	putBody := `{"webhooks":[
		{"url":"https://a.example/hook","events":["Message"],"hmac_key":"secretAAA"},
		{"url":"https://b.example/hook","events":["ReadReceipt"],"active":true}
	]}`
	req := httptest.NewRequest(http.MethodPut, "/webhook", strings.NewReader(putBody))
	req.Header.Set("token", token)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secretAAA") {
		t.Fatalf("PUT response leaked the HMAC key: %s", rr.Body.String())
	}

	// Column holds the JSON array; users.events holds the union.
	var col, events string
	if err := s.db.QueryRow(`SELECT webhook, events FROM users WHERE id=$1`, "u-mw").Scan(&col, &events); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(col), "[") {
		t.Errorf("column is not a JSON array: %q", col)
	}
	if events != "Message,ReadReceipt" {
		t.Errorf("union events = %q; want Message,ReadReceipt", events)
	}

	// GET returns the webhooks list with hmac_configured, no key leak.
	getReq := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	getReq.Header.Set("token", token)
	getRR := httptest.NewRecorder()
	s.router.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status = %d; body=%s", getRR.Code, getRR.Body.String())
	}
	if strings.Contains(getRR.Body.String(), "secretAAA") {
		t.Fatalf("GET leaked the HMAC key: %s", getRR.Body.String())
	}

	data := envelopeData(t, getRR.Body.Bytes())
	hooks, ok := data["webhooks"].([]interface{})
	if !ok || len(hooks) != 2 {
		t.Fatalf("webhooks = %v; want 2 entries", data["webhooks"])
	}
	first := hooks[0].(map[string]interface{})
	second := hooks[1].(map[string]interface{})
	if first["hmac_configured"] != true {
		t.Errorf("webhook[0].hmac_configured = %v; want true", first["hmac_configured"])
	}
	if second["hmac_configured"] != false {
		t.Errorf("webhook[1].hmac_configured = %v; want false", second["hmac_configured"])
	}
	if _, leaked := first["hmac_key"]; leaked {
		t.Errorf("webhook[0] exposes hmac_key")
	}
	if data["webhook"] != "https://a.example/hook" {
		t.Errorf("legacy webhook field = %v; want first URL", data["webhook"])
	}
}

// TestWebhookArrayValidation covers R8 mapping to HTTP 400 via the handler.
func TestWebhookArrayValidation(t *testing.T) {
	withEncryptionKey(t)
	s := makeTestServer(t)
	const token = "tok-mw-validate"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, connected) VALUES ($1,$2,$3,0)`,
		"u-mwv", "tester", token); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/webhook",
		strings.NewReader(`{"webhooks":[{"url":"","events":["Message"]}]}`))
	req.Header.Set("token", token)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for empty url (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestRemoveStoredWebhookByURL covers the R6 read-modify-write core: survivors
// keep their stored shape (encrypted key, events) and the union is recomputed.
func TestRemoveStoredWebhookByURL(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	stored := []storedWebhook{
		{URL: "https://a.example", Events: []string{"Message"}, HmacKey: "Y2lwaGVy", Active: bp(true)},
		{URL: "https://b.example", Events: []string{"ReadReceipt"}, Active: bp(true)},
	}

	t.Run("removing one keeps the other verbatim", func(t *testing.T) {
		col, union, removed, err := removeStoredWebhookByURL(stored, "https://b.example")
		if err != nil || removed != 1 {
			t.Fatalf("removed=%d err=%v; want 1, nil", removed, err)
		}
		if union != "Message" {
			t.Errorf("union = %q; want Message", union)
		}
		var got []storedWebhook
		if err := json.Unmarshal([]byte(col), &got); err != nil {
			t.Fatalf("unmarshal column: %v", err)
		}
		if len(got) != 1 || got[0].URL != "https://a.example" || got[0].HmacKey != "Y2lwaGVy" {
			t.Fatalf("survivor not preserved: %+v", got)
		}
	})

	t.Run("removing the last yields empty column", func(t *testing.T) {
		single := []storedWebhook{{URL: "https://only.example", Events: []string{"Message"}}}
		col, union, removed, err := removeStoredWebhookByURL(single, "https://only.example")
		if err != nil || removed != 1 || col != "" || union != "" {
			t.Fatalf("got col=%q union=%q removed=%d err=%v; want empty,empty,1,nil", col, union, removed, err)
		}
	})

	t.Run("unknown url removes nothing", func(t *testing.T) {
		_, _, removed, err := removeStoredWebhookByURL(stored, "https://nope.example")
		if err != nil || removed != 0 {
			t.Fatalf("removed=%d err=%v; want 0, nil", removed, err)
		}
	})
}

// TestDeleteWebhookScoped covers R6 end-to-end: DELETE ?url=<u> removes one
// webhook (preserving the rest, including its encrypted key), 404s on unknown
// URLs, and clears the row when the last one is removed.
func TestDeleteWebhookScoped(t *testing.T) {
	withEncryptionKey(t)
	s := makeTestServer(t)
	const token = "tok-mw-delete"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, connected) VALUES ($1,$2,$3,0)`,
		"u-mwd", "tester", token); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/webhook", strings.NewReader(body))
		req.Header.Set("token", token)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
		}
	}
	del := func(rawURL string) *httptest.ResponseRecorder {
		t.Helper()
		path := "/webhook"
		if rawURL != "" {
			path += "?url=" + url.QueryEscape(rawURL)
		}
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("token", token)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		return rr
	}

	put(`{"webhooks":[
		{"url":"https://a.example/hook","events":["Message"],"hmac_key":"secretAAA"},
		{"url":"https://b.example/hook","events":["ReadReceipt"]}
	]}`)

	// Remove b: a survives with its HMAC intact; union shrinks to Message.
	rr := del("https://b.example/hook")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE b status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secretAAA") {
		t.Fatalf("DELETE leaked the HMAC key: %s", rr.Body.String())
	}
	data := envelopeData(t, rr.Body.Bytes())
	hooks := data["webhooks"].([]interface{})
	if len(hooks) != 1 {
		t.Fatalf("remaining webhooks = %d; want 1", len(hooks))
	}
	survivor := hooks[0].(map[string]interface{})
	if survivor["url"] != "https://a.example/hook" || survivor["hmac_configured"] != true {
		t.Errorf("survivor = %v; want a with hmac_configured true", survivor)
	}
	var col, events string
	if err := s.db.QueryRow(`SELECT webhook, events FROM users WHERE id=$1`, "u-mwd").Scan(&col, &events); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if events != "Message" {
		t.Errorf("union events after delete = %q; want Message", events)
	}

	// Unknown URL -> 404.
	if rr := del("https://ghost.example/hook"); rr.Code != http.StatusNotFound {
		t.Fatalf("DELETE unknown status = %d; want 404 (body=%s)", rr.Code, rr.Body.String())
	}

	// Remove the last one -> row cleared.
	if rr := del("https://a.example/hook"); rr.Code != http.StatusOK {
		t.Fatalf("DELETE last status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if err := s.db.QueryRow(`SELECT webhook, events FROM users WHERE id=$1`, "u-mwd").Scan(&col, &events); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if col != "" || events != "" {
		t.Errorf("after removing last: webhook=%q events=%q; want both empty", col, events)
	}
}

// TestDeleteWebhookAllClears covers the no-query DELETE: clears everything.
func TestDeleteWebhookAllClears(t *testing.T) {
	withEncryptionKey(t)
	s := makeTestServer(t)
	const token = "tok-mw-delall"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, webhook, events, connected) VALUES ($1,$2,$3,$4,$5,0)`,
		"u-mwda", "tester", token, "https://legacy.example/hook", "Message"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/webhook", nil)
	req.Header.Set("token", token)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var col, events string
	if err := s.db.QueryRow(`SELECT webhook, events FROM users WHERE id=$1`, "u-mwda").Scan(&col, &events); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if col != "" || events != "" {
		t.Errorf("after clear: webhook=%q events=%q; want both empty", col, events)
	}
}
