package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// recordingBusinessResolver stands in for client.GetUserInfo. It records every
// batch it receives and mirrors whatsmeow's behaviour: the verified name is not
// returned, it is written straight into the contact store.
type recordingBusinessResolver struct {
	batches [][]types.JID
	err     error
	names   map[string]string
	store   fakeContactStore
}

func (r *recordingBusinessResolver) resolve(_ context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	batch := make([]types.JID, len(jids))
	copy(batch, jids)
	r.batches = append(r.batches, batch)
	if r.err != nil {
		return nil, r.err
	}
	for _, jid := range jids {
		name, ok := r.names[jid.String()]
		if !ok {
			continue
		}
		contact := r.store.contactByJID[jid.String()]
		contact.Found = true
		contact.BusinessName = name
		r.store.contactByJID[jid.String()] = contact
	}
	return map[types.JID]types.UserInfo{}, nil
}

// newBusinessResolverClient wires a client whose contact store is the same map the
// resolver writes into, so a resolved name becomes readable exactly like in production.
func newBusinessResolverClient(contacts map[string]types.ContactInfo, names map[string]string) (*whatsmeow.Client, *recordingBusinessResolver) {
	contactStore := fakeContactStore{contactByJID: contacts}
	resolver := &recordingBusinessResolver{names: names, store: contactStore}
	client := &whatsmeow.Client{
		Store: &store.Device{
			LIDs:     fakeGroupLIDStore{},
			Contacts: contactStore,
		},
	}
	return client, resolver
}

func groupWith(participants ...types.GroupParticipant) *types.GroupInfo {
	return &types.GroupInfo{Participants: participants}
}

// P2 AC-1, AC-3, AC-4: only nameless participants with a resolved phone number
// enter the batch, and the verified name lands in ContactName.
func TestResolveMissingBusinessNamesFillsContactName(t *testing.T) {
	business := types.NewJID("5511777777777", types.DefaultUserServer)
	known := types.NewJID("5511999999999", types.DefaultUserServer)
	lidOnly := types.NewJID("12345", types.HiddenUserServer)

	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{
			known.String(): {Found: true, FullName: "João Silva"},
		},
		map[string]string{business.String(): "Padaria do Zé LTDA"},
	)

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(
			types.GroupParticipant{JID: business, PhoneNumber: business},
			types.GroupParticipant{JID: known, PhoneNumber: known},
			types.GroupParticipant{JID: lidOnly, LID: lidOnly},
		),
	}, resolver.resolve)

	if len(resolver.batches) != 1 {
		t.Fatalf("resolver batches = %d, want 1", len(resolver.batches))
	}
	if got, want := resolver.batches[0], []types.JID{business}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("batch = %v, want %v (named and phoneless participants must stay out)", got, want)
	}
	if got := groups[0].Participants[0].ContactName; got != "Padaria do Zé LTDA" {
		t.Errorf("ContactName[0] = %q, want %q", got, "Padaria do Zé LTDA")
	}
	if got := groups[0].Participants[1].ContactName; got != "João Silva" {
		t.Errorf("ContactName[1] = %q, want %q (existing name must survive)", got, "João Silva")
	}
	if got := groups[0].Participants[2].ContactName; got != "" {
		t.Errorf("ContactName[2] = %q, want empty (no phone number, no lookup)", got)
	}
}

// P2 AC-2: with no opt-in there is no resolver, so nothing is queried and the
// response carries exactly what the contact store already had.
func TestEnrichGroupListWithoutResolverQueriesNothing(t *testing.T) {
	business := types.NewJID("5511777777777", types.DefaultUserServer)
	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{},
		map[string]string{business.String(): "Padaria do Zé LTDA"},
	)

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(types.GroupParticipant{JID: business, PhoneNumber: business}),
	}, nil)

	if len(resolver.batches) != 0 {
		t.Fatalf("resolver batches = %d, want 0 (no opt-in means no network call)", len(resolver.batches))
	}
	if got := groups[0].Participants[0].ContactName; got != "" {
		t.Errorf("ContactName = %q, want empty", got)
	}
}

// P2 AC-5: the same participant across groups is queried once and named everywhere.
func TestResolveMissingBusinessNamesDedupesAcrossGroups(t *testing.T) {
	business := types.NewJID("5511777777777", types.DefaultUserServer)

	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{},
		map[string]string{business.String(): "Padaria do Zé LTDA"},
	)

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(types.GroupParticipant{JID: business, PhoneNumber: business}),
		groupWith(types.GroupParticipant{JID: business, PhoneNumber: business}),
	}, resolver.resolve)

	if len(resolver.batches) != 1 || len(resolver.batches[0]) != 1 {
		t.Fatalf("batches = %v, want a single batch with a single JID", resolver.batches)
	}
	for i := range groups {
		if got := groups[i].Participants[0].ContactName; got != "Padaria do Zé LTDA" {
			t.Errorf("group %d ContactName = %q, want %q", i, got, "Padaria do Zé LTDA")
		}
	}
}

// Edge case: two distinct participants sharing one phone number both get the name.
func TestResolveMissingBusinessNamesAppliesToEveryOccurrenceOfAPhone(t *testing.T) {
	phone := types.NewJID("5511777777777", types.DefaultUserServer)
	lidA := types.NewJID("11111", types.HiddenUserServer)
	lidB := types.NewJID("22222", types.HiddenUserServer)

	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{},
		map[string]string{phone.String(): "Padaria do Zé LTDA"},
	)

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(
			types.GroupParticipant{JID: lidA, LID: lidA, PhoneNumber: phone},
			types.GroupParticipant{JID: lidB, LID: lidB, PhoneNumber: phone},
		),
	}, resolver.resolve)

	if len(resolver.batches[0]) != 1 {
		t.Fatalf("batch = %v, want the shared phone queried once", resolver.batches[0])
	}
	for i := 0; i < 2; i++ {
		if got := groups[0].Participants[i].ContactName; got != "Padaria do Zé LTDA" {
			t.Errorf("ContactName[%d] = %q, want %q", i, got, "Padaria do Zé LTDA")
		}
	}
}

// P2 AC-6: above the cap only the first 64 are queried; the rest stay nameless.
func TestResolveMissingBusinessNamesCapsTheBatch(t *testing.T) {
	const total = maxBusinessNameLookups + 6

	participants := make([]types.GroupParticipant, 0, total)
	names := make(map[string]string, total)
	for i := 0; i < total; i++ {
		phone := types.NewJID(fmt.Sprintf("55117770000%02d", i), types.DefaultUserServer)
		participants = append(participants, types.GroupParticipant{JID: phone, PhoneNumber: phone})
		names[phone.String()] = fmt.Sprintf("Empresa %02d", i)
	}

	client, resolver := newBusinessResolverClient(map[string]types.ContactInfo{}, names)

	groups := enrichGroupList(context.Background(), client,
		[]*types.GroupInfo{groupWith(participants...)}, resolver.resolve)

	if len(resolver.batches) != 1 {
		t.Fatalf("resolver batches = %d, want 1", len(resolver.batches))
	}
	if got := len(resolver.batches[0]); got != maxBusinessNameLookups {
		t.Fatalf("batch size = %d, want %d", got, maxBusinessNameLookups)
	}
	for i := 0; i < maxBusinessNameLookups; i++ {
		if got, want := groups[0].Participants[i].ContactName, fmt.Sprintf("Empresa %02d", i); got != want {
			t.Fatalf("ContactName[%d] = %q, want %q", i, got, want)
		}
	}
	for i := maxBusinessNameLookups; i < total; i++ {
		if got := groups[0].Participants[i].ContactName; got != "" {
			t.Errorf("ContactName[%d] = %q, want empty (beyond the cap)", i, got)
		}
	}
}

// P2 AC-7: a failed lookup is best-effort — the names already known survive.
func TestResolveMissingBusinessNamesKeepsExistingNamesOnError(t *testing.T) {
	business := types.NewJID("5511777777777", types.DefaultUserServer)
	known := types.NewJID("5511999999999", types.DefaultUserServer)

	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{known.String(): {Found: true, FullName: "João Silva"}},
		map[string]string{business.String(): "Padaria do Zé LTDA"},
	)
	resolver.err = errors.New("usync failed")

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(
			types.GroupParticipant{JID: business, PhoneNumber: business},
			types.GroupParticipant{JID: known, PhoneNumber: known},
		),
	}, resolver.resolve)

	if got := groups[0].Participants[0].ContactName; got != "" {
		t.Errorf("ContactName[0] = %q, want empty after a failed lookup", got)
	}
	if got := groups[0].Participants[1].ContactName; got != "João Silva" {
		t.Errorf("ContactName[1] = %q, want %q", got, "João Silva")
	}
}

// P2 AC-8: no verified name means no name — nothing is invented.
func TestResolveMissingBusinessNamesLeavesUnresolvedEmpty(t *testing.T) {
	personal := types.NewJID("5511777777777", types.DefaultUserServer)

	client, resolver := newBusinessResolverClient(map[string]types.ContactInfo{}, map[string]string{})

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(types.GroupParticipant{JID: personal, PhoneNumber: personal}),
	}, resolver.resolve)

	if len(resolver.batches) != 1 {
		t.Fatalf("resolver batches = %d, want 1", len(resolver.batches))
	}
	if got := groups[0].Participants[0].ContactName; got != "" {
		t.Errorf("ContactName = %q, want empty", got)
	}
}

// Edge case: nobody is missing a name, so there is nothing to query.
func TestResolveMissingBusinessNamesSkipsWhenNobodyIsMissing(t *testing.T) {
	known := types.NewJID("5511999999999", types.DefaultUserServer)

	client, resolver := newBusinessResolverClient(
		map[string]types.ContactInfo{known.String(): {Found: true, FullName: "João Silva"}},
		map[string]string{},
	)

	enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(types.GroupParticipant{JID: known, PhoneNumber: known}),
	}, resolver.resolve)

	if len(resolver.batches) != 0 {
		t.Fatalf("resolver batches = %d, want 0 (nothing missing, nothing to query)", len(resolver.batches))
	}
}

// Edge case: an empty group list resolves nothing and returns an empty slice.
func TestEnrichGroupListWithNoGroupsIsANoOp(t *testing.T) {
	client, resolver := newBusinessResolverClient(map[string]types.ContactInfo{}, map[string]string{})

	groups := enrichGroupList(context.Background(), client, nil, resolver.resolve)

	if len(groups) != 0 {
		t.Errorf("groups = %d, want 0", len(groups))
	}
	if len(resolver.batches) != 0 {
		t.Errorf("resolver batches = %d, want 0", len(resolver.batches))
	}
}

// P2 AC-2: only the exact opt-in value turns the lookup on.
func TestBusinessNameResolverForOnlyOnExactOptIn(t *testing.T) {
	client := &whatsmeow.Client{}
	cases := []struct {
		query string
		want  bool
	}{
		{"", false},
		{"?resolveBusiness=", false},
		{"?resolveBusiness=false", false},
		{"?resolveBusiness=1", false},
		{"?resolveBusiness=TRUE", false},
		{"?resolveBusiness=true", true},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/group/info"+tc.query, nil)
		if got := businessNameResolverFor(req, client) != nil; got != tc.want {
			t.Errorf("businessNameResolverFor(%q) resolver != nil = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// P2 AC-2: the opt-in does not change how the route itself behaves.
func TestGroupInfoEndpointAcceptsResolveBusiness(t *testing.T) {
	s := makeTestServer(t)
	const token = "tok-group-business"
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, connected) VALUES ($1,$2,$3,$4)`,
		"u-gb", "tester", token, 0); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/group/info?groupJID=120362023605733675@g.us&resolveBusiness=true", nil)
	req.Header.Set("token", token)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Fatalf("route /group/info not registered (404)")
	}
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("auth failed for a valid token (401): %s", rr.Body.String())
	}
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "no session") {
		t.Errorf("expected 500 \"no session\"; got %d: %s", rr.Code, rr.Body.String())
	}
}

// Edge case: no contact store means no resolution path — and no panic.
func TestResolveMissingBusinessNamesWithoutContactStore(t *testing.T) {
	phone := types.NewJID("5511777777777", types.DefaultUserServer)
	resolver := &recordingBusinessResolver{names: map[string]string{}, store: fakeContactStore{contactByJID: map[string]types.ContactInfo{}}}
	client := &whatsmeow.Client{Store: &store.Device{}}

	groups := enrichGroupList(context.Background(), client, []*types.GroupInfo{
		groupWith(types.GroupParticipant{JID: phone, PhoneNumber: phone}),
	}, resolver.resolve)

	if len(resolver.batches) != 0 {
		t.Errorf("resolver batches = %d, want 0", len(resolver.batches))
	}
	if got := groups[0].Participants[0].ContactName; got != "" {
		t.Errorf("ContactName = %q, want empty", got)
	}
}
