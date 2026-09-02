package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// handOffToRunningInstance asks an already-running Quartermaster on addr to
// raise its window, and reports whether one answered.
//
// This is what makes double-clicking the icon twice do the obvious thing. The
// desktop has no concept of "already running"; without this the second launch
// walks the whole boot path -- GPU probe, model discovery, possibly REWRITING
// the generated config the running instance is watching -- only to die on
// "address already in use" with a console the user cannot see.
//
// Deliberately NOT a mutex or a lock file. A stale lock after a crash is a
// support ticket ("it says it is already running and it is not"); a port that
// answers is proof of life that cannot go stale. The cost is that the address
// has to be guessed before the config is read, so a config with a `listeners:`
// block may point this at the wrong port. That is why a miss is not an error:
// nothing answering means "carry on booting", and a genuine port conflict still
// surfaces later exactly as it did before.
func handOffToRunningInstance(addr string, useTLS bool) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	scheme := "http"
	client := &http.Client{Timeout: 2 * time.Second}
	if useTLS {
		scheme = "https"
		// The certificate is almost always self-signed and we are talking to
		// 127.0.0.1: there is no name to verify and no network to intercept.
		// This client only ever reaches loopback and reads one boolean.
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	resp, err := client.Get(scheme + "://127.0.0.1:" + port + "/api/app/show")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()

	// Verify the marker rather than trusting a 200. Something unrelated may own
	// that port, and mistaking it for us would swallow a real conflict: the user
	// would get no window, no error, and a process that exited silently.
	var out struct {
		App   string `json:"app"`
		Shown bool   `json:"shown"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || json.Unmarshal(body, &out) != nil {
		return false
	}
	return out.App == "quartermaster" && out.Shown
}
