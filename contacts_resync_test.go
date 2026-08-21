package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// fetchAppStateCall records one FetchAppState invocation.
type fetchAppStateCall struct {
	name            appstate.WAPatchName
	fullSync        bool
	onlyIfNotSynced bool
}

type fakeAppStateFetcher struct {
	calls        []fetchAppStateCall
	err          error
	disconnected bool
}

func (f *fakeAppStateFetcher) IsConnected() bool { return !f.disconnected }

func (f *fakeAppStateFetcher) FetchAppState(_ context.Context, name appstate.WAPatchName, fullSync, onlyIfNotSynced bool) error {
	f.calls = append(f.calls, fetchAppStateCall{name: name, fullSync: fullSync, onlyIfNotSynced: onlyIfNotSynced})
	return f.err
}

// countingAllContactsStore counts GetAllContacts calls so a test can prove the
// count never runs when the sync itself failed.
type countingAllContactsStore struct {
	fakeContactStore
	all   map[types.JID]types.ContactInfo
	err   error
	calls int
}

func (s *countingAllContactsStore) GetAllContacts(context.Context) (map[types.JID]types.ContactInfo, error) {
	s.calls++
	return s.all, s.err
}

// CNR-01 / AC-1, AC-2: the contact patch is fully re-fetched and the store is
// counted after the sync.
func TestResyncContactsFetchesContactPatchAndCounts(t *testing.T) {
	fetcher := &fakeAppStateFetcher{}
	contacts := &countingAllContactsStore{all: map[types.JID]types.ContactInfo{
		types.NewJID("5511999999999", types.DefaultUserServer): {Found: true, FullName: "João Silva"},
		types.NewJID("5531888888888", types.DefaultUserServer): {Found: true, PushName: "Maria"},
	}}

	total, err := resyncContacts(context.Background(), fetcher, contacts)
	if err != nil {
		t.Fatalf("resyncContacts returned error: %v", err)
	}

	if len(fetcher.calls) != 1 {
		t.Fatalf("FetchAppState calls = %d, want 1", len(fetcher.calls))
	}
	call := fetcher.calls[0]
	if call.name != appstate.WAPatchCriticalUnblockLow {
		t.Errorf("patch = %q, want %q", call.name, appstate.WAPatchCriticalUnblockLow)
	}
	if !call.fullSync {
		t.Errorf("fullSync = false, want true")
	}
	if call.onlyIfNotSynced {
		t.Errorf("onlyIfNotSynced = true, want false")
	}
	if total != 2 {
		t.Errorf("contacts = %d, want 2", total)
	}
}

// CNR-02 / AC-5: a failed sync propagates the error and never counts contacts.
func TestResyncContactsPropagatesFetchError(t *testing.T) {
	fetchErr := errors.New("app state fetch failed")
	fetcher := &fakeAppStateFetcher{err: fetchErr}
	contacts := &countingAllContactsStore{all: map[types.JID]types.ContactInfo{
		types.NewJID("5511999999999", types.DefaultUserServer): {Found: true},
	}}

	total, err := resyncContacts(context.Background(), fetcher, contacts)
	if !errors.Is(err, fetchErr) {
		t.Fatalf("err = %v, want %v", err, fetchErr)
	}
	if total != 0 {
		t.Errorf("contacts = %d, want 0", total)
	}
	if contacts.calls != 0 {
		t.Errorf("GetAllContacts calls = %d, want 0 (must not count after a failed sync)", contacts.calls)
	}
}

// CNR-02 / AC-6: a failed count is surfaced even though the sync succeeded.
func TestResyncContactsPropagatesCountError(t *testing.T) {
	countErr := errors.New("contact store unavailable")
	fetcher := &fakeAppStateFetcher{}
	contacts := &countingAllContactsStore{err: countErr}

	total, err := resyncContacts(context.Background(), fetcher, contacts)
	if !errors.Is(err, countErr) {
		t.Fatalf("err = %v, want %v", err, countErr)
	}
	if total != 0 {
		t.Errorf("contacts = %d, want 0", total)
	}
	if len(fetcher.calls) != 1 {
		t.Errorf("FetchAppState calls = %d, want 1 (the sync itself ran)", len(fetcher.calls))
	}
}

// CNR-02 / AC-4: a disconnected session is refused before any app-state fetch.
func TestResyncContactsRefusesDisconnectedSession(t *testing.T) {
	fetcher := &fakeAppStateFetcher{disconnected: true}
	contacts := &countingAllContactsStore{}

	total, err := resyncContacts(context.Background(), fetcher, contacts)
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("err = %v, want %v", err, errNotConnected)
	}
	if total != 0 {
		t.Errorf("contacts = %d, want 0", total)
	}
	if len(fetcher.calls) != 0 {
		t.Errorf("FetchAppState calls = %d, want 0 (must not sync while disconnected)", len(fetcher.calls))
	}
	if contacts.calls != 0 {
		t.Errorf("GetAllContacts calls = %d, want 0", contacts.calls)
	}
}

// CNR-01 / AC-2: the success payload carries the flag and the counted total.
func TestFormatResyncContactsResult(t *testing.T) {
	got := formatResyncContactsResult(7)

	if got["success"] != true {
		t.Errorf("success = %v, want true", got["success"])
	}
	if got["contacts"] != 7 {
		t.Errorf("contacts = %v, want 7", got["contacts"])
	}
}

// CNR-02 / AC-3: the route is wired and authenticated; with no WhatsApp client in
// tests the handler reports "no session".
func TestResyncContactsEndpointNoSession(t *testing.T) {
	s := makeTestServer(t)
	const token = "tok-contacts-resync"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, connected) VALUES ($1,$2,$3,$4)`,
		"u-cr", "tester", token, 0); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/user/contacts/resync", nil)
	req.Header.Set("token", token)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Fatalf("route /user/contacts/resync not registered (404)")
	}
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("auth failed for a valid token (401): %s", rr.Body.String())
	}
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "no session") {
		t.Errorf("expected 500 \"no session\"; got %d: %s", rr.Code, rr.Body.String())
	}
}
