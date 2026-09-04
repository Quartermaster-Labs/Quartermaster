package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
)

// fakeInstall lays out one install exactly as internal/backends/install.go
// expects it, which is also exactly what docker/app/fetch-backends.sh writes:
// a versioned directory holding the executable and a .qm-install.json.
func fakeInstall(t *testing.T, root, comp, version, variant, exeName string) string {
	t.Helper()
	dir := filepath.Join(root, "bin", comp, version+"-"+variant)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, exeName)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf := map[string]any{
		"component": comp, "version": version, "variant": variant,
		"exe": exeName, "asset": comp + ".zip",
		"installedAt": time.Now().UTC().Format(time.RFC3339), "sizeBytes": 0,
	}
	b, _ := json.Marshal(mf)
	if err := os.WriteFile(filepath.Join(dir, ".qm-install.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return exe
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func newGenerateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
	if err := os.WriteFile(path, []byte("settings:\n  modelsRoot: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The container case: binaries baked into the image, a registry that has never
// heard of them. Without adoption they are "installed" and never launched.
func TestServer_AdoptInstalledBackends_RegistersUnknownInstall(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	exe := fakeInstall(t, root, "llama-server", "b10796", "vulkan", exeName("llama-server"))

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
	if list[i].Path != exe {
		t.Errorf("Path = %q, want %q", list[i].Path, exe)
	}
	if list[i].Version != "b10796" || list[i].Variant != "vulkan" {
		t.Errorf("version/variant = %q/%q", list[i].Version, list[i].Variant)
	}
	// First backend of its class, so it takes the auto-pick.
	if !list[i].Default {
		t.Error("first backend of a class should become the default")
	}
}

// A row that still resolves is the user's choice, and adoption must not
// silently repoint it at a newer build they chose not to activate.
func TestServer_AdoptInstalledBackends_LeavesWorkingRowAlone(t *testing.T) {
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

	n, err := AdoptInstalledBackends(gen, backends.NewManager(root, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("adopted %d, want 0", n)
	}
	list, _ := autogen.LoadSidecarBackendList(gen)
	if i := managedEntry(list, "llama-server"); i < 0 || list[i].Path != oldExe {
		t.Errorf("working row was repointed; Path = %q, want %q", list[i].Path, oldExe)
	}
}

// A row whose binary is gone launches nothing, so re-pointing it at a build
// that exists is strictly better than leaving it broken.
func TestServer_AdoptInstalledBackends_RepairsDanglingRow(t *testing.T) {
	gen := newGenerateFile(t)
	root := t.TempDir()
	newExe := fakeInstall(t, root, "llama-server", "b10796", "vulkan", exeName("llama-server"))

	if err := autogen.UpsertSidecarBackendList(gen, []autogen.BackendEntry{{
		ID: "managed-llama-server", Kind: "llama", Name: "llama.cpp",
		Path: filepath.Join(root, "gone", exeName("llama-server")), Managed: true,
		Component: "llama-server", Version: "b10000", Variant: "vulkan",
	}}); err != nil {
		t.Fatal(err)
	}

	n, err := AdoptInstalledBackends(gen, backends.NewManager(root, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("adopted %d, want 1", n)
	}
	list, _ := autogen.LoadSidecarBackendList(gen)
	i := managedEntry(list, "llama-server")
	if list[i].Path != newExe {
		t.Errorf("Path = %q, want %q", list[i].Path, newExe)
	}
	// The id is referenced by per-model overrides, so it must survive a repair.
	if list[i].ID != "managed-llama-server" {
		t.Errorf("row id changed to %q", list[i].ID)
	}
}

// Nothing installed, nothing to do, and in particular no sidecar write: this
// runs on every single start.
func TestServer_AdoptInstalledBackends_NoInstallsIsANoop(t *testing.T) {
	gen := newGenerateFile(t)
	before, _ := os.ReadFile(autogen.SidecarPath(gen))

	n, err := AdoptInstalledBackends(gen, backends.NewManager(t.TempDir(), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("adopted %d, want 0", n)
	}
	after, _ := os.ReadFile(autogen.SidecarPath(gen))
	if string(before) != string(after) {
		t.Error("sidecar was rewritten with nothing to adopt")
	}
}
