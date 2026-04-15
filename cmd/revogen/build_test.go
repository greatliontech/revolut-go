package main

import (
	"testing"
)

func TestSingularise(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"accounts", "account"},
		{"categories", "category"},
		{"boxes", "box"},
		{"classes", "class"}, // -ses → strip 2 chars
		{"ox", "ox"},
		{"data", "data"},
		{"ids", "id"},
	}
	for _, c := range cases {
		if got := singularise(c.in); got != c.want {
			t.Errorf("singularise(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestStripTagPrefix(t *testing.T) {
	cases := []struct {
		name string
		segs []string
		tags []string
		want []string
	}{
		{"tag equals segment", []string{"accounts", "list"}, []string{"Accounts"}, []string{"list"}},
		{"singularised tag", []string{"transfer"}, []string{"Transfers"}, []string{}},
		{"multi-word tag", []string{"card-invitations", "cancel"}, []string{"CardInvitations"}, []string{"cancel"}},
		{"hyphen prefix", []string{"accounting-categories"}, []string{"Accounting"}, []string{"categories"}},
		{"no match", []string{"misc"}, []string{"Banking"}, []string{"misc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripTagPrefix(c.segs, c.tags)
			if len(got) != len(c.want) {
				t.Fatalf("got %v; want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v; want %v", got, c.want)
				}
			}
		})
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
		if got := canonicalParamKey(c.in); got != c.want {
			t.Errorf("canonicalParamKey(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestUnsetCond(t *testing.T) {
	named := map[string]*NamedType{
		"Currency":    {GoName: "Currency", Kind: KindAlias, AliasTarget: "string"},
		"State":       {GoName: "State", Kind: KindEnum, EnumBase: "string"},
		"IntEnum":     {GoName: "IntEnum", Kind: KindEnum, EnumBase: "int"},
		"NumberAlias": {GoName: "NumberAlias", Kind: KindAlias, AliasTarget: "json.Number"},
		"UnionX":      {GoName: "UnionX", Kind: KindUnion},
		"Nested":      {GoName: "Nested", Kind: KindStruct},
	}
	cases := []struct {
		goType string
		expr   string
		want   string
	}{
		{"string", "req.Foo", `req.Foo == ""`},
		{"json.Number", "req.Amount", `req.Amount == ""`},
		{"time.Time", "req.At", `req.At.IsZero()`},
		{"int", "req.N", `req.N == 0`},
		{"bool", "req.Flag", ""},
		{"*bool", "req.Flag", "req.Flag == nil"},
		{"*int64", "req.N", "req.N == nil"},
		{"[]string", "req.Tags", "len(req.Tags) == 0"},
		{"*Foo", "req.Foo", "req.Foo == nil"},
		{"core.Currency", "req.Cur", `req.Cur == ""`},
		{"Currency", "req.Cur", `req.Cur == ""`},
		{"State", "req.State", `req.State == ""`},
		{"IntEnum", "req.N", "req.N == 0"},
		{"NumberAlias", "req.X", `req.X == ""`},
		{"UnionX", "req.U", "req.U == nil"},
		{"Nested", "req.Nested", ""},
	}
	for _, c := range cases {
		if got := unsetCond(c.goType, c.expr, named); got != c.want {
			t.Errorf("unsetCond(%q, %q) = %q; want %q", c.goType, c.expr, got, c.want)
		}
	}
}

func TestSanitisePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/accounts/{id}/balance", "accounts//balance"},
		{"/", ""},
		{"/ping", "ping"},
		{"/a/{b}/c/{d}", "a//c/"},
	}
	for _, c := range cases {
		if got := sanitisePath(c.in); got != c.want {
			t.Errorf("sanitisePath(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestIsDateTimeFormat(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"date-time", true},
		{"date-time or date", true},
		{"date", true},
		{"uuid", false},
	}
	for _, c := range cases {
		if got := isDateTimeFormat(c.in); got != c.want {
			t.Errorf("isDateTimeFormat(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}
