package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// Currency is an ISO 4217 currency code (e.g. "GBP", "EUR", "USD").
type Currency string

// Money is a decimal amount paired with a currency.
//
// Amount is stored as a decimal string ("9000.60") to avoid float rounding.
// Use [Money.Float64] when an approximation is acceptable.
//
// Revolut endpoints encode the amount as either a JSON number or a JSON
// string depending on the resource. Money's JSON codec accepts both forms
// on input and emits a JSON number on output.
type Money struct {
	Amount   string
	Currency Currency
}

// Float64 returns Amount parsed as a float64. The bool is false if Amount
// is empty or not numeric.
func (m Money) Float64() (float64, bool) {
	if m.Amount == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m.Amount, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (m Money) MarshalJSON() ([]byte, error) {
	if m.Amount == "" && m.Currency == "" {
		return []byte("null"), nil
	}
	amt := m.Amount
	if amt == "" {
		amt = "0"
	}
	if _, err := strconv.ParseFloat(amt, 64); err != nil {
		return nil, errors.New("revolut: Money.Amount is not a valid decimal: " + m.Amount)
	}
	cur, err := json.Marshal(string(m.Currency))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"amount":`)
	buf.WriteString(amt)
	buf.WriteString(`,"currency":`)
	buf.Write(cur)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (m *Money) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*m = Money{}
		return nil
	}
	var aux struct {
		Amount   json.RawMessage `json:"amount"`
		Currency Currency        `json:"currency"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Currency = aux.Currency
	raw := bytes.TrimSpace(aux.Amount)
	switch {
	case len(raw) == 0, bytes.Equal(raw, []byte("null")):
		m.Amount = ""
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		m.Amount = s
	default:
		m.Amount = string(raw)
	}
	return nil
}
