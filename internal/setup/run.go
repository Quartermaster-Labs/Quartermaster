package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/peimports"
)

// jobPoll is how often the backend install job is sampled for progress. The
// manager throttles its own byte counter to one update per 512 KiB, so polling
// faster than this buys nothing.
const jobPoll = 250 * time.Millisecond

// Start kicks off the install described by c and returns immediately.
//
// It refuses to start a second run while one is in flight: the steps write to
// the same directory and the same generate file, and two of them interleaving
// would produce a tree matching neither set of choices. A finished or failed
// run CAN be restarted, so a user who fixes a bad path and clicks again gets a
// retry rather than a dead window.
func (w *Wizard) Start(ctx context.Context, c Choices) error {
	if strings.TrimSpace(c.Dir) == "" {
		return errors.New("no install directory given")
	}
	dir, err := filepath.Abs(c.Dir)
	if err != nil {
		return fmt.Errorf("install directory: %w", err)
	}
	c.Dir = dir

	w.mu.Lock()
	if w.busy {
		w.mu.Unlock()
		return errors.New("an install is already running")
	}
	w.busy = true
	w.st = Status{Phase: PhasePlacing, Step: "Starting", InstallDir: dir}
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			w.busy = false
			w.mu.Unlock()
		}()
		if err := w.run(ctx, c); err != nil {
			w.fail(err)
			return
		}
		w.step(PhaseDone, "Ready")
	}()
	return nil
}

// run is the whole install, in order. Each step may fail the run; only backend
// installs degrade to a warning, because a missing backend leaves a usable app
// that can fetch it later from Settings, while a missing binary or an
// unwritable generate file does not.
func (w *Wizard) run(ctx context.Context, c Choices) error {
	w.step(PhasePlacing, "Installing quartermaster")
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", c.Dir, err)
	}
	if w.opts.Place != nil {
		if err := w.opts.Place(c, func(msg string) {
			w.set(func(s *Status) { s.Detail = msg })
		}); err != nil {
			return err
		}
	}

	w.step(PhaseConfiguring, "Writing configuration")
	genPath := filepath.Join(c.Dir, "config", "quartermaster-generate.yaml")
	if err := ensureGenerate(genPath); err != nil {
		return err
	}
	// An empty modelsRoot is a legitimate answer ("I'll pick a folder later"),
	// and it still has to be written: the seeded file carries the example's
	// value, and leaving that in place would point a fresh install at a folder
	// that exists only on the machine the example was written on.
	if err := setSettingsKey(genPath, "modelsRoot", c.ModelsRoot); err != nil {
		return fmt.Errorf("setting modelsRoot: %w", err)
	}

	if len(c.Components) > 0 {
		w.step(PhaseBackends, "Downloading backends")
		w.installBackends(ctx, c, genPath)
	}
	return nil
}

// ensureGenerate makes sure a generate control file exists at path, seeding it
// from the example that ships alongside when there is one.
//
// autogen.EnsureConfig reads this file and fails hard if it is absent, so the
// wizard cannot leave the step to first boot. Seeding from the example is
// preferred purely for its comments; see minimalGenerate.
func ensureGenerate(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	example := filepath.Join(filepath.Dir(path), "quartermaster-generate.example.yaml")
	if b, err := os.ReadFile(example); err == nil && len(b) > 0 {
		return os.WriteFile(path, b, 0o644)
	}
	return os.WriteFile(path, []byte(minimalGenerate), 0o644)
}

// installBackends downloads each chosen component and records it in the sidecar
// backend registry, so the app comes up with its servers already wired.
//
// Installs run one at a time even though the manager permits different
// components in parallel: they share the user's bandwidth, and a single
// progress bar meaning "the thing named in Detail" is honest, where one summing
// three concurrent downloads is not.
func (w *Wizard) installBackends(ctx context.Context, c Choices, genPath string) {
	mgr := backends.NewManager(c.Dir, w.opts.Log)
	mgr.GpuNames = GpuNames
	mgr.Preflight = peimports.Hint

	for _, id := range c.Components {
		comp, ok := mgr.Find(id)
		if !ok {
			w.warn(fmt.Sprintf("unknown backend %q, skipped", id))
			continue
		}
		// The user picked one compute backend for the whole wizard, but not
		// every component publishes it (yt-dlp has a single "any" build, the
		// upscaler one Vulkan build per OS). Passing a variant the component
		// does not have would fail the install; passing "" lets the manager
		// fall back to that component's own default.
		variant := ""
		if v, ok := comp.Variant(c.Variant); ok && len(v.Patterns[runtime.GOOS]) > 0 {
			variant = c.Variant
		}

		name := comp.Name
		w.set(func(s *Status) { s.Detail = name; s.Downloaded, s.Total = 0, 0 })
		jobID, err := mgr.Install(comp.ID, variant, "latest")
		if err != nil {
			w.warn(fmt.Sprintf("%s: %v", name, err))
			continue
		}
		job, err := w.awaitJob(ctx, mgr, jobID, name)
		if err != nil {
			w.warn(fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if job.Warning != "" {
			w.warn(fmt.Sprintf("%s: %s", name, job.Warning))
		}
		if err := registerBackend(genPath, mgr, comp, job); err != nil {
			w.warn(fmt.Sprintf("%s installed but not registered: %v", name, err))
		}
	}
}

// awaitJob blocks until a backend install finishes, mirroring its byte counters
// into the wizard's status as it goes.
func (w *Wizard) awaitJob(ctx context.Context, mgr *backends.Manager, id, name string) (backends.Job, error) {
	t := time.NewTicker(jobPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return backends.Job{}, ctx.Err()
		case <-t.C:
			job, ok := mgr.Job(id)
			if !ok {
				return backends.Job{}, errors.New("install job vanished")
			}
			w.set(func(s *Status) {
				s.Detail = name
				s.Downloaded, s.Total = job.Downloaded, job.Total
			})
			if job.Phase == backends.PhaseError {
				return job, errors.New(job.Err)
			}
			if job.Phase == backends.PhaseDone {
				return job, nil
			}
		}
	}
}

// registerBackend writes the freshly installed build into the sidecar backend
// registry, which is what makes the app launch it.
//
// This is (*server.Server).registerManagedBackend without the config
// regeneration: nothing is running yet, and the app regenerates on first boot
// anyway because the sidecar is folded into autogen's inputs hash. The default
// rule is the server's own -- the first backend of a class wins and later ones
// do not steal it -- so a wizard run over an existing install cannot silently
// repoint a class the user had already configured.
func registerBackend(genPath string, mgr *backends.Manager, comp backends.Component, job backends.Job) error {
	if comp.Kind == "" {
		return nil // a helper (yt-dlp): installed, never registered as a backend
	}
	var inst backends.Installed
	for _, i := range mgr.Installed(comp.ID) {
		if i.Version == job.Version && i.Variant == job.Variant {
			inst = i
			break
		}
	}
	if inst.Exe == "" {
		return errors.New("no install manifest found")
	}

	list, err := autogen.LoadSidecarBackendList(genPath)
	if err != nil {
		return err
	}
	row := autogen.BackendEntry{
		ID:        "managed-" + comp.ID,
		Kind:      comp.Kind,
		Name:      comp.Name,
		Path:      inst.Exe,
		Managed:   true,
		Component: comp.ID,
		Version:   inst.Version,
		Variant:   inst.Variant,
	}
	replaced := false
	for i, e := range list {
		if e.Managed && strings.EqualFold(e.Component, comp.ID) {
			row.ID, row.Default = e.ID, e.Default
			list[i] = row
			replaced = true
			break
		}
	}
	if !replaced {
		classTaken := false
		for _, e := range list {
			if autogen.KindClass(e.Kind) == autogen.KindClass(comp.Kind) {
				classTaken = true
				break
			}
		}
		row.Default = !classTaken
		list = append(list, row)
	}
	return autogen.UpsertSidecarBackendList(genPath, list)
}

// Finish launches the install and signals the window to close.
func (w *Wizard) Finish(launch bool) error {
	st := w.Status()
	if launch && w.opts.Launch != nil && st.InstallDir != "" {
		if err := w.opts.Launch(st.InstallDir); err != nil {
			return err
		}
	}
	select {
	case <-w.finish: // already closed; Finish is idempotent
	default:
		close(w.finish)
	}
	return nil
}
