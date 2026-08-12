package server

import (
	"math"
	"strings"
	"testing"
)

func TestEvalExpr(t *testing.T) {
	cases := map[string]float64{
		"2+3*4":            14,
		"(2+3)*4":          20,
		"1299.50 / 12":     108.29166666666667,
		"2^10":             1024,
		"2^-2":             0.25,
		"-5 + 3":           -2,
		"20%":              0.2,
		"250 * (1 - 20%)":  200,
		"sqrt(144)":        12,
		"round(3.14159,2)": 3.14,
		"max(100, 250, 7)": 250,
		"avg(2,4,6)":       4,
		"1,299.50 * 2":     2599, // grouping commas: no function call in the expression
		"1.5e3 + 1":        1501,
		"pi":               math.Pi,
	}
	for in, want := range cases {
		got, err := evalExpr(in)
		if err != nil {
			t.Errorf("evalExpr(%q) errored: %v", in, err)
			continue
		}
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("evalExpr(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEvalExprErrors(t *testing.T) {
	// Every one of these must fail rather than return a number: a wrong answer
	// is worse than no answer, and this is the surface model text reaches.
	for _, in := range []string{
		"1/0",
		"2+",
		"(1+2",
		"1+2)",
		"x + 1",
		"os.Exit(1)",
		"import os",
		"2e",
		"round()",
		"pow(2)",
		"",
		strings.Repeat("(", 40) + "1",
	} {
		if v, err := evalExpr(in); err == nil {
			t.Errorf("evalExpr(%q) = %v, want an error", in, v)
		}
	}
}

func TestParseCalcArgs(t *testing.T) {
	if got := parseCalcArgs(`{"expression":" 2 + 2 "}`); got != "2 + 2" {
		t.Errorf("expression: got %q", got)
	}
	if got := parseCalcArgs(`{"expr":"3*3"}`); got != "3*3" {
		t.Errorf("alias: got %q", got)
	}
	if got := parseCalcArgs(`nope`); got != "" {
		t.Errorf("bad json: got %q", got)
	}
}

func TestFmtCalcNum(t *testing.T) {
	cases := map[float64]string{
		14:                 "14",
		0.1 + 0.2:          "0.3", // float noise must not reach the model
		108.29166666666667: "108.291666667",
		1024:               "1024",
	}
	for in, want := range cases {
		if got := fmtCalcNum(in); got != want {
			t.Errorf("fmtCalcNum(%v) = %q, want %q", in, got, want)
		}
	}
}
