package names

import "testing"

func TestTypeName(t *testing.T) {
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
		if got := TypeName(c.in); got != c.want {
			t.Errorf("TypeName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestParamName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"account_id", "accountID"},
		{"cardInvitationId", "cardInvitationID"},
		{"id", "id"},
		{"URL", "url"},
		{"api_key", "apiKey"},
	}
	for _, c := range cases {
		if got := ParamName(c.in); got != c.want {
			t.Errorf("ParamName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalParamKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"card_invitation_id", "cardinvitationid"},
		{"cardInvitationId", "cardinvitationid"},
		{"card-invitation-id", "cardinvitationid"},
		{"ID", "id"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CanonicalParamKey(c.in); got != c.want {
			t.Errorf("CanonicalParamKey(%q) = %q; want %q", c.in, got, c.want)
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
		if got := CamelToKebab(c.in); got != c.want {
			t.Errorf("CamelToKebab(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestSingularise(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"accounts", "account"},
		{"categories", "category"},
		{"boxes", "box"},
		{"classes", "class"},
		{"ox", "ox"},
		{"data", "data"},
		{"ids", "id"},
	}
	for _, c := range cases {
		if got := Singularise(c.in); got != c.want {
			t.Errorf("Singularise(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestRegister(t *testing.T) {
	Register("xyz")
	// "xyz" is now an initialism → TypeName("xyz_value") keeps "XYZ"
	// capitalised.
	got := TypeName("xyz_value")
	if got != "XYZValue" {
		t.Errorf("Register didn't take effect: %q", got)
	}
}
