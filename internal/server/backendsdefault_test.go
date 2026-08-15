package server

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// classDefaultFor decides which card gets the "in use" chip. Every installed
// component points its own registry row at a build, so this is the ONLY thing
// separating "this backend's selected build" from "the backend that launches" —
// get it wrong and several cards of one class all claim to be in use.
func TestClassDefaultFor(t *testing.T) {
	list := []autogen.BackendEntry{
		{ID: "hand", Kind: "llama", Name: "llama-hip", Path: `E:\hip\llama-server.exe`},
		{ID: "managed-llama-server", Kind: "llama", Name: "llama.cpp"},
		{ID: "managed-custom-rocm", Kind: "llama", Name: "llamacpp-rocm"},
		{ID: "sd", Kind: "sd", Name: "sd-server", Default: true},
	}
	starred := func(i int) []autogen.BackendEntry {
		out := append([]autogen.BackendEntry(nil), list...)
		out[i].Default = true
		return out
	}

	cases := []struct {
		name     string
		list     []autogen.BackendEntry
		class    string
		mine     int
		wantIs   bool
		wantOwn  string
		wantImpl bool
	}{
		// The bug: three llama rows, no ★ anywhere. resolveBackend takes the
		// first of the class, so exactly one of these is true and the other two
		// must name it rather than all reporting "no default set".
		{name: "no star, first of class wins", list: list, class: "llm", mine: 0, wantIs: true, wantImpl: true},
		{name: "no star, second loses to first", list: list, class: "llm", mine: 1, wantOwn: "llama-hip", wantImpl: true},
		{name: "no star, tracked repo loses to first", list: list, class: "llm", mine: 2, wantOwn: "llama-hip", wantImpl: true},

		// A deliberate ★ beats list order, and is not reported as implicit.
		{name: "star wins over earlier row", list: starred(2), class: "llm", mine: 2, wantIs: true},
		{name: "earlier row loses to star", list: starred(2), class: "llm", mine: 0, wantOwn: "llamacpp-rocm"},

		// Not installed at all (no row): still must name the winner.
		{name: "uninstalled names the winner", list: list, class: "llm", mine: -1, wantOwn: "llama-hip", wantImpl: true},

		// A class with no rows resolves through nothing, so no card may claim it.
		{name: "empty class", list: list, class: "asr", mine: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is, own, impl := classDefaultFor(tc.list, tc.class, tc.mine)
			if is != tc.wantIs || own != tc.wantOwn || impl != tc.wantImpl {
				t.Fatalf("got (%v, %q, %v), want (%v, %q, %v)", is, own, impl, tc.wantIs, tc.wantOwn, tc.wantImpl)
			}
		})
	}
}

// A row with no name falls back to its path: an unnamed hand-entered backend
// still has to be identifiable in "X runs instead".
func TestClassDefaultFor_UnnamedOwner(t *testing.T) {
	list := []autogen.BackendEntry{
		{ID: "hand", Kind: "llama", Path: `E:\hip\llama-server.exe`, Default: true},
		{ID: "managed-llama-server", Kind: "llama", Name: "llama.cpp"},
	}
	if _, own, _ := classDefaultFor(list, "llm", 1); own != `E:\hip\llama-server.exe` {
		t.Fatalf("owner = %q, want the path", own)
	}
}
