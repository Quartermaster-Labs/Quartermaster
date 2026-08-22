package autogen

// Process-level settings: the ones that used to be reachable only as command
// line flags, so a packaged install (double-clicked, no start.cmd) had no way to
// change its own ports or its remote-access policy.
//
// These are NOT part of Settings and never reach the generated config. They are
// consumed by main() BEFORE the config exists — the listen address decides which
// socket gets bound, which is settled long before any model is sized — so they
// live in their own block, with their own loader that reads the control files
// directly rather than going through LoadGenerateFile.
//
// Precedence, highest first:
//
//	argv  >  sidecar app block (dashboard)  >  settings.app (generate file)  >  built-in default
//
// argv wins because a flag is how you rescue an install whose stored listen
// address no longer works: `quartermaster.exe -listen 127.0.0.1:1250` must come
// up regardless of what the dashboard last saved.

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AppSettings is the process-level block. Every field is optional: a zero value
// means "not configured here", and the caller falls through to its own default.
// The three tri-state flags are pointers because false is a real choice for each
// — "don't check for updates" must be distinguishable from "unset".
type AppSettings struct {
	// Listen is the main API + dashboard address ("host:port"). Changing it
	// requires a restart: the socket is bound once at startup.
	Listen string `yaml:"listen,omitempty"`
	// PlaygroundListen is the extra address serving the standalone playground
	// app. Empty means no playground listener. Restart to apply.
	PlaygroundListen string `yaml:"playgroundListen,omitempty"`
	// AdminAllow is a comma-separated list of extra IPs/CIDRs allowed to reach
	// the dashboard/admin endpoints when listening beyond loopback — e.g.
	// "100.64.0.0/10" for a tailnet. Restart to apply.
	AdminAllow string `yaml:"adminAllow,omitempty"`
	// AdminOpen serves the unauthenticated dashboard to EVERY remote host. It is
	// the legacy behaviour and a genuine exposure: the admin surface has no
	// password, so this hands config editing and the model hub to anyone who can
	// reach the port. Restart to apply.
	AdminOpen *bool `yaml:"adminOpen,omitempty"`
	// WatchModels re-scans the models folder periodically and hot-reloads when it
	// changes. Defaults to on.
	WatchModels *bool `yaml:"watchModels,omitempty"`
	// WatchModelsIntervalSec is that poll interval. 0 => the caller's default.
	WatchModelsIntervalSec int `yaml:"watchModelsIntervalSec,omitempty"`
	// UpdateCheck polls GitHub for new releases. Defaults to on, and is a no-op
	// outside a release build regardless.
	UpdateCheck *bool `yaml:"updateCheck,omitempty"`
	// HfToken authenticates Hugging Face requests (gated repos, higher rate
	// limits). The HF_TOKEN / HUGGING_FACE_HUB_TOKEN environment variables still
	// win, so a shell that already exports one is not silently overridden.
	HfToken string `yaml:"hfToken,omitempty"`
}

// appSettingsFile is the minimal shape needed to pull settings.app out of the
// generate file.
//
// Deliberately not LoadGenerateFile: that applies every sizing default and
// merges the whole sidecar, and it is called with a models dir the caller has
// not resolved yet at this point in startup. This block is read before any of
// that exists, so it parses the two files on its own terms.
type appSettingsFile struct {
	Settings struct {
		App AppSettings `yaml:"app"`
	} `yaml:"settings"`
}

// LoadAppSettings resolves the process-level block: the generate file's
// settings.app, with the dashboard-owned sidecar block layered on top.
// A missing file at either level is not an error — it means "nothing set".
func LoadAppSettings(generatePath string) (AppSettings, error) {
	var out AppSettings
	if generatePath != "" {
		data, err := os.ReadFile(generatePath)
		switch {
		case err == nil:
			var gf appSettingsFile
			if err := yaml.Unmarshal(data, &gf); err != nil {
				return AppSettings{}, fmt.Errorf("parsing %s: %w", generatePath, err)
			}
			out = gf.Settings.App
		case !os.IsNotExist(err):
			return AppSettings{}, err
		}
	}
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return AppSettings{}, err
	}
	if sc.App != nil {
		out = mergeAppSettings(out, *sc.App)
	}
	return out, nil
}

// mergeAppSettings layers next over base, field by field: an unset field in next
// inherits base. Written out rather than reflected because "unset" differs by
// field — "" for the strings, 0 for the interval, nil for the tri-state flags —
// and collapsing that into one rule is how a legitimate empty AdminAllow (the
// user clearing their tailnet range) turns back into the old value.
func mergeAppSettings(base, next AppSettings) AppSettings {
	out := base
	if next.Listen != "" {
		out.Listen = next.Listen
	}
	if next.PlaygroundListen != "" {
		out.PlaygroundListen = next.PlaygroundListen
	}
	if next.AdminAllow != "" {
		out.AdminAllow = next.AdminAllow
	}
	if next.AdminOpen != nil {
		out.AdminOpen = next.AdminOpen
	}
	if next.WatchModels != nil {
		out.WatchModels = next.WatchModels
	}
	if next.WatchModelsIntervalSec != 0 {
		out.WatchModelsIntervalSec = next.WatchModelsIntervalSec
	}
	if next.UpdateCheck != nil {
		out.UpdateCheck = next.UpdateCheck
	}
	if next.HfToken != "" {
		out.HfToken = next.HfToken
	}
	return out
}

// UpsertSidecarApp stores the dashboard's process-level block VERBATIM.
//
// A replace, not a merge, unlike the settings patch: the dashboard renders this
// section as one form and PUTs all of it, and the fields here have no meaningful
// "unset from this section" state — clearing AdminAllow must actually clear it,
// which a merge could not express.
func UpsertSidecarApp(generatePath string, app AppSettings) error {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return err
	}
	sc.App = &app
	return writeSidecar(generatePath, sc)
}

// LoadSidecarApp returns only the dashboard-owned block (nil when unsaved), so
// the settings API can report which values the user actually pinned as opposed
// to inherited from the generate file.
func LoadSidecarApp(generatePath string) (*AppSettings, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.App, nil
}
