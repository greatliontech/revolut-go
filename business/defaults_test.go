package business

import (
	"encoding/json"
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

// TestApplyDefaults_JSONNumberField verifies the json.Number path:
// the default literal wraps the integer in json.Number so the
// assignment type-checks and the wire encoding stays integer.
func TestApplyDefaults_JSONNumberField(t *testing.T) {
	p := &GetCardInvitationsParams{}
	p.ApplyDefaults()
	if p.Limit != json.Number("100") {
		t.Errorf("Limit=%q; want 100", p.Limit)
	}
}
