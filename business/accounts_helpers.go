package business

// BalanceFloat returns Balance parsed as a float64. The bool is false if
// the balance is unset or not numeric.
//
// Balance is emitted as json.Number by revogen so the exact decimal
// string Revolut returned is preserved; call this helper when an
// approximate float is acceptable.
func (a Account) BalanceFloat() (float64, bool) {
	if a.Balance == "" {
		return 0, false
	}
	v, err := a.Balance.Float64()
	if err != nil {
		return 0, false
	}
	return v, true
}
