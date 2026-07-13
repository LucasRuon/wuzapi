package main

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// GET /user/pn/{lid} resolves a LID back to its phone-number JID — the inverse of
// GET /user/lid/{jid}.
//
// Why it matters: HistorySync messages are addressed only by LID and carry no
// SenderAlt/RecipientAlt, so a consumer importing history has no phone number for
// the chat and cannot key it to a contact. These tests pin the resolution the
// handler depends on.

func clientWithLIDs(pnByLID map[string]types.JID) *whatsmeow.Client {
	return &whatsmeow.Client{
		Store: &store.Device{
			LIDs: fakeGroupLIDStore{pnByLID: pnByLID},
		},
	}
}

func TestGetCachedPNForLIDResolvesKnownLID(t *testing.T) {
	lid := types.NewJID("277270321213562", types.HiddenUserServer)
	pn := types.NewJID("5511975028321", types.DefaultUserServer)

	client := clientWithLIDs(map[string]types.JID{lid.String(): pn})

	got, err := getCachedPNForLID(context.Background(), client, lid)
	if err != nil {
		t.Fatalf("expected the LID to resolve, got error: %v", err)
	}
	if got.String() != pn.String() {
		t.Fatalf("expected phone JID %s, got %s", pn, got)
	}
}

// An unknown LID must be an explicit error, never the zero JID: silently returning
// an empty JID would let a caller key a chat to a contact with no phone number.
func TestGetCachedPNForLIDFailsOnUnknownLID(t *testing.T) {
	client := clientWithLIDs(map[string]types.JID{})

	if _, err := getCachedPNForLID(context.Background(), clientWithLIDs(nil), types.NewJID("1", types.HiddenUserServer)); err == nil {
		t.Fatal("expected an error for an unknown LID with a nil map, got nil")
	}

	if _, err := getCachedPNForLID(context.Background(), client, types.NewJID("999", types.HiddenUserServer)); err == nil {
		t.Fatal("expected an error for an unknown LID, got nil")
	}
}

func TestGetCachedPNForLIDFailsWithoutLIDStore(t *testing.T) {
	client := &whatsmeow.Client{Store: &store.Device{}}

	if _, err := getCachedPNForLID(context.Background(), client, types.NewJID("1", types.HiddenUserServer)); err == nil {
		t.Fatal("expected an error when the LID store is unavailable, got nil")
	}
}

// parseJIDNormalized must leave a LID untouched. It applies the Brazilian
// ninth-digit normalization, which is only meaningful for phone JIDs — if it
// rewrote the LID, the store lookup would miss.
func TestParseJIDNormalizedLeavesLIDUntouched(t *testing.T) {
	client := clientWithLIDs(nil)
	raw := "277270321213562@lid"

	jid, ok := parseJIDNormalized(client, raw)
	if !ok {
		t.Fatalf("expected %q to parse", raw)
	}
	if jid.String() != raw {
		t.Fatalf("expected the LID to survive parsing unchanged, got %s", jid)
	}
	if jid.Server != types.HiddenUserServer {
		t.Fatalf("expected server %q, got %q", types.HiddenUserServer, jid.Server)
	}
}
