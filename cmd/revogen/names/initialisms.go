package names

// defaultInitialisms returns the seed set the generator starts with.
// Put acronyms here rather than inline in names.go so the list is
// easy to scan and extend.
//
// Revolut uses region codes (UK, AU, RO, EUR) as discriminator tags;
// keeping them upper-case in generated identifiers matches the spec's
// intent and aligns with Go's convention that acronyms preserve case.
func defaultInitialisms() map[string]bool {
	return map[string]bool{
		"ID":    true,
		"URL":   true,
		"URI":   true,
		"API":   true,
		"HTTP":  true,
		"HTTPS": true,
		"UUID":  true,
		"ULID":  true,
		"IBAN":  true,
		"BIC":   true,
		"SWIFT": true,
		"JWT":   true,
		"JWS":   true,
		"JSON":  true,
		"XML":   true,
		"CSV":   true,
		"HTML":  true,
		"PDF":   true,
		"SEPA":  true,
		"ACH":   true,
		"VAT":   true,
		"KYC":   true,
		"AML":   true,
		"MFA":   true,
		"SMS":   true,
		"ISO":   true,
		"IP":    true,
		"TLS":   true,
		"FAPI":  true,
		"BSB":   true,
		"COP":   true,

		// Region / discriminator codes observed across vendored specs.
		"UK":  true,
		"AU":  true,
		"RO":  true,
		"EUR": true,
	}
}
