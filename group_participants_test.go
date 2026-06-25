package main

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type fakeGroupLIDStore struct {
	pnByLID map[string]types.JID
}

func (f fakeGroupLIDStore) PutManyLIDMappings(context.Context, []store.LIDMapping) error {
	return nil
}

func (f fakeGroupLIDStore) PutLIDMapping(context.Context, types.JID, types.JID) error {
	return nil
}

func (f fakeGroupLIDStore) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	return f.pnByLID[lid.String()], nil
}

func (f fakeGroupLIDStore) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, nil
}

func (f fakeGroupLIDStore) GetManyLIDsForPNs(context.Context, []types.JID) (map[types.JID]types.JID, error) {
	return nil, nil
}

type fakeContactStore struct {
	contactByJID map[string]types.ContactInfo
}

func (f fakeContactStore) PutPushName(context.Context, types.JID, string) (bool, string, error) {
	return false, "", nil
}

func (f fakeContactStore) PutBusinessName(context.Context, types.JID, string) (bool, string, error) {
	return false, "", nil
}

func (f fakeContactStore) PutContactName(context.Context, types.JID, string, string) error {
	return nil
}

func (f fakeContactStore) PutAllContactNames(context.Context, []store.ContactEntry) error {
	return nil
}

func (f fakeContactStore) PutManyRedactedPhones(context.Context, []store.RedactedPhoneEntry) error {
	return nil
}

func (f fakeContactStore) GetContact(_ context.Context, user types.JID) (types.ContactInfo, error) {
	return f.contactByJID[user.String()], nil
}

func (f fakeContactStore) GetAllContacts(context.Context) (map[types.JID]types.ContactInfo, error) {
	return nil, nil
}

func TestFillGroupParticipantPhoneNumbersFromLIDStore(t *testing.T) {
	lid := types.NewJID("12345", types.HiddenUserServer)
	pn := types.NewJID("5511999999999", types.DefaultUserServer)
	existingPN := types.NewJID("5531999999999", types.DefaultUserServer)

	client := &whatsmeow.Client{
		Store: &store.Device{
			LIDs: fakeGroupLIDStore{
				pnByLID: map[string]types.JID{
					lid.String(): pn,
				},
			},
		},
	}
	groupInfo := &types.GroupInfo{
		Participants: []types.GroupParticipant{
			{JID: lid, LID: lid},
			{JID: types.NewJID("67890", types.HiddenUserServer), LID: types.NewJID("67890", types.HiddenUserServer)},
			{JID: existingPN, PhoneNumber: existingPN},
		},
	}

	fillGroupParticipantPhoneNumbers(context.Background(), client, groupInfo)

	if got := groupInfo.Participants[0].PhoneNumber; got != pn {
		t.Fatalf("resolved PhoneNumber = %s, want %s", got, pn)
	}
	if got := groupInfo.Participants[1].PhoneNumber; !got.IsEmpty() {
		t.Fatalf("unknown LID PhoneNumber = %s, want empty", got)
	}
	if got := groupInfo.Participants[2].PhoneNumber; got != existingPN {
		t.Fatalf("existing PhoneNumber = %s, want unchanged %s", got, existingPN)
	}
}

func TestEnrichGroupInfoResolvesContactName(t *testing.T) {
	lid := types.NewJID("12345", types.HiddenUserServer)
	pn := types.NewJID("5511999999999", types.DefaultUserServer)
	pushOnly := types.NewJID("5531888888888", types.DefaultUserServer)
	anon := types.NewJID("67890", types.HiddenUserServer)

	client := &whatsmeow.Client{
		Store: &store.Device{
			LIDs: fakeGroupLIDStore{
				pnByLID: map[string]types.JID{lid.String(): pn},
			},
			Contacts: fakeContactStore{
				contactByJID: map[string]types.ContactInfo{
					// Resolved via phone number, FullName has priority over PushName.
					pn.String(): {Found: true, FullName: "João Silva", PushName: "Jô"},
					// Resolved via primary JID, falls back to PushName.
					pushOnly.String(): {Found: true, PushName: "Maria"},
				},
			},
		},
	}
	groupInfo := &types.GroupInfo{
		Participants: []types.GroupParticipant{
			{JID: lid, LID: lid},
			{JID: pushOnly, PhoneNumber: pushOnly},
			// Anonymous announcement user: native DisplayName must be preserved.
			{JID: anon, DisplayName: "anon"},
		},
	}

	enriched := enrichGroupInfo(context.Background(), client, groupInfo, make(map[types.JID]string))

	if got := enriched.Participants[0].ContactName; got != "João Silva" {
		t.Fatalf("ContactName[0] = %q, want %q", got, "João Silva")
	}
	if got := enriched.Participants[0].PhoneNumber; got != pn {
		t.Fatalf("PhoneNumber[0] = %s, want %s", got, pn)
	}
	if got := enriched.Participants[1].ContactName; got != "Maria" {
		t.Fatalf("ContactName[1] = %q, want %q", got, "Maria")
	}
	// DisplayName is never overwritten; the anonymous user keeps it and has no contact name.
	if got := enriched.Participants[2].DisplayName; got != "anon" {
		t.Fatalf("DisplayName[2] = %q, want unchanged %q", got, "anon")
	}
	if got := enriched.Participants[2].ContactName; got != "" {
		t.Fatalf("ContactName[2] = %q, want empty", got)
	}
}

// countingContactStore records how many times GetContact is called.
type countingContactStore struct {
	fakeContactStore
	calls *int
}

func (c countingContactStore) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	*c.calls++
	return c.fakeContactStore.GetContact(ctx, user)
}

func TestEnrichGroupInfoNameCacheAvoidsRedundantLookups(t *testing.T) {
	pn := types.NewJID("5511999999999", types.DefaultUserServer)
	calls := 0
	client := &whatsmeow.Client{
		Store: &store.Device{
			Contacts: countingContactStore{
				fakeContactStore: fakeContactStore{
					contactByJID: map[string]types.ContactInfo{
						pn.String(): {Found: true, FullName: "João Silva"},
					},
				},
				calls: &calls,
			},
		},
	}

	nameCache := make(map[types.JID]string)
	// Same participant appears across two groups; the contact store is hit once.
	for i := 0; i < 2; i++ {
		groupInfo := &types.GroupInfo{
			Participants: []types.GroupParticipant{{JID: pn, PhoneNumber: pn}},
		}
		enriched := enrichGroupInfo(context.Background(), client, groupInfo, nameCache)
		if got := enriched.Participants[0].ContactName; got != "João Silva" {
			t.Fatalf("ContactName = %q, want %q", got, "João Silva")
		}
	}

	if calls != 1 {
		t.Fatalf("GetContact calls = %d, want 1 (cache should dedupe across groups)", calls)
	}
}
