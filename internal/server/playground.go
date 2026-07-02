package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Playground holds the config for the standalone playground app: the listen
// address it is served on and the data dir where per-user credentials and chat
// history are stored. nil unless -playground-port is set.
//
// Auth here is deliberately NOT serious — plaintext username/password in a JSON
// file, a plain (unsigned) cookie. Its only job is to key chat history per user
// so different people on the same box keep separate conversations.
type Playground struct {
	Addr    string // listen address this app is served on (e.g. ":8081")
	DataDir string // dir for users.json + chats/<user>.json

	mu sync.Mutex
}

type playgroundCtxKey struct{}

const pgCookie = "pg_user"

var pgUserRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// SetPlayground attaches (or clears) the playground config. Called once at
// startup and re-attached to each rebuilt server on reload, like AutogenAdmin.
func (s *Server) SetPlayground(p *Playground) { s.playground = p }

// markPlayground tags the request context when it arrived on the playground
// listen address, so handlePlaygroundMode can report which app to render.
func (s *Server) markPlayground(addr string, r *http.Request) *http.Request {
	if s.playground != nil && addr == s.playground.Addr {
		return r.WithContext(context.WithValue(r.Context(), playgroundCtxKey{}, true))
	}
	return r
}

func isPlaygroundRequest(r *http.Request) bool {
	v, _ := r.Context().Value(playgroundCtxKey{}).(bool)
	return v
}

// GET /api/mode — tells the SPA which app to render. On the playground port it
// returns playground=true; elsewhere it reports the playground port (if any) so
// the dashboard can link to it.
func (s *Server) handlePlaygroundMode(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"playground": isPlaygroundRequest(r)}
	if s.playground != nil {
		_, port, _ := net.SplitHostPort(s.playground.Addr)
		resp["playgroundPort"] = port
	}
	writeJSON(w, resp)
}

// --- credential + chat storage -------------------------------------------

func (p *Playground) usersPath() string { return filepath.Join(p.DataDir, "users.json") }

func (p *Playground) loadUsers() (map[string]string, error) {
	b, err := os.ReadFile(p.usersPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	users := map[string]string{}
	if err := json.Unmarshal(b, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (p *Playground) saveUsers(users map[string]string) error {
	if err := os.MkdirAll(p.DataDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(users, "", "  ")
	return os.WriteFile(p.usersPath(), b, 0o644)
}

func (p *Playground) chatsPath(user string) string {
	return filepath.Join(p.DataDir, "chats", user+".json")
}

func (p *Playground) prefsPath(user string) string {
	return filepath.Join(p.DataDir, "prefs", user+".json")
}

func (p *Playground) imageChatsPath(user string) string {
	return filepath.Join(p.DataDir, "imagechats", user+".json")
}

// POST /auth/login {username,password}. Unknown username registers; known one
// must match. On success sets the pg_user cookie.
func (s *Server) handlePlaygroundLogin(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !pgUserRe.MatchString(body.Username) {
		http.Error(w, "username must be 1-40 chars: letters, digits, _ or -", http.StatusBadRequest)
		return
	}
	if body.Password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	users, err := p.loadUsers()
	if err != nil {
		http.Error(w, "could not read users", http.StatusInternalServerError)
		return
	}
	if existing, ok := users[body.Username]; ok {
		if existing != body.Password {
			http.Error(w, "wrong password", http.StatusUnauthorized)
			return
		}
	} else {
		users[body.Username] = body.Password
		if err := p.saveUsers(users); err != nil {
			http.Error(w, "could not save user", http.StatusInternalServerError)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     pgCookie,
		Value:    body.Username,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]string{"username": body.Username})
}

// POST /auth/logout — clears the cookie.
func (s *Server) handlePlaygroundLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: pgCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// GET /auth/me — current user from the cookie, or 401.
func (s *Server) handlePlaygroundMe(w http.ResponseWriter, r *http.Request) {
	user := playgroundUser(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"username": user})
}

// playgroundUser reads + validates the username cookie.
func playgroundUser(r *http.Request) string {
	c, err := r.Cookie(pgCookie)
	if err != nil || !pgUserRe.MatchString(c.Value) {
		return ""
	}
	return c.Value
}

// GET /api/chats — the user's saved sessions (opaque JSON array, client-owned).
// PUT /api/chats — overwrite the whole array. The client owns the list (add /
// rename / delete happen client-side), the server just persists it per user.
func (s *Server) handlePlaygroundChats(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).chatsPath, "[]")
}

// GET/PUT /api/imagechats — the user's saved image threads. Same
// client-owns-the-blob model as /api/chats, just a separate file.
func (s *Server) handlePlaygroundImageChats(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).imageChatsPath, "[]")
}

// GET/PUT /api/prefs — the user's playground settings (opaque JSON object:
// system prompt, temperature, web-search / reasoning toggles, etc.). Same
// client-owns-the-blob model as chats, just defaulting to {} when absent.
func (s *Server) handlePlaygroundPrefs(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).prefsPath, "{}")
}

// serveUserBlob reads (GET) or overwrites (PUT) a per-user JSON file, opaque to
// the server. pathFn picks which file; empty is the GET response when missing.
func (s *Server) serveUserBlob(w http.ResponseWriter, r *http.Request, pathFn func(*Playground, string) string, empty string) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	user := playgroundUser(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	path := pathFn(p, user)

	switch r.Method {
	case http.MethodGet:
		p.mu.Lock()
		b, err := os.ReadFile(path)
		p.mu.Unlock()
		if err != nil {
			if os.IsNotExist(err) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(empty))
				return
			}
			http.Error(w, "could not read data", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case http.MethodPut:
		var blob json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&blob); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = os.WriteFile(path, blob, 0o644)
		}
		p.mu.Unlock()
		if err != nil {
			http.Error(w, "could not save data", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
