package main

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// Stored TLS files reach the flags as a pair. A lone half is dropped rather
// than applied: the server exits when only one is set, so filling in half of a
// pair would turn stored settings into a startup failure.
func TestAppSettings_TlsPairing(t *testing.T) {
	for _, tc := range []struct {
		name              string
		app               autogen.AppSettings
		wantCert, wantKey string
	}{
		{
			name:     "both are applied",
			app:      autogen.AppSettings{TlsCertFile: "/etc/ssl/qm.pem", TlsKeyFile: "/etc/ssl/qm.key"},
			wantCert: "/etc/ssl/qm.pem",
			wantKey:  "/etc/ssl/qm.key",
		},
		{
			name: "a lone cert is ignored",
			app:  autogen.AppSettings{TlsCertFile: "/etc/ssl/qm.pem"},
		},
		{
			name: "a lone key is ignored",
			app:  autogen.AppSettings{TlsKeyFile: "/etc/ssl/qm.key"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newBundleFlags()
			if err := fs.Parse(nil); err != nil {
				t.Fatal(err)
			}
			applyAppSettings(fs, map[string]bool{}, tc.app)
			if got := valueOf(t, fs, "tls-cert-file"); got != tc.wantCert {
				t.Errorf("tls-cert-file = %q, want %q", got, tc.wantCert)
			}
			if got := valueOf(t, fs, "tls-key-file"); got != tc.wantKey {
				t.Errorf("tls-key-file = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

// argv still wins, which is what makes a flag the way to rescue an install
// whose stored certificate has expired or moved.
func TestAppSettings_ArgvTlsWins(t *testing.T) {
	fs := newBundleFlags()
	if err := fs.Parse([]string{"-tls-cert-file", "/tmp/mine.pem", "-tls-key-file", "/tmp/mine.key"}); err != nil {
		t.Fatal(err)
	}
	given := map[string]bool{"tls-cert-file": true, "tls-key-file": true}
	applyAppSettings(fs, given, autogen.AppSettings{TlsCertFile: "/etc/ssl/qm.pem", TlsKeyFile: "/etc/ssl/qm.key"})
	if got := valueOf(t, fs, "tls-cert-file"); got != "/tmp/mine.pem" {
		t.Errorf("tls-cert-file = %q, want the argv value", got)
	}
}
