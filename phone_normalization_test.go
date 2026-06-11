package main

import (
	"reflect"
	"testing"
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
