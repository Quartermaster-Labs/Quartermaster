package autogen

import "testing"

// -cms is emitted on every text model, and extraArgs is appended verbatim after
// it, so a copy that slipped into extraArgs (the launch-box editor used to not
// parse the flag) came out on the line twice, gaining one more per round trip.
// The emitter hoists it back out and honours it as the pin it was meant to be.
func TestAutogen_hoistCmsFromExtra(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantExtra string
		wantStep  int
	}{
		{"nothing to hoist", "--foo bar", "--foo bar", 0},
		{"empty", "", "", 0},
		{"alone", "-cms 256", "", 256},
		{"between other args", "--foo bar -cms 256 --baz", "--foo bar --baz", 256},
		{"long alias", "--checkpoint-min-step 512 --foo", "--foo", 512},
		{"several copies keep the first", "-cms 256 --foo -cms 512", "--foo", 256},
		{"dangling flag is dropped", "--foo -cms", "--foo", 0},
		{"non-numeric value is dropped, not pinned", "-cms abc --foo", "--foo", 0},
		{"a longer flag ending in cms is left alone", "--not-cms 256", "--not-cms 256", 0},
	}
	for _, tc := range tests {
		extra, step := hoistCmsFromExtra(tc.in)
		if extra != tc.wantExtra || step != tc.wantStep {
			t.Errorf("%s: got %q/%d, want %q/%d", tc.name, extra, step, tc.wantExtra, tc.wantStep)
		}
	}
}
