package backends

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// manifestName marks an install directory. Its presence (not a central index)
// is what makes a version "installed", so a half-extracted or hand-deleted
// folder can never show up as usable.
const manifestName = ".qm-install.json"

// downloadTimeout bounds one asset fetch. llama.cpp CUDA zips run to ~500 MB.
const downloadTimeout = 30 * time.Minute

// Installed is one version of a component on disk.
type Installed struct {
	Component   string    `json:"component"`
	Version     string    `json:"version"`
	Variant     string    `json:"variant"`
	Exe         string    `json:"exe"` // absolute path to the executable
	Dir         string    `json:"dir"`
	Asset       string    `json:"asset"`
	InstalledAt time.Time `json:"installedAt"`
	SizeBytes   int64     `json:"sizeBytes"`
}

// manifest is the on-disk form (Exe relative to Dir so the bundle stays movable).
type manifest struct {
	Component   string    `json:"component"`
	Version     string    `json:"version"`
	Variant     string    `json:"variant"`
	Exe         string    `json:"exe"`
	Asset       string    `json:"asset"`
	InstalledAt time.Time `json:"installedAt"`
	SizeBytes   int64     `json:"sizeBytes"`
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// sanitizeSeg makes a release tag safe as a single path segment.
func sanitizeSeg(s string) string {
	s = unsafePath.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "" {
		s = "unknown"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// ComponentDir is where every installed version of a component lives.
func (m *Manager) ComponentDir(comp string) string {
	return filepath.Join(m.root, "bin", sanitizeSeg(comp))
}

// InstallDir is the versioned, side-by-side directory for one build. Versions
// are never overwritten, so an update can't brick a working setup and an older
// build stays one click away.
func (m *Manager) InstallDir(comp, version, variant string) string {
	return filepath.Join(m.ComponentDir(comp), sanitizeSeg(version)+"-"+sanitizeSeg(variant))
}

// Installed lists every installed version of a component, newest install first.
func (m *Manager) Installed(comp string) []Installed {
	ents, err := os.ReadDir(m.ComponentDir(comp))
	if err != nil {
		return nil
	}
	var out []Installed
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.ComponentDir(comp), e.Name())
		inst, err := readManifest(dir)
		if err != nil {
			continue // not ours, or a failed extract
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstalledAt.After(out[j].InstalledAt) })
	return out
}

// AllInstalled lists installed builds across every catalog component.
func (m *Manager) AllInstalled() []Installed {
	var out []Installed
	for _, c := range m.Catalog() {
		out = append(out, m.Installed(c.ID)...)
	}
	return out
}

func readManifest(dir string) (Installed, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return Installed{}, err
	}
	var mf manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return Installed{}, err
	}
	exe := filepath.Join(dir, filepath.FromSlash(mf.Exe))
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		return Installed{}, fmt.Errorf("executable missing from %s", dir)
	}
	// Measure the directory rather than trusting the manifest: what the user
	// wants to see is the disk this build occupies, and manifests written before
	// this recorded only the executable (llama-server.exe is a stub in front of
	// the ggml DLLs, so it read as 0 MB).
	size := mf.SizeBytes
	if n, err := dirSize(dir); err == nil {
		size = n
	}
	return Installed{
		Component: mf.Component, Version: mf.Version, Variant: mf.Variant,
		Exe: exe, Dir: dir, Asset: mf.Asset,
		InstalledAt: mf.InstalledAt, SizeBytes: size,
	}, nil
}

// dirSize totals the regular files under dir. Unreadable entries are skipped
// rather than failing the scan: a wrong-by-one-file size is better than a build
// disappearing from the installed list.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip what we can't stat, keep walking
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func writeManifest(dir string, mf manifest) error {
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), data, 0o644)
}

// Uninstall removes one installed build. Refuses when it is the active exe of a
// registry entry — the caller points the entry elsewhere first.
func (m *Manager) Uninstall(comp, version, variant string) error {
	dir := m.InstallDir(comp, version, variant)
	if _, err := readManifest(dir); err != nil {
		return fmt.Errorf("not installed: %s %s (%s)", comp, version, variant)
	}
	return os.RemoveAll(dir)
}

// download streams an asset to dst, reporting progress. Total is the size the
// API reported (0 when unknown) so the UI can show a bar before the first byte.
func (m *Manager) download(ctx context.Context, src, dst string, onProgress func(done, total int64)) error {
	if err := validAssetURL(src); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", ghUserAgent)
	resp, err := m.dl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, on: onProgress}
	if _, err := io.Copy(f, pr); err != nil {
		return err
	}
	return f.Sync()
}

// progressReader reports bytes read at most every progressEvery bytes so a
// 500 MB download doesn't generate a million callbacks.
type progressReader struct {
	r       io.Reader
	total   int64
	done    int64
	lastRep int64
	on      func(done, total int64)
}

const progressEvery = 512 << 10

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.on != nil && (p.done-p.lastRep >= progressEvery || err != nil) {
		p.lastRep = p.done
		p.on(p.done, p.total)
	}
	return n, err
}

// extract unpacks an archive (zip or tar.gz) into dir. Entry paths are checked
// against dir so a crafted archive can't write outside it (zip slip).
func extract(archivePath, dir string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, dir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archivePath, dir)
	}
	return fmt.Errorf("unsupported archive type: %s", filepath.Base(archivePath))
}

// safeJoin resolves name inside dir, rejecting absolute paths and ../ escapes.
func safeJoin(dir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive entry: %s", name)
	}
	out := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, out)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive entry: %s", name)
	}
	return out, nil
}

func extractZip(src, dir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	var links []linkEntry
	for _, f := range zr.File {
		dst, err := safeJoin(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		// A zip symlink is a tiny entry whose CONTENT is the target path; written
		// out verbatim it becomes a text file with a library's name, which the
		// loader rejects as a bad ELF instead of following. Defer it like a tar
		// link so the SONAME chain survives (see applyLinks).
		if f.Mode()&os.ModeSymlink != 0 {
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			links = append(links, linkEntry{dst: dst, target: string(target)})
			continue
		}
		err = writeFile(dst, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return applyLinks(dir, links)
}

func extractTarGz(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var links []linkEntry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return applyLinks(dir, links)
		}
		if err != nil {
			return err
		}
		dst, err := safeJoin(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := writeFile(dst, tr, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			links = append(links, linkEntry{dst: dst, target: h.Linkname})
		case tar.TypeLink:
			links = append(links, linkEntry{dst: dst, target: h.Linkname, hard: true})
		default:
			// Devices, fifos and the rest are skipped: no backend archive needs
			// them, and they are the other half of a path-escape trick.
		}
	}
}

func writeFile(dst string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, r)
	return err
}

// linkEntry is one archived symlink (or hard link), held back until every
// regular file has been written: an archive is free to list a link before the
// file it points at.
type linkEntry struct {
	dst    string // absolute path of the link itself
	target string // link text, exactly as the archive stores it
	hard   bool
}

// applyLinks materialises the deferred links.
//
// Why this matters: llama.cpp's Linux bundles ship each library as a SONAME
// chain of symlinks (libllama-common.so -> .so.0 -> .so.0.3.0), and the ELF
// headers reference the MIDDLE name. Dropping the links left only the versioned
// files on disk, so every spawn died with "libllama-common.so.0: cannot open
// shared object file" no matter what was on LD_LIBRARY_PATH.
//
// Rounds, not one pass: on Windows a symlink needs a privilege most accounts
// lack, so the fallback copies the target - which has to exist first, and a
// chain's links can appear in any order. A round that resolves nothing means
// the rest are dangling (target absent from the archive); that is left alone
// rather than failing the install, since nothing may ever load them.
func applyLinks(dir string, links []linkEntry) error {
	for len(links) > 0 {
		var stuck []linkEntry
		for _, l := range links {
			done, err := applyLink(dir, l)
			if err != nil {
				return err
			}
			if !done {
				stuck = append(stuck, l)
			}
		}
		if len(stuck) == len(links) {
			return nil
		}
		links = stuck
	}
	return nil
}

// applyLink writes one link, or reports false when it needs another round.
func applyLink(dir string, l linkEntry) (bool, error) {
	// A link is the other half of a path-escape trick, so resolve where it
	// actually points and refuse anything outside the install directory. A
	// symlink's text is relative to the link, a tar hard link's to the archive
	// root.
	base := dir
	if !l.hard {
		base = filepath.Dir(l.dst)
	}
	// Archives are unix-shaped, so "/etc/passwd" is an absolute target even on
	// Windows, where filepath.IsAbs would call it relative and join it INTO the
	// install directory.
	target := filepath.FromSlash(l.target)
	if target == "" || filepath.IsAbs(target) || strings.HasPrefix(l.target, "/") || filepath.VolumeName(target) != "" {
		return false, fmt.Errorf("unsafe archive link: %s -> %s", filepath.Base(l.dst), l.target)
	}
	abs := filepath.Join(base, target)
	if rel, err := filepath.Rel(dir, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, fmt.Errorf("unsafe archive link: %s -> %s", filepath.Base(l.dst), l.target)
	}
	_ = os.Remove(l.dst) // a re-extract over a previous install
	if !l.hard {
		// Dangling is fine here: a real symlink resolves whenever the target
		// lands, so this needs no ordering.
		if err := os.Symlink(target, l.dst); err == nil {
			return true, nil
		}
	}
	st, err := os.Stat(abs)
	if err != nil {
		return false, nil // target not written yet - retry next round
	}
	if !st.Mode().IsRegular() {
		return false, nil
	}
	if l.hard {
		if err := os.Link(abs, l.dst); err == nil {
			return true, nil
		}
	}
	return true, copyFile(abs, l.dst) // 0o755: every link in a backend bundle is a library or an exe
}

// findExe locates the component's executable anywhere under dir (release zips
// nest it in a subfolder, and the nesting differs per project and per build).
func findExe(dir, name string) (string, error) {
	var hit string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || hit != "" || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), name) {
			hit = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if hit == "" {
		return "", fmt.Errorf("%q not found in the downloaded archive", name)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(hit, 0o755) // zip entries from CI often lack the exec bit
	}
	return hit, nil
}
