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
