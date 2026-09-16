package server

// The model browser's audio.cpp tab.
//
// audio.cpp is the one backend here whose weights cannot be found by browsing
// the hub. Its 70-odd families are published as a handful of shared GGUF repos
// (audio-cpp/audio.cpp-gguf and friends), so a hub search answers with a few
// hundred loose file names and nothing saying which family a name belongs to,
// which of them are alternatives to each other, or that an f5_tts gguf is
// useless without the vocab.txt beside it. Upstream's own answer to that is
// model_specs/*.json, which every release ships next to the binary, and this
// file serves it as a catalog: family -> packages -> the exact repo file set.
//
// It is a VIEW over the existing downloader, not a second one. A package's
// files go to /api/hub/download as an ordinary StartRequest, so resume, the
// journal, the manifest and the free-disk check are all the same code. What this
// endpoint adds is which files to ask for.

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/audiocpp"
	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

type audioCppPackageDTO struct {
	audiocpp.Package
	// Local marks a package whose every file is already on disk: the row renders
	// as installed rather than as a download button. Judged per FILE, because a
	// package is a set - a gguf present without its vocab.txt is not installed.
	Local bool `json:"local"`
	// SizeBytes is the total download for the set, 0 when it cannot be known
	// (no hub reachable and nothing on disk). "How big is it" is the first
	// question asked of any model, and the answer has to be the whole SET: the
	// gguf alone understates a package that ships a sidecar.
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

type audioCppFamilyDTO struct {
	audiocpp.Family
	Packages []audioCppPackageDTO `json:"packages"`
	// Task is what a model of this family would be emitted as ("tts"/"asr"), and
	// Supported says whether we serve it at all. Unsupported families are still
	// listed, with a reason: "audio.cpp can run this, quartermaster cannot yet"
	// is information, and hiding it makes the catalog look arbitrarily short.
	Task      string `json:"task,omitempty"`
	Supported bool   `json:"supported"`
	// Reason is empty for a supported family. The two failures are different and
	// the UI says so: a known family with no class here (music, stem separation,
	// codecs) is a roadmap item, while an unknown one means the installed backend
	// is newer than the table in internal/autogen/audiocpp.go.
	Reason string `json:"reason,omitempty"`
}

// handleAPIHubAudioCpp serves the audio.cpp catalog of the INSTALLED backend.
// An install that is missing, or too old to ship model_specs/, is an empty
// catalog and a 200: the browser hides the tab rather than showing an error for
// a backend the user may simply not use.
func (s *Server) handleAPIHubAudioCpp(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	if s.autogen == nil {
		// The registry that names the installed backend is autogen's, so without
		// -generate there is no install to read a catalog out of.
		shared.SendResponse(w, r, http.StatusNotImplemented, "the audio.cpp catalog requires the server to run with -generate")
		return
	}
	exe, backendID, backendName := s.audioCppInstall()
	dir := audiocpp.SpecsDir(exe)
	fams, err := audiocpp.Load(dir)
	if err != nil {
		writeJSON(w, map[string]any{"installed": exe != "", "specsDir": dir, "families": []any{}, "error": err.Error()})
		return
	}

	// One walk and one hub call per REPO, not per package: the catalog's 200-odd
	// packages share a handful of repos, and each would otherwise be a directory
	// tree walk and a round trip. Both are memoized for the request; the hub
	// adapter caches its detail response beyond it.
	local := map[string]map[string]hub.LocalFile{}
	localFor := func(repo string) map[string]hub.LocalFile {
		if m, ok := local[repo]; ok {
			return m
		}
		m := s.hub.LocalFiles(repo)
		local[repo] = m
		return m
	}
	src, haveSrc := s.hub.Source("")
	remote := map[string]map[string]int64{}
	remoteFor := func(repo string) map[string]int64 {
		if m, ok := remote[repo]; ok {
			return m
		}
		m := map[string]int64{}
		if haveSrc {
			// A failure here is not an error for the catalog: offline, rate
			// limited or a repo that has moved all mean "no size to show", and
			// the rest of the row is still true and still downloadable.
			if det, err := src.Detail(r.Context(), repo); err == nil {
				for _, f := range det.Files {
					m[f.Path] = f.SizeBytes
				}
			}
		}
		remote[repo] = m
		return m
	}

	out := make([]audioCppFamilyDTO, 0, len(fams))
	for _, f := range fams {
		task, known := autogen.AudioCppFamilySupport(f.Family)
		dto := audioCppFamilyDTO{Family: f, Task: task, Supported: task != ""}
		switch {
		case task != "":
		case known:
			dto.Reason = "audio.cpp serves this family, but quartermaster has no " + familyJob(f) + " class yet"
		default:
			dto.Reason = "this backend build is newer than quartermaster's family table"
		}
		dto.Packages = make([]audioCppPackageDTO, 0, len(f.Packages))
		for _, p := range f.Packages {
			have := localFor(p.Repo)
			sizes := remoteFor(p.Repo)
			all := len(p.Files) > 0
			total, sized := int64(0), len(p.Files) > 0
			for _, rel := range p.Files {
				// LocalFiles keys are repo-relative and slash-separated, which is
				// what the spec states too; normalize anyway so a spec written
				// with backslashes cannot read as "not downloaded" forever.
				key := filepath.ToSlash(rel)
				lf, ok := have[key]
				if !ok || lf.Size <= 0 {
					all = false
				}
				// The hub is the authority on what a download costs; the copy on
				// disk is the fallback, which is what keeps sizes on screen for
				// an already-downloaded package with no network.
				switch n := sizes[key]; {
				case n > 0:
					total += n
				case lf.Size > 0:
					total += lf.Size
				default:
					sized = false
				}
			}
			// Partial is not a size: a set summed from the two files of three we
			// could price reads as a small download for a large one.
			if !sized {
				total = 0
			}
			dto.Packages = append(dto.Packages, audioCppPackageDTO{Package: p, Local: all, SizeBytes: total})
		}
		out = append(out, dto)
	}
	writeJSON(w, map[string]any{
		"installed":   exe != "",
		"backendId":   backendID,
		"backendName": backendName,
		"specsDir":    dir,
		"modelsRoot":  s.hubModelsRoot(),
		"families":    out,
	})
}

// familyJob names the job an unserved family does, for the catalog's reason
// line. The spec's own category is the right source: it is upstream's word for
// what the model is, and it distinguishes music from stem separation from voice
// conversion, which "unsupported" would not.
func familyJob(f audiocpp.Family) string {
	switch strings.ToLower(strings.TrimSpace(f.Category)) {
	case "audio_generation":
		return "music"
	case "audio_tools":
		return "audio tools"
	case "voice_conversion":
		return "voice conversion"
	case "speech_analysis":
		return "speech analysis"
	case "":
		return "matching"
	default:
		return strings.ReplaceAll(strings.ToLower(f.Category), "_", " ")
	}
}

// audioCppInstall finds the registered audio.cpp backend, preferring the ★ row
// over a derived per-build one: the catalog belongs to the build the user
// actually runs, and the specs of two installed builds can differ (that is the
// whole reason this is read from the install rather than embedded).
func (s *Server) audioCppInstall() (exe, id, name string) {
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		return "", "", ""
	}
	for _, e := range gf.Settings.Backends {
		if !autogen.IsAudioCppKind(e.Kind) || strings.TrimSpace(e.Path) == "" {
			continue
		}
		if e.Build && exe != "" {
			continue
		}
		if exe == "" || e.Default || !e.Build {
			exe, id, name = strings.TrimSpace(e.Path), e.ID, e.Name
		}
		if e.Default {
			break
		}
	}
	return exe, id, name
}
