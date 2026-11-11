package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestBrazilianMobileVariants(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "celular BR com nono dígito gera variante sem o 9",
			input: "5541999198525",
			want:  []string{"5541999198525", "554199198525"},
		},
		{
			name:  "celular BR sem nono dígito gera variante com o 9",
			input: "554199198525",
			want:  []string{"554199198525", "5541999198525"},
		},
		{
			name:  "celular BR de outro DDD com o 9",
			input: "5511987654321",
			want:  []string{"5511987654321", "551187654321"},
		},
		{
			name:  "telefone fixo BR (8 dígitos começando com 3) não é elegível",
			input: "554133221100",
			want:  nil,
		},
		{
			name:  "número não brasileiro não é alterado",
			input: "12025550173",
			want:  nil,
		},
		{
			name:  "tamanho inválido retorna nil",
			input: "5541999",
			want:  nil,
		},
		{
			name:  "9 dígitos locais que não começam com 9 não é elegível",
			input: "5541899198525",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := brazilianMobileVariants(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("brazilianMobileVariants(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// fakeLIDStore só implementa GetPNForLID; os demais métodos da interface não
// são usados por resolveLIDRecipient.
type fakeLIDStore struct {
	store.LIDStore
	pn  types.JID
	err error
}

func (f fakeLIDStore) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	return f.pn, f.err
}

func TestResolveLIDRecipient(t *testing.T) {
	lid := types.NewJID("78576594497566", types.HiddenUserServer)
	clientWith := func(f fakeLIDStore) *whatsmeow.Client {
		return &whatsmeow.Client{Store: &store.Device{LIDs: f}}
	}
	cases := []struct {
		name   string
		client *whatsmeow.Client
		want   types.JID
	}{
		{
			name:   "LID mapeado no store: usa o telefone registrado",
			client: clientWith(fakeLIDStore{pn: types.NewJID("554199198525", types.DefaultUserServer)}),
			want:   types.NewJID("554199198525", types.DefaultUserServer),
		},
		{
			name:   "LID sem mapeamento: envia direto para o LID",
			client: clientWith(fakeLIDStore{}),
			want:   lid,
		},
		{
			name:   "erro no store: envia direto para o LID",
			client: clientWith(fakeLIDStore{err: errors.New("db")}),
			want:   lid,
		},
		{
			name:   "sem client: envia direto para o LID",
			client: nil,
			want:   lid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveLIDRecipient(context.Background(), tc.client, lid); got != tc.want {
				t.Errorf("resolveLIDRecipient() = %v, want %v", got, tc.want)
			}
		})
	}
}
