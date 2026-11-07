package autogen

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every recipe must parse, state at least one match condition, and name only
// roles resolveComponents can actually report missing: a typo'd role key is a
// part the browser would never offer.
func TestAutogen_CompanionCatalog(t *testing.T) {
	fams, err := companionFamilies()
	if err != nil {
		t.Fatalf("companions.json: %v", err)
	}
	if len(fams) == 0 {
		t.Fatal("companions.json: no families")
	}
	known := map[string]bool{"vae": true, "llm": true, "tokenizer": true, "clip_l": true, "clip_g": true, "t5xxl": true}
	seen := map[string]bool{}
	for _, f := range fams {
		if f.ID == "" || seen[f.ID] {
			t.Errorf("family id %q blank or duplicated", f.ID)
		}
		seen[f.ID] = true
		if len(f.Arch) == 0 && len(f.Name) == 0 && f.CondHidden == 0 {
			t.Errorf("%s: no match condition, it would never match", f.ID)
		}
		if len(f.Parts) == 0 {
			t.Errorf("%s: no parts", f.ID)
		}
		for role, src := range f.Parts {
			if !known[role] {
				t.Errorf("%s: unknown role %q", f.ID, role)
			}
			if src.Repo == "" || src.Path == "" || src.Label == "" || strings.Count(src.Repo, "/") != 1 {
				t.Errorf("%s/%s: incomplete source %+v", f.ID, role, src)
			}
		}
	}
}

func TestAutogen_PlanImageCompanions(t *testing.T) {
	s := Settings{ModelsRoot: t.TempDir()}

	// Nothing on disk: MOSS needs all three, in resolveComponents' order.
	plan, ok := PlanImageCompanions(s, Metadata{Architecture: "moss_image", CondHidden: 4096}, "unsloth/MOSS-Image-SFT-GGUF/MOSS-Image-SFT-Q8_0.gguf")
	if !ok || plan.Family != "moss-image" || len(plan.Unsourced) != 0 {
		t.Fatalf("moss plan = %+v ok=%v", plan, ok)
	}
	var roles []string
	for _, f := range plan.Files {
		roles = append(roles, f.Role)
	}
	if strings.Join(roles, ",") != "vae,llm,tokenizer" || plan.Files[1].Repo != "unsloth/Qwen3.5-9B-GGUF" {
		t.Errorf("moss files = %+v", plan.Files)
	}

	// FLUX.3 reports arch flux; the name and width pick it over FLUX.1 (which
	// would ask for CLIP-L + T5).
	plan, _ = PlanImageCompanions(s, Metadata{Architecture: "flux", CondHidden: 20480}, "unsloth/FLUX.3-klein-GGUF/flux-3-klein-Q8_0.gguf")
	if plan.Family != "flux3-klein" {
		t.Errorf("flux3 family = %q (%+v)", plan.Family, plan)
	}
	plan, _ = PlanImageCompanions(s, Metadata{Architecture: "flux"}, "city96/FLUX.1-dev-gguf/flux1-dev-Q8_0.gguf")
	if plan.Family != "flux1" || len(plan.Files) != 3 {
		t.Errorf("flux1 = %+v", plan)
	}

	// A family without a recipe is reported, never silently "complete".
	plan, ok = PlanImageCompanions(s, Metadata{Architecture: "wan", CondHidden: 3072}, "unsloth/ERNIE-Image-Turbo-GGUF/ernie-image-turbo-Q8_0.gguf")
	if !ok || plan.Family != "" || len(plan.Unsourced) == 0 || len(plan.Files) != 0 {
		t.Errorf("ernie = %+v ok=%v", plan, ok)
	}

	// Not a split diffusion model: nothing to offer.
	if _, ok := PlanImageCompanions(s, Metadata{Architecture: "llama"}, "x/y/llama-8b.gguf"); ok {
		t.Error("an LLM got a companion plan")
	}
	if _, ok := PlanImageCompanions(s, Metadata{Architecture: "sdxl"}, "x/y/sdxl.gguf"); ok {
		t.Error("a full checkpoint got a companion plan")
	}
}

// A companion already on disk is never offered again: the plan is
// resolveComponents' missing list, run against the real pool.
func TestAutogen_PlanImageCompanionsSkipsLocal(t *testing.T) {
	dir := t.TempDir()
	writeSafetensors(t, filepath.Join(dir, "flux2", "ae.safetensors"),
		map[string][]int64{"decoder.conv_in.weight": {512, 32, 3, 3}})
	s := Settings{ModelsRoot: dir}
	plan, ok := PlanImageCompanions(s, Metadata{Architecture: "moss_image", CondHidden: 4096}, "unsloth/MOSS-Image-SFT-GGUF/MOSS-Image-SFT-Q8_0.gguf")
	if !ok || strings.Join(plan.Missing, ",") != "llm,tokenizer" {
		t.Errorf("missing = %v, want the vae satisfied from disk", plan.Missing)
	}
	// ...and the satisfied role is reported with the file that satisfies it.
	if len(plan.Have) != 1 || plan.Have[0].Role != "vae" || filepath.Base(plan.Have[0].Path) != "ae.safetensors" {
		t.Errorf("have = %+v, want the flux2 vae", plan.Have)
	}
}
