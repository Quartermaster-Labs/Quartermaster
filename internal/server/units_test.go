package server

import (
	"math"
	"strings"
	"testing"
)

func TestConvertUnit(t *testing.T) {
	cases := []struct {
		amount   float64
		from, to string
		want     float64
	}{
		{1, "in", "cm", 2.54},
		{6, "ft", "m", 1.8288},
		{5, "kg", "lbs", 11.0231},
		{1, "US gal", "l", 3.7854},
		{100, "km/h", "mph", 62.1371},
		{1, "TB", "TiB", 0.9095}, // the 10% the whole unit exists to expose
		{1, "kWh", "J", 3.6e6},
		{20, "C", "F", 68},
		{-40, "c", "f", -40},
		{0, "C", "K", 273.15},
		{1, "sq ft", "m2", 0.0929},
	}
	for _, c := range cases {
		got, _, _, err := convertUnit(c.amount, c.from, c.to)
		if err != nil {
			t.Errorf("convertUnit(%v,%q,%q) errored: %v", c.amount, c.from, c.to, err)
			continue
		}
		if math.Abs(got-c.want) > math.Abs(c.want)*1e-4+1e-4 {
			t.Errorf("convertUnit(%v,%q,%q) = %v, want %v", c.amount, c.from, c.to, got, c.want)
		}
	}
}

func TestConvertUnitRejectsCrossDimension(t *testing.T) {
	// Answering a kg→cm request with a number would hide a misread spec.
	for _, c := range [][2]string{{"kg", "cm"}, {"C", "kg"}, {"m", "F"}, {"m", "widgets"}, {"", "cm"}} {
		if v, _, _, err := convertUnit(1, c[0], c[1]); err == nil {
			t.Errorf("convertUnit(1,%q,%q) = %v, want an error", c[0], c[1], v)
		}
	}
}

func TestNormUnit(t *testing.T) {
	// Punctuation and spacing vary per model; the distinguishing characters
	// (slash, quote) must survive.
	cases := map[string]string{
		" KM/H ":      "km/h",
		"sq. ft":      "sqft",
		"m²":          "m2",
		"Fluid Ounce": "fluidounce",
		"\"":          "\"",
	}
	for in, want := range cases {
		if got := normUnit(in); got != want {
			t.Errorf("normUnit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUnitArgs(t *testing.T) {
	amount, from, to := parseUnitArgs(`{"amount":15.6,"from":"in","to":"cm"}`)
	if amount != 15.6 || from != "in" || to != "cm" {
		t.Fatalf("got %v %q %q", amount, from, to)
	}
	// A string amount goes through the expression parser, so "2*3" works and a
	// missing amount means one unit.
	if a, _, _ := parseUnitArgs(`{"value":"2*3","unit":"kg","target":"lb"}`); a != 6 {
		t.Errorf("string amount: got %v", a)
	}
	if a, f, _ := parseUnitArgs(`{"from":"kg","to":"lb"}`); a != 1 || f != "kg" {
		t.Errorf("default amount: got %v %q", a, f)
	}
}

func TestFormatUnitConvert(t *testing.T) {
	v, cf, ct, err := convertUnit(1, "in", "cm")
	if err != nil {
		t.Fatal(err)
	}
	if got := formatUnitConvert(1, cf, v, ct); !strings.Contains(got, "1 in = 2.54 cm") {
		t.Errorf("got %q", got)
	}
}
