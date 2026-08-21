//go:build windows

package nativewin

import (
	"reflect"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

// hideStatusBar turns off the little URL bubble WebView2 pops into the bottom
// corner whenever the pointer rests on a link.
//
// The app window draws no browser chrome, so that bubble is the one piece of
// browser UI the user can still see -- and on a hash-routed SPA it shows
// "…/ui/#/models", an implementation detail nobody asked for. There is a
// setting for it (ICoreWebView2Settings::IsStatusBarEnabled), but go-webview2
// keeps the Chromium instance in an unexported field and exposes no way in, so
// the only route to the setting is to read that field.
//
// Deliberately best-effort: a library upgrade that renames the field, or a
// runtime that predates the setting, must cost us a bubble and not a window.
func hideStatusBar(w webview2.WebView) {
	c := chromiumOf(w)
	if c == nil {
		return
	}
	s, err := c.GetSettings()
	if err != nil || s == nil {
		return
	}
	_ = s.PutIsStatusBarEnabled(false)
}

// chromiumOf digs the *edge.Chromium out of go-webview2's unexported `browser`
// field. Returns nil for any shape it does not recognise.
func chromiumOf(w webview2.WebView) *edge.Chromium {
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() || !f.CanAddr() {
		return nil
	}
	// NewAt to read an unexported field: the value is only ever read, never
	// written, and the pointer stays owned by the webview.
	b := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
	c, _ := b.(*edge.Chromium)
	return c
}
