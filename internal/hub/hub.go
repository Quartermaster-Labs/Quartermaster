// Package hub browses model hubs (Hugging Face today, ModelScope later) and
// downloads model files into the models folder, so acquiring a model is no
// longer out-of-band.
//
// Two halves:
//
//   - A Source adapter (hf.go) — search, repo detail, file list, README, and
//     the rules for which hosts a download may come from. The interface exists
//     now so a second hub is an adapter, not a fork of the download manager.
//   - A download Manager (download.go) — resumable, cancellable, multi-file
//     jobs with progress the dashboard polls, mirroring internal/backends'
//     job model. That package's download() is deliberately NOT reused: it
//     os.Create's and streams with no resume, which is fine for a 200 MB
//     backend zip and useless for a 40 GB quant on a flaky line.
//
// Everything here is admin-gated at the HTTP layer (internal/server/hubapi.go)
// and never runs on behalf of an inference request.
package hub

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Query is one hub search. Text is the user's search box verbatim.
type Query struct {
	Text  string
	Limit int
	Sort  string // "downloads" (default), "likes", "modified"
	// Kind narrows by what quartermaster can actually run. "" means GGUF text
	// models, which is the overwhelmingly common case; see Source.Search.
	Kind string
	// MaxParamsB drops repos whose name says they are bigger than N billion
	// parameters. 0 = no cap. A repo whose size cannot be read from its name is
	// KEPT — see ParamsB.
	MaxParamsB float64
	// MaxAgeDays keeps only repos CREATED within N days ("trendy": what is both
	// popular and new, since the list is already ordered by the chosen sort).
	// 0 = any age. Repos stating no creation date are kept, same posture as
	// MaxParamsB.
	MaxAgeDays int
	// Skip is the offset into the hub's own result list, for paging. The UI
	// loads more as the user scrolls rather than capping at a page size.
	Skip int
}

// Page is one slice of a hub's result list.
//
// NextSkip counts the hub's OWN rows, not the surviving ones: the adapters
// over-fetch and then filter (size cap, age), so advancing by the number of
// models returned here would silently re-request rows already shown — or skip
// rows never shown.
type Page struct {
	Models   []Model `json:"models"`
	NextSkip int     `json:"nextSkip"`
	HasMore  bool    `json:"hasMore"`
}

// AvatarSource is an OPTIONAL extra a Source may implement: the publisher's
// avatar image URL, or "" when they have none.
//
// Deliberately not part of Source. It is decoration — a row without a picture
// falls back to a monogram and loses nothing — and forcing every future adapter
// to implement it would be paying for the hub that has no such endpoint. Type-
// assert for it; see handleAPIHubAvatar.
type AvatarSource interface {
	Avatar(ctx context.Context, author string) (string, error)
}

// Model is one repo in a search result list.
type Model struct {
	ID        string    `json:"id"`     // "owner/name"
	Source    string    `json:"source"` // adapter id, e.g. "hf"
	Author    string    `json:"author"`
	Name      string    `json:"name"`
	Downloads int64     `json:"downloads"`
	Likes     int64     `json:"likes"`
	Updated   time.Time `json:"updated,omitzero"`
	// Created is when the repo was first published. It is what the "trendy"
	// filter judges by: Updated moves for a README fix, so a two-year-old repo
	// touched yesterday is not a new release.
	Created  time.Time `json:"created,omitzero"`
	Pipeline string    `json:"pipeline,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Gated    bool      `json:"gated"`   // needs a license accepted on the hub
	Private  bool      `json:"private"` // needs a token
	// ParamsB is the size in billions of parameters read out of the repo NAME
	// (see ParamsB), 0 when it states none. Sent so the browser renders the same
	// number the size filter judged the repo by — a UI that parsed it again
	// would eventually disagree with the filter, which is unexplainable to a
	// user looking at both.
	ParamsB float64 `json:"paramsB,omitempty"`
}

// File is one downloadable file in a repo.
type File struct {
	Path      string `json:"path"` // repo-relative, may contain subfolders
	SizeBytes int64  `json:"sizeBytes"`
	// Shard/Shards describe multi-part GGUFs (`-00001-of-00003.gguf`). Every
	// file sharing a Group is one logical download: a lone shard is useless.
	Shard  int    `json:"shard,omitempty"`
	Shards int    `json:"shards,omitempty"`
	Group  string `json:"group"`
	// Projector marks a vision/audio mmproj file — a companion to a model's
	// weights, not a model. The picker sorts these last and labels the fit
	// column "companion", since a projector is charged on top of whichever file
	// you pick rather than sized on its own.
	Projector bool `json:"projector,omitempty"`
	// Local marks a file already sitting in the models folder at (at least) the
	// size the hub reports. It is filled in by the HTTP layer, not by an
	// adapter: what is on disk is a property of this installation, not of the
	// hub. See Manager.LocalFiles and handleAPIHubModel.
	Local bool `json:"local,omitempty"`
}

// ModelDetail is a repo page: metadata, README, and the full file list.
type ModelDetail struct {
	Model
	Readme string `json:"readme,omitempty"`
	Files  []File `json:"files"`
}

// Source is one model hub. Implementations must be safe for concurrent use.
type Source interface {
	ID() string
	Name() string
	Search(ctx context.Context, q Query) (Page, error)
	Detail(ctx context.Context, repoID string) (ModelDetail, error)
	// FileURL builds the download URL for a repo-relative path.
	FileURL(repoID, path string) (string, error)
	// CheckURL rejects any URL not served by this hub's own hosts. The path is
	// user-chosen; the host must not be. Applied to redirects too, since the
	// hub answers file requests with a CDN redirect.
	CheckURL(raw string) error
	// Authorize attaches credentials for gated/private repos, if configured.
	Authorize(req *http.Request)
}

// --- filename parsing, shared by every adapter ---

// There is deliberately no quant regex here. The picker shows each file's whole
// name, so nothing needs the tag isolated — and isolating it was a steady source
// of wrong labels: a name is only conventionally a quant tag, publishers add
// recipe markers (`UD`, `i1`) and suffixes the pattern doesn't know, and any
// miss silently mislabelled the row rather than failing. The authority for what
// a file actually is has always been the GGUF header, which autogen reads once
// the file is on disk.

// shardRe matches llama.cpp's split naming: `-00002-of-00005.gguf`.
var shardRe = regexp.MustCompile(`(?i)-(\d{5})-of-(\d{5})\.gguf$`)

// projectorRe matches a multimodal projector filename. Publishers write it both
// ways ("mmproj-F16.gguf", "Qwen2-VL-7B.mmproj.gguf") and occasionally spell it
// out, so match the token anywhere in the base name rather than anchoring it.
var projectorRe = regexp.MustCompile(`(?i)(^|[-_.])mm[-_.]?proj`)

// classify fills in Shard/Shards, Group and Projector for one repo file. Group
// is the key that turns N shards into one logical download; unsharded files are
// their own group.
func classify(f *File) {
	base := f.Path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	f.Projector = projectorRe.MatchString(base)
	if m := shardRe.FindStringSubmatch(f.Path); m != nil {
		f.Shard, _ = strconv.Atoi(m[1])
		f.Shards, _ = strconv.Atoi(m[2])
		f.Group = shardRe.ReplaceAllString(f.Path, ".gguf")
		return
	}
	f.Group = f.Path
}

// paramsRe matches a parameter count in a repo name: "70B", "3.6b", "8x7B",
// "A22B". Anchored on a separator so the "3" in "Qwen3" cannot be read as a
// size, and so a quant tag ("Q4_K_M") never matches.
var paramsRe = regexp.MustCompile(`(?i)(?:^|[-_./])(?:([0-9]+)x)?([0-9]+(?:\.[0-9]+)?)\s*b(?:[-_./]|$)`)

// ParamsB reads a repo's parameter count, in billions, out of its name. It
// returns 0 when the name does not state one.
//
// This is the only size signal a hub search gives us: the list endpoint returns
// no parameter count, and the real number lives in a GGUF header we would have
// to download the model to read. So it is a *name* parser, with two rules that
// matter:
//
//   - The FIRST size token wins. A MoE repo is named for its total and its
//     active size ("Qwen3-235B-A22B"), in that order, and the total is the one
//     that has to fit in VRAM. Taking the last match would read a 235B model as
//     a 22B one — the exact mistake this filter exists to prevent.
//   - An unreadable name means unknown, not zero. Callers KEEP unknowns:
//     hiding a repo because its author skipped the convention is worse than
//     showing one that turns out to be too big, which the file sizes on its own
//     page will say plainly.
//
// "8x7B" is expanded as the product (56), which overstates a sparse MoE's real
// total (~47B) but errs toward hiding rather than offering something too big.
func ParamsB(name string) float64 {
	for _, m := range paramsRe.FindAllStringSubmatch(name, -1) {
		n, err := strconv.ParseFloat(m[2], 64)
		if err != nil || n <= 0 {
			continue
		}
		if m[1] != "" {
			if mult, err := strconv.Atoi(m[1]); err == nil && mult > 0 {
				n *= float64(mult)
			}
		}
		return n
	}
	return 0
}

// WithinParams reports whether a repo passes a MaxParamsB cap. Unknown sizes
// pass; see ParamsB.
func WithinParams(name string, maxB float64) bool {
	if maxB <= 0 {
		return true
	}
	p := ParamsB(name)
	return p == 0 || p < maxB
}

// IsModelFile reports whether a repo file is worth listing. Repos carry
// tokenizers, configs, images and .safetensors originals that quartermaster
// has no use for; showing all of them buries the four files that matter.
func IsModelFile(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".gguf"):
		return true
	case strings.HasSuffix(lower, ".ggml"): // SAM segmentation models
		return true
	}
	return false
}
