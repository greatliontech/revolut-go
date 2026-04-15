package core

import (
	"encoding/json"
	"testing"
)

func TestMoneyUnmarshal_NumberAndString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Money
	}{
		{"number", `{"amount":9000.6,"currency":"GBP"}`, Money{Amount: "9000.6", Currency: "GBP"}},
		{"integer", `{"amount":100,"currency":"EUR"}`, Money{Amount: "100", Currency: "EUR"}},
		{"string", `{"amount":"12.34","currency":"USD"}`, Money{Amount: "12.34", Currency: "USD"}},
		{"null", `null`, Money{}},
		{"null_amount", `{"amount":null,"currency":"GBP"}`, Money{Currency: "GBP"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got Money
			if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMoneyMarshal_EmitsNumber(t *testing.T) {
	t.Parallel()
	m := Money{Amount: "9000.60", Currency: "GBP"}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"amount":9000.60,"currency":"GBP"}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestMoneyMarshal_Zero(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(Money{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != "null" {
		t.Fatalf("got %s, want null", got)
	}
}

func TestMoneyMarshal_InvalidAmount(t *testing.T) {
	t.Parallel()
	if _, err := json.Marshal(Money{Amount: "not-a-number", Currency: "GBP"}); err == nil {
		t.Fatal("expected error for invalid amount, got nil")
	}
}

func TestMoneyRoundtrip(t *testing.T) {
	t.Parallel()
	in := Money{Amount: "42.50", Currency: "EUR"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Money
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in != out {
		t.Fatalf("roundtrip mismatch: in=%+v out=%+v", in, out)
	}
}
