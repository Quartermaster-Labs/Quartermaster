package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ghUserAgent = "quartermaster-backend-manager"
	ghTimeout   = 30 * time.Second
	// releaseTTL keeps the version picker responsive without hammering the API:
	// unauthenticated GitHub allows 60 requests/hour per IP, and one modal open
	// can touch every component.
	releaseTTL = 10 * time.Minute
	// releasePage caps how far back the version picker can reach. llama.cpp cuts
	// several releases a day, so 30 is roughly a week of builds.
	releasePage = 30
)

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Release is the subset of a GitHub release the manager needs.
type Release struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"htmlUrl"`
	PublishedAt time.Time `json:"publishedAt"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

// AssetNames lists the release's asset file names.
func (r Release) AssetNames() []string {
	out := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		out = append(out, a.Name)
	}
	return out
}

// AssetByName returns the named asset.
func (r Release) AssetByName(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

type cachedReleases struct {
	rel []Release
	at  time.Time
}

// ghClient fetches (and briefly caches) release listings.
type ghClient struct {
	http *http.Client

	mu    sync.Mutex
	cache map[string]cachedReleases
}

func newGHClient() *ghClient {
	return &ghClient{
		http:  &http.Client{Timeout: ghTimeout},
		cache: map[string]cachedReleases{},
	}
}

// Releases lists a repo's recent releases, newest first. Results are cached for
// releaseTTL unless force is set (the UI's explicit "check for updates").
func (g *ghClient) Releases(ctx context.Context, repo string, force bool) ([]Release, error) {
	g.mu.Lock()
	if c, ok := g.cache[repo]; ok && !force && time.Since(c.at) < releaseTTL {
		g.mu.Unlock()
		return c.rel, nil
	}
	g.mu.Unlock()

	api := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", repo, releasePage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", ghUserAgent)
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("github rate limit reached (%s) — retry later, or set GITHUB_TOKEN", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %s: %s", repo, resp.Status)
	}
	var raw []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		if r.Draft {
			continue
		}
		rel := Release{
			Tag: r.TagName, Name: r.Name, HTMLURL: r.HTMLURL,
			PublishedAt: r.PublishedAt, Prerelease: r.Prerelease,
		}
		for _, a := range r.Assets {
			rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		}
		out = append(out, rel)
	}
	g.mu.Lock()
	g.cache[repo] = cachedReleases{rel: out, at: time.Now()}
	g.mu.Unlock()
	return out, nil
}

// pickRelease resolves a requested version: "" or "latest" => the newest
// non-prerelease that actually carries an asset for this variant/OS, else the
// exact tag.
// installable, when non-nil, reports whether a release carries an asset this
// host can actually use; "latest" skips those it can't. Real-ESRGAN's newest
// release is source-only, so without this "install latest" resolves to a
// release with nothing to download.
func pickRelease(rels []Release, tag string, installable func(Release) bool) (Release, bool) {
	usable := func(r Release) bool { return installable == nil || installable(r) }

	tag = strings.TrimSpace(tag)
	if tag != "" && !strings.EqualFold(tag, "latest") {
		for _, r := range rels {
			if r.Tag == tag {
				return r, true // an explicit tag is honoured as asked; the caller reports the mismatch
			}
		}
		return Release{}, false
	}
	for _, r := range rels {
		if !r.Prerelease && usable(r) {
			return r, true
		}
	}
	// Nothing stable is installable: fall back to a prerelease that is, then to
	// the newest release at all so the caller's error names a real tag.
	for _, r := range rels {
		if usable(r) {
			return r, true
		}
	}
	if len(rels) > 0 {
		return rels[0], true
	}
	return Release{}, false
}

// validAssetURL only permits https downloads from GitHub-controlled hosts, so a
// poisoned or MITM'd API response can't make us fetch and run an arbitrary
// binary. Same guard as internal/update.
func validAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	ok := host == "github.com" ||
		strings.HasSuffix(host, ".github.com") ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if u.Scheme != "https" || !ok {
		return fmt.Errorf("refusing non-GitHub download URL: %s", raw)
	}
	return nil
}
