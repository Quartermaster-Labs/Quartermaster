package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/chain"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// CreateAuthMiddleware returns middleware that validates API keys when the
// config declares any. It accepts the key via Authorization: Bearer,
// Authorization: Basic (password field), or x-api-key. When no keys are
// configured the middleware is a pass-through.
//
// A rejection is logged (never the key itself — only whether one was sent),
// because "my client gets 401" is otherwise indistinguishable in the log from
// "my client never reached the server".
func CreateAuthMiddleware(cfg config.Config, proxylog *logmon.Monitor) chain.Middleware {
	keys := cfg.RequiredAPIKeys
	scopes := buildKeyScopes(cfg)
	return func(next http.Handler) http.Handler {
		if len(keys) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := shared.ExtractAPIKey(r)

			// Constant-time compare, and never short-circuit the loop: both a
			// byte-wise `==` and an early `break` leak how much of a guessed key
			// was right through response timing.
			valid := false
			for _, key := range keys {
				if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 1 {
					valid = true
				}
			}
			if !valid {
				reason := "invalid API key"
				if provided == "" {
					reason = "no API key sent"
				}
				proxylog.Warnf("auth: rejected %s %s from %s (%s)", r.Method, r.URL.Path, clientIP(r), reason)
				// Bearer, not Basic: browsers auto-prompt a native username/password
				// dialog for a Basic challenge, which made every unkeyed browser /v1
				// call (titles, compaction, images) pop a "sign in" box mid-chat.
				// Bearer is also the scheme this API actually uses.
				w.Header().Set("WWW-Authenticate", `Bearer realm="quartermaster"`)
				shared.SendResponse(w, r, http.StatusUnauthorized, "unauthorized: invalid or missing API key")
				return
			}

			// Scope the request to the key's allowed models (if any) so the
			// catalog listing and dispatch can enforce it downstream.
			next.ServeHTTP(w, withKeyScope(r, scopes[provided]))
		})
	}
}

// CreateRequestContextMiddleware returns middleware that extracts model and
// auth info from the request into the context. Requests where no model can be
// identified are rejected with a 404.
func CreateRequestContextMiddleware(cfg config.Config) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := shared.FetchContext(r, cfg)
			if err != nil {
				shared.SendError(w, r, shared.ErrNoModelInContext)
				return
			}
			_ = data
			next.ServeHTTP(w, r)
		})
	}
}

// CreateCORSMiddleware returns middleware that answers OPTIONS preflight
// requests with permissive CORS headers (see issues #81, #77, #42). Non-OPTIONS
// requests pass through untouched.
func CreateCORSMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
				w.Header().Set("Access-Control-Allow-Headers", sanitizeAccessControlRequestHeaderValues(headers))
			} else {
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, X-Requested-With")
			}
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

func isTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
	case r >= 'A' && r <= 'Z':
	case r >= '0' && r <= '9':
	case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
	default:
		return false
	}
	return true
}

// sanitizeAccessControlRequestHeaderValues drops any header names that contain
// characters outside the HTTP token grammar before echoing them back.
func sanitizeAccessControlRequestHeaderValues(headerValues string) string {
	parts := strings.Split(headerValues, ",")
	valid := make([]string, 0, len(parts))

	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}

		validPart := true
		for _, c := range v {
			if !isTokenChar(c) {
				validPart = false
				break
			}
		}
		if validPart {
			valid = append(valid, v)
		}
	}

	return strings.Join(valid, ", ")
}
