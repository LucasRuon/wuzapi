package main

import (
	"reflect"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestWhatsAppLabelSnapshotBuildResponseFiltersInactiveStateByDefault(t *testing.T) {
	snapshot := newWhatsAppLabelSnapshot()

	labelType := waSyncAction.LabelEditAction_CUSTOM
	hotName := "Lead quente"
	hotColor := int32(3)
	hotDeleted := false
	hotActive := true
	hotOrder := int32(7)
	oldName := "Antiga"
	oldColor := int32(1)
	oldDeleted := true
	oldActive := false
	now := time.Date(2026, 6, 12, 16, 50, 0, 0, time.UTC)

	snapshot.applyEvent(&events.LabelEdit{
		Timestamp:    now,
		LabelID:      "crm-hot",
		FromFullSync: true,
		Action: &waSyncAction.LabelEditAction{
			Name:       &hotName,
			Color:      &hotColor,
			Deleted:    &hotDeleted,
			IsActive:   &hotActive,
			OrderIndex: &hotOrder,
			Type:       &labelType,
		},
	})
	snapshot.applyEvent(&events.LabelEdit{
		Timestamp:    now,
		LabelID:      "crm-old",
		FromFullSync: true,
		Action: &waSyncAction.LabelEditAction{
			Name:     &oldName,
			Color:    &oldColor,
			Deleted:  &oldDeleted,
			IsActive: &oldActive,
		},
	})

	jid1 := types.NewJID("5511999999999", types.DefaultUserServer)
	jid2 := types.NewJID("5511888888888", types.DefaultUserServer)
	labeled := true
	unlabeled := false
	snapshot.applyEvent(&events.LabelAssociationChat{
		JID:          jid1,
		Timestamp:    now,
		LabelID:      "crm-hot",
		FromFullSync: true,
		Action:       &waSyncAction.LabelAssociationAction{Labeled: &labeled},
	})
	snapshot.applyEvent(&events.LabelAssociationChat{
		JID:          jid2,
		Timestamp:    now,
		LabelID:      "crm-hot",
		FromFullSync: true,
		Action:       &waSyncAction.LabelAssociationAction{Labeled: &unlabeled},
	})

	defaultResponse := snapshot.buildResponse(false, false, now)
	if len(defaultResponse.Labels) != 1 {
		t.Fatalf("default labels length = %d, want 1: %#v", len(defaultResponse.Labels), defaultResponse.Labels)
	}
	if defaultResponse.Labels[0].ID != "crm-hot" {
		t.Fatalf("default label ID = %q, want crm-hot", defaultResponse.Labels[0].ID)
	}
	if len(defaultResponse.Labels[0].ChatJIDs) != 1 || defaultResponse.Labels[0].ChatJIDs[0] != jid1.String() {
		t.Fatalf("default label chat JIDs = %#v, want [%s]", defaultResponse.Labels[0].ChatJIDs, jid1.String())
	}
	if len(defaultResponse.ChatAssociations) != 1 {
		t.Fatalf("default associations length = %d, want 1: %#v", len(defaultResponse.ChatAssociations), defaultResponse.ChatAssociations)
	}
	if defaultResponse.ChatAssociations[0].JID != jid1.String() {
		t.Fatalf("default association JID = %q, want %s", defaultResponse.ChatAssociations[0].JID, jid1.String())
	}

	fullResponse := snapshot.buildResponse(true, true, now)
	if len(fullResponse.Labels) != 2 {
		t.Fatalf("full labels length = %d, want 2: %#v", len(fullResponse.Labels), fullResponse.Labels)
	}
	if len(fullResponse.ChatAssociations) != 2 {
		t.Fatalf("full associations length = %d, want 2: %#v", len(fullResponse.ChatAssociations), fullResponse.ChatAssociations)
	}
}

func TestWhatsAppLabelSnapshotChatLabelIDs(t *testing.T) {
	snapshot := newWhatsAppLabelSnapshot()

	now := time.Date(2026, 6, 12, 16, 50, 0, 0, time.UTC)
	target := types.NewJID("5511999999999", types.DefaultUserServer)
	other := types.NewJID("5511888888888", types.DefaultUserServer)
	labeled := true
	unlabeled := false

	// target: label "10" active, "20" active, "30" explicitly removed.
	snapshot.applyEvent(&events.LabelAssociationChat{JID: target, Timestamp: now, LabelID: "20", Action: &waSyncAction.LabelAssociationAction{Labeled: &labeled}})
	snapshot.applyEvent(&events.LabelAssociationChat{JID: target, Timestamp: now, LabelID: "10", Action: &waSyncAction.LabelAssociationAction{Labeled: &labeled}})
	snapshot.applyEvent(&events.LabelAssociationChat{JID: target, Timestamp: now, LabelID: "30", Action: &waSyncAction.LabelAssociationAction{Labeled: &unlabeled}})
	// other chat must not leak into target's set.
	snapshot.applyEvent(&events.LabelAssociationChat{JID: other, Timestamp: now, LabelID: "99", Action: &waSyncAction.LabelAssociationAction{Labeled: &labeled}})

	got := snapshot.chatLabelIDs(target.String())
	want := []string{"10", "20"} // sorted, only active, only this JID
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chatLabelIDs(%s) = %#v, want %#v", target.String(), got, want)
	}

	if empty := snapshot.chatLabelIDs("000@s.whatsapp.net"); len(empty) != 0 {
		t.Fatalf("chatLabelIDs(unknown) = %#v, want empty", empty)
	}
}

func TestDiffChatLabels(t *testing.T) {
	cases := []struct {
		name             string
		current, desired []string
		wantAdd, wantRem []string
	}{
		{"add and remove", []string{"10", "20"}, []string{"20", "30"}, []string{"30"}, []string{"10"}},
		{"idempotent no-op", []string{"10", "20"}, []string{"20", "10"}, []string{}, []string{}},
		{"add all from empty", nil, []string{"10", "20"}, []string{"10", "20"}, []string{}},
		{"clear all", []string{"10", "20"}, nil, []string{}, []string{"10", "20"}},
		{"dedup and ignore empty", []string{"10", "10", ""}, []string{"10", "", "20", "20"}, []string{"20"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAdd, gotRem := diffChatLabels(tc.current, tc.desired)
			if !reflect.DeepEqual(gotAdd, tc.wantAdd) {
				t.Errorf("toAdd = %#v, want %#v", gotAdd, tc.wantAdd)
			}
			if !reflect.DeepEqual(gotRem, tc.wantRem) {
				t.Errorf("toRemove = %#v, want %#v", gotRem, tc.wantRem)
			}
		})
	}
}
