package validate

import (
	"encoding/json"
	"testing"
)

func TestIsUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"11111111-1111-1111-1111-111111111111", true},
		{"a1b2c3d4-e5f6-7890-abcd-ef0123456789", true},
		{"A1B2C3D4-E5F6-7890-ABCD-EF0123456789", true},
		{"", false},
		{"not-a-uuid", false},
		{"11111111-1111-1111-1111-11111111111", false},  // 35 chars
		{"11111111-1111-1111-1111-1111111111111", false}, // 37 chars
		{"11111111_1111-1111-1111-111111111111", false},  // wrong separator
		{"gggggggg-1111-1111-1111-111111111111", false}, // non-hex
		{"{11111111-1111-1111-1111-111111111111}", false},
	}
	for _, tc := range cases {
		if got := IsUUID(tc.in); got != tc.want {
			t.Errorf("IsUUID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	if !MatchPattern("^[A-Z]{3}$", "USD") {
		t.Error("USD should match ^[A-Z]{3}$")
	}
	if MatchPattern("^[A-Z]{3}$", "usd") {
		t.Error("usd should not match")
	}
	// cache hit branch (second call with same pattern)
	if !MatchPattern("^[A-Z]{3}$", "GBP") {
		t.Error("GBP should match (cache hit)")
	}
}

func TestMatchPattern_BadPattern(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("want panic on bad spec pattern")
		}
	}()
	MatchPattern("[unterminated", "x")
}

func TestNumberAsFloat(t *testing.T) {
	cases := []struct {
		in   json.Number
		want float64
	}{
		{"", 0},
		{"42", 42},
		{"3.14", 3.14},
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		if got := NumberAsFloat(tc.in); got != tc.want {
			t.Errorf("NumberAsFloat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHasJSONKey(t *testing.T) {
	probe := map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
	}
	if !HasJSONKey(probe, "a") {
		t.Error("present key not found")
	}
	if HasJSONKey(probe, "b") {
		t.Error("absent key found")
	}
}
