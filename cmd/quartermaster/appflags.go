package main

// Bridging the stored process-level settings (autogen.AppSettings — ports,
// remote access, update polling, HF token) onto the flag set.
//
// Everything downstream keeps reading the flags it always read; this just fills
// them in from the control files when argv did not. That is deliberate: the
// alternative — teaching every consumer to consult a settings struct as well as
// its flag — is where two sources of truth start disagreeing about which port
// is bound.

import (
	"flag"
	"os"
	"strconv"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// applyAppSettings writes the stored process-level settings into fs, skipping
// any flag the user typed.
//
// argvGiven must be the set captured immediately after flag.Parse, BEFORE the
// bundle defaults ran — flag.Visit cannot tell an argv flag from one another
// stage set programmatically, and the whole precedence order depends on that
// distinction. Run it after applyBundlePaths (which resolves -generate) and
// before applyBundleNetDefaults (which is the last-resort fallback).
func applyAppSettings(fs *flag.FlagSet, argvGiven map[string]bool, app autogen.AppSettings) {
	set := func(name, value string) {
		if !argvGiven[name] {
			_ = fs.Set(name, value)
		}
	}

	if app.Listen != "" {
		set("listen", app.Listen)
	}
	if app.PlaygroundListen != "" {
		set("playground-port", app.PlaygroundListen)
	}
	if app.AdminAllow != "" {
		set("admin-allow", app.AdminAllow)
	}
	if app.AdminOpen != nil {
		set("admin-open", strconv.FormatBool(*app.AdminOpen))
	}
	if app.WatchModels != nil {
		set("watch-models", strconv.FormatBool(*app.WatchModels))
	}
	if app.WatchModelsIntervalSec > 0 {
		set("watch-models-interval", strconv.Itoa(app.WatchModelsIntervalSec)+"s")
	}
	// Stored as "check for updates", exposed as a negative flag: only an explicit
	// false is worth writing, and -no-update-check=false is already the default.
	if app.UpdateCheck != nil && !*app.UpdateCheck {
		set("no-update-check", "true")
	}
}

// applyHfToken exports a stored Hugging Face token to the environment, where the
// hub client already looks for one.
//
// An environment variable that is already set always wins: a shell (or a service
// manager) that exports HF_TOKEN is stating an intent for this process, and
// silently substituting a token saved months ago in the dashboard would send the
// wrong credential to a gated repo with no indication why.
func applyHfToken(app autogen.AppSettings) {
	if strings.TrimSpace(app.HfToken) == "" {
		return
	}
	for _, k := range []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "HUGGINGFACE_TOKEN"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return
		}
	}
	_ = os.Setenv("HF_TOKEN", strings.TrimSpace(app.HfToken))
}
