package business

import (
	"testing"
)

// TestApplyDefaults_FillsZero pins the opt-in default behaviour:
// a Params struct at its zero value gets the spec-declared default
// when ApplyDefaults is called; an explicit non-zero value is left
// alone.
func TestApplyDefaults_FillsZero(t *testing.T) {
	p := &GetAccountingCategoriesParams{}
	p.ApplyDefaults()
	if p.Limit != 100 {
		t.Errorf("Limit=%d; want 100", p.Limit)
	}
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	p := &GetAccountingCategoriesParams{Limit: 7}
	p.ApplyDefaults()
	if p.Limit != 7 {
		t.Errorf("Limit=%d; want 7 (explicit value overwritten)", p.Limit)
	}
}

func TestApplyDefaults_NilReceiverIsNoop(t *testing.T) {
	var p *GetAccountingCategoriesParams
	p.ApplyDefaults() // must not panic
}

// TestApplyDefaults_IntField verifies the `type: number,
// format: integer` quirk: Revolut types limits as number-with-int
// format, which the generator now maps to Go int rather than the
// less ergonomic json.Number.
func TestApplyDefaults_IntField(t *testing.T) {
	p := &GetCardInvitationsParams{}
	p.ApplyDefaults()
	if p.Limit != 100 {
		t.Errorf("Limit=%d; want 100", p.Limit)
	}
}
