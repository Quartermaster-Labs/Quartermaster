package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

// Playground holds the config for the standalone playground app: the listen
// address it is served on and the data dir where per-user credentials and chat
// history are stored. nil unless -playground-port is set.
//
// Auth here is deliberately NOT serious — plaintext username/password in a JSON
// file. Its only job is to key chat history per user so different people on the
// same box keep separate conversations. The cookie IS authenticated though
// (HMAC, see cookieValue): an unsigned one would have made the password
// decorative, since anyone could have typed document.cookie='pg_user=victim'
// and read that user's whole history.
type Playground struct {
	Addr    string // listen address this app is served on (e.g. ":8081")
	DataDir string // users.json + per-user folders (users/<user>/{chats,imagechats,speechchats,prefs}.json + media/)
	// SelfBase is quartermaster's own inference loopback (e.g. "http://127.0.0.1:8080"),
	// where the server-side turn runner POSTs /v1/chat/completions so template /
	// canon / routing / slotcache all apply. Set at startup from the main listen addr.
	SelfBase string
	// GeneratePath is the -generate control file ("" when not generating). The
	// turn runner reads the backend registry beside it to find the CPU title
	// model used for reasoning-box titles (titlegen.go). Set at startup only.
	GeneratePath string

	mu sync.Mutex

	// turns owns server-side turn generation (see turns_design.md / turns.go).
	turns *turnManager

	// secretOnce/secret back the cookie HMAC key (see cookieSecret).
	secretOnce sync.Once
	secret     []byte
}

// InitTurns wires the server-side turn runner. Called once at startup, before
// SetPlayground; the manager is carried on the Playground so it survives a
// server rebuild on reload (like the rest of the playground config).
func (p *Playground) InitTurns(log *logmon.Monitor) {
	if p.turns == nil {
		p.turns = newTurnManager(p, log)
	}
}

// LoopbackBase turns a listen address (":8080", "0.0.0.0:8080", "127.0.0.1:8080")
// into the loopback base URL the turn runner self-POSTs to.
func LoopbackBase(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return "http://127.0.0.1:8080"
	}
	return "http://127.0.0.1:" + port
}

type playgroundCtxKey struct{}

const pgCookie = "pg_user"

// maxBlobBytes caps a per-user blob PUT (chats / imagechats / speechchats /
// prefs). These are decoded whole into memory before being written, so without
// a cap one request can exhaust RAM. Generous on purpose: a chat thread can
// legitimately carry many inline data: URLs before extractMedia splits them
// out, and a speech thread carries audio. Real threads land far below this.
const maxBlobBytes = 128 << 20

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
	// 0600: the file is beside the binary, so a backup, a synced folder or a
	// screenshot of the install dir would otherwise carry everyone's credential.
	return os.WriteFile(p.usersPath(), b, 0o600)
}

// hashPassword stores a bcrypt hash rather than the password itself. The login
// remains deliberately unserious (see the type comment) — this is not about
// defending the playground, it is that people type a password they use
// elsewhere, and users.json lives next to the binary where a backup or a sync
// client will happily copy it.
func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// checkPassword verifies pw against a stored value, transparently accepting the
// plaintext entries written by builds before hashing existed. ok reports the
// match; upgrade carries a fresh hash to persist when the stored value was one
// of those legacy plaintexts, so an install migrates on first login instead of
// locking everyone out.
func checkPassword(stored, pw string) (ok bool, upgrade string) {
	if strings.HasPrefix(stored, "$2") { // bcrypt: $2a$ / $2b$ / $2y$
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(pw)) == nil, ""
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(pw)) != 1 {
		return false, ""
	}
	h, err := hashPassword(pw)
	if err != nil {
		return true, "" // matched; just could not upgrade
	}
	return true, h
}

// Per-user layout: DataDir/users/<user>/{chats,imagechats,speechchats,prefs}.json
// with generated media split out into DataDir/users/<user>/media/ (see extractMedia)
// so the tab JSONs stay small structure-only blobs instead of MB of inline base64.
func (p *Playground) userDir(user string) string { return filepath.Join(p.DataDir, "users", user) }

func (p *Playground) chatsPath(user string) string {
	return filepath.Join(p.userDir(user), "chats.json")
}

func (p *Playground) prefsPath(user string) string {
	return filepath.Join(p.userDir(user), "prefs.json")
}

func (p *Playground) imageChatsPath(user string) string {
	return filepath.Join(p.userDir(user), "imagechats.json")
}

func (p *Playground) speechChatsPath(user string) string {
	return filepath.Join(p.userDir(user), "speechchats.json")
}

func (p *Playground) mediaDir(user string) string { return filepath.Join(p.userDir(user), "media") }

// dataURLRe matches an inline "data:<mime>;base64,<payload>" URL. base64's
// alphabet excludes the JSON string terminator (") so the payload run stops
// cleanly at the closing quote without needing to model the surrounding JSON.
var dataURLRe = regexp.MustCompile(`data:([\w.+-]+/[\w.+-]+);base64,([A-Za-z0-9+/]+={0,2})`)

// extractMedia rewrites inline base64 data: URLs in a raw JSON blob to
// /api/media/<file> references, writing each decoded blob to the user's media
// dir (deduped by content hash). It runs on raw bytes, not a parsed structure,
// so everything else (numbers, key order, timestamps) is byte-preserved and it
// works for any tab's JSON shape. Already-rewritten refs don't match, so it's
// idempotent — a client re-PUTing refs is a no-op.
func (p *Playground) extractMedia(user string, raw []byte) []byte {
	return dataURLRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := dataURLRe.FindSubmatch(m)
		data, err := base64.StdEncoding.DecodeString(string(sub[2]))
		if err != nil || len(data) == 0 {
			return m // leave malformed payloads inline rather than lose them
		}
		sum := sha256.Sum256(data)
		kind := mediaKind(string(sub[1]))
		name := hex.EncodeToString(sum[:8]) + "." + mimeExt(string(sub[1]))
		dir := filepath.Join(p.mediaDir(user), kind)
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil { // write once; hash dedups
			if os.MkdirAll(dir, 0o755) == nil {
				os.WriteFile(path, data, 0o644)
			}
		}
		return []byte("/api/media/" + kind + "/" + name)
	})
}

// mediaRefRe matches an "/api/media/<kind>/<name>" reference written by
// extractMedia. Both segments are character-constrained (no "/", no ".." — the
// only dot is the extension separator), so a match can't escape the user's
// media dir when joined as a path.
var mediaRefRe = regexp.MustCompile(`/api/media/([a-z]+)/([0-9a-f]+)\.([a-z0-9]+)`)

// inlineMedia is extractMedia's inverse: it rewrites media refs in a raw JSON
// blob back to inline "data:<mime>;base64,<payload>" URLs, reading each blob
// from the user's media dir.
//
// Needed because a chat turn's history is replayed to the MODEL, not to a
// browser. The client's copy of an attached image becomes a ref the moment the
// session round-trips through extractMedia (the post-turn sync pulls the
// rewritten copy back), so a follow-up message to a vision model would forward
// "/api/media/image/ab12.png" as the image_url — a path llama-server can't
// resolve. The browser resolves refs itself via GET /api/media; the model can't.
//
// ponytail: raw-bytes regex like extractMedia, so it's shape-agnostic and
// byte-preserves everything else. A ref whose file is gone is left as-is.
func (p *Playground) inlineMedia(user string, raw []byte) []byte {
	return mediaRefRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := mediaRefRe.FindSubmatch(m)
		kind, name, ext := string(sub[1]), string(sub[2]), string(sub[3])
		mime := extMime(ext)
		if mime == "" {
			return m
		}
		data, err := os.ReadFile(filepath.Join(p.mediaDir(user), kind, name+"."+ext))
		if err != nil {
			return m
		}
		return []byte("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data))
	})
}

// extMime maps the extensions extractMedia writes back to their media type.
// Unknown extensions return "" — the ref is then left alone rather than
// guessed at, since a wrong type would confuse the model's media decoder.
func extMime(ext string) string {
	switch ext {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "webm":
		return "audio/webm"
	case "ogg":
		return "audio/ogg"
	}
	return ""
}

// gcMedia deletes media files in the user's dir that no tab JSON references
// (an orphan left after a chat/image/speech entry was deleted client-side).
// MUST be called under p.mu and after the triggering write, so the union scan
// across all tabs sees committed state — otherwise a concurrent write to another
// tab could delete a file it just referenced. Only ref-removing client PUTs call
// it; the streaming turn writer only adds refs, so it skips GC to avoid churn.
//
// ponytail: substring scan of the (now small, ref-only) JSONs, not a parse — a
// "/api/media/<name>" only ever appears as a ref, so containment is enough.
func (p *Playground) gcMedia(user string) {
	kinds, err := os.ReadDir(p.mediaDir(user))
	if err != nil {
		return
	}
	var refs []byte
	for _, fn := range []func(string) string{p.chatsPath, p.imageChatsPath, p.speechChatsPath} {
		b, _ := os.ReadFile(fn(user))
		refs = append(refs, b...)
	}
	for _, k := range kinds {
		if !k.IsDir() {
			continue
		}
		dir := filepath.Join(p.mediaDir(user), k.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if !bytes.Contains(refs, []byte("/api/media/"+k.Name()+"/"+f.Name())) {
				os.Remove(filepath.Join(dir, f.Name()))
			}
		}
	}
}

// mediaKind buckets a MIME type into a top-level media subfolder so images and
// audio live apart. Unknown types go to "other".
func mediaKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	default:
		return "other"
	}
}

// mimeExt maps the media types the playground actually emits to a file
// extension (so http.ServeFile sets the right Content-Type on read). Unknown
// types fall back to the alnum tail of the subtype, or "bin".
func mimeExt(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/webm":
		return "webm"
	case "audio/ogg":
		return "ogg"
	}
	sub := mime[strings.LastIndexByte(mime, '/')+1:]
	ext := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(sub))
	if ext == "" {
		return "bin"
	}
	return ext
}

// Migrate moves the pre-split flat layout (DataDir/<kind>/<user>.json with inline
// base64) into the per-user folder layout with media extracted. Idempotent and
// best-effort: skips users already migrated, leaves the old files in place. Runs
// once at startup.
func (p *Playground) Migrate() {
	if p == nil {
		return
	}
	kinds := map[string]func(string) string{
		"chats":       p.chatsPath,
		"imagechats":  p.imageChatsPath,
		"speechchats": p.speechChatsPath,
		"prefs":       p.prefsPath,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for kind, newPathFn := range kinds {
		entries, _ := os.ReadDir(filepath.Join(p.DataDir, kind))
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			user := strings.TrimSuffix(e.Name(), ".json")
			if !pgUserRe.MatchString(user) {
				continue
			}
			newPath := newPathFn(user)
			if _, err := os.Stat(newPath); err == nil {
				continue // already migrated
			}
			raw, err := os.ReadFile(filepath.Join(p.DataDir, kind, e.Name()))
			if err != nil {
				continue
			}
			raw = p.extractMedia(user, raw)
			if os.MkdirAll(filepath.Dir(newPath), 0o755) == nil {
				os.WriteFile(newPath, raw, 0o644)
			}
		}
	}
}

// minPasswordLen is a floor, not a policy. Nothing here is a security boundary
// (see the Playground type comment); it only stops an empty-ish password being
// set by accident on an account that keeps someone's chat history.
const minPasswordLen = 6

// readCredentials decodes and validates the {username,password} body shared by
// login and signup. It writes the error response itself and returns ok=false.
func readCredentials(w http.ResponseWriter, r *http.Request) (user, pass string, ok bool) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", "", false
	}
	body.Username = strings.TrimSpace(body.Username)
	if !pgUserRe.MatchString(body.Username) {
		http.Error(w, "username must be 1-40 chars: letters, digits, _ or -", http.StatusBadRequest)
		return "", "", false
	}
	if body.Password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return "", "", false
	}
	return body.Username, body.Password, true
}

// setSessionCookie issues the authenticated pg_user cookie.
func (p *Playground) setSessionCookie(w http.ResponseWriter, r *http.Request, user string) {
	http.SetCookie(w, &http.Cookie{
		Name:     pgCookie,
		Value:    p.cookieValue(user),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30, // 30d — persist login across tab/browser close
	})
}

// POST /auth/login {username,password} — existing accounts only. Creating one is
// POST /auth/signup: a login form that silently registers whatever username is
// typed turns every typo into a new empty account, and gives no way to tell
// "wrong password" from "no such user".
func (s *Server) handlePlaygroundLogin(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	username, password, ok := readCredentials(w, r)
	if !ok {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	users, err := p.loadUsers()
	if err != nil {
		http.Error(w, "could not read users", http.StatusInternalServerError)
		return
	}
	stored, exists := users[username]
	if !exists {
		// Failed logins are the one playground event worth a WARN: a burst of them
		// from one address is the only signal a password is being guessed, and
		// until now the whole auth path was silent in the log.
		s.proxylog.Warnf("playground: login rejected for %q from %s (no such user)", username, clientIP(r))
		http.Error(w, "no such user — sign up first", http.StatusUnauthorized)
		return
	}
	match, upgrade := checkPassword(stored, password)
	if !match {
		s.proxylog.Warnf("playground: login rejected for %q from %s (wrong password)", username, clientIP(r))
		http.Error(w, "wrong password", http.StatusUnauthorized)
		return
	}
	if upgrade != "" { // legacy plaintext entry, rewrite it as a hash
		users[username] = upgrade
		if err := p.saveUsers(users); err != nil {
			s.proxylog.Warnf("could not upgrade stored password for %q: %v", username, err)
		}
	}

	s.proxylog.Infof("playground: %q logged in from %s", username, clientIP(r))
	p.setSessionCookie(w, r, username)
	writeJSON(w, map[string]string{"username": username})
}

// POST /auth/signup {username,password} — creates an account and logs it in.
// 409 when the name is taken.
func (s *Server) handlePlaygroundSignup(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	username, password, ok := readCredentials(w, r)
	if !ok {
		return
	}
	if len(password) < minPasswordLen {
		http.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	users, err := p.loadUsers()
	if err != nil {
		http.Error(w, "could not read users", http.StatusInternalServerError)
		return
	}
	if _, exists := users[username]; exists {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		http.Error(w, "could not create user", http.StatusInternalServerError)
		return
	}
	users[username] = hash
	if err := p.saveUsers(users); err != nil {
		s.proxylog.Errorf("playground: could not save new user %q: %v", username, err)
		http.Error(w, "could not save user", http.StatusInternalServerError)
		return
	}
	s.proxylog.Infof("playground: account %q created from %s", username, clientIP(r))

	p.setSessionCookie(w, r, username)
	writeJSON(w, map[string]string{"username": username})
}

// POST /auth/logout — clears the cookie.
func (s *Server) handlePlaygroundLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: pgCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// GET /auth/me — current user from the cookie, or 401.
func (s *Server) handlePlaygroundMe(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	user := p.userFromRequest(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"username": user})
}

// GET /api/inference-key — hands a working inference API key to logged-in
// playground users. Remote playground browsers can't read /api/apikeys (admin,
// this host only, by design), so without this their in-browser /v1 calls —
// chat titles, auto-compaction, image/speech generation — would 401. The key
// carries exactly the access the server-owned turn runner already exercises
// (an unscoped key when one is configured, else the first key); with no keys
// configured it is empty and clients send no auth header.
func (s *Server) handlePlaygroundInferenceKey(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	// Mirrors the pgChain guard (requirePlaygroundOrAdmin): this host may read
	// it outright; a remote playground caller must be logged in.
	if !s.adminAllowed(r) && p.userFromRequest(r) == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"key": s.pickSelfKey("")})
}

// cookieSecret returns the HMAC key for the session cookie, loading it from
// DataDir/.cookie-secret on first use and minting a fresh 32-byte one if that
// file is absent. Persisting it is what lets a login survive a restart; if the
// dir is unwritable we keep the minted key in memory only (everyone is logged
// out on the next restart, which beats an unauthenticated cookie).
func (p *Playground) cookieSecret() []byte {
	p.secretOnce.Do(func() {
		path := filepath.Join(p.DataDir, ".cookie-secret")
		if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
			p.secret = b
			return
		}
		p.secret = make([]byte, 32)
		if _, err := rand.Read(p.secret); err != nil {
			// crypto/rand failing is unrecoverable for auth purposes; leaving the
			// key zeroed would make every signature forgeable, so panic instead.
			panic("playground: crypto/rand unavailable: " + err.Error())
		}
		if os.MkdirAll(p.DataDir, 0o755) == nil {
			os.WriteFile(path, p.secret, 0o600)
		}
	})
	return p.secret
}

// cookieValue formats the authenticated session cookie: "<user>.<mac>", where
// mac is HMAC-SHA256(secret, user). The username stays readable (it is not a
// secret) but cannot be swapped for someone else's without the key.
func (p *Playground) cookieValue(user string) string {
	m := hmac.New(sha256.New, p.cookieSecret())
	m.Write([]byte(user))
	return user + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// userFromRequest reads, validates and AUTHENTICATES the session cookie,
// returning "" for anything that does not carry a signature made with this
// server's key. A pre-HMAC (bare-username) cookie has no "." and so fails here
// — that costs those sessions one re-login, which is the right trade for a
// cookie that was previously forgeable by hand.
func (p *Playground) userFromRequest(r *http.Request) string {
	c, err := r.Cookie(pgCookie)
	if err != nil {
		return ""
	}
	user, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !pgUserRe.MatchString(user) {
		return ""
	}
	want := hmac.New(sha256.New, p.cookieSecret())
	want.Write([]byte(user))
	got, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return ""
	}
	return user
}

// GET /api/chats — the user's saved sessions (opaque JSON array, client-owned).
// PUT /api/chats — overwrite the whole array. The client owns the list (add /
// rename / delete happen client-side), the server just persists it per user.
func (s *Server) handlePlaygroundChats(w http.ResponseWriter, r *http.Request) {
	// PUT is merge-guarded: while a turn is generating, the server owns that
	// chat's in-flight assistant message, so a whole-array client PUT must not
	// revert it (see turns.go guardedChatsPut). GET stays a plain blob read.
	p := s.playground
	if r.Method == http.MethodPut && p != nil && p.turns != nil {
		user := p.userFromRequest(r)
		if user == "" {
			http.Error(w, "not logged in", http.StatusUnauthorized)
			return
		}
		var clientArr []map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlobBytes)).Decode(&clientArr); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		clientArr = p.turns.guardedChatsPut(user, clientArr)
		p.writeChatsLocked(user, clientArr)
		p.gcMedia(user)
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.serveUserBlob(w, r, (*Playground).chatsPath, "[]")
}

// GET/PUT /api/imagechats — the user's saved image threads. Same
// client-owns-the-blob model as /api/chats, just a separate file.
func (s *Server) handlePlaygroundImageChats(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).imageChatsPath, "[]")
}

// GET/PUT /api/speechchats — the user's saved speech threads. Same
// client-owns-the-blob model as /api/chats, just a separate file.
func (s *Server) handlePlaygroundSpeechChats(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).speechChatsPath, "[]")
}

// GET/PUT /api/prefs — the user's playground settings (opaque JSON object:
// system prompt, temperature, web-search / reasoning toggles, etc.). Same
// client-owns-the-blob model as chats, just defaulting to {} when absent.
func (s *Server) handlePlaygroundPrefs(w http.ResponseWriter, r *http.Request) {
	s.serveUserBlob(w, r, (*Playground).prefsPath, "{}")
}

// GET /api/media/{file...} — serves a generated media blob from the logged-in
// user's media dir (path is "<kind>/<name>", e.g. "image/ab12.png"). The
// path.Clean + "../" reject pins access under the media dir (no traversal);
// http.ServeFile sets Content-Type by extension and honors Range requests so
// audio scrubbing works.
func (s *Server) handlePlaygroundMedia(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	user := p.userFromRequest(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	file := path.Clean("/" + r.PathValue("file")) // collapse ../, force absolute
	if file == "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(p.mediaDir(user), filepath.FromSlash(file)))
}

// serveUserBlob reads (GET) or overwrites (PUT) a per-user JSON file, opaque to
// the server. pathFn picks which file; empty is the GET response when missing.
func (s *Server) serveUserBlob(w http.ResponseWriter, r *http.Request, pathFn func(*Playground, string) string, empty string) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	user := p.userFromRequest(r)
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlobBytes)).Decode(&blob); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		blob = p.extractMedia(user, blob)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = os.WriteFile(path, blob, 0o644)
		}
		if err == nil {
			p.gcMedia(user)
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
