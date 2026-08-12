package server

import "testing"

func TestNormCurrency(t *testing.T) {
	cases := map[string]string{
		"usd":   "USD",
		" eur ": "EUR",
		"RON":   "RON",
		"US":    "",
		"USDT":  "",
		"US1":   "",
		"":      "",
	}
	for in, want := range cases {
		if got := normCurrency(in); got != want {
			t.Errorf("normCurrency(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseConvertArgs(t *testing.T) {
	cases := []struct {
		raw    string
		amount float64
		from   string
		to     string
	}{
		{`{"amount":1299,"from":"RON","to":"EUR"}`, 1299, "RON", "EUR"},
		// Missing amount means "what is the rate": one unit.
		{`{"from":"usd","to":"eur"}`, 1, "USD", "EUR"},
		// Aliases a weak model reaches for instead of the schema's names.
		{`{"value":"1,299.50","source":"ron","target":"eur"}`, 1299.5, "RON", "EUR"},
		// A symbol copied off the page must not fail the call.
		{`{"amount":"€249.99","from":"EUR","to":"USD"}`, 249.99, "EUR", "USD"},
		// A bad code is refused rather than escaped — it goes into an upstream URL.
		{`{"amount":10,"from":"dollars","to":"EUR"}`, 10, "", "EUR"},
		{`not json`, 0, "", ""},
	}
	for _, c := range cases {
		amount, from, to := parseConvertArgs(c.raw)
		if amount != c.amount || from != c.from || to != c.to {
			t.Errorf("parseConvertArgs(%s) = %v %q %q, want %v %q %q", c.raw, amount, from, to, c.amount, c.from, c.to)
		}
	}
}

func TestTrimNum(t *testing.T) {
	cases := map[float64]string{
		1299:      "1299.00",
		4.9712:    "4.97",
		0:         "0.00",
		0.0002145: "0.000215",
	}
	for in, want := range cases {
		if got := trimNum(in); got != want {
			t.Errorf("trimNum(%v) = %q, want %q", in, got, want)
		}
	}
}
