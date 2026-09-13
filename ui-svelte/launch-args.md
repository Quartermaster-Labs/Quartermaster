# Launch arguments

Status: **implemented on `radu0120/launch-args`** (phases 1-5, see Phases below); the editor half is
llama-only so far. Supersedes the two-way launch-parameters box in
`ModelConfigModal.svelte`. Issue #38 (mmap flipping back on, `-cram` duplicating on each save) is one
of the symptoms this shape removes.

Goal: the user can write any backend flag, in their own order and spelling, and it is applied exactly
as written. quartermaster keeps owning every flag the user did not touch, so generator and sizer
improvements still reach the model after an upgrade. A pinned flag is a constraint on the plan too:
the sizer folds the values it understands into its own decisions (`internal/autogen/pins.go`), so the
emitted flags, the baked `estVramGB`/`estRamGB` and the editor's memory panel describe the launch the
text pins, not a second launch nobody runs.

## How it works today

The modal shows one editable box, "Launch parameters (Custom)". It is a rendering of the command
quartermaster would spawn, not an independent store.

- **Override** (`internal/autogen/overrides.go`): about sixty structured fields (`ctx`, `ngl`,
  `mmap`, `cacheType`, sampler knobs, ...) plus one free string bucket, `ExtraArgs`.
- **Emitter** (`internal/autogen/generate_cmd.go`, `buildCmdLines` / `RenderSoloCmd`): turns the
  override plus model metadata into the argv stored as `cmd:` in the generated `config.yaml`.
  `internal/process/process_command.go` shlex-splits it at spawn; there is no shell.
- **Editor** (`ui-svelte/src/components/ModelConfigModal.svelte`): `onCmdBlur` parses the visible
  text back into fields with `parseCmdFields` (`ui-svelte/src/components/modelCmdForm.ts`); field
  changes re-render the box through `POST /api/models/{id}/preview` (`handleAPIModelCmdPreview`,
  debounced 150 ms); Save writes the override, regenerates the config and reloads.
- **Four flag models, nothing keeping them in sync**: the Go emitter, the TypeScript parser,
  `ParseCmd` in `internal/config/config.go` (token-exact, used to parse the configured command), and
  `estimateInputFromCmd` in `internal/server/configapi_estimate.go` (partial, reads only what the
  sizer needs).

Because the box is a view, the user's bytes are decomposed on blur and re-synthesized from fields.
Anything `parseCmdFields` does not recognise is pushed into `extraArgs` and emitted verbatim at the
end of the command.

## Why it keeps breaking

Five mechanisms exist whose whole job is to paper over the round trip:

| Mechanism | Where | Papers over |
|---|---|---|
| `IGNORE_VALUE` / `IGNORE_BOOL` | `modelCmdForm.ts` | flags that legalise a text edit without pinning a field (and are silently dropped otherwise) |
| delta helpers (`cmsEdit`, `ckptEdit`, `samplerDelta`, `genDefaultKv`, `genDefaultNum`) | `ModelConfigModal.svelte` | "did the user write this, or is it just the rendered default?" |
| hoists (`hoistChatTemplate`, `hoistCms`) plus `hoistCmsFromExtra` | `modelCmdForm.ts`, `generate_cmd.go` | recover a flag the parser moved into `extraArgs` |
| tri-state inference (`mmapInheritOn`, `variantMmapInherit`) | `ModelConfigModal.svelte` | whether a boolean was pinned, guessed by reading the effective command |
| per-variant command fetches | `ModelConfigModal.svelte` | keep a variant pane from rendering the base model's command |

Each one answers that question differently, so each has a failure mode. The bug classes:

- **Accumulation**: a flag the parser does not know (`-cram` before it was patched) goes into
  `extraArgs` while the emitter emits its own copy from a structured field, so the command gains one
  copy per save.
- **Reset**: `mmapInheritOn` reads `config.cmd`, the effective command, to decide whether the user
  pinned mmap. Once the pin is in the command, the delta says "same as rendered", Save writes `""`,
  and the generator flips the flag back to the placement default.
- **Invisible flags**: `parseCmdFields` switches on the whole token, so `--ctx-size=32768` matches
  no case and lands in `extraArgs`; `val()` returns "" when the next token starts with `-`, so a
  negative sampler value is lost.
- **Disagreement**: the preview and the spawned argv are produced by different code paths, and only
  the first is on screen.

Root cause: the command text is used as a lossy interchange format between the form and the emitter.
Patching the parser does not fix that class; the next flag added to the emitter recreates it. `-cms`
was patched with a parser case plus a hoist, `-cram` was not.

## Requirements

| # | Requirement |
|---|---|
| R1 | What the user types is stored and applied verbatim. No normalisation, reordering, or rewriting on save. |
| R2 | Saving twice changes nothing the second time. |
| R3 | Flags the user did not touch stay generator-owned, so sizer upgrades apply on their own. |
| R4 | Any flag the backend understands is expressible, including flags quartermaster has no field for. |
| R5 | The command that will run is visible, with clear provenance. |
| R6 | An invalid flag fails in the UI, not at spawn. |
| R7 | A form control and the text can never silently disagree about a flag. |

## Design

### Two layers, one direction of composition

- **Custom launch arguments**: the user's text, stored verbatim (new `customArgs` on the override;
  the old `extraArgs` key is read as a fallback) and never rewritten by the app.
- **Generated command**: what the emitter computes today from the structured fields.
- **Composition** happens in one function used both to render and to emit: for every knob present in
  the custom text, drop all generated occurrences of that knob, then append the custom text verbatim
  at the end. Last wins, which is also how llama.cpp resolves a repeated flag.

Suppression is per **knob**, driven by the flag table below:

- aliases collapse (`-c` and `--ctx-size` are one knob, so either spelling suppresses the generated
  `-c`);
- chain flags (`--spec-type`) replace all generated occurrences, because keeping half the chain would
  mix two configurations;
- a genuinely additive flag is marked in the table and never suppressed.

### The flag table

One checked-in Go table, exported to the UI through the DTO, with a row per flag:

| Column | Example |
|---|---|
| canonical name + aliases | `-c` / `--ctx-size` |
| knob | `ctx` |
| value-taking / boolean | value |
| repeatable policy | replace-all, first-wins |

It is the single source for suppression (which generated token to drop), for the ownership rule
below, and for validation. A unit test walks golden commands produced by every emitter and asserts
every emitted flag is in the table. That test is the anti-rot mechanism, and it is what would have
caught `-cram` the day it was added.

### Knob ownership in the form

A control is bound to its knob, never to the text:

- the knob's flag is present in the custom text: the control shows the parsed value, with a marker
  ("from launch args") when the value is not one the control would produce (`--load-mode dio`).
  Editing the control rewrites the token in the custom text; reset deletes the token.
- the knob's flag is not present: the structured field owns it, exactly as today.

On save the server **zeroes a structured field whose knob the custom text owns** (one place,
`applyOverrideDTO`). Without this, a stale `mmap: "off"` sits invisibly under `--load-mode none` and
resurfaces the moment the token is deleted, which is the issue #38 bug in another costume.

The end state above is write-back. Until it lands the form is **mark + lock**, the read-only half: a
control whose knob the text owns is disabled, dimmed, and badged with the exact flag (`-fa off`),
with the toggle position taken from the token's on/off where it parses. The text stays the only
editor. The llama Default tab (including Advanced) does this today; the variant tab does not yet.

### Pinned values feed the sizer

Composition makes a flag win at spawn. Left at that, the plan around it described a different
launch: `-c 5000` overrode the emitted `-c` while the baked `estVramGB` and the editor's memory panel
still showed the sizer's pick. `internal/autogen/pins.go` closes that: the sizer reads the pinned
knobs back and sizes around them, and the generated occurrence of each flag then says the same thing
as the text that replaces it.

- **Pinned knobs**: `-c`, `-ngl`, `--n-cpu-moe`, `-ctk`/`-ctv`, `--no-kv-offload`, `--parallel`,
  `-ub`, `--spec-type`, `--rope-scaling`, `--ctx-checkpoints`, `-cms`. Anything else (samplers,
  `-cram`, `--no-op-offload`, ...) still wins at spawn and does not move the plan.
- **Where**: `emitModel` folds the model-wide scalars into its override copy before sizing, and a
  per-profile pass does the same for each profile's own text (blank inherits, `none` drops it). The
  preview path (`RenderSoloCmdLayers`) applies the same pins, so a preview equals the save.
- **`-c` is the whole pool**: `profile.Ctx` is the per-slot window, so a pin divides by `--parallel`
  and `buildCmdLines` multiplies it back out. A pinned window is used as-is, not rounded to a 4096
  multiple. `-ngl <blocks>` means every layer on GPU, not "auto".
- **Estimate**: the `/estimate` endpoint gained `custom` (the effective text) and `parallel` (slot
  count) params; `Pins.ApplyToEstimate` folds the pins over the form fields, so the panel shows the
  pinned ctx and the KV its pool actually reserves (`parallel` charges every slot's share, which the
  preview used to under-report). The modal panel is a **candidate**: it always describes the form as
  it would launch, never the running process. `actual=true` seeds from the loaded command (used by
  the dashboard band in `stores/vram.ts`), and that window is used exactly: `-c` is decoded as the
  total pool (`estimateInputFromCmd`) and never re-rounded to the sizer's ladder. Seeding the modal
  from the running argv was dropped 2026-09-13: a process can be older than the config (a save
  applies without a restart), so the readout snapped back to the old window right after a save.

### The two panes

- **Custom launch arguments**, collapsed by default, behind an "Enable custom launch arguments"
  toggle. Enabled means applied. Disabled keeps the text in the sidecar but greys it and does not
  apply it, so switching off is reversible without retyping. Tooltip: flags written here are appended
  to the generated command and replace the generated flag for the same setting; empty means the
  generated command runs as-is.
- **Final launch arguments**, collapsed by default, read-only. One element per token, styled by
  provenance: custom tokens green, a replaced generated value available in a tooltip or struck
  through, suppressed tokens either struck through or summarised ("3 replaced, 1 added"). This pane
  is the source of truth for what runs.
- The estimate panel stays visible and reads the composed command, so an override that busts VRAM or
  context shows up as such.

### Provenance in the API

`POST /api/models/{id}/preview` returns the command in layers:

```json
{
  "cmd": "-m ... -cram 2048 --load-mode none",
  "custom": "--load-mode none -cram 2048",
  "generated": "-m ... -c 8192",
  "effective": "-m ... -cram 2048 --load-mode none",
  "ownedKnobs": ["cacheRam", "loadMode"],
  "tokens": [
    {"text": "-m", "source": "generated"},
    {"text": "-cram", "source": "custom"},
    {"text": "2048", "source": "custom"}
  ]
}
```

(`cmd` duplicates `effective` for callers that just want the string. `GET
/api/models/{id}/config` keeps returning the saved `cmd` plus the override's `customArgs`: the
stored command cannot be decomposed back into layers after the fact. Per-token `issues` arrives with
the validation phase.)

The browser renders provenance; it never diffs strings. `effective` is what gets copied and what the
estimate consumes.

### Plumbing flags

Some flags are load-bearing for quartermaster itself. When the custom text contains one, Save warns,
the confirmation names the consequence, and the flag is honoured if confirmed:

| Flag | Consequence to name |
|---|---|
| `-m` | the model id serves a different file; the override key must be derived from the **generated** layer (`resolveModelGguf` reads the composed command today), or the settings pages start editing the other file's override |
| `--port`, `--host` | the router health-checks the port it assigned; the model can be spawned and never reachable |
| `--slot-save-path` | snapshots go to the new path but pruning and preamble seeding keep the configured one, so those features go quiet |

(Honouring `--port`/`--host` was argued against; the decision was warn, confirm, honour.)

### Validation

Two sources, unioned:

1. the checked-in table (what quartermaster emits);
2. the selected backend binary's `--help`, memoised on path + size + mtime, same pattern and probe
   budget as `ListBackendDevices` (`internal/autogen/backenddev.go`).

Rules: unknown to both = error with a nearest-match suggestion; known to the table but not to this
binary = warning ("this backend is older than the flag"); a flag only the binary lists (newer build
than the table) is accepted silently; autocomplete comes from the union. The same check runs at
startup against the generated command, so an emitter flag the installed binary no longer accepts
becomes a visible notice instead of a spawn failure.

Enforcement is **warn + confirm, never a block** (decided 2026-09-13): the pane shows each issue
inline, tints the box, and Save asks first when an unknown flag stands ("llama-server will refuse to
start with --cms"), then stores the text if confirmed. The check runs against the override about to
be PUT, so a debounced keystroke cannot make the confirm lie; the Default tab and every variant with
its own text are checked. A flag the probe could not judge (backend `--help` unreadable) is a warning,
never a confirm. Rationale: the text is the user's, the API is used by qm-tools and hand edits too,
and a hard reject would trap a real flag the checked-in table has not learned yet.

Implemented (`internal/autogen/validate.go`, `RenderSoloCmdLayers`; pane + save guard in
`LaunchArgsPanes.svelte` / `ModelConfigModal.svelte`). The startup check landed 2026-09-13
(`internal/server/flagcheck.go`): at boot every llama-server command in the running config is
validated against the installed binary's `--help`, and a flag the emitter writes that this build
dropped (or a typo that got saved anyway) is logged as a warning naming the model, so the failure is
visible before the spawn. Autocomplete was dropped 2026-09-13: not wanted, typing flags by hand is
the point of the box.

### Variants

Unchanged: blank custom text inherits the model-wide text, the `none` sentinel clears it
(`inheritStr`, `internal/autogen/inherit.go`).

## What gets deleted

- the blur-parse path: `onCmdBlur`, `parseCmdFields`, `IGNORE_VALUE` / `IGNORE_BOOL`, the chat
  template and `-cms` hoists;
- the delta helpers (`cmsEdit`, `ckptEdit`, `samplerDelta`, `genDefaultKv`, `genDefaultNum`);
- the tri-state inference (`mmapInheritOn`, `variantMmapInherit`) and, once provenance comes from the
  DTO, the per-variant command fetches;
- `hoistCmsFromExtra`; the general reconcile takes its job.

## Migration

Lazy. No sidecar rewrite, no forced regen; old fields keep working. Acceptance test for phase 1: with
an empty custom text, every existing sidecar composes a **byte-identical** command to today's.

Also update the `extraArgs` field description for qm-tools in `internal/server/turns_qm_fields.go` to
the new meaning.

## Phases

1. **Go**: flag table, composition/reconcile, split DTO (`custom` / `generated` / `effective` /
   `tokens` / `issues`), shadowed-field zeroing in `applyOverrideDTO`, backend `--help` probe.
   Done on `radu0120/launch-args` (`flagtable.go`, `customargs.go`, `backendhelp.go`,
   `validate.go`).
2. **Modal (llama form only)**: the two panes, verbatim editor, read-only provenance box; delete the
   blur parse. Issue #38 is fixed here. Done on `radu0120/launch-args`
   (`LaunchArgsPanes.svelte`; `parseCmdFields`, `IGNORE_VALUE`/`IGNORE_BOOL` and their tests deleted).
3. **Controls**: write tokens, presence-based pins, reset to auto, plumbing confirmations. The
   read-only half landed 2026-09-13 (`knobTokens`/`lockedBool` in `modelCmdForm.ts`, `knobBadge` in
   `ModelConfigModal.svelte`): an owned control is disabled, dimmed and badged with the flag, and a
   toggle follows the token's on/off. Default tab including Advanced is covered; the variant tab is
   not.
4. **Validation UI**: inline issues + save confirm done (2026-09-13); the startup check landed the
   same day (`internal/server/flagcheck.go`); autocomplete dropped (not needed).
5. **Pins feed the sizer** (2026-09-13): `internal/autogen/pins.go` reads the composition-relevant
   flags out of the custom text and folds them into the override copy (`emitModel`,
   `RenderSoloCmdLayers`) and the estimate (`Pins.ApplyToEstimate`, applied server-side from the
   `custom` query param). A pinned `-c`/`-ctk`/`-ctv`/`-ub`/`-ngl`/`--n-cpu-moe`/`--parallel`/
   `--spec-type`/`--rope-scaling`/`--ctx-checkpoints`/`-cms`/`--no-kv-offload` is a constraint: the
   plan is sized around it, so the emitted flags and the baked `estVramGB`/`estRamGB` describe the
   launch that runs. Unmodeled knobs still only win at spawn. The `parallel` and `custom` estimate
   params carry the slot count and the effective text from the editor.
6. **Later**: the same panes for the image, audio and SAM forms with their own tables.

## Non-goals (this pass)

- image, audio and SAM forms keep the read-only box;
- no shell features: the command stays an argv split by shlex (`SanitizeCommand`), exactly as the
  process layer spawns it.
