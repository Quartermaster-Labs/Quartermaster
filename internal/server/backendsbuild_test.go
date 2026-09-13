package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

func install(exe, version, variant string) backends.Installed {
	return backends.Installed{Exe: exe, Version: version, Variant: variant}
}

// The settings editor never sees Build rows, so a PUT rebuilds the list from
// the body alone. Dropping the derived rows there would unpin every model that
// pointed at a build, silently sending it back to its class default.
func TestServer_MergeBackendList_ReattachesBuildRows(t *testing.T) {
	stored := []autogen.BackendEntry{
		{ID: "managed-sd-server", Kind: "sd", Name: "stable-diffusion.cpp", Path: "C:/bin/sd/vulkan/sd-server.exe", Managed: true, Component: "sd-server", Version: "master-841", Variant: "vulkan", Default: true},
		{ID: "build-sd-server-master-841-rocm", Kind: "sd", Name: "stable-diffusion.cpp", Path: "C:/bin/sd/rocm/sd-server.exe", Managed: true, Build: true, Component: "sd-server", Version: "master-841", Variant: "rocm"},
		{ID: "mine", Kind: "llama", Name: "hand-built", Path: "C:/bin/llama.exe"},
	}
	// What the editor sends: the rows it renders, with edited fields.
	body := []backendEntryDTO{
		{ID: "managed-sd-server", Kind: "sd", Name: "stable-diffusion.cpp", Path: "C:/whatever", Default: true},
		{ID: "mine", Kind: "llama", Name: "hand-built", Path: "C:/bin/llama2.exe"},
	}

	got := mergeBackendList(body, stored)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(got), got)
	}
	// Managed path + provenance come from the store, the user's own fields from
	// the body.
	if got[0].Path != stored[0].Path || !got[0].Managed || got[0].Component != "sd-server" {
		t.Errorf("managed row lost its provenance: %+v", got[0])
	}
	if got[1].Path != "C:/bin/llama2.exe" || got[1].Managed {
		t.Errorf("manual row = %+v", got[1])
	}
	if got[2].ID != "build-sd-server-master-841-rocm" || !got[2].Build || got[2].Path != "C:/bin/sd/rocm/sd-server.exe" {
		t.Errorf("build row not reattached: %+v", got[2])
	}
}

// A body that does echo a build row (an older UI, a script) must not be able to
// rewrite the row's path: derived rows are server-owned like the managed row.
func TestServer_MergeBackendList_EchoedBuildRowKeepsStoredPath(t *testing.T) {
	stored := []autogen.BackendEntry{{
		ID: "build-sd-server-master-841-rocm", Kind: "sd", Path: "C:/real/sd-server.exe",
		Managed: true, Build: true, Component: "sd-server", Version: "master-841", Variant: "rocm",
	}}
	body := []backendEntryDTO{{ID: "build-sd-server-master-841-rocm", Kind: "sd", Path: "C:/fake/sd-server.exe"}}

	got := mergeBackendList(body, stored)
	if len(got) != 1 || got[0].Path != "C:/real/sd-server.exe" || !got[0].Build {
		t.Fatalf("got %+v", got)
	}
}

// The model editor offers every installed build, so one row per build has to
// exist. They are appended after the rows the user owns, and the component's
// activated row must stay where it was: the legacy exe slots and the implicit
// class default are read positionally off the first row of a kind.
func TestServer_SyncBuildRows_ListsEveryInstalledBuild(t *testing.T) {
	list := []autogen.BackendEntry{
		{ID: "managed-sd-server", Kind: "sd", Name: "stable-diffusion.cpp", Path: "C:/sd/vulkan.exe", Managed: true, Component: "sd-server", Version: "841", Variant: "vulkan", Default: true},
		{ID: "mine", Kind: "sd", Name: "hand-built", Path: "C:/sd/mine.exe"},
	}
	installs := []backends.Installed{
		install("C:/sd/vulkan.exe", "841", "vulkan"),
		install("C:/sd/rocm.exe", "841", "rocm"),
	}

	got, changed := syncBuildRows(list, "sd-server", "stable-diffusion.cpp", "sd", installs)
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (%v)", len(got), got)
	}
	if got[0].ID != "managed-sd-server" || got[1].ID != "mine" {
		t.Errorf("existing rows moved: %v", got)
	}
	want := []struct{ id, path string }{
		{"build-sd-server-841-vulkan", "C:/sd/vulkan.exe"},
		{"build-sd-server-841-rocm", "C:/sd/rocm.exe"},
	}
	for i, w := range want {
		row := got[2+i]
		if row.ID != w.id || row.Path != w.path {
			t.Errorf("row %d = %q/%q, want %q/%q", 2+i, row.ID, row.Path, w.id, w.path)
		}
		if !row.Build || !row.Managed || row.Component != "sd-server" || row.Kind != "sd" || row.Name != "stable-diffusion.cpp" {
			t.Errorf("row %d provenance = %+v", 2+i, row)
		}
		if row.Default {
			t.Errorf("row %d is marked default; only the activated row may be", 2+i)
		}
	}
}

// Called on every startup: an unchanged install set must not rewrite the
// sidecar, and a changed one must rewrite in place rather than shuffle the file.
func TestServer_SyncBuildRows_IsIdempotentAndDropsStaleBuilds(t *testing.T) {
	installs := []backends.Installed{
		install("C:/sd/vulkan.exe", "841", "vulkan"),
		install("C:/sd/rocm.exe", "841", "rocm"),
	}
	list := []autogen.BackendEntry{
		{ID: "managed-sd-server", Kind: "sd", Path: "C:/sd/vulkan.exe", Managed: true, Component: "sd-server", Default: true},
		{ID: "tail", Kind: "llama", Path: "C:/llama.exe"},
	}
	first, _ := syncBuildRows(list, "sd-server", "stable-diffusion.cpp", "sd", installs)

	again, changed := syncBuildRows(first, "sd-server", "stable-diffusion.cpp", "sd", installs)
	if changed {
		t.Error("changed = true on an unchanged install set")
	}
	if len(again) != len(first) {
		t.Fatalf("len = %d, want %d", len(again), len(first))
	}

	// One build removed: its row goes, the surviving block keeps its position,
	// and unrelated rows are not touched.
	pruned, changed := syncBuildRows(first, "sd-server", "stable-diffusion.cpp", "sd", installs[:1])
	if !changed {
		t.Fatal("changed = false after a build was removed")
	}
	if len(pruned) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(pruned), pruned)
	}
	if pruned[0].ID != "managed-sd-server" || pruned[1].ID != "tail" {
		t.Errorf("non-build rows moved: %v", pruned)
	}
	if pruned[2].ID != "build-sd-server-841-vulkan" {
		t.Errorf("build block = %v", pruned[2:])
	}
}

// One component's build rows are none of another component's business, and a
// hand-entered row that happens to name the component is not the manager's.
func TestServer_SyncBuildRows_LeavesOtherComponentsAlone(t *testing.T) {
	other := autogen.BackendEntry{ID: "build-llama-server-b10796-vulkan", Kind: "llama", Path: "C:/llama.exe", Managed: true, Build: true, Component: "llama-server", Version: "b10796", Variant: "vulkan"}
	manual := autogen.BackendEntry{ID: "custom", Kind: "sd", Path: "C:/x.exe", Component: "sd-server"}
	list := []autogen.BackendEntry{other, manual}

	got, changed := syncBuildRows(list, "sd-server", "stable-diffusion.cpp", "sd", []backends.Installed{install("C:/sd.exe", "841", "vulkan")})
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if len(got) != 3 || got[0].ID != other.ID || got[1].ID != manual.ID {
		t.Fatalf("got %v", got)
	}
	if !got[0].Build || !got[0].Managed {
		t.Errorf("other component's build row was rewritten: %+v", got[0])
	}
	if got[1].Build || got[1].Managed {
		t.Errorf("manual row was rewritten: %+v", got[1])
	}
}

// managedEntry answers "which row does the manager own", and that is the row it
// activates, updates and marks default. A derived row must never answer it.
func TestServer_ManagedEntry_SkipsBuildRows(t *testing.T) {
	list := []autogen.BackendEntry{
		{ID: "build-sd-server-841-rocm", Managed: true, Build: true, Component: "sd-server"},
		{ID: "managed-sd-server", Managed: true, Component: "sd-server"},
	}
	if i := managedEntry(list, "sd-server"); i != 1 {
		t.Fatalf("managedEntry = %d, want 1", i)
	}
	if i := managedEntry(list, "nope"); i != -1 {
		t.Fatalf("managedEntry(nope) = %d, want -1", i)
	}
}

// The container case, now with more than one build on disk: the activated
// build's row is written first and every installed build gets a row behind it.
func TestServer_AdoptInstalledBackends_AdoptsWithBuildRows(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	newest := fakeInstall(t, root, "llama-server", "b10796", "vulkan", exeName("llama-server"))
	older := fakeInstall(t, root, "llama-server", "b10000", "rocm", exeName("llama-server"))

	n, err := AdoptInstalledBackends(gen, backends.NewManager(root, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("adopted %d, want 1", n)
	}
	list, err := autogen.LoadSidecarBackendList(gen)
	if err != nil {
		t.Fatal(err)
	}
	i := managedEntry(list, "llama-server")
	if i < 0 {
		t.Fatal("no managed row written for llama-server")
	}
	// Two installs written in the same second: Installed() only guarantees
	// newest-first by manifest time, so which one is adopted is either build.
	if list[i].Path != newest && list[i].Path != older {
		t.Errorf("activated path = %q, want one of the two installs", list[i].Path)
	}
	byID := map[string]autogen.BackendEntry{}
	for _, e := range list {
		byID[e.ID] = e
	}
	for _, want := range []struct{ id, path string }{
		{"build-llama-server-b10796-vulkan", newest},
		{"build-llama-server-b10000-rocm", older},
	} {
		row, ok := byID[want.id]
		if !ok {
			t.Fatalf("no row %q in %v", want.id, list)
		}
		if row.Path != want.path || !row.Build || !row.Managed || row.Kind != "llama" {
			t.Errorf("row %q = %+v", want.id, row)
		}
	}
	// The activated row must precede the build rows: deriveBackendExes and
	// resolveBackend both take the first row of a kind.
	if list[0].ID != "managed-llama-server" {
		t.Errorf("first row = %q, want the managed row", list[0].ID)
	}
}

// Removing a build removes its row too: a model pinned to it must fall back to
// the class default rather than launch a path that no longer exists.
func TestServer_PruneBuildRows_DropsRemovedBuild(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	kept := fakeInstall(t, root, "llama-server", "b10796", "vulkan", exeName("llama-server"))
	doomed := filepath.Join(root, "bin", "llama-server", "b10000-rocm")
	fakeInstall(t, root, "llama-server", "b10000", "rocm", exeName("llama-server"))

	if _, err := AdoptInstalledBackends(gen, backends.NewManager(root, nil), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		autogen:  &AutogenAdmin{GeneratePath: gen},
		backends: backends.NewManager(root, nil),
		proxylog: logmon.NewWriter(io.Discard),
	}
	changed, err := s.pruneBuildRows("llama-server")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("prune = false, want true")
	}
	list, _ := autogen.LoadSidecarBackendList(gen)
	var builds []string
	for _, e := range list {
		if e.Build {
			builds = append(builds, e.Path)
		}
	}
	if len(builds) != 1 || builds[0] != kept {
		t.Errorf("build rows = %v, want just %q", builds, kept)
	}

	// Nothing left to prune: no write, so no regen behind it.
	again, err := s.pruneBuildRows("llama-server")
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Error("prune wrote the same list twice")
	}
}

// A helper (yt-dlp) has installs on disk but no registry row and no kind.
func TestServer_PruneBuildRows_HelperIsANoop(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	fakeInstall(t, root, "yt-dlp", "2026.08.19", "any", exeName("yt-dlp"))

	s := &Server{
		autogen:  &AutogenAdmin{GeneratePath: gen},
		backends: backends.NewManager(root, nil),
		proxylog: logmon.NewWriter(io.Discard),
	}
	if changed, err := s.pruneBuildRows("yt-dlp"); err != nil || changed {
		t.Fatalf("prune(yt-dlp) = %v, %v; want false, nil", changed, err)
	}
}

// A working row is the user's choice, but the build list beside it is still
// refreshed: pinning those builds is the whole point.
func TestServer_AdoptInstalledBackends_RefreshesBuildRowsOnly(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	oldExe := fakeInstall(t, root, "llama-server", "b10000", "vulkan", exeName("llama-server"))
	fakeInstall(t, root, "llama-server", "b10796", "vulkan", exeName("llama-server"))

	if err := autogen.UpsertSidecarBackendList(gen, []autogen.BackendEntry{{
		ID: "managed-llama-server", Kind: "llama", Name: "llama.cpp",
		Path: oldExe, Managed: true, Component: "llama-server",
		Version: "b10000", Variant: "vulkan", Default: true,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := AdoptInstalledBackends(gen, backends.NewManager(root, nil), nil); err != nil {
		t.Fatal(err)
	}
	list, _ := autogen.LoadSidecarBackendList(gen)
	if i := managedEntry(list, "llama-server"); i < 0 || list[i].Path != oldExe {
		t.Fatalf("working row was repointed: %v", list)
	}
	builds := 0
	for _, e := range list {
		if e.Build {
			builds++
		}
	}
	if builds != 2 {
		t.Errorf("build rows = %d, want 2 (%v)", builds, list)
	}
}
