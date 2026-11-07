package autogen

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// Companion downloads: the files a split diffusion model needs beside its DiT
// (VAE, text encoder, tokenizer), and where to get each one.
//
// The two halves of that answer have different owners. WHICH roles are missing
// is resolveComponents' call, run against the live encoder pool exactly as a
// regen would run it, so the browser offers precisely what the next config would
// otherwise warn about and never an encoder already on disk. WHERE a role comes
// from is data (companions.json): nothing machine-readable states it. A DiT
// names the encoder WIDTH it wants, not a repo, and sd-server's own model
// catalog lists slots and labels but no URLs.

//go:embed companions.json
var companionsJSON []byte

// CompanionSource is one downloadable file of a recipe.
type CompanionSource struct {
	Repo  string `json:"repo"`
	Path  string `json:"path"`
	Label string `json:"label"`
}

type companionFamily struct {
	ID         string                     `json:"id"`
	Arch       []string                   `json:"arch"`
	Name       []string                   `json:"name"`
	CondHidden int64                      `json:"condHidden"`
	Parts      map[string]CompanionSource `json:"parts"`
}

var (
	companionOnce sync.Once
	companionFams []companionFamily
	companionErr  error
)

func companionFamilies() ([]companionFamily, error) {
	companionOnce.Do(func() {
		var doc struct {
			Families []companionFamily `json:"families"`
		}
		companionErr = json.Unmarshal(companionsJSON, &doc)
		companionFams = doc.Families
	})
	return companionFams, companionErr
}

// matches reports whether every condition the family states holds. An absent
// condition is no constraint, but a family must state at least one.
func (f companionFamily) matches(arch, name string, condHidden int64) bool {
	if len(f.Arch) == 0 && len(f.Name) == 0 && f.CondHidden == 0 {
		return false
	}
	if len(f.Arch) > 0 {
		ok := false
		for _, a := range f.Arch {
			if a = strings.ToLower(a); arch == a || strings.HasPrefix(arch, a) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Name) > 0 {
		ok := false
		for _, n := range f.Name {
			if strings.Contains(name, strings.ToLower(n)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return f.CondHidden == 0 || f.CondHidden == condHidden
}

// CompanionFile is one file a companion plan would download, for one role.
type CompanionFile struct {
	Role string `json:"role"`
	CompanionSource
}

// CompanionPlan is the answer for one DiT.
type CompanionPlan struct {
	// Family is the matched recipe id, "" when none matched.
	Family string `json:"family"`
	// Missing are the roles the next regen would warn about (resolveComponents'
	// missing list): the roles nothing on disk satisfies.
	Missing []string `json:"missing"`
	// Files are the recipe's sources for the missing roles, in Missing order.
	Files []CompanionFile `json:"files"`
	// Unsourced are missing roles the recipe names no source for (or every
	// missing role, when no recipe matched). The browser says so rather than
	// implying the model is complete.
	Unsourced []string `json:"unsourced"`
	// Have are the roles the disk already satisfies, with the file the next
	// regen will wire to each. Without them a model needing nothing looks the
	// same as a model the planner does not understand (the browser shows no box
	// either way), and a wrong same-width encoder pick is invisible until the
	// first render comes out off-prompt.
	Have []CompanionHave `json:"have"`
}

// CompanionHave is one role already satisfied from disk, and by which file.
type CompanionHave struct {
	Role string `json:"role"`
	Path string `json:"path"`
}

// PlanImageCompanions answers "what else does this DiT need, and from where",
// given a header read before the file is downloaded. name is any string that
// carries the model's name (the repo path is best: "unsloth/MOSS-Image-SFT-GGUF/
// MOSS-Image-SFT-Q8_0.gguf"), since several families are told apart only by
// name, exactly as in resolveComponents.
//
// ok is false for anything that is not a split diffusion model: an LLM, or a
// full SD/SDXL checkpoint whose encoders are baked in. Those need nothing.
func PlanImageCompanions(s Settings, meta Metadata, name string) (plan CompanionPlan, ok bool) {
	vid := videoInfoFrom(meta)
	arch := effectiveImageArch(meta)
	if !vid.is() && !isImageArch(arch) {
		return CompanionPlan{}, false
	}
	// The same gate imageCmdLines applies: a full checkpoint wires nothing
	// external, so it can be missing nothing.
	if !vid.is() && isFullCheckpointArch(arch) {
		return CompanionPlan{}, false
	}
	comps, missing := resolveComponents(s.Encoders, nil, arch, name, encoderPoolFor(s.RootList()), meta.CondHidden, vid)
	plan.Missing = append([]string{}, missing...)
	plan.Have = comps.roles()
	plan.Files = []CompanionFile{}
	plan.Unsourced = []string{}
	if len(missing) == 0 {
		return plan, true
	}
	a := strings.ToLower(strings.TrimSpace(arch))
	n := strings.ToLower(name)
	var fam *companionFamily
	if fams, err := companionFamilies(); err == nil {
		for i := range fams {
			if fams[i].matches(a, n, meta.CondHidden) {
				fam = &fams[i]
				break
			}
		}
	}
	if fam == nil {
		plan.Unsourced = append(plan.Unsourced, missing...)
		return plan, true
	}
	plan.Family = fam.ID
	for _, role := range missing {
		src, have := fam.Parts[role]
		if !have || src.Repo == "" || src.Path == "" {
			plan.Unsourced = append(plan.Unsourced, role)
			continue
		}
		plan.Files = append(plan.Files, CompanionFile{Role: role, CompanionSource: src})
	}
	return plan, true
}
