package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// Job phases, in order. A job ends in done, error or canceled.
//
// PhasePaused is the one phase that is neither running nor terminal: the job
// stopped on purpose, its bytes are on disk, and Resume picks it up where it
// stopped. It exists because pause and cancel had been the same operation —
// Cancel kept the `.part` too, so the only button on a download was labelled
// for the destructive meaning and did the non-destructive thing.
const (
	PhaseQueued      = "queued"
	PhaseChecking    = "checking"
	PhaseDownloading = "downloading"
	PhaseRegistering = "registering"
	PhasePaused      = "paused"
	PhaseDone        = "done"
	PhaseError       = "error"
	PhaseCanceled    = "canceled"
)

// Why a stop happened. The transfer is stopped the same way for both — the job
// context is canceled — so the intent has to be recorded before the cancel, or
// run() cannot tell a pause from a discard.
const (
	stopPause  = "pause"
	stopCancel = "cancel"
)

const (
	maxJobs        = 20
	attempts       = 5               // per file, each resuming where the last stopped
	stallTimeout   = 2 * time.Minute // no bytes for this long => retry
	progressEvery  = 1 << 20         // report at most every MiB
	diskMarginByte = 2 << 30         // keep 2 GiB free after the download
	partSuffix     = ".part"
)

// JobFile is one file inside a download job.
type JobFile struct {
	Path string `json:"path"` // repo-relative
	Size int64  `json:"size"` // as the hub reported it (0 when unknown)
	Done int64  `json:"done"`
	// Skipped marks a file already present at full size AND at the revision the
	// hub is currently serving — a re-run of a job that half-finished picks up
	// where it left off rather than refetching. A file present at full size but
	// at a superseded content id is NOT skipped: that is the case where the
	// publisher replaced the quant under its own name.
	Skipped bool `json:"skipped,omitempty"`
	// OID is the hub's content id for the revision this job is fetching, empty
	// when the hub states none. Carried on the job because run() needs to
	// compare it against the manifest, and recorded there once the bytes land.
	OID string `json:"oid,omitempty"`
	// Replaced marks a file this job overwrote rather than created: it was on
	// disk, and either it was superseded upstream or the user forced the pull.
	// It is what stops a stale `.part` from a previous revision being resumed
	// into the new one.
	Replaced bool `json:"replaced,omitempty"`
}

// Job tracks one download from admission to config regeneration. It is copied
// out under the manager lock, never handed out by pointer.
type Job struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Repo       string    `json:"repo"`
	Label      string    `json:"label,omitempty"` // the picked file's name, for the UI
	Dir        string    `json:"dir"`
	Files      []JobFile `json:"files"`
	Phase      string    `json:"phase"`
	Downloaded int64     `json:"downloaded"`
	Total      int64     `json:"total"`
	Err        string    `json:"error,omitempty"`
	// Gated marks the 401/403 case, so the UI can offer the model page link
	// instead of a retry button.
	Gated bool `json:"gated,omitempty"`
	// Force refetches every file in the job even when one is already on disk at
	// full size and at a matching content id. It is the escape hatch for the
	// case the manifest cannot see: a file replaced behind our back, or one
	// that predates the manifest and so has no recorded identity to compare.
	Force    bool      `json:"force,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
}

// Done reports whether the job reached a terminal phase.
func (j Job) Done() bool {
	return j.Phase == PhaseDone || j.Phase == PhaseError || j.Phase == PhaseCanceled
}

// Manager owns the download job list and the destination layout.
//
// One job runs per repo at a time — a second request for the same repo is
// QUEUED, not refused, and starts when the running one ends. Different repos
// download in parallel, and the files inside one job run sequentially, which is
// deliberate: three concurrent 20 GB pulls on one line finish no sooner and
// make every progress bar useless. Queueing exists because the alternative was
// an error at the moment of picking: two quants of one repo, or a model plus
// its projector, are the ordinary case, and "come back in forty minutes and
// click again" is not an answer.
type Manager struct {
	// ModelsRoot resolves the models folder at call time rather than at
	// construction, because it is a config value that a live reload can change.
	ModelsRoot func() string
	// OnComplete runs after a job's files are all on disk, for the caller to
	// regenerate the config and hot-reload. Optional.
	OnComplete func(Job) error

	log func(string)
	src map[string]Source
	hc  *http.Client

	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	active map[string]context.CancelFunc // job id -> cancel
	stop   map[string]string             // job id -> stopPause | stopCancel
	byRepo map[string]string             // source/repo -> running job id
	// wait is the admission queue, oldest first: jobs registered while their
	// repo was busy. They sit in PhaseQueued and are launched by pumpLocked
	// when the repo frees up.
	wait []string
	seq  int
}

func NewManager(modelsRoot func() string, log func(string), sources ...Source) *Manager {
	if log == nil {
		log = func(string) {}
	}
	m := &Manager{
		ModelsRoot: modelsRoot,
		log:        log,
		src:        map[string]Source{},
		jobs:       map[string]*Job{},
		active:     map[string]context.CancelFunc{},
		stop:       map[string]string{},
		byRepo:     map[string]string{},
		// No client timeout: a 40 GB file legitimately takes hours. The per-
		// attempt context, the response-header timeout and the stall watchdog
		// are what bound a hung transfer.
		hc: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 60 * time.Second,
				TLSHandshakeTimeout:   30 * time.Second,
			},
		},
	}
	for _, s := range sources {
		m.src[s.ID()] = s
	}
	return m
}

// Source looks up an adapter by id.
func (m *Manager) Source(id string) (Source, bool) {
	if id == "" {
		id = "hf"
	}
	s, ok := m.src[id]
	return s, ok
}

// Sources lists the registered adapters, id-sorted for a stable UI.
func (m *Manager) Sources() []Source {
	out := make([]Source, 0, len(m.src))
	for _, s := range m.src {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		if j, ok := m.jobs[id]; ok {
			out = append(out, *j)
		}
	}
	return out
}

func (m *Manager) Job(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (m *Manager) update(id string, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		fn(j)
	}
}

// Pause stops a running job and keeps every byte on disk. Resume continues from
// the `.part` — across a restart too, since the transfer resumes from whatever
// is already in the file rather than from a remembered offset.
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	cancel, ok := m.active[id]
	if ok {
		m.stop[id] = stopPause
	} else if m.dequeueLocked(id) {
		// Never started: there is no transfer to stop and no bytes to keep, so
		// parking it is just taking it out of the queue. It lands in the same
		// phase as a paused transfer so Resume is the one verb that restarts
		// either.
		if j, known := m.jobs[id]; known {
			j.Phase = PhasePaused
		}
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %s is not running", id)
	}
	cancel()
	return nil
}

// Cancel stops a job and DISCARDS its partial bytes — that is the whole
// difference from Pause, and why the UI asks first. Files the job had already
// finished go too: a lone shard of a multi-part GGUF is not a model, and leaving
// one behind publishes a broken entry into the catalog. Files that were already
// present before the job started (Skipped) are never touched.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	cancel, running := m.active[id]
	if running {
		m.stop[id] = stopCancel
	}
	j, known := m.jobs[id]
	var snap Job
	if known {
		snap = *j
	}
	// A job still in the admission queue never opened a file. Drop it from the
	// queue and mark it canceled WITHOUT discarding anything: any `.part` under
	// that repo belongs to whatever is running (or paused) there, not to this
	// job, and deleting it would throw away someone else's bytes.
	if !running && known && m.dequeueLocked(id) {
		j.Phase, j.Finished = PhaseCanceled, time.Now()
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if !known {
		return fmt.Errorf("unknown job %s", id)
	}
	if running {
		// run() does the discarding once the transfer has actually stopped —
		// deleting a file the writer still holds open fails on Windows.
		cancel()
		return nil
	}
	if snap.Phase != PhasePaused {
		return fmt.Errorf("job %s is not running", id)
	}
	m.discard(snap)
	m.update(id, func(j *Job) { j.Phase, j.Finished = PhaseCanceled, time.Now() })
	return nil
}

// Resume restarts a paused job under its original id, so the UI keeps one row
// instead of growing a new one per pause.
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown job %s", id)
	}
	if j.Phase != PhasePaused {
		m.mu.Unlock()
		return fmt.Errorf("job %s is not paused", id)
	}
	src, okSrc := m.src[j.Source]
	if !okSrc {
		m.mu.Unlock()
		return fmt.Errorf("unknown hub %q", j.Source)
	}
	key := j.Source + "/" + j.Repo
	j.Phase, j.Err, j.Gated = PhaseQueued, "", false
	j.Finished = time.Time{}
	if _, busy := m.byRepo[key]; busy {
		// The repo is busy, so this waits its turn rather than being refused —
		// same admission rule as Start.
		m.wait = append(m.wait, id)
	} else {
		m.launchLocked(id, key, src)
	}
	m.mu.Unlock()
	return nil
}

// launchLocked starts the background transfer for an already-registered job.
// Caller holds m.mu. Shared by Start and Resume so the bookkeeping — the
// per-repo lock, the cancel func, the stop intent — has exactly one owner.
func (m *Manager) launchLocked(id, key string, src Source) {
	m.byRepo[key] = id
	runCtx, cancel := context.WithCancel(context.Background())
	m.active[id] = cancel
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, id)
			delete(m.stop, id)
			delete(m.byRepo, key)
			// The repo just freed up: whatever was waiting on it starts here,
			// which is the only place a queued job is ever launched from.
			m.pumpLocked()
			m.mu.Unlock()
		}()
		// A panic in here would otherwise take the whole process down with it —
		// this is a bare goroutine, so unlike the HTTP handlers nothing above it
		// recovers. The dangerous part is not the transfer but OnComplete, which
		// runs the caller's config regeneration + hot reload over a models tree
		// that just changed: the same code reached from a request survives a bug
		// as a 500, and reached from here it killed quartermaster right as a
		// 40 GB download landed. Fail the job, print the stack, stay up.
		defer func() {
			if r := recover(); r != nil {
				m.log(fmt.Sprintf("hub: PANIC in download job %s: %v\n%s", id, r, debug.Stack()))
				m.update(id, func(j *Job) {
					j.Phase, j.Finished = PhaseError, time.Now()
					j.Err = fmt.Sprintf("internal error: %v (the files may still be on disk; see the log)", r)
				})
			}
		}()
		m.run(runCtx, id, src)
	}()
}

// pumpLocked launches every queued job whose repo is now free, oldest first.
// Caller holds m.mu.
//
// It also drops entries that are no longer waiting for anything: a job canceled
// or paused out of the queue leaves its id behind, and the history trim can
// remove one outright.
func (m *Manager) pumpLocked() {
	kept := m.wait[:0]
	for _, id := range m.wait {
		j, ok := m.jobs[id]
		if !ok || j.Phase != PhaseQueued {
			continue
		}
		src, okSrc := m.src[j.Source]
		if !okSrc {
			j.Phase, j.Finished = PhaseError, time.Now()
			j.Err = fmt.Sprintf("unknown hub %q", j.Source)
			continue
		}
		key := j.Source + "/" + j.Repo
		if _, busy := m.byRepo[key]; busy {
			kept = append(kept, id)
			continue
		}
		m.launchLocked(id, key, src)
	}
	m.wait = kept
}

// dequeueLocked removes a job from the admission queue and reports whether it
// was there. Membership is the ONLY reliable test for "queued but not started":
// a job is in PhaseQueued for the moment between launch and run()'s first
// phase update too, and treating that one as unstarted would leave a live
// transfer running behind a canceled row. Caller holds m.mu.
func (m *Manager) dequeueLocked(id string) bool {
	for i, w := range m.wait {
		if w == id {
			m.wait = append(m.wait[:i], m.wait[i+1:]...)
			return true
		}
	}
	return false
}

// discard removes what this job wrote: every `.part`, plus the files the job
// itself completed. Never a Skipped file — that one predates the job and may
// well be a model the user is running.
func (m *Manager) discard(j Job) {
	var gone []string
	for _, f := range j.Files {
		if f.Skipped {
			continue
		}
		dst := filepath.Join(j.Dir, filepath.FromSlash(f.Path))
		if err := os.Remove(dst + partSuffix); err == nil {
			m.log("hub: discarded partial " + f.Path + partSuffix)
		}
		if f.Size > 0 && f.Done >= f.Size {
			if err := os.Remove(dst); err == nil {
				m.log("hub: discarded " + f.Path)
				gone = append(gone, f.Path)
			}
		}
	}
	// A record for a file that is no longer there would be judged against the
	// next download of the same name, which is a different file entirely.
	m.forgetManifest(j.Dir, gone)
	// Both only succeed while empty, which is exactly when they should go — the
	// repo folder, then the publisher folder it nests in.
	if os.Remove(j.Dir) == nil {
		_ = os.Remove(filepath.Dir(j.Dir))
	}
}

// manifestName is the per-repo record of what we downloaded, written next to
// the bytes. Dotted so it reads as metadata, and ignored by model discovery,
// which is looking for weights.
const manifestName = ".quartermaster-hub.json"

// manifest records, per repo-relative path, the hub's content id AT THE MOMENT
// WE FETCHED IT.
//
// It exists because a filename is not an identity. Publishers re-upload a quant
// in place — a fixed chat template, a corrected tokenizer, a rebuild against a
// newer converter — under the same name, and often at a byte count so close to
// the old one that a size comparison cannot tell the two apart. Without a
// recorded id, the new revision reads as "already downloaded" and the only way
// to get it is to rename or delete the file by hand.
//
// It is trust-on-first-write, deliberately: verifying the on-disk bytes against
// the id would mean hashing tens of gigabytes to render a picker row. So the
// manifest describes what this downloader put there, not what is there now. A
// file replaced behind our back keeps its old record and will read as current —
// which is what the forced re-download is for. A file with no record at all
// (hand-copied, or downloaded before this existed) falls back to the size check
// and is never called stale, because we have nothing to call it stale against.
type manifest struct {
	Files map[string]manifestEntry `json:"files"`
}

type manifestEntry struct {
	Size    int64     `json:"size"`
	OID     string    `json:"oid,omitempty"`
	Fetched time.Time `json:"fetched,omitzero"`
}

// manifestMu serialises the read-modify-write. Jobs are one-per-repo, so two
// writers to one manifest is not the ordinary case, but the file is small and
// losing an entry to a race would silently re-hide a stale quant.
var manifestMu sync.Mutex

// readManifest returns a repo folder's record, empty when there is none. A
// missing or unparseable manifest is not an error: it means "we know nothing
// about these files", which is exactly how the fallback path treats it.
func readManifest(dir string) manifest {
	man := manifest{Files: map[string]manifestEntry{}}
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return man
	}
	var got manifest
	if json.Unmarshal(b, &got) != nil || got.Files == nil {
		return man
	}
	return got
}

// recordManifest merges one file's identity into the repo's manifest. A failure
// to write is logged and swallowed: the bytes are on disk and usable, and
// failing a finished 40 GB download over a bookkeeping file would be absurd.
// The cost of the miss is only that the file falls back to the size check.
func (m *Manager) recordManifest(dir, path string, e manifestEntry) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	man := readManifest(dir)
	man.Files[path] = e
	b, err := json.MarshalIndent(man, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, manifestName), b, 0o644)
	}
	if err != nil {
		m.log("hub: could not record " + path + " in the repo manifest: " + err.Error())
	}
}

// forgetManifest drops entries for files that have just been deleted. Leaving
// them would describe bytes that are not there, and the next download of the
// same name would be judged against the identity of a file nobody has.
func (m *Manager) forgetManifest(dir string, paths []string) {
	if len(paths) == 0 {
		return
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	man := readManifest(dir)
	for _, p := range paths {
		delete(man.Files, p)
	}
	file := filepath.Join(dir, manifestName)
	if len(man.Files) == 0 {
		// An empty manifest would keep the repo dir alive past the cleanup that
		// removes it once the last model is gone.
		_ = os.Remove(file)
		return
	}
	if b, err := json.MarshalIndent(man, "", "  "); err == nil {
		_ = os.WriteFile(file, b, 0o644)
	}
}

// LocalFile is what this installation has for one repo-relative path: the size
// actually on disk, and the content id it was fetched at when we know one.
type LocalFile struct {
	// Size on disk. Compared against what the hub says the file weighs, since
	// a short file is a truncated copy, not a finished download.
	Size int64 `json:"size"`
	// OID is the hub's id for the revision we downloaded, empty when unknown —
	// a hand-copied file, or one that predates the manifest. Empty means "no
	// opinion", never "different": the fallback is the size check.
	OID string `json:"oid,omitempty"`
}

// LocalFiles reports which of a repo's files are already on disk, as
// repo-relative slash paths mapped to their size and recorded identity. It is
// what lets the picker say "downloaded" on a row instead of offering a 20 GB
// pull the user already has — and, since the identity came with it, what lets
// it say "update available" when the repo has replaced that file under the
// same name.
//
// A `.part` is deliberately NOT reported: a half-file is not a model, and the
// row for one should stay a download button. The manifest itself is skipped for
// the same reason it is dotted — it is our bookkeeping, not a repo file.
//
// Nothing here is authoritative about what quartermaster can load; it is a
// filename-level "is this byte range on disk, and is it the revision the hub is
// serving", which is exactly the question the picker asks. An unreadable or
// missing repo folder is an empty map, never an error: not having downloaded
// anything is the normal case.
func (m *Manager) LocalFiles(repo string) map[string]LocalFile {
	out := map[string]LocalFile{}
	root := strings.TrimSpace(m.ModelsRoot())
	if root == "" || strings.TrimSpace(repo) == "" {
		return out
	}
	dir := filepath.Join(root, RepoDirName(repo))
	man := readManifest(dir)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, partSuffix) || d.Name() == manifestName {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		lf := LocalFile{Size: info.Size()}
		// Only trust the recorded id if it still describes this file. A size
		// that has moved since means something replaced the bytes without going
		// through us, and the id we hold is about a file that is gone.
		if e, ok := man.Files[key]; ok && e.Size == lf.Size {
			lf.OID = e.OID
		}
		out[key] = lf
		return nil
	})
	return out
}

// SweepPartials deletes `.part` files under root that nothing is downloading any
// more and that have not been touched for maxAge. Orphans are otherwise
// permanent: a job list lives in memory, so a crash or a kill leaves a 12 GB
// stub in the models folder that no page ever mentions again. The age gate is
// what keeps it from eating a transfer a user paused overnight and means to
// finish — a `.part` is resumable, so deleting a fresh one throws away work.
//
// It returns how many files it removed and how many bytes that freed.
func SweepPartials(root string, maxAge time.Duration, log func(string)) (int, int64) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, 0
	}
	var n int
	var freed int64
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, partSuffix) {
			return nil //nolint:nilerr // an unreadable subtree is not worth failing a sweep over
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		size := info.Size()
		if os.Remove(p) != nil {
			return nil
		}
		n++
		freed += size
		if log != nil {
			log(fmt.Sprintf("hub: removed orphaned partial %s (%s)", p, humanBytes(size)))
		}
		return nil
	})
	return n, freed
}

// StartRequest is one download: a set of repo files that belong together.
type StartRequest struct {
	Source string   `json:"source"`
	Repo   string   `json:"repo"`
	Files  []string `json:"files"` // repo-relative paths
	Label  string   `json:"label,omitempty"`
	// Force refetches files already on disk instead of skipping them. See
	// Job.Force — it is for what the manifest cannot see, not the ordinary
	// upstream-replaced case, which run() detects on its own.
	Force bool `json:"force,omitempty"`
}

// Start admits a download and runs it in the background, returning the job id.
// Sizes come from the hub's file list so the free-disk check and the progress
// bar both have a denominator before the first byte arrives.
func (m *Manager) Start(ctx context.Context, req StartRequest) (string, error) {
	src, ok := m.Source(req.Source)
	if !ok {
		return "", fmt.Errorf("unknown hub %q", req.Source)
	}
	if len(req.Files) == 0 {
		return "", errors.New("no files selected")
	}
	det, err := src.Detail(ctx, req.Repo)
	if err != nil {
		return "", err
	}
	listed := map[string]File{}
	for _, f := range det.Files {
		listed[f.Path] = f
	}
	files := make([]JobFile, 0, len(req.Files))
	var total int64
	for _, p := range req.Files {
		f, known := listed[p]
		if !known {
			return "", fmt.Errorf("%s is not a downloadable file in %s", p, req.Repo)
		}
		// The id is taken here, from the same listing the size came from, so the
		// job records the revision it actually fetched rather than whatever the
		// repo is serving by the time the transfer finishes.
		files = append(files, JobFile{Path: p, Size: f.SizeBytes, OID: f.OID})
		total += f.SizeBytes
	}

	root := strings.TrimSpace(m.ModelsRoot())
	if root == "" {
		return "", errors.New("no models folder configured: set modelsRoot or pass -models-dir")
	}
	dir := filepath.Join(root, RepoDirName(req.Repo))

	m.mu.Lock()
	key := src.ID() + "/" + req.Repo
	m.seq++
	id := fmt.Sprintf("dl-%d-%d", time.Now().UnixNano()/1e6, m.seq)
	m.jobs[id] = &Job{
		ID: id, Source: src.ID(), Repo: req.Repo, Label: req.Label,
		Dir: dir, Files: files, Phase: PhaseQueued, Total: total,
		Force: req.Force, Started: time.Now(),
	}
	m.order = append(m.order, id)
	// Trim finished jobs out of the history, never a running one.
	for len(m.order) > maxJobs {
		old := m.order[0]
		if j, ok := m.jobs[old]; ok && !j.Done() {
			break
		}
		m.order = m.order[1:]
		delete(m.jobs, old)
	}
	if _, running := m.byRepo[key]; running {
		// One transfer per repo, but a second pick is queued rather than
		// refused: picking two quants, or a model and its projector, is the
		// ordinary case and both belong in the same folder.
		m.wait = append(m.wait, id)
	} else {
		m.launchLocked(id, key, src)
	}
	m.mu.Unlock()
	return id, nil
}

// RepoDirName maps "owner/name" onto `<owner>/<name>` under the models root —
// one folder per repo, nested under the publisher, mirroring the hub's own
// layout and the one every other local-model tool writes. It keeps multi-part
// shards and an image model's sibling vae/clip/encoder files together the way
// autogen's discovery expects (which walks the tree, so depth costs nothing).
//
// It returns a slash-separated *relative path*, not a single name: callers join
// it onto the models root, and filepath.Join normalises the separator.
func RepoDirName(repoID string) string {
	owner, name, ok := strings.Cut(repoID, "/")
	if !ok {
		return sanitizeSeg(repoID)
	}
	return filepath.Join(sanitizeSeg(owner), sanitizeSeg(name))
}

func sanitizeSeg(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
	// An all-dots segment is "." or ".." — harmless while the two halves were
	// glued with "__", a directory climb now that they are joined as a path.
	// (validRepoID already refuses one; this is the layer that must not depend
	// on that being true.)
	if strings.Trim(out, ".") == "" {
		out = strings.ReplaceAll(out, ".", "-")
	}
	return out
}

// haveCurrent reports whether the file on disk is both complete and the
// revision the hub is serving now — the only case where skipping it is right.
//
// The size test alone is what made a re-uploaded quant unreachable: publishers
// replace a file under its own name, and a rebuild at the same quant lands
// within bytes of the old one (often ON it), so `have >= want` reads the new
// revision as already downloaded and the user has to rename or delete the file
// by hand to get it. The content id settles it when both sides state one.
//
// When either side states no id the size test stands, unchanged. That is the
// deliberate conservative half: without an id we cannot distinguish "the same
// file" from "a different file of the same length", and re-pulling 40 GB on a
// guess is the worse error of the two. Force is how the user overrides it.
func (m *Manager) haveCurrent(dir string, f JobFile, man manifest, force bool) bool {
	if force {
		return false
	}
	have := existingSize(filepath.Join(dir, filepath.FromSlash(f.Path)))
	if f.Size <= 0 || have < f.Size {
		return false
	}
	// The recorded entry describes the file only while its size still matches;
	// anything else means the bytes were replaced without going through us, and
	// the id we hold is about a file that no longer exists.
	rec, known := man.Files[f.Path]
	if known && rec.Size == have && rec.OID != "" && f.OID != "" && rec.OID != f.OID {
		return false
	}
	return true
}

func (m *Manager) run(ctx context.Context, id string, src Source) {
	job, _ := m.Job(id)
	m.update(id, func(j *Job) { j.Phase = PhaseChecking })

	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		m.fail(id, err)
		return
	}

	// Free-disk check up front. Discovering at 90% that the drive is full costs
	// the whole transfer, and on Windows it also leaves an unusable .part that
	// looks like a corrupt model.
	// One manifest read for the whole job: what is on disk cannot change under
	// us mid-check, and re-reading it per file would only invite the two passes
	// below to disagree about which files they are fetching.
	man := readManifest(job.Dir)
	need := job.Total
	for i, f := range job.Files {
		if m.haveCurrent(job.Dir, f, man, job.Force) {
			job.Files[i].Skipped = true
			need -= f.Size
			continue
		}
		// A file being refetched is a REPLACEMENT, and its `.part` — if there is
		// one — was written for the revision being replaced. Resuming into it
		// would splice two different files together, so it does not count
		// toward what is already downloaded and run() removes it below.
		if existingSize(filepath.Join(job.Dir, filepath.FromSlash(f.Path))) > 0 {
			job.Files[i].Replaced = true
			continue
		}
		need -= partSize(filepath.Join(job.Dir, filepath.FromSlash(f.Path)) + partSuffix)
	}
	if need > 0 {
		if free, err := freeBytes(job.Dir); err == nil && free < need+diskMarginByte {
			m.fail(id, fmt.Errorf("not enough free disk: need %s plus headroom, %s available on %s",
				humanBytes(need), humanBytes(free), job.Dir))
			return
		}
	}

	m.update(id, func(j *Job) { j.Phase = PhaseDownloading })
	var done int64
	for i, f := range job.Files {
		dst := filepath.Join(job.Dir, filepath.FromSlash(f.Path))
		if f.Skipped {
			done += f.Size
			idx := i
			m.update(id, func(j *Job) {
				j.Files[idx].Done, j.Files[idx].Skipped = f.Size, true
				j.Downloaded = done
			})
			m.log(fmt.Sprintf("hub: %s already present, skipping", f.Path))
			continue
		}
		if f.Replaced {
			// The old bytes stay put until the new file is renamed over them,
			// so a cancel or a failure here leaves the working copy intact.
			// Only the `.part` goes, because it belongs to the old revision.
			if os.Remove(dst+partSuffix) == nil {
				m.log("hub: discarded a partial of the superseded " + f.Path)
			}
			why := "the hub is serving a different file under this name"
			if job.Force {
				why = "re-download requested"
			}
			m.log(fmt.Sprintf("hub: refetching %s (%s)", f.Path, why))
		}
		base := done
		idx := i
		err := m.fetchFile(ctx, src, job.Repo, f, dst, func(n int64) {
			m.update(id, func(j *Job) {
				j.Files[idx].Done = n
				j.Downloaded = base + n
			})
		})
		if err != nil {
			if ctx.Err() != nil {
				m.stopped(id, job.Repo)
				return
			}
			m.fail(id, err)
			return
		}
		// Record what we just fetched, so the next time this repo is opened the
		// picker can tell this revision from whatever replaces it.
		m.recordManifest(job.Dir, f.Path, manifestEntry{
			Size: existingSize(dst), OID: f.OID, Fetched: time.Now(),
		})
		done = base + f.Size
	}

	m.update(id, func(j *Job) { j.Phase = PhaseRegistering })
	if m.OnComplete != nil {
		cur, _ := m.Job(id)
		if err := m.OnComplete(cur); err != nil {
			// The files ARE on disk; only the config refresh failed. Say so
			// rather than reporting a failed download the user would retry.
			m.update(id, func(j *Job) {
				j.Phase, j.Finished = PhaseDone, time.Now()
				j.Err = "downloaded, but refreshing the config failed: " + err.Error()
			})
			return
		}
	}
	m.update(id, func(j *Job) { j.Phase, j.Finished = PhaseDone, time.Now() })
	m.log(fmt.Sprintf("hub: %s downloaded into %s", job.Repo, job.Dir))
}

// stopped lands a job that was interrupted on purpose. A pause keeps the bytes;
// a cancel discards them here, once the transfer has let go of the file — an
// open handle makes the delete fail outright on Windows.
func (m *Manager) stopped(id, repo string) {
	m.mu.Lock()
	intent := m.stop[id]
	var snap Job
	if j, ok := m.jobs[id]; ok {
		snap = *j
	}
	m.mu.Unlock()

	if intent == stopCancel {
		m.discard(snap)
		m.update(id, func(j *Job) { j.Phase, j.Finished = PhaseCanceled, time.Now() })
		m.log(fmt.Sprintf("hub: download of %s canceled, partial files removed", repo))
		return
	}
	// Default to pause, including the shutdown case: the caller lost interest in
	// the transfer, not in the bytes.
	m.update(id, func(j *Job) { j.Phase, j.Finished = PhasePaused, time.Now() })
	m.log(fmt.Sprintf("hub: download of %s paused", repo))
}

func (m *Manager) fail(id string, err error) {
	var ae *AuthError
	gated := errors.As(err, &ae)
	m.update(id, func(j *Job) {
		j.Phase, j.Finished, j.Err, j.Gated = PhaseError, time.Now(), err.Error(), gated
	})
	m.log("hub: download failed: " + err.Error())
}

// fetchFile downloads one file with Range resume, retrying on a dropped or
// stalled connection. Bytes land in `<dst>.part` and are renamed into place
// only on success, so a half-file is never visible to model discovery.
func (m *Manager) fetchFile(ctx context.Context, src Source, repo string, f JobFile, dst string, onProgress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	url, err := src.FileURL(repo, f.Path)
	if err != nil {
		return err
	}
	part := dst + partSuffix
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 1 {
			wait := time.Duration(1<<uint(attempt-1)) * time.Second
			m.log(fmt.Sprintf("hub: %s attempt %d/%d in %s (%v)", f.Path, attempt, attempts, wait, lastErr))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		n, err := m.fetchOnce(ctx, src, url, part, onProgress)
		if err == nil {
			// Rename over any stale file: a partial from a previous quant with
			// the same name would otherwise win.
			if err := os.Rename(part, dst); err != nil {
				return err
			}
			if f.Size > 0 && n != f.Size {
				m.log(fmt.Sprintf("hub: warning: %s is %d bytes, hub reported %d", f.Path, n, f.Size))
			}
			return nil
		}
		lastErr = err
		var ae *AuthError
		if errors.As(err, &ae) || ctx.Err() != nil {
			// A license wall and a cancel are both terminal; retrying a 403
			// five times just delays telling the user to accept the license.
			ae2 := ae
			if ae2 != nil {
				ae2.Repo = repo
			}
			return err
		}
	}
	return fmt.Errorf("%s: %w", f.Path, lastErr)
}

// fetchOnce runs a single attempt, resuming from whatever is already in part.
// It returns the total size of part on success.
func (m *Manager) fetchOnce(ctx context.Context, src Source, url, part string, onProgress func(int64)) (int64, error) {
	if err := src.CheckURL(url); err != nil {
		return 0, err
	}
	have := partSize(part)

	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", hfUserAgent)
	src.Authorize(req)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	// Every redirect hop is host-checked, so a poisoned Location cannot walk the
	// download off the hub.
	hc := *m.hc
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return src.CheckURL(r.URL.String())
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		// The server ignored Range (or there was nothing to resume): start over.
		have = 0
		flags |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		// Already complete on disk from an earlier run.
		return have, nil
	default:
		return 0, hubHTTPError(resp)
	}

	fh, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, err
	}
	defer fh.Close()

	// Stall watchdog: a TCP connection that goes quiet without erroring would
	// otherwise hang the job forever, and "downloading, 41%" for six hours is
	// the worst possible failure mode.
	var lastAt atomic.Int64
	lastAt.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastAt.Load())) > stallTimeout {
					cancel()
					return
				}
			}
		}
	}()

	pr := &progressReader{
		r:    resp.Body,
		base: have,
		on:   onProgress,
		tick: func() { lastAt.Store(time.Now().UnixNano()) },
	}
	written, err := io.Copy(fh, pr)
	total := have + written
	if err != nil {
		if ctx.Err() == nil && attemptCtx.Err() != nil {
			return total, fmt.Errorf("transfer stalled for %s", stallTimeout)
		}
		return total, err
	}
	if err := fh.Sync(); err != nil {
		return total, err
	}
	onProgress(total)
	return total, nil
}

// progressReader reports absolute bytes-on-disk at most every progressEvery, so
// a 40 GB file does not generate a million callbacks (each of which takes the
// manager lock).
type progressReader struct {
	r       io.Reader
	base    int64 // bytes already on disk before this attempt
	done    int64
	lastRep int64
	on      func(int64)
	tick    func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		if p.tick != nil {
			p.tick()
		}
		if p.on != nil && p.done-p.lastRep >= progressEvery {
			p.lastRep = p.done
			p.on(p.base + p.done)
		}
	}
	return n, err
}

func existingSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

func partSize(path string) int64 { return existingSize(path) }

func freeBytes(dir string) (int64, error) {
	// Stat the nearest existing ancestor: the repo folder may not exist yet.
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			u, err := disk.Usage(d)
			if err != nil {
				return 0, err
			}
			return int64(u.Free), nil
		}
		if parent := filepath.Dir(d); parent == d {
			return 0, fmt.Errorf("no existing ancestor of %s", dir)
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
