package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// HF is the Hugging Face adapter.
//
// Only the read-only public API is used: /api/models for search, /api/models/{id}
// for the file list, and the raw README for the model page. Downloads go through
// /resolve/, which answers with a CDN redirect — hence CheckURL allowing the
// CDN hosts as well as huggingface.co itself.
type HF struct {
	// Token, when set, is sent as a bearer for gated and private repos. It
	// never reaches the browser: every hub call is proxied server-side.
	Token func() string

	hc    *http.Client
	once  sync.Once
	mu    sync.Mutex
	cache map[string]hfCached
}

type hfCached struct {
	at  time.Time
	val any
}

const (
	hfAPI       = "https://huggingface.co"
	hfCacheTTL  = 10 * time.Minute
	hfTimeout   = 20 * time.Second
	hfUserAgent = "quartermaster"
)

func NewHF() *HF { return &HF{} }

func (h *HF) ID() string   { return "hf" }
func (h *HF) Name() string { return "Hugging Face" }

func (h *HF) client() *http.Client {
	h.once.Do(func() {
		h.hc = &http.Client{Timeout: hfTimeout}
		h.cache = map[string]hfCached{}
	})
	return h.hc
}

// Authorize attaches the stored token. Go's http client drops the Authorization
// header on a cross-domain redirect, so the CDN leg is unauthenticated by
// construction — which is what HF's signed URLs expect.
func (h *HF) Authorize(req *http.Request) {
	if h.Token == nil {
		return
	}
	if t := strings.TrimSpace(h.Token()); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

// CheckURL pins downloads to hosts Hugging Face controls. The file path comes
// from the model list (and ultimately from the user), so the host must be ours
// no matter what the API or a redirect claims.
func (h *HF) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	ok := host == "huggingface.co" ||
		strings.HasSuffix(host, ".huggingface.co") ||
		host == "hf.co" ||
		strings.HasSuffix(host, ".hf.co")
	if u.Scheme != "https" || !ok {
		return fmt.Errorf("refusing non-Hugging-Face download URL: %s", raw)
	}
	return nil
}

func (h *HF) FileURL(repoID, path string) (string, error) {
	if err := validRepoID(repoID); err != nil {
		return "", err
	}
	if err := validRepoPath(path); err != nil {
		return "", err
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return hfAPI + "/" + repoID + "/resolve/main/" + strings.Join(segs, "/"), nil
}

// --- search ---

type hfModelJSON struct {
	ID           string   `json:"id"`
	Author       string   `json:"author"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"lastModified"`
	CreatedAt    string   `json:"createdAt"`
	Pipeline     string   `json:"pipeline_tag"`
	Tags         []string `json:"tags"`
	Private      bool     `json:"private"`
	// Gated is `false` for open repos and a string ("auto"/"manual") for gated
	// ones, so it cannot be decoded straight into a bool.
	Gated json.RawMessage `json:"gated"`
	Sibs  []hfSibling     `json:"siblings"`
}

type hfSibling struct {
	Path string `json:"rfilename"`
	Size int64  `json:"size"`
	// Oid is the git blob sha of a plain file. Non-LFS in a GGUF repo means a
	// README or a config, so in practice the LFS oid below is the one that
	// matters — but taking both costs nothing and keeps the identity defined
	// for every file we might download.
	Oid string `json:"oid"`
	LFS *struct {
		Size int64  `json:"size"`
		Oid  string `json:"oid"`
	} `json:"lfs"`
}

func (m hfModelJSON) toModel() Model {
	author, name := m.Author, m.ID
	if i := strings.Index(m.ID, "/"); i >= 0 {
		if author == "" {
			author = m.ID[:i]
		}
		name = m.ID[i+1:]
	}
	out := Model{
		ID:        m.ID,
		Source:    "hf",
		Author:    author,
		Name:      name,
		Downloads: m.Downloads,
		Likes:     m.Likes,
		Pipeline:  m.Pipeline,
		Tags:      m.Tags,
		Private:   m.Private,
		Gated:     len(m.Gated) > 0 && string(m.Gated) != "false" && string(m.Gated) != "null",
		ParamsB:   ParamsB(name),
	}
	if t, err := time.Parse(time.RFC3339, m.LastModified); err == nil {
		out.Updated = t
	}
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		out.Created = t
	}
	return out
}

// searchFilters maps a Query.Kind onto the hub-side filter tags, ANDed by the
// API. Filtering at the hub beats filtering locally: an unfiltered "qwen" search
// is thousands of safetensors repos quartermaster cannot load, and a category
// tab that filtered a 30-row page client-side would mostly show nothing.
//
// The kinds are the UI's own model categories (`MODEL_CATEGORIES` in
// ui-svelte/src/lib/modelUtils.ts) so browsing and the local catalog agree on
// what "Image" or "TTS" means; each pairs `gguf` with the hub's pipeline tag,
// since a repo we cannot load is not worth listing under any category.
func searchFilters(kind string) []string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "llm", "text", "gguf":
		return []string{"gguf"}
	case "any":
		return nil
	case "image":
		return []string{"gguf", "text-to-image"}
	case "tts":
		return []string{"gguf", "text-to-speech"}
	case "transcribe":
		return []string{"gguf", "automatic-speech-recognition"}
	case "embed":
		return []string{"gguf", "feature-extraction"}
	case "segment":
		// mask-generation, not image-segmentation: it is what the SAM/BiRefNet
		// GGUF repos quartermaster's segment backend loads are tagged with.
		return []string{"gguf", "mask-generation"}
	default:
		return []string{strings.ToLower(strings.TrimSpace(kind))}
	}
}

// hfExpand is the field set every search asks for.
//
// `expand[]` is opt-in per field — asking for one drops all the others — so the
// list has to name everything Model carries. It exists for `createdAt`, which
// the plain listing does not return and which is the only honest input to the
// "trendy" filter: `lastModified` moves for a README fix.
var hfExpand = []string{
	"author", "downloads", "likes", "lastModified", "createdAt",
	"pipeline_tag", "tags", "gated", "private",
}

func (h *HF) Search(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	sortBy := q.Sort
	switch sortBy {
	case "likes", "lastModified", "downloads":
	case "modified":
		sortBy = "lastModified"
	default:
		sortBy = "downloads"
	}
	// Neither the size cap nor the age gate is a hub filter — HF has no
	// parameter-count filter and no created-after one — so ask for more rows
	// than a page needs or a filtered page comes back nearly empty.
	fetch := limit
	if q.MaxParamsB > 0 || q.MaxAgeDays > 0 {
		fetch = min(limit*3, 100)
	}

	v := url.Values{}
	if s := strings.TrimSpace(q.Text); s != "" {
		v.Set("search", s)
	}
	for _, f := range searchFilters(q.Kind) {
		v.Add("filter", f)
	}
	for _, f := range hfExpand {
		v.Add("expand[]", f)
	}
	v.Set("sort", sortBy)
	v.Set("direction", "-1")
	v.Set("limit", fmt.Sprint(fetch))
	if q.Skip > 0 {
		v.Set("skip", fmt.Sprint(q.Skip))
	}

	key := "search:" + v.Encode()
	if hit, ok := cacheGet[[]Model](h, key); ok {
		return hfPage(hit, q, fetch), nil
	}
	var raw []hfModelJSON
	if err := h.getJSON(ctx, hfAPI+"/api/models?"+v.Encode(), &raw); err != nil {
		return Page{}, err
	}
	out := make([]Model, 0, len(raw))
	for _, m := range raw {
		out = append(out, m.toModel())
	}
	// Cache what the hub said, filter after: the caps are a per-request view of
	// the same page, so a capped search must not poison an uncapped one.
	cachePut(h, key, out)
	return hfPage(out, q, fetch), nil
}

// hfPage applies the response-side filters and reports where the NEXT page
// starts. The offset advances by what the hub returned, never by what survived
// — the filters are a view of that page, not a re-ordering of the hub's list.
//
// Nothing is trimmed to Limit: Limit sizes the fetch, and trimming the survivors
// would throw away rows the caller has already paid a round trip for and would
// then have to re-request under a different offset.
func hfPage(raw []Model, q Query, fetch int) Page {
	out := capParams(raw, q.MaxParamsB, 0)
	out = capAge(out, q.MaxAgeDays)
	return Page{
		Models:   out,
		NextSkip: q.Skip + len(raw),
		// A short page means the hub has nothing more under this query.
		HasMore: len(raw) >= fetch && fetch > 0,
	}
}

// capAge drops repos created longer than maxDays ago. A repo stating no
// creation date is KEPT, the same posture as an unreadable parameter count:
// this filter narrows a listing, and it should never be the reason a repo is
// invisible for a fact about its metadata rather than about the model.
func capAge(in []Model, maxDays int) []Model {
	if maxDays <= 0 {
		return in
	}
	cutoff := time.Now().AddDate(0, 0, -maxDays)
	out := make([]Model, 0, len(in))
	for _, m := range in {
		if m.Created.IsZero() || m.Created.After(cutoff) {
			out = append(out, m)
		}
	}
	return out
}

// capParams applies the size cap and trims to the requested page size.
func capParams(in []Model, maxB float64, limit int) []Model {
	out := in
	if maxB > 0 {
		out = make([]Model, 0, len(in))
		for _, m := range in {
			// Judge by the field the browser is also shown, not by a second
			// parse of the id: the owner segment is part of the id and could
			// carry a number of its own, and a filter that disagrees with the
			// badge next to it is unexplainable.
			if m.ParamsB == 0 || m.ParamsB < maxB {
				out = append(out, m)
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// --- author avatars ---

// Avatar resolves one author's avatar image URL, or "" when they have none.
//
// HF has two account kinds behind one namespace and no endpoint that covers
// both, so an org lookup is tried first and a user lookup second — `unsloth` is
// an org, `bartowski` is a person, and a repo id doesn't say which. The "" for
// a miss is CACHED like any hit: a namespace that is neither answers 404 twice,
// and the browser asks again for every row of every search.
func (h *HF) Avatar(ctx context.Context, author string) (string, error) {
	if err := validSegment(author); err != nil {
		return "", err
	}
	key := "avatar:" + author
	if hit, ok := cacheGet[string](h, key); ok {
		return hit, nil
	}
	var body struct {
		AvatarURL string `json:"avatarUrl"`
	}
	for _, kind := range []string{"organizations", "users"} {
		var got struct {
			AvatarURL string `json:"avatarUrl"`
		}
		if err := h.getJSON(ctx, hfAPI+"/api/"+kind+"/"+url.PathEscape(author)+"/avatar", &got); err == nil && got.AvatarURL != "" {
			body = got
			break
		}
	}
	cachePut(h, key, body.AvatarURL)
	return body.AvatarURL, nil
}

// --- detail ---

func (h *HF) Detail(ctx context.Context, repoID string) (ModelDetail, error) {
	if err := validRepoID(repoID); err != nil {
		return ModelDetail{}, err
	}
	key := "detail:" + repoID
	if hit, ok := cacheGet[ModelDetail](h, key); ok {
		return hit, nil
	}
	var raw hfModelJSON
	// blobs=true is what makes siblings carry a size; without it the file list
	// is names only and the file picker has no sizes to show.
	if err := h.getJSON(ctx, hfAPI+"/api/models/"+repoID+"?blobs=true", &raw); err != nil {
		return ModelDetail{}, err
	}
	det := ModelDetail{Model: raw.toModel()}
	if det.ID == "" {
		det.ID = repoID
	}
	for _, s := range raw.Sibs {
		if !IsModelFile(s.Path) {
			continue
		}
		f := File{Path: s.Path, SizeBytes: s.Size, OID: s.Oid}
		if s.LFS != nil {
			if f.SizeBytes == 0 {
				f.SizeBytes = s.LFS.Size
			}
			// The LFS oid identifies the CONTENT; the plain oid identifies the
			// pointer file that stands in for it, which changes for reasons the
			// bytes don't. Prefer the content id wherever there is one.
			if s.LFS.Oid != "" {
				f.OID = s.LFS.Oid
			}
		}
		classify(&f)
		det.Files = append(det.Files, f)
	}
	sort.Slice(det.Files, func(i, j int) bool { return det.Files[i].Path < det.Files[j].Path })
	det.Readme = h.readme(ctx, repoID)
	cachePut(h, key, det)
	return det, nil
}

// readme fetches the model card. A missing or oversized card is not an error —
// the file list is the useful half of the page.
func (h *HF) readme(ctx context.Context, repoID string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hfAPI+"/"+repoID+"/raw/main/README.md", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", hfUserAgent)
	h.Authorize(req)
	resp, err := h.client().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return ""
	}
	return string(b)
}

// --- plumbing ---

func (h *HF) getJSON(ctx context.Context, rawURL string, out any) error {
	if err := h.CheckURL(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", hfUserAgent)
	req.Header.Set("Accept", "application/json")
	h.Authorize(req)
	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return hubHTTPError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// hubHTTPError turns a hub response into an error the UI can act on. 401/403 on
// a gated repo means "accept the license", which is a link the user follows —
// not a retry.
func hubHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthError{Status: resp.StatusCode, Detail: msg}
	case http.StatusNotFound:
		return fmt.Errorf("not found on the hub (404)")
	}
	if msg == "" {
		return fmt.Errorf("hub request failed: %s", resp.Status)
	}
	return fmt.Errorf("hub request failed: %s: %s", resp.Status, msg)
}

// AuthError marks the gated/private case so the HTTP layer can tell the user to
// accept the license or add a token, rather than showing a generic failure.
type AuthError struct {
	Status int
	Detail string
	Repo   string
}

func (e *AuthError) Error() string {
	if e.Repo != "" {
		return fmt.Sprintf("access denied for %s (%d): accept the license on the model page, or add a Hugging Face token", e.Repo, e.Status)
	}
	return fmt.Sprintf("access denied by the hub (%d): the repo is gated or private", e.Status)
}

func cacheGet[T any](h *HF, key string) (T, bool) {
	var zero T
	h.client()
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.cache[key]
	if !ok || time.Since(c.at) > hfCacheTTL {
		return zero, false
	}
	v, ok := c.val.(T)
	return v, ok
}

func cachePut(h *HF, key string, val any) {
	h.client()
	h.mu.Lock()
	defer h.mu.Unlock()
	// Bound the map: a session that browses a hundred repos should not pin
	// their file lists and READMEs for the process lifetime.
	if len(h.cache) > 64 {
		for k, c := range h.cache {
			if time.Since(c.at) > hfCacheTTL {
				delete(h.cache, k)
			}
		}
		if len(h.cache) > 128 {
			h.cache = map[string]hfCached{}
		}
	}
	h.cache[key] = hfCached{at: time.Now(), val: val}
}

// validRepoID accepts "owner/name" with hub-legal characters only, so a repo id
// can never inject path segments or query strings into an API URL.
func validRepoID(id string) error {
	owner, name, ok := strings.Cut(id, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("invalid repo id %q, want owner/name", id)
	}
	for _, s := range []string{owner, name} {
		if err := validSegment(s); err != nil {
			return fmt.Errorf("invalid repo id %q", id)
		}
	}
	return nil
}

// validSegment checks one hub-legal name part — an owner, a repo name, or an
// author looked up on its own. Runs before the value becomes a URL path.
func validSegment(s string) error {
	if s == "" || s == "." || s == ".." {
		return fmt.Errorf("invalid name %q", s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("invalid name %q", s)
		}
	}
	return nil
}

// validRepoPath rejects anything that could escape the destination folder once
// joined, before it is ever used as a URL or a filename.
func validRepoPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("invalid file path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid file path %q", p)
		}
	}
	return nil
}
