package config

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// CmdInfo is a parsed, read-only view of a rendered launch command.
//
// A model's command is a STRING all the way from autogen's emitter to the
// spawn, so every consumer that wants one fact out of it (which gguf? which
// port? is the slot cache on?) has to take it apart again. Doing that ad hoc
// produced two classes of bug: substring sniffs like
// strings.Contains(cmd, "--spec-type draft-mtp") that silently stop matching
// when the emitter wraps the flag onto its own line, and a full shlex split on
// the request hot path (per-request family lookup / slot-cache gating).
//
// ParseCmd centralizes and memoizes the split, so callers ask token-exact
// questions (Has/Value/Values) instead of scanning raw text, and repeat asks
// for the same command are a map lookup.
//
// A CmdInfo must be treated as immutable — it is shared between callers.
type CmdInfo struct {
	// Argv is the sanitized token list, nil if the command could not be split.
	Argv []string
	// ModelPath is the model file the command loads: the value of -m / --model /
	// --diffusion-model, slash-normalized. "" when the command names none (a
	// non-llama.cpp upstream). This is the fork's model "family" key — every
	// variant of one model (ctx tiers, game/judge profiles) is the same file
	// launched with different placement flags.
	ModelPath string
}

// modelPathFlags are the flags whose value is the loaded model file; the first
// one appearing in the command decides. sd-server loads via --diffusion-model,
// qwentts/asr/sam via --model, TTS.cpp via --model-path, llama-server via -m.
// A backend missing from this list has no family key, which is not cosmetic: the
// config editor refuses to save an override for a model whose gguf it cannot
// find ("model has no gguf path to override"), so every emitted backend's model
// flag belongs here.
var modelPathFlags = []string{"-m", "--model", "--model-path", "--diffusion-model"}

// cmdInfoCache memoizes by raw command string. Commands are stable config data
// (one per model, changing only on reload), so the cache is small and never
// needs per-entry eviction — it is dropped wholesale past a generous cap so a
// pathological caller (many one-off adhoc commands) can't grow it without
// bound.
var (
	cmdInfoMu    sync.RWMutex
	cmdInfoCache = map[string]*CmdInfo{}
)

const cmdInfoCacheMax = 512

// ParseCmd splits a rendered launch command and extracts the facts consumers
// share. It never returns nil; an unparseable command yields a CmdInfo with a
// nil Argv, so callers find no flags and fall back to their defaults exactly as
// they did when each of them called SanitizeCommand itself.
func ParseCmd(cmd string) *CmdInfo {
	cmdInfoMu.RLock()
	info, ok := cmdInfoCache[cmd]
	cmdInfoMu.RUnlock()
	if ok {
		return info
	}

	info = &CmdInfo{}
	if argv, err := SanitizeCommand(cmd); err == nil {
		info.Argv = argv
	}
	if v, ok := info.Value(modelPathFlags...); ok {
		info.ModelPath = filepath.ToSlash(strings.TrimSpace(v))
	}

	cmdInfoMu.Lock()
	if len(cmdInfoCache) >= cmdInfoCacheMax {
		cmdInfoCache = map[string]*CmdInfo{}
	}
	cmdInfoCache[cmd] = info
	cmdInfoMu.Unlock()
	return info
}

// Has reports whether any of the given flags appears as its own token (or in
// --flag=value form). Token-exact: unlike a substring test it can't be fooled
// by a longer flag that starts the same way, nor defeated by a line break
// between the flag and its value.
func (c *CmdInfo) Has(flags ...string) bool {
	for _, a := range c.Argv {
		for _, f := range flags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return true
			}
		}
	}
	return false
}

// Value returns the value of the first of the given flags that carries one,
// accepting both "--flag value" and "--flag=value". ok is false when no flag
// matched or the match was the final token with nothing after it.
func (c *CmdInfo) Value(flags ...string) (string, bool) {
	for i, a := range c.Argv {
		for _, f := range flags {
			if strings.HasPrefix(a, f+"=") {
				return strings.TrimPrefix(a, f+"="), true
			}
			if a == f {
				if i+1 < len(c.Argv) {
					return c.Argv[i+1], true
				}
				return "", false
			}
		}
	}
	return "", false
}

// Values returns every value given for a flag, in order. Repeatable flags
// (--spec-type, --lora) are accumulated by the backend rather than overridden,
// so a caller asking "is draft-mtp active" must look at all of them.
func (c *CmdInfo) Values(flags ...string) []string {
	var out []string
	for i, a := range c.Argv {
		for _, f := range flags {
			if strings.HasPrefix(a, f+"=") {
				out = append(out, strings.TrimPrefix(a, f+"="))
			} else if a == f && i+1 < len(c.Argv) {
				out = append(out, c.Argv[i+1])
			}
		}
	}
	return out
}

// Int is Value parsed as a base-10 int; ok is false if absent or non-numeric.
func (c *CmdInfo) Int(flags ...string) (int, bool) {
	v, ok := c.Value(flags...)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// HasValue reports whether any of the flags is present with the exact value v.
func (c *CmdInfo) HasValue(v string, flags ...string) bool {
	for _, got := range c.Values(flags...) {
		if got == v {
			return true
		}
	}
	return false
}
