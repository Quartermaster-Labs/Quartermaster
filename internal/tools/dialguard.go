package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// Web search takes its endpoint FROM THE CALLER: the SearXNG base URL is a
// per-request field, because the instance people run is their own and it is
// almost always on loopback or the LAN (a WSL container, a box in the closet).
// That is the feature, and it is also a server-side request forgery: whoever
// can reach /api/websearch can make this process fetch a URL of their choosing
// and read the first 4 MiB of the answer.
//
// The two facts cannot both be served by one rule, so the rule is about WHO IS
// ASKING rather than what they asked for:
//
//   - a caller on loopback is the person running this program, and their own
//     private SearXNG keeps working exactly as before;
//   - any other caller (the listener binds 0.0.0.0 and the dashboard API takes
//     no credential) may only reach public addresses, which leaves them no way
//     to use this process as a probe for the network it sits on.
//
// The decision travels on the context so it cannot be forgotten at a call site:
// the handler marks the request once, and every dial underneath it -- including
// the ones a redirect adds later -- is checked.

type publicOnlyKey struct{}

// PublicOnly marks ctx as untrusted: web-search dials made under it must land
// on a public address. Set once per request by the HTTP layer.
func PublicOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicOnlyKey{}, true)
}

func isPublicOnly(ctx context.Context) bool {
	v, _ := ctx.Value(publicOnlyKey{}).(bool)
	return v
}

// guardDial runs after the name is resolved, on the address the connection is
// actually about to use, so a host that resolves to a private address -- or
// re-resolves to one on a later attempt -- is refused rather than dialled.
func guardDial(network, address string, _ syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" && network != "tcp" {
		return fmt.Errorf("blocked network %q", network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("unresolvable address %q", address)
	}
	if !shared.IsPublicIP(ip) {
		return fmt.Errorf("blocked non-public address %s", ip)
	}
	return nil
}

var (
	plainDialer   = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guardedDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: guardDial}
)

func webSearchDial(ctx context.Context, network, address string) (net.Conn, error) {
	if isPublicOnly(ctx) {
		return guardedDialer.DialContext(ctx, network, address)
	}
	return plainDialer.DialContext(ctx, network, address)
}

// webSearchProxy keeps the environment's proxy for a trusted caller and drops
// it for a guarded one: a proxy dials on our behalf, and the address check
// would then only ever see the proxy's own address, never the destination.
func webSearchProxy(req *http.Request) (*url.URL, error) {
	if isPublicOnly(req.Context()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

func newWebSearchTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 webSearchProxy,
		DialContext:           webSearchDial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
