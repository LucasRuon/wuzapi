package main

import (
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"google.golang.org/protobuf/proto"
)

func TestParseListType(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    waSyncAction.LabelEditAction_ListType
		wantErr bool
	}{
		{"empty defaults to CUSTOM", "", waSyncAction.LabelEditAction_CUSTOM, false},
		{"whitespace defaults to CUSTOM", "   ", waSyncAction.LabelEditAction_CUSTOM, false},
		{"exact CUSTOM", "CUSTOM", waSyncAction.LabelEditAction_CUSTOM, false},
		{"lowercase", "custom", waSyncAction.LabelEditAction_CUSTOM, false},
		{"mixed case", "Unread", waSyncAction.LabelEditAction_UNREAD, false},
		{"trimmed", "  favorites  ", waSyncAction.LabelEditAction_FAVORITES, false},
		{"new type ARCHIVED", "archived", waSyncAction.LabelEditAction_ARCHIVED, false},
		{"new type LOCKED", "LOCKED", waSyncAction.LabelEditAction_LOCKED, false},
		{"new type INVITES", "invites", waSyncAction.LabelEditAction_INVITES, false},
		{"new type THIRD_PARTY", "THIRD_PARTY", waSyncAction.LabelEditAction_THIRD_PARTY, false},
		{"unknown is error", "bogus", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseListType(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got none (value %v)", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseListType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildTypedListEditMirrorsLabelEditMutation(t *testing.T) {
	order := int32(2)
	active := true
	patch := buildTypedListEdit("crm-hot", "Lead quente", 3, false,
		waSyncAction.LabelEditAction_CUSTOM, &order, &active)

	if patch.Type != appstate.WAPatchRegular {
		t.Fatalf("patch.Type = %v, want WAPatchRegular", patch.Type)
	}
	if len(patch.Mutations) != 1 {
		t.Fatalf("len(Mutations) = %d, want 1", len(patch.Mutations))
	}
	m := patch.Mutations[0]

	wantIndex := []string{appstate.IndexLabelEdit, "crm-hot"}
	if len(m.Index) != len(wantIndex) || m.Index[0] != wantIndex[0] || m.Index[1] != wantIndex[1] {
		t.Fatalf("Index = %v, want %v", m.Index, wantIndex)
	}
	if m.Version != 3 {
		t.Fatalf("Version = %d, want 3 (must match whatsmeow's newLabelEditMutation)", m.Version)
	}

	action := m.Value.GetLabelEditAction()
	if action == nil {
		t.Fatal("LabelEditAction is nil")
	}
	if action.GetName() != "Lead quente" || action.GetColor() != 3 || action.GetDeleted() {
		t.Fatalf("name/color/deleted not set correctly: %+v", action)
	}
	if action.GetType() != waSyncAction.LabelEditAction_CUSTOM {
		t.Fatalf("Type = %v, want CUSTOM", action.GetType())
	}
	if action.OrderIndex == nil || action.GetOrderIndex() != 2 {
		t.Fatalf("OrderIndex = %v, want 2", action.OrderIndex)
	}
	if action.IsActive == nil || !action.GetIsActive() {
		t.Fatalf("IsActive = %v, want true", action.IsActive)
	}
}

func TestBuildTypedListEditAppliesDefaultsWhenNil(t *testing.T) {
	// Omitidos, isActive/orderIndex recebem os defaults que fazem a lista colar no
	// contato (espelham buildActiveLabelEdit): IsActive=true e OrderIndex>0 derivado
	// do id numérico.
	t.Run("numeric id derives orderIndex and active", func(t *testing.T) {
		action := buildTypedListEdit("42", "Lead", 0, false,
			waSyncAction.LabelEditAction_CUSTOM, nil, nil).Mutations[0].Value.GetLabelEditAction()
		if action.IsActive == nil || !action.GetIsActive() {
			t.Fatalf("IsActive default = %v, want true", action.IsActive)
		}
		if action.OrderIndex == nil || action.GetOrderIndex() != 42 {
			t.Fatalf("OrderIndex default = %v, want 42 (derived from id)", action.OrderIndex)
		}
	})
	t.Run("non-numeric id stays active with nil orderIndex", func(t *testing.T) {
		action := buildTypedListEdit("crm-hot", "Lead", 0, false,
			waSyncAction.LabelEditAction_CUSTOM, nil, nil).Mutations[0].Value.GetLabelEditAction()
		if action.IsActive == nil || !action.GetIsActive() {
			t.Fatalf("IsActive default = %v, want true", action.IsActive)
		}
		if action.OrderIndex != nil {
			t.Fatalf("OrderIndex = %v, want nil for non-numeric id", action.OrderIndex)
		}
	})
}

func TestBuildActiveLabelEditEqualsTypedCustom(t *testing.T) {
	// buildActiveLabelEdit (path /chat/label/*) deve produzir exatamente o mesmo
	// mutation que buildTypedListEdit com CUSTOM e defaults — garante que etiquetas
	// e listas CUSTOM nunca divirjam.
	got := buildActiveLabelEdit("42", "Lead", 3, false).Mutations[0].Value
	want := buildTypedListEdit("42", "Lead", 3, false, waSyncAction.LabelEditAction_CUSTOM, nil, nil).Mutations[0].Value
	if !proto.Equal(got, want) {
		t.Fatalf("buildActiveLabelEdit != buildTypedListEdit(CUSTOM, nil, nil)\n got=%v\nwant=%v", got, want)
	}
}
