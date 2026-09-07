package main

// The no-UI install path.
//
// The wizard's normal shape is a window (or a browser) driving an HTTP API on
// loopback, which is exactly what a headless server does not have: there is no
// display for the window, no browser for the fallback, and the API is bound to
// 127.0.0.1 and Host-checked, so it cannot be driven from the operator's
// desktop either. Before this, installing on such a box meant running the
// server binary by hand and writing the generate file yourself.
//
// So this mode answers the wizard's questions from argv and runs the same
// install, reporting to stderr. It shares every step with the interactive path:
// the same Choices struct, the same Wizard.Start, the same backend downloads.
// Only the source of the answers and the reporting surface differ.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// statusPoll is how often the run is sampled for progress. The interactive UI
// polls at a similar rate; a terminal needs no faster, and every sample that
// reports the same step prints nothing.
const statusPoll = 250 * time.Millisecond

// headlessChoices fills a Choices from the command line, taking anything the
// operator did not name from the same probe the wizard's first screen shows.
//
// Defaulting to the probe rather than to a fixed list is what keeps the two
// paths honest: a headless install picks the compute backend this machine would
// have been offered, including the platform fallbacks (no CUDA build exists for
// Linux, so an NVIDIA box there is recommended Vulkan, not a download that
// cannot happen).
func headlessChoices(dir, modelsRoot, variant, components string) setup.Choices {
	probe := setup.NewProbe(dir)

	c := setup.Choices{
		Dir:        dir,
		ModelsRoot: modelsRoot,
		Variant:    variant,
	}
	if c.Variant == "" {
		c.Variant = probe.Variant
	}

	switch strings.TrimSpace(components) {
	case "":
		for _, comp := range probe.Components {
			if comp.Selected {
				c.Components = append(c.Components, comp.ID)
			}
		}
	case "none":
		// Explicitly nothing: the operator wants the files and the config, and
		// will pick backends from Settings later.
	default:
		for _, id := range strings.Split(components, ",") {
			if id = strings.TrimSpace(id); id != "" {
				c.Components = append(c.Components, id)
			}
		}
	}
	return c
}

// runHeadless performs the install and returns the process exit code.
//
// Progress is printed only when it changes, so a log captured by a service
// manager or an SSH session stays readable: an install is minutes of backend
// downloads, and a line per poll would be thousands of them.
func runHeadless(wiz *setup.Wizard, c setup.Choices, launch bool) int {
	report := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "setup: "+format+"\n", args...)
	}
	report("installing into %s", c.Dir)
	if len(c.Components) > 0 {
		report("backends: %s (%s)", strings.Join(c.Components, ", "), c.Variant)
	}

	if err := wiz.Start(context.Background(), c); err != nil {
		report("could not start: %v", err)
		return 1
	}

	last := ""
	for {
		st := wiz.Status()
		if line := strings.TrimSpace(st.Step + " " + st.Detail); line != last && line != "" {
			report("%s", line)
			last = line
		}
		switch st.Phase {
		case setup.PhaseError:
			report("failed: %s", st.Error)
			return 1
		case setup.PhaseDone:
			for _, w := range st.Warnings {
				report("warning: %s", w)
			}
			// Finish runs Options.Launch, which is the whole point on a server:
			// the install is useless until something starts it, and there is no
			// window here to click "Launch" in.
			if err := wiz.Finish(launch); err != nil {
				report("install completed but launching failed: %v", err)
				return 1
			}
			report("done")
			return 0
		}
		time.Sleep(statusPoll)
	}
}
