package server

import "testing"

// The UI slices the answer text with JavaScript string indices to place tool
// cards, so lens() must count the way JS does. A byte count put the cards
// several characters too far right in any answer containing an emoji.
func TestUtf16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"I was only able to", 18},
		{"6th Grade Prank! \U0001F480", 19}, // 💀 is one rune, two UTF-16 units
		{"café — ok", 9},                    // multi-byte, still BMP
	}
	for _, tc := range cases {
		if got := utf16Len(tc.in); got != tc.want {
			t.Errorf("utf16Len(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The offset a search records must be a valid split point in the string the UI
// holds — i.e. slicing there must not land inside a word.
func TestLensOffsetSplitsCleanly(t *testing.T) {
	at := &activeTurn{content: "Fine \U0001F480 I wa"}
	n, _, _ := at.lens()
	units := []rune{}
	for _, r := range at.content {
		units = append(units, r)
		if r > 0xFFFF {
			units = append(units, 0) // stand-in for the low surrogate
		}
	}
	if n != len(units) {
		t.Fatalf("contentLen = %d, want %d (UTF-16 units)", n, len(units))
	}
	if n == len(at.content) {
		t.Errorf("still counting bytes: %d", n)
	}
}
