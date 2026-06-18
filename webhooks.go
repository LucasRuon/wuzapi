package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// maxWebhooksPerInstance caps how many webhooks a single instance may register
// (R8) — guards against abuse and unbounded fan-out per event.
const maxWebhooksPerInstance = 10

// Validation errors surfaced by encodeWebhooks; the caller maps these to HTTP 400.
var (
	errTooManyWebhooks = errors.New("too many webhooks")
	errEmptyWebhookURL = errors.New("webhook url cannot be empty")
)

// WebhookTarget is a single resolved webhook destination consumed by the
// dispatch fan-out. Events is already validated and resolved (it inherits the
// instance events when the webhook declares none or "All"). EncryptedHmacKey is
// the AES-GCM ciphertext (already base64-decoded) or nil when the webhook is
// unsigned.
type WebhookTarget struct {
	URL              string
	Events           []string
	EncryptedHmacKey []byte
	Active           bool
}

// storedWebhook is the at-rest JSON shape persisted inside users.webhook when
// the column holds the new array format. hmac_key is base64 of the AES-GCM
// ciphertext — never plaintext at rest.
type storedWebhook struct {
	URL     string   `json:"url"`
	Events  []string `json:"events,omitempty"`
	HmacKey string   `json:"hmac_key,omitempty"`
	Active  *bool    `json:"active,omitempty"`
}

// webhookInput is the API request shape for a single entry of the `webhooks`
// array on PUT/POST /webhook. hmac_key here is PLAINTEXT (encrypted at rest).
type webhookInput struct {
	URL     string   `json:"url"`
	Events  []string `json:"events,omitempty"`
	HmacKey string   `json:"hmac_key,omitempty"`
	Active  *bool    `json:"active,omitempty"`
}

// parseWebhooks is the SINGLE source of truth for interpreting the users.webhook
// column (R1). It never panics on bad input — malformed JSON is logged and
// yields an empty slice so one bad row cannot take dispatch down.
//
//   - empty/blank      -> [] (no webhook configured)
//   - starts with '['  -> parse JSON array of storedWebhook (invalid -> log + [])
//   - anything else    -> 1 legacy target inheriting fallbackEvents/fallbackHmac
func parseWebhooks(raw, fallbackEvents string, fallbackHmac []byte) []WebhookTarget {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []WebhookTarget{}
	}

	if trimmed[0] != '[' {
		// Legacy single URL: inherit the instance-level events and HMAC key.
		return []WebhookTarget{{
			URL:              raw,
			Events:           validateEvents(splitEvents(fallbackEvents)),
			EncryptedHmacKey: fallbackHmac,
			Active:           true,
		}}
	}

	var stored []storedWebhook
	if err := json.Unmarshal([]byte(trimmed), &stored); err != nil {
		log.Error().Err(err).Msg("Failed to parse webhooks JSON array; treating as no webhooks")
		return []WebhookTarget{}
	}

	targets := make([]WebhookTarget, 0, len(stored))
	for _, sw := range stored {
		url := strings.TrimSpace(sw.URL)
		if url == "" {
			continue // skip entries without a destination
		}

		var encKey []byte
		if sw.HmacKey != "" {
			decoded, err := base64.StdEncoding.DecodeString(sw.HmacKey)
			if err != nil {
				log.Error().Err(err).Str("url", url).Msg("Failed to decode webhook HMAC key; sending unsigned")
			} else {
				encKey = decoded
			}
		}

		active := true
		if sw.Active != nil {
			active = *sw.Active
		}

		targets = append(targets, WebhookTarget{
			URL:              url,
			Events:           resolveTargetEvents(sw.Events, fallbackEvents),
			EncryptedHmacKey: encKey,
			Active:           active,
		})
	}
	return targets
}

// splitEvents turns a CSV events string (the users.events form) into a trimmed,
// non-empty slice.
func splitEvents(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validateEvents keeps only supported event types (mirroring the discard-on-write
// behavior used elsewhere) and de-duplicates while preserving order.
func validateEvents(events []string) []string {
	var out []string
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !Find(supportedEventTypes, e) {
			log.Warn().Str("Type", e).Msg("Event type discarded")
			continue
		}
		if !Find(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// resolveTargetEvents resolves a webhook's own events list. Empty or containing
// "All" means "inherit the instance events"; otherwise the webhook's own
// validated list is used.
func resolveTargetEvents(own []string, fallbackEvents string) []string {
	valid := validateEvents(own)
	if len(valid) == 0 || Find(valid, "All") {
		return validateEvents(splitEvents(fallbackEvents))
	}
	return valid
}

// unionEvents returns the users.events string that keeps the top-level event
// filter coherent with the per-webhook fan-out (R4/R7). If any webhook inherits
// (declares no events) or explicitly wants "All", the union collapses to "All"
// so the top-level filter never drops an event some webhook wants. Otherwise it
// is the de-duplicated union of every webhook's validated events.
func unionEvents(eventLists [][]string) string {
	var union []string
	for _, evs := range eventLists {
		valid := validateEvents(evs)
		if len(valid) == 0 || Find(valid, "All") {
			return "All"
		}
		for _, e := range valid {
			if !Find(union, e) {
				union = append(union, e)
			}
		}
	}
	return strings.Join(union, ",")
}

// computeUnion is unionEvents over the events of every API webhook input.
func computeUnion(inputs []webhookInput) string {
	lists := make([][]string, len(inputs))
	for i, in := range inputs {
		lists[i] = in.Events
	}
	return unionEvents(lists)
}

// encodeWebhooks validates API webhook inputs and produces the at-rest column
// value (JSON array) plus the union events string (R4/R8). HMAC keys are
// encrypted here — plaintext in, base64 ciphertext at rest. Validation errors
// (errTooManyWebhooks / errEmptyWebhookURL) map to HTTP 400 at the caller;
// encryption errors map to 500.
func encodeWebhooks(inputs []webhookInput) (column string, union string, err error) {
	if len(inputs) > maxWebhooksPerInstance {
		return "", "", errTooManyWebhooks
	}

	stored := make([]storedWebhook, 0, len(inputs))
	for _, in := range inputs {
		url := strings.TrimSpace(in.URL)
		if url == "" {
			return "", "", errEmptyWebhookURL
		}

		hmacB64 := ""
		if strings.TrimSpace(in.HmacKey) != "" {
			enc, encErr := encryptHMACKey(in.HmacKey)
			if encErr != nil {
				return "", "", fmt.Errorf("failed to encrypt hmac key: %w", encErr)
			}
			hmacB64 = base64.StdEncoding.EncodeToString(enc)
		}

		active := true
		if in.Active != nil {
			active = *in.Active
		}
		activeCopy := active

		stored = append(stored, storedWebhook{
			URL:     url,
			Events:  validateEvents(in.Events),
			HmacKey: hmacB64,
			Active:  &activeCopy,
		})
	}

	colBytes, err := json.Marshal(stored)
	if err != nil {
		return "", "", err
	}
	return string(colBytes), computeUnion(inputs), nil
}

// summarizeWebhooks builds the safe public view of the configured webhooks for
// GET and write responses (R5): URL, resolved events, active flag and a boolean
// hmac_configured. The HMAC key itself is NEVER exposed.
func summarizeWebhooks(targets []WebhookTarget) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(targets))
	for _, t := range targets {
		out = append(out, map[string]interface{}{
			"url":             t.URL,
			"events":          t.Events,
			"active":          t.Active,
			"hmac_configured": len(t.EncryptedHmacKey) > 0,
		})
	}
	return out
}

// firstWebhookURL returns the first target URL (the legacy single-webhook view
// for old clients), or empty when there are none.
func firstWebhookURL(targets []WebhookTarget) string {
	if len(targets) > 0 {
		return targets[0].URL
	}
	return ""
}

// parseStoredWebhooks returns the at-rest array form of the column verbatim
// (preserving inherited events, encrypted keys and active flags). The bool is
// false when the column is not the new array form (empty, legacy single URL, or
// malformed JSON) — callers handle those cases separately.
func parseStoredWebhooks(raw string) ([]storedWebhook, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '[' {
		return nil, false
	}
	var stored []storedWebhook
	if err := json.Unmarshal([]byte(trimmed), &stored); err != nil {
		return nil, false
	}
	return stored, true
}

// removeStoredWebhookByURL drops every stored entry whose url matches target and
// re-serializes the survivors verbatim (R6), returning the new column value, the
// recomputed union events and how many entries were removed. When nothing
// remains both column and union are empty (caller clears the row). When nothing
// matched, removed is 0 and the caller responds 404.
func removeStoredWebhookByURL(stored []storedWebhook, target string) (column string, union string, removed int, err error) {
	kept := make([]storedWebhook, 0, len(stored))
	for _, sw := range stored {
		if strings.TrimSpace(sw.URL) == target {
			removed++
			continue
		}
		kept = append(kept, sw)
	}
	if removed == 0 || len(kept) == 0 {
		return "", "", removed, nil
	}

	lists := make([][]string, len(kept))
	for i, sw := range kept {
		lists[i] = sw.Events
	}
	colBytes, err := json.Marshal(kept)
	if err != nil {
		return "", "", removed, err
	}
	return string(colBytes), unionEvents(lists), removed, nil
}

// webhooksForEvent returns the active webhooks subscribed to eventType, applying
// the per-webhook gating (R2) and reusing checkIfSubscribedToEvent. Each returned
// target keeps its own HMAC key (R3). Pure/testable — does no network I/O.
func webhooksForEvent(targets []WebhookTarget, eventType, userID string) []WebhookTarget {
	out := make([]WebhookTarget, 0, len(targets))
	for _, t := range targets {
		if !t.Active {
			continue
		}
		if !checkIfSubscribedToEvent(t.Events, eventType, userID) {
			continue
		}
		out = append(out, t)
	}
	return out
}
