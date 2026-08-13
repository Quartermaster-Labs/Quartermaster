package server

import "testing"

// TestQmCutFold pins how the "estimate:<model id>" target is split — a model id
// can hold dashes and '@', and a target that merely STARTS with "estimate" must
// not be mistaken for the estimate target.
func TestQmCutFold(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"estimate:qwen3-35b", "qwen3-35b", true},
		{"Estimate: qwen3-35b", "qwen3-35b", true},
		{"estimate qwen3-35b@ctx32768", "qwen3-35b@ctx32768", true},
		{"estimate=qwen3", "qwen3", true},
		{"estimate", "", true},   // bare: caller reports the missing id
		{"estimates", "", false}, // a model actually named this stays a model id
		{"estimate-model", "", false},
		{"models", "", false},
		{"est", "", false},
	}
	for _, c := range cases {
		got, ok := qmCutFold(c.in, "estimate")
		if ok != c.wantOK || got != c.want {
			t.Errorf("qmCutFold(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestQmScalarString: option values arrive as decoded JSON, so an integer ctx is
// a float64. Printing it as "32768.000000" would fail the handler's Atoi and the
// estimate would silently size against the default context instead.
func TestQmScalarString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(32768), "32768"},
		{float64(21.5), "21.5"},
		{"q8_0", "q8_0"},
		{"  f16 ", "f16"},
		{true, "true"},
		{false, "false"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := qmScalarString(c.in); got != c.want {
			t.Errorf("qmScalarString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
