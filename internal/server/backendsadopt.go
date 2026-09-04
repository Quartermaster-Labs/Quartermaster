package server

// Adoption: making an install the registry does not know about usable.
//
// registerManagedBackend only runs as a callback on an install this process
// performed. Everything else that can put a backend on disk -- a container
// image that bakes one in, a restored backup, a directory copied between
// machines -- leaves a build that the manager lists as installed and that the
// config still does not launch, with no way to fix it from the UI short of
// reinstalling over the top.
//
// So the registry is reconciled with the disk once, at startup, before the
// first config generation.

import (
	"os"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
)

// AdoptInstalledBackends points the sidecar registry at any installed build
// whose component has no usable row yet, and reports how many it adopted.
//
// It is deliberately called before autogen.EnsureConfig rather than after the
// server is up: the sidecar is folded into autogen's inputs hash, so writing it
// first means the very first generated config already launches the adopted
// build. Adopting afterwards would need a second regeneration.
//
// This never overrides a working row. A component whose managed row still
// points at an executable that exists is left exactly as it is, so a user who
// activated an older build keeps it, and a hand-entered path is never touched
// (those rows are not Managed). The one case it does take over is a row whose
// Path has gone -- a bundle moved or a version deleted from underneath the
// config -- where re-pointing at a build that is actually present beats
// launching a file that is not there.
func AdoptInstalledBackends(genPath string, mgr *backends.Manager, logf func(string)) (int, error) {
	if genPath == "" || mgr == nil {
		return 0, nil
	}
	list, err := autogen.LoadSidecarBackendList(genPath)
	if err != nil {
		return 0, err
	}

	adopted := 0
	for _, comp := range mgr.Catalog() {
		// No kind means a helper (yt-dlp): installed, never a backend row.
		if comp.Kind == "" {
			continue
		}
		i := managedEntry(list, comp.ID)
		if i >= 0 && list[i].Path != "" {
			if st, err := os.Stat(list[i].Path); err == nil && !st.IsDir() {
				continue // the row still works; leave the user's choice alone
			}
		}
		// Installed() is newest-first, which is the right pick for a component
		// nothing has expressed a preference about.
		installs := mgr.Installed(comp.ID)
		if len(installs) == 0 {
			continue
		}
		inst := installs[0]
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
		if i >= 0 {
			// Keep the id: per-model overrides reference it by name.
			row.ID, row.Default = list[i].ID, list[i].Default
			list[i] = row
		} else {
			// Same rule as an interactive install: the first backend of a class
			// becomes the auto-pick, and a populated class is never restolen.
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
		adopted++
		if logf != nil {
			logf("adopted installed backend " + comp.ID + " " + inst.Version + " (" + inst.Variant + ")")
		}
	}

	if adopted == 0 {
		return 0, nil
	}
	if err := autogen.UpsertSidecarBackendList(genPath, list); err != nil {
		return 0, err
	}
	return adopted, nil
}
