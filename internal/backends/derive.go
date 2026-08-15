package backends

import (
	"fmt"
	"regexp"
	"strings"
)

// Asset-pattern derivation for user-tracked repos.
//
// The built-in catalog carries hand-written asset regexes because we ship one
// table for every user. A user tracking their own repo gets the same machinery
// without ever seeing a regex: they pick the asset they want out of the newest
// release, and DeriveUnique turns that one example into a pattern that will
// still match the equivalent asset in next week's build.
//
// The rule is simply "version-ish tokens vary, everything else identifies the
// build":
//
//	llama-b1234-bin-win-vulkan-x64.zip -> ^llama-.*-bin-win-vulkan-x64\.zip$
//
// Two properties matter more than cleverness here, because the result decides
// which binary gets downloaded and executed:
//
//   - It must not match a *different* flavour. DeriveUnique verifies the
//     derived pattern against the whole release and tightens it until it
//     matches exactly the asset that was picked.
//   - When it cannot be made safe, it degrades to the literal file name, which
//     fails closed on the next release ("assets changed, re-pick") rather than
//     silently installing the wrong build.

// repoRe bounds an "owner/name" GitHub repo reference.
//
// This is a security control, not a nicety: ghClient.Releases interpolates the
// repo straight into an api.github.com URL path, so a user-supplied "../..",
// "@evil.example", scheme or query would re-target the request at whatever the
// attacker likes and validAssetURL would then happily accept the assets it
// named. Everything outside this character set is rejected before it can reach
// a URL.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidateRepo checks an "owner/name" reference, rejecting anything that could
// escape the API URL path.
func ValidateRepo(repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return fmt.Errorf("repository is required")
	}
	if !repoRe.MatchString(repo) {
		return fmt.Errorf("%q is not an owner/name GitHub repository", repo)
	}
	// "." and ".." are valid path segments to the regex above but not valid repo
	// names, and they are exactly the ones that would traverse the API path.
	for _, seg := range strings.Split(repo, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%q is not an owner/name GitHub repository", repo)
		}
	}
	return nil
}

// ParseRepo accepts either "owner/name" or a github.com URL and returns the
// validated "owner/name" form. The UI lets the user paste whatever they have in
// the address bar.
func ParseRepo(in string) (string, error) {
	s := strings.TrimSpace(in)
	s = strings.TrimSuffix(s, "/")
	if i := strings.Index(s, "github.com/"); i >= 0 {
		s = s[i+len("github.com/"):]
	}
	s = strings.TrimSuffix(s, ".git")
	// A pasted URL can carry extra path segments (/releases, /tree/main, …).
	if parts := strings.Split(s, "/"); len(parts) > 2 {
		s = parts[0] + "/" + parts[1]
	}
	if err := ValidateRepo(s); err != nil {
		return "", err
	}
	return s, nil
}

// versionRes are the token shapes that change from release to release and must
// therefore become wildcards. Anything else is treated as identifying the build
// (platform, GPU runtime, architecture, extension).
var versionRes = []*regexp.Regexp{
	regexp.MustCompile(`^v?[0-9]+$`),             // 1, v2, 1234
	regexp.MustCompile(`^b[0-9]+$`),              // llama.cpp build numbers: b1234
	regexp.MustCompile(`^r[0-9]+$`),              // r7
	regexp.MustCompile(`^[0-9]{8}$`),             // 20260815 (nightly dates)
	regexp.MustCompile(`^[0-9a-f]{7,40}$`),       // git shas
	regexp.MustCompile(`^v?[0-9]+[a-z]?[0-9]*$`), // 1a, 2rc1-ish single tokens
	regexp.MustCompile(`^(rc|alpha|beta|pre)[0-9]*$`),
}

func versionish(tok string) bool {
	t := strings.ToLower(tok)
	if t == "" {
		return false
	}
	for _, re := range versionRes {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

// piece is one lexical unit of an asset name: a separator run, or a token that
// is either literal or wildcarded.
type piece struct {
	text string
	sep  bool
	wild bool
}

// isSep reports the characters asset names use to join their parts. Splitting
// on these (rather than on any non-alphanumeric) keeps "gfx110X" and "x64" as
// single identifying tokens.
func isSep(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '+' }

// lex splits an asset name into alternating token/separator pieces and marks
// which tokens vary between releases.
func lex(name string) []piece {
	var out []piece
	cur := strings.Builder{}
	curSep := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		s := cur.String()
		out = append(out, piece{text: s, sep: curSep, wild: !curSep && versionish(s)})
		cur.Reset()
	}
	for i, r := range name {
		s := isSep(r)
		if i > 0 && s != curSep {
			flush()
		}
		curSep = s
		cur.WriteRune(r)
	}
	flush()
	return out
}

// render turns lexed pieces into an anchored regex. Adjacent wildcards (and the
// separators between them) collapse into a single ".*" so a "13.3" version
// becomes one wildcard rather than ".*\..*".
func render(ps []piece) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(ps); i++ {
		if !ps[i].wild {
			b.WriteString(regexp.QuoteMeta(ps[i].text))
			continue
		}
		// Swallow the rest of this wildcard run: (wild sep)* wild.
		for i+2 < len(ps) && ps[i+1].sep && ps[i+2].wild {
			i += 2
		}
		b.WriteString(".*")
	}
	b.WriteByte('$')
	return b.String()
}

// literal is the exact-name pattern — the fail-closed fallback.
func literal(name string) string { return "^" + regexp.QuoteMeta(name) + "$" }

// DerivePattern turns one asset name into an anchored pattern with its
// version-ish parts wildcarded. It does not consider the rest of the release;
// callers that have the full asset list should use DeriveUnique.
func DerivePattern(name string) string {
	ps := lex(name)
	pat := render(ps)
	// A name that is nothing but version tokens would derive to "^.*$" and match
	// every asset in the release. Pin it to the literal name instead.
	if !hasLiteral(ps) {
		return literal(name)
	}
	return pat
}

func hasLiteral(ps []piece) bool {
	for _, p := range ps {
		if !p.sep && !p.wild {
			return true
		}
	}
	return false
}

// DeriveUnique derives a pattern for `name` that matches it and nothing else in
// `all` (the asset names of the release it was picked from).
//
// Wildcarding every version-ish token is right for the common case but wrong
// when a release ships several builds that differ *only* in a version — the
// canonical example being llama.cpp's side-by-side CUDA 12.x and 13.x zips,
// where the naive pattern matches both and the install becomes a coin flip.
// When that happens the rightmost wildcard is restored to its literal text and
// the check repeats, so the toolkit version gets pinned while the build number
// stays free. This is the same guarantee Variant.PairKey gives the built-in
// catalog, arrived at from an example instead of a hand-written capture.
//
// If no amount of tightening disambiguates, the literal name is returned: a
// future release simply won't match, which surfaces as "re-pick this asset"
// rather than as the wrong binary on disk.
func DeriveUnique(name string, all []string) string {
	ps := lex(name)
	if !hasLiteral(ps) {
		return literal(name)
	}
	for {
		pat := render(ps)
		re, err := regexp.Compile(pat)
		if err != nil {
			return literal(name)
		}
		hits := 0
		for _, n := range all {
			if re.MatchString(n) {
				hits++
			}
		}
		// 0 hits is impossible for a well-formed derivation (the source name is in
		// `all`), but treating it as ambiguous keeps the fallback honest if `all`
		// omits it.
		if hits == 1 {
			return pat
		}
		if !unwildRightmost(ps) {
			return literal(name)
		}
	}
}

// unwildRightmost restores the last wildcarded token to a literal, reporting
// whether there was one left to restore.
func unwildRightmost(ps []piece) bool {
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i].wild {
			ps[i].wild = false
			return true
		}
	}
	return false
}

// SuggestLabel names a variant from the parts of its asset name that identify
// the build, so a tracked source arrives with "Windows gfx110X" rather than
// "variant 1". Purely cosmetic — the user can rename it.
func SuggestLabel(name string) string {
	// Drop the extension and any leading repo-ish prefix, keep identifying tokens.
	base := name
	for _, ext := range []string{".tar.gz", ".tgz", ".zip", ".exe", ".7z"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	var keep []string
	for _, p := range lex(base) {
		if p.sep || p.wild {
			continue
		}
		l := strings.ToLower(p.text)
		if l == "bin" || l == "x64" || l == "amd64" {
			continue
		}
		keep = append(keep, p.text)
	}
	if len(keep) == 0 {
		return "default"
	}
	if len(keep) > 4 {
		keep = keep[len(keep)-4:]
	}
	return strings.Join(keep, " ")
}

// ClosestAsset finds the asset in `all` most similar to `exemplar`, scored 0-100
// by token overlap. It is a diagnostic, never an installer: when a source's
// pattern stops matching because upstream renamed its assets, this is what turns
// a bare "no asset found" into "closest is X — re-pick?".
func ClosestAsset(exemplar string, all []string) (string, int) {
	want := tokenSet(exemplar)
	if len(want) == 0 {
		return "", 0
	}
	best, bestScore := "", 0
	for _, n := range all {
		got := tokenSet(n)
		inter := 0
		for t := range want {
			if got[t] {
				inter++
			}
		}
		union := len(want) + len(got) - inter
		if union == 0 {
			continue
		}
		if s := inter * 100 / union; s > bestScore {
			best, bestScore = n, s
		}
	}
	return best, bestScore
}

func tokenSet(name string) map[string]bool {
	out := map[string]bool{}
	for _, p := range lex(name) {
		if p.sep || p.wild {
			continue
		}
		out[strings.ToLower(p.text)] = true
	}
	return out
}
