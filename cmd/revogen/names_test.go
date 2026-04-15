package main

import "testing"

func TestGoTypeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"account", "Account"},
		{"account_id", "AccountID"},
		{"http_url", "HTTPURL"},
		{"card-invitations", "CardInvitations"},
		{"Card Invitations", "CardInvitations"},
		{"ValidateAccountNameRequestUK", "ValidateAccountNameRequestUK"},
		{"validate_account_name_request_uk", "ValidateAccountNameRequestUK"},
		{"Day-Opening-Hours", "DayOpeningHours"},
		{"iban_code", "IBANCode"},
		{"vatNumber", "VATNumber"},
	}
	for _, c := range cases {
		if got := goTypeName(c.in); got != c.want {
			t.Errorf("goTypeName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestGoFieldName(t *testing.T) {
	if got := goFieldName("request_id"); got != "RequestID" {
		t.Errorf("goFieldName(request_id) = %q", got)
	}
	if got := goFieldName("created_at"); got != "CreatedAt" {
		t.Errorf("goFieldName(created_at) = %q", got)
	}
}

func TestGoParamName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"account_id", "accountID"},
		{"cardInvitationId", "cardInvitationID"},
		{"id", "id"},
		{"URL", "url"}, // param names are unexported, so initialism lowercased.
		{"api_key", "apiKey"},
	}
	for _, c := range cases {
		if got := goParamName(c.in); got != c.want {
			t.Errorf("goParamName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestSplitCamel(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Foo", []string{"Foo"}},
		{"FooBar", []string{"Foo", "Bar"}},
		{"fooBarBaz", []string{"foo", "Bar", "Baz"}},
		{"URLParser", []string{"URLParser"}},
	}
	for _, c := range cases {
		got := splitCamel(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCamel(%q) = %v; want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCamel(%q)[%d] = %q; want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestCamelToKebab(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"getAccount", "get-account"},
		{"createPaymentDraft", "create-payment-draft"},
		{"a", "a"},
	}
	for _, c := range cases {
		if got := camelToKebab(c.in); got != c.want {
			t.Errorf("camelToKebab(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
