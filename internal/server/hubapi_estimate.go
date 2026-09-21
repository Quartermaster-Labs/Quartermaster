package server

// Pre-download sizing for the model browser.
//
// The picker used to answer "does it fit?" from file size alone, which is a
// hint and says so. This route answers the question the user actually has —
// how much CONTEXT the file leaves room for — by Range-fetching the candidate's
// GGUF header off the hub and running the same sizer the config editor does.
// Nothing is written to disk and no model is loaded.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// How far into the file to read, in steps. The parser SKIPS the tokenizer vocab
// but the bytes still have to be in the stream to skip over, and that array —
// not the dimensions anything is computed from — is what sets the size: a
// 250k-token vocab with its merges runs to several MB on its own. Measured
// against real repos, the first step is where the models tested land (Qwen3-4B
// parses at 6.0 MiB, gemma-3-12b and Llama-3.1-8B at 8.0), which leaves no room
// to cut it further — 6 MiB would make the second step the common case.
//
// Each step fetches only the bytes it ADDS (see hub.FetchRange), so guessing low
// costs a second round trip but never a second copy of the same megabytes. That
// is what makes a tight first step affordable: re-requesting from zero would
// spend more on a miss than the whole header is worth.
var hubHeadSteps = []int64{8 << 20, 24 << 20, hub.MaxHeaderBytes}

// hubEstimateResp is one picker row's sizing. Every field is best-effort: a
// header that won't parse answers with Err set and the UI falls back to the
// size-only verdict it already had.
type hubEstimateResp struct {
	Repo    string  `json:"repo"`
	Path    string  `json:"path"`
	Fits    bool    `json:"fits"`
	Ctx     int     `json:"ctx"`     // context the sizer picked for this budget
	MaxCtx  int     `json:"maxCtx"`  // the model's own trained ceiling
	AtMax   bool    `json:"atMax"`   // Ctx reached MaxCtx — "max context"
	Offload bool    `json:"offload"` // part of the model lands on the CPU
	EstVram float64 `json:"estVramGB"`
	Target  float64 `json:"targetVramGB"`
	Err     string  `json:"err,omitempty"`
}

// hubEstimateCache memoizes by repo+path. A header pull is a network round trip
// to a CDN and the picker asks for every row of a repo at once, so without this
// re-opening a repo re-fetches the same headers. Entries are small and the
// answer only moves when the VRAM target does, which is why the target is part
// of the key.
type hubEstCacheEntry struct {
	resp hubEstimateResp
	at   time.Time
}

var (
	hubEstMu    sync.Mutex
	hubEstCache = map[string]hubEstCacheEntry{}
)

const hubEstTTL = 30 * time.Minute

// hubEstimateFanout bounds how many rows one batch sizes at a time. It is not
// a throughput knob: a repo's quants share ONE header fetch (see hubMetaJob),
// so all but the first worker is parked on that fetch anyway. It is there so a
// repo of two unrelated models does not open a fetch per model at once.
const hubEstimateFanout = 4

// hubEstimateMaxPaths caps one batch. A picker never shows this many rows; the
// cap is for a request that did not come from it.
const hubEstimateMaxPaths = 64

// handleAPIHubEstimate sizes candidate files before they are downloaded.
//
// Sharded models are sized as a SET: `bytes` is summed over every file sharing
// the candidate's group, taken from the hub's own listing rather than the
// request, because shard 1's own length would price a fifth of the weights.
//
// `path` MAY BE REPEATED, and that is the form the picker uses. One row per
// request was the obvious shape and the wrong one: a browser allows six
// connections per origin, one of which the /api/events stream holds forever,
// so a repo sized five rows at a time left no socket for anything else on the
// page — clicking Download did nothing until a header fetch finished. A batch
// answers the whole repo over ONE connection, streaming NDJSON so rows still
// fill in as they resolve rather than landing together at the end.
func (s *Server) handleAPIHubEstimate(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	if s.autogen == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "sizing needs -generate")
		return
	}
	q := r.URL.Query()
	src, ok := s.hub.Source(q.Get("source"))
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown hub")
		return
	}
	repo := strings.Trim(q.Get("repo"), "/")
	paths := q["path"]
	if repo == "" || len(paths) == 0 || paths[0] == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "repo and path are required")
		return
	}
	if len(paths) > hubEstimateMaxPaths {
		paths = paths[:hubEstimateMaxPaths]
	}

	// LoadGenerateFile, NOT LoadBaseSettings: the base file carries the
	// hand-authored VRAM target, while the one the user actually set in the
	// dashboard lives in the sidecar's settings patch and is only overlaid here.
	// Reading the base alone sized every candidate against a stale budget (a
	// shipped file says 7 GB), so every row reported the same near-budget
	// footprint regardless of the model. Every other sizing path in this package
	// loads it this way.
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	set := gf.Settings

	// A single path keeps the plain-JSON answer it always had: the batch form is
	// an addition, not a replacement, and one row is still asked for on its own.
	if len(paths) == 1 {
		writeJSON(w, s.hubEstimateCached(r, src, repo, paths[0], set))
		return
	}
	s.streamHubEstimates(w, r, src, repo, paths, set)
}

// streamHubEstimates writes one JSON object per line as each row resolves.
//
// NDJSON rather than one array at the end because the point of the batch is to
// stop holding five sockets, NOT to make the table land later: the first row
// still appears as soon as the shared header parses. Order is completion order,
// which is why every row repeats its own repo+path.
func (s *Server) streamHubEstimates(w http.ResponseWriter, r *http.Request, src hub.Source, repo string, paths []string, set autogen.Settings) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var mu sync.Mutex // serializes the writer; workers finish in any order
	enc := json.NewEncoder(w)
	emit := func(row hubEstimateResp) {
		mu.Lock()
		defer mu.Unlock()
		if err := enc.Encode(row); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	work := make(chan string)
	var wg sync.WaitGroup
	n := min(hubEstimateFanout, len(paths))
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range work {
				emit(s.hubEstimateCached(r, src, repo, path, set))
			}
		}()
	}
feed:
	for _, path := range paths {
		select {
		case work <- path:
		case <-r.Context().Done():
			// The user closed the repo or navigated away. Nothing left to write
			// this to, so stop handing out work.
			break feed
		}
	}
	close(work)
	wg.Wait()
}

// hubEstimateCached is one row, memoized. Failures are answered as a row with
// Err set, never as an HTTP error: the picker still renders that row, just
// without a context figure, and in a batch one bad path must not sink the rest.
func (s *Server) hubEstimateCached(r *http.Request, src hub.Source, repo, path string, set autogen.Settings) hubEstimateResp {
	if set.TargetVramGB <= 0 {
		// No budget, nothing to size against. Not an error — the picker simply
		// keeps showing sizes.
		return hubEstimateResp{Repo: repo, Path: path, Err: "no VRAM target configured"}
	}

	key := repo + "\x00" + path + "\x00" + src.ID()
	hubEstMu.Lock()
	if e, ok := hubEstCache[key]; ok && time.Since(e.at) < hubEstTTL && e.resp.Target == set.TargetVramGB {
		hubEstMu.Unlock()
		return e.resp
	}
	hubEstMu.Unlock()

	out := s.hubEstimate(r, src, repo, path, set)
	hubEstMu.Lock()
	hubEstCache[key] = hubEstCacheEntry{resp: out, at: time.Now()}
	hubEstMu.Unlock()
	return out
}

// hubEstimate does the work: total the shard set, pull the header, size it.
// Every failure lands in Err rather than an HTTP error — the row this answers
// still renders, just without a context figure.
func (s *Server) hubEstimate(r *http.Request, src hub.Source, repo, path string, set autogen.Settings) hubEstimateResp {
	out := hubEstimateResp{Repo: repo, Path: path, Target: set.TargetVramGB}

	det, err := src.Detail(r.Context(), repo)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	var total int64
	var group string
	for _, f := range det.Files {
		if f.Path == path {
			group = f.Group
			break
		}
	}
	if group == "" {
		out.Err = "no such file in this repo"
		return out
	}
	for _, f := range det.Files {
		if f.Group == group {
			total += f.SizeBytes
		}
	}

	meta, err := s.hubModelMeta(r, src, repo, path, group, total)
	if err != nil {
		out.Err = err.Error()
		return out
	}

	// Ctx 0 = let the sizer pick the largest window that fits, which is exactly
	// the number the row wants to show.
	est, err := autogen.EstimatePlan(set, meta, autogen.EstimateInput{})
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Ctx = est.Ctx
	out.MaxCtx = int(meta.ContextLength)
	// Within one rounding step counts as the whole window. The sizer floors its
	// pick to a 4096 boundary, so a model trained at, say, 128000 tokens tops out
	// at 124928 and would otherwise never read as "max" no matter how much VRAM
	// the row is sized against.
	out.AtMax = out.MaxCtx > 0 && out.Ctx+4096 > out.MaxCtx
	out.EstVram = est.EstVramGB
	out.Fits = est.EstVramGB <= set.TargetVramGB
	// A plan that leaves layers (or experts) on the CPU still "fits" the budget —
	// it just runs slower, and the row says so instead of implying full residency.
	out.Offload = est.NCpuMoe > 0 || (meta.BlockCount > 0 && est.Ngl < int(meta.BlockCount))
	return out
}

// hubMetaFamily reduces a file name to the MODEL it is a quantization of, by
// cutting the quant tag out of it: `Qwen3-8B-Q4_K_M.gguf` and
// `Qwen3-8B-Q2_K.gguf` both reduce to `QWEN3-8B|.GGUF`. Everything the header
// answers apart from file size is architecture — layer count, heads, vocab,
// trained window — and quantizing a model does not change any of it, so one
// header speaks for every quant beside it.
//
// The part after the tag is KEPT, not discarded: `…-Q4_K_M-MTP.gguf` carries
// extra prediction layers and is a different model from `…-Q4_K_M.gguf`. A name
// with no quant tag at all returns "" — a repo can hold two unrelated models
// (and always holds the projector), and sharing a header between those would be
// a confidently wrong answer rather than a missing one.
func hubMetaFamily(name string) string {
	q := quantFromPath(name)
	if q == "" {
		return ""
	}
	up := strings.ToUpper(name)
	i := strings.Index(up, q)
	if i < 0 {
		return ""
	}
	cut := "-_."
	return strings.TrimRight(up[:i], cut) + "|" + strings.TrimLeft(up[i+len(q):], cut)
}

// hubMetaJob is one in-flight-or-finished header parse. It exists so the five
// rows of a repo the picker sizes CONCURRENTLY produce one fetch between them:
// a plain cache would have all five miss together and pull the same header five
// times, which is the whole cost this is trying to avoid.
type hubMetaJob struct {
	done chan struct{}
	meta autogen.Metadata
	err  error
	at   time.Time
}

var hubMetaJobs = map[string]*hubMetaJob{} // guarded by hubEstMu

// hubModelMeta returns the parsed header for a candidate, fetched once per model
// rather than once per file. The one field that genuinely differs between quants
// — the weight bytes — never came from the header anyway: it is the size the hub
// listed, so it is stamped on per caller.
func (s *Server) hubModelMeta(r *http.Request, src hub.Source, repo, path, group string, totalBytes int64) (autogen.Metadata, error) {
	key := src.ID() + "\x00" + repo + "\x00"
	if fam := hubMetaFamily(group); fam != "" {
		key += fam
	} else {
		key += "#" + group // speaks only for itself
	}

	hubEstMu.Lock()
	job, ok := hubMetaJobs[key]
	if ok {
		select {
		case <-job.done:
			if time.Since(job.at) >= hubEstTTL {
				ok = false
			}
		default: // still running — wait on it rather than starting a second one
		}
	}
	if !ok {
		job = &hubMetaJob{done: make(chan struct{})}
		hubMetaJobs[key] = job
		hubEstMu.Unlock()

		meta, err := s.hubHeaderMeta(r, src, repo, path, totalBytes)
		job.meta, job.err = meta, err
		hubEstMu.Lock()
		job.at = time.Now()
		if err != nil {
			// Failures are not cached: a header that failed because THIS request
			// was cancelled must not answer for the rest of the repo.
			delete(hubMetaJobs, key)
		}
		hubEstMu.Unlock()
		close(job.done)
	} else {
		hubEstMu.Unlock()
		select {
		case <-job.done:
		case <-r.Context().Done():
			return autogen.Metadata{}, r.Context().Err()
		}
	}
	if job.err != nil {
		return autogen.Metadata{}, job.err
	}

	meta := job.meta
	meta.Path = path
	meta.FileSizeGB = float64(totalBytes) / (1 << 30)
	return meta, nil
}

// hubHeaderMeta pulls and parses the candidate's GGUF header, EXTENDING the
// prefix a step at a time until it parses. A truncated header fails inside the
// parser as an unexpected EOF, which is the only reliable signal that the
// tensor-info table ran past what was fetched — the header carries no length of
// its own. Each step asks only for the bytes past what is already in hand, so
// the whole walk costs one copy of the header however many steps it takes.
func (s *Server) hubHeaderMeta(r *http.Request, src hub.Source, repo, path string, totalBytes int64) (autogen.Metadata, error) {
	var buf []byte
	var lastErr error
	for _, upto := range hubHeadSteps {
		want := upto - int64(len(buf))
		if want <= 0 {
			continue
		}
		chunk, err := s.hub.FetchRange(r.Context(), src, repo, path, int64(len(buf)), want)
		if err != nil {
			return autogen.Metadata{}, err
		}
		short := int64(len(chunk)) < want
		buf = append(buf, chunk...)

		meta, err := autogen.ReadGgufMetadataFrom(bytes.NewReader(buf), path, totalBytes)
		if err == nil {
			return meta, nil
		}
		lastErr = err
		if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			break // a real parse failure; a longer prefix won't help
		}
		if short {
			break // the whole file was shorter than the ask, so there is no more
		}
	}
	return autogen.Metadata{}, lastErr
}
