package backends

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job phases, in order. A job ends in "done" or "error".
const (
	PhaseResolving   = "resolving"
	PhaseDownloading = "downloading"
	PhaseExtracting  = "extracting"
	PhaseRegistering = "registering"
	PhaseDone        = "done"
	PhaseError       = "error"
)

// maxJobs bounds the job history the UI polls.
const maxJobs = 20

// Job tracks one install from resolve to registration. It is copied out under
// the manager lock, never handed out by pointer.
type Job struct {
	ID         string    `json:"id"`
	Component  string    `json:"component"`
	Variant    string    `json:"variant"`
	Version    string    `json:"version"` // resolved tag once known
	Phase      string    `json:"phase"`
	Asset      string    `json:"asset,omitempty"`
	Downloaded int64     `json:"downloaded"`
	Total      int64     `json:"total"`
	Err        string    `json:"error,omitempty"`
	Exe        string    `json:"exe,omitempty"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished,omitzero"`
}

// Done reports whether the job reached a terminal phase.
func (j Job) Done() bool { return j.Phase == PhaseDone || j.Phase == PhaseError }

// Manager owns the install directory, the GitHub client and the job list.
//
// One install runs per component at a time (a second request for the same
// component is refused, not queued) — different components install in parallel.
type Manager struct {
	root string // bundle root; installs land under <root>/bin/<component>/
	log  func(string)
	gh   *ghClient
	dl   *http.Client

	// GpuNames reports the host's GPU names for variant auto-selection. Optional.
	GpuNames func() []string
	// OnInstalled is called after a successful install so the caller can point
	// the backend registry at the new exe and regenerate the config. Optional.
	OnInstalled func(Installed) error

	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	active map[string]string // component -> job id currently running
	seq    int
}

// NewManager builds a manager rooted at the bundle directory (the folder holding
// the quartermaster executable, unless overridden).
func NewManager(root string, log func(string)) *Manager {
	if log == nil {
		log = func(string) {}
	}
	if strings.TrimSpace(root) == "" {
		root = defaultRoot()
	}
	return &Manager{
		root:   root,
		log:    log,
		gh:     newGHClient(),
		dl:     &http.Client{Timeout: 0}, // per-request context carries the deadline
		jobs:   map[string]*Job{},
		active: map[string]string{},
	}
}

// defaultRoot is the directory holding the running executable — the same
// bundle-relative layout the Windows installer and every other runtime path in
// this project use.
func defaultRoot() string {
	if self, err := os.Executable(); err == nil {
		return filepath.Dir(self)
	}
	wd, _ := os.Getwd()
	return wd
}

// Root returns the install root (bin/ hangs off this).
func (m *Manager) Root() string { return m.root }

// Releases lists a component's recent releases (cached; force refetches).
func (m *Manager) Releases(ctx context.Context, comp string, force bool) ([]Release, error) {
	c, ok := Find(comp)
	if !ok {
		return nil, fmt.Errorf("unknown component %q", comp)
	}
	return m.gh.Releases(ctx, c.Repo, force)
}

// Jobs returns the job history, newest first.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		if j, ok := m.jobs[id]; ok {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Job returns one job by id.
func (m *Manager) Job(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// update mutates a job under the lock.
func (m *Manager) update(id string, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		fn(j)
	}
}

// Install starts a background install of one component build and returns the
// job id. version "" or "latest" resolves to the newest non-prerelease.
func (m *Manager) Install(comp, variant, version string) (string, error) {
	c, ok := Find(comp)
	if !ok {
		return "", fmt.Errorf("unknown component %q", comp)
	}
	if c.Manual {
		return "", fmt.Errorf("%s cannot be installed automatically: %s", c.Name, c.Setup)
	}
	if variant == "" {
		variant = c.DefaultVariant(m.gpuNames(), runtime.GOOS)
	}
	if _, ok := c.Variant(variant); !ok {
		return "", fmt.Errorf("%s: unknown variant %q", comp, variant)
	}

	m.mu.Lock()
	if prev, busy := m.active[c.ID]; busy {
		m.mu.Unlock()
		return "", fmt.Errorf("%s is already installing (job %s)", c.Name, prev)
	}
	m.seq++
	id := c.ID + "-" + strconv.Itoa(m.seq)
	m.jobs[id] = &Job{
		ID: id, Component: c.ID, Variant: variant, Version: version,
		Phase: PhaseResolving, Started: time.Now(),
	}
	m.order = append(m.order, id)
	m.active[c.ID] = id
	// Trim history, never dropping a running job.
	for len(m.order) > maxJobs {
		old := m.order[0]
		if j, ok := m.jobs[old]; ok && !j.Done() {
			break
		}
		delete(m.jobs, old)
		m.order = m.order[1:]
	}
	m.mu.Unlock()

	go m.run(id, c, variant, version)
	return id, nil
}

func (m *Manager) gpuNames() []string {
	if m.GpuNames == nil {
		return nil
	}
	return m.GpuNames()
}

// Suggest returns the variant a fresh install of comp should preselect.
func (m *Manager) Suggest(comp string) string {
	c, ok := Find(comp)
	if !ok {
		return ""
	}
	return c.DefaultVariant(m.gpuNames(), runtime.GOOS)
}

// run performs the install. Errors land on the job, never on a caller — the UI
// polls for them.
func (m *Manager) run(id string, c Component, variant, version string) {
	defer func() {
		m.mu.Lock()
		delete(m.active, c.ID)
		m.mu.Unlock()
	}()

	fail := func(err error) {
		m.log(fmt.Sprintf("backend install %s failed: %v", id, err))
		m.update(id, func(j *Job) {
			j.Phase, j.Err, j.Finished = PhaseError, err.Error(), time.Now()
		})
	}

	ctx := context.Background()
	rels, err := m.gh.Releases(ctx, c.Repo, false)
	if err != nil {
		fail(err)
		return
	}
	rel, ok := pickRelease(rels, version, func(r Release) bool {
		_, _, err := c.MatchAssets(variant, runtime.GOOS, r.AssetNames())
		return err == nil
	})
	if !ok {
		fail(fmt.Errorf("%s: no release %q found in the last %d releases", c.ID, version, releasePage))
		return
	}
	primary, extras, err := c.MatchAssets(variant, runtime.GOOS, rel.AssetNames())
	if err != nil {
		fail(err)
		return
	}
	asset, _ := rel.AssetByName(primary)
	m.update(id, func(j *Job) {
		j.Version, j.Asset, j.Total, j.Phase = rel.Tag, primary, asset.Size, PhaseDownloading
	})

	// Stage into a sibling ".tmp" directory so a failed download or extract can
	// never leave a half-built version that readManifest would accept.
	final := m.InstallDir(c.ID, rel.Tag, variant)
	staging := final + ".tmp"
	if err := os.RemoveAll(staging); err != nil {
		fail(err)
		return
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(staging) // no-op once renamed away

	tmpDir, err := os.MkdirTemp("", "qm-backend-")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(tmpDir)

	fetch := func(a Asset) (string, error) {
		dst := filepath.Join(tmpDir, sanitizeSeg(a.Name))
		err := m.download(ctx, a.URL, dst, func(done, total int64) {
			m.update(id, func(j *Job) {
				j.Downloaded = done
				if total > 0 {
					j.Total = total
				}
			})
		})
		return dst, err
	}

	dl, err := fetch(asset)
	if err != nil {
		fail(err)
		return
	}

	m.update(id, func(j *Job) { j.Phase = PhaseExtracting })
	var exe string
	if c.Bare {
		// The asset IS the executable — no archive, nothing to search for.
		exe = filepath.Join(staging, c.ExeName())
		if err := copyFile(dl, exe); err != nil {
			fail(err)
			return
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(exe, 0o755)
		}
	} else {
		if err := extract(dl, staging); err != nil {
			fail(err)
			return
		}
		// Extras (the CUDA runtime) go next to the primary build. Best-effort:
		// a missing cudart shouldn't discard an otherwise-good llama-server.
		for _, name := range extras {
			ea, ok := rel.AssetByName(name)
			if !ok {
				continue
			}
			ed, err := fetch(ea)
			if err != nil {
				m.log(fmt.Sprintf("backend install %s: extra asset %s failed: %v", id, name, err))
				continue
			}
			if err := extract(ed, staging); err != nil {
				m.log(fmt.Sprintf("backend install %s: extracting %s failed: %v", id, name, err))
			}
		}
		exe, err = findExe(staging, c.ExeName())
		if err != nil {
			fail(err)
			return
		}
	}

	relExe, err := filepath.Rel(staging, exe)
	if err != nil {
		fail(err)
		return
	}
	// Whole install, not just the executable: the DLLs beside it are most of the
	// download. Extras (cudart) are already unpacked at this point.
	size, _ := dirSize(staging)
	if err := writeManifest(staging, manifest{
		Component: c.ID, Version: rel.Tag, Variant: variant,
		Exe: filepath.ToSlash(relExe), Asset: primary,
		InstalledAt: time.Now(), SizeBytes: size,
	}); err != nil {
		fail(err)
		return
	}

	// Reinstalling the same version+variant replaces it wholesale.
	if err := os.RemoveAll(final); err != nil {
		fail(err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		fail(err)
		return
	}
	if err := os.Rename(staging, final); err != nil {
		fail(err)
		return
	}

	inst, err := readManifest(final)
	if err != nil {
		fail(err)
		return
	}
	m.update(id, func(j *Job) { j.Phase, j.Exe = PhaseRegistering, inst.Exe })
	if m.OnInstalled != nil {
		if err := m.OnInstalled(inst); err != nil {
			fail(fmt.Errorf("installed to %s but registering it failed: %w", inst.Dir, err))
			return
		}
	}
	m.log(fmt.Sprintf("installed %s %s (%s) -> %s", c.ID, rel.Tag, variant, inst.Exe))
	m.update(id, func(j *Job) { j.Phase, j.Finished = PhaseDone, time.Now() })
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(dst, in, 0o755)
}
