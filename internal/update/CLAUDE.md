# internal/update

## Purpose

Keeps a running quartermaster on the latest release by **swapping its own executable in place** —
no installer, no wizard, one click. Polls GitHub releases, verifies the download, renames the new
binary over the running one, and (on a desktop install) relaunches into it.

## Why there is no installer in this path

The first version downloaded the Inno Setup installer and ran it. Every update was therefore an
interactive reinstall: it re-asked for the install directory and the models folder, and it re-ran
the backend fetch — which **deleted the user's working llama.cpp build** and replaced it with the
wizard's default. An update that can silently repoint `serverExe` at a different compute backend is
not an update.

`packaging/windows/installer.iss` still exists, and still ships, but it is now **first-install
only** and it no longer asks or fetches anything at all. The configuration pages and the
`fetch-backend.ps1` runs are gone, replaced by `cmd/quartermaster-setup`, which embeds the
installer and drives it silently. What Inno does now is place files, register the uninstaller and
create the Start Menu group — none of which can repoint a working install at a different backend,
so the upgrade-detection code it used to need is gone too.

## The swap

The app is one self-contained binary — the UI is embedded, and backends, config and user data all
live outside it. So a new version *is* a new file:

```
download bare binary -> verify sha256 -> rename running exe aside -> rename new binary in -> restart
```

- **Renaming a running executable is legal** on Windows (only *deleting* or *overwriting* a mapped
  image is refused) and on every unix. That single fact is what lets one code path cover all
  platforms with no helper process, no restart shim, and no `MoveFileEx` reboot queue.
- **Both renames stay inside the exe's own directory** (`.qm-update/`, a *sibling of the exe*, never
  `%TEMP%`) — so neither can fail on a cross-device boundary, and each is atomic.
- **The aside-copy is the rollback.** If the second rename fails, the first is undone and the
  install is byte-for-byte what it was. Only if *both* fail is there a problem, and the error names
  the file to move back by hand.
- The stale `.old` is swept by `SweepOld` on the **next** start, when nothing has it mapped.
- `renameRetry` absorbs the transient sharing violations Windows scanners and indexers produce.

## Who restarts

`restartMode()` decides, and it is re-read at apply time, not cached at startup.

| Environment | `Restart` | Behaviour |
|---|---|---|
| Desktop (tray, a shortcut, a terminal) | `auto` | Swap, shut down, then `main` calls `Spawn()` — same argv, same cwd, detached |
| Supervised (systemd, WinSW) | `manual` | Swap and stop. The UI says "restart the service to finish" |
| Container | — | `Blocked`. The image is the unit of update; a swapped binary vanishes with the container |

A server that bounces itself unasked is a fault, not a feature — hence `manual`. Note that both
supervisor definitions restart **on failure only**, so "exit and let the supervisor pick it up"
would simply have left the service down.

Detection: `INVOCATION_ID`/`JOURNAL_STREAM` (systemd sets these in every unit, so existing installs
need no unit-file change), `svc.IsWindowsService()`, or an explicit `QM_SUPERVISED=1` — which is
what `packaging/windows/quartermaster-service.xml` sets, because **WinSW runs the exe as a plain
child process that the Windows service API cannot see**.

`Spawn()` is called by `main` *after* teardown completes, so the replacement never races the dying
process for the listen sockets.

**On Windows the replacement must break out of the job object**, and this is not optional:
`process.SetupTreeCleanup` puts quartermaster in a job with `KILL_ON_JOB_CLOSE` so a crash reaps the
backends, job membership is inherited, and the replacement was therefore killed by the OS a
heartbeat after it started, every time, silently, because `cmd.Start()` had already returned
success. `detachedAttr` adds `CREATE_BREAKAWAY_FROM_JOB` and the job is created `BREAKAWAY_OK`;
`Spawn` retries without the flag if some outer job refuses, since a replacement that runs and might
be reaped beats one that never started. Note the fix lives in the OUTGOING binary: an install
updating *away* from a build that predates it still gets the old behaviour once, because that build
is the one doing the spawning.

## The other consumer: first install

`FetchBinary` (`fetch.go`) is the same lookup and the same verification, pointed at a directory
instead of at the running exe. `cmd/quartermaster-setup` calls it on unix when a lone setup download
has no binary beside it to copy, which is what lets one downloaded file install the whole thing
where there is no Inno package to embed. `Repo` lives here too, so the wizard and the updater cannot
disagree about which repository an install belongs to.

It deliberately does **not** reuse `Checker.Apply`: nothing is being replaced, there is no rollback
to arrange and no restart to decide, and the caller is a wizard with its own progress UI. What it
does reuse is the part that matters, `assetName` + digest + `SHA256SUMS` fallback, so a first install
is verified to exactly the standard an update is.

## Gotchas

- **Asset names are a contract.** `assetName()` must match what the release actually publishes
  (`Makefile: dist`, `packaging/windows/build-release.ps1`). A name that drifts does not error —
  that platform just silently stops seeing updates. Matching is **exact**, so
  `quartermaster-windows-amd64.exe` is never confused with `quartermaster-setup-windows-amd64-vX.Y.Z.exe` sitting
  in the same release. Note the Windows asset name is **not** the installed filename (which is
  `Quartermaster.exe`) — `exePath()` swaps by path, so the two are free to differ, and the asset
  name must stay frozen or pre-rename installs stop seeing updates.
- **No digest, no update.** The GitHub API's per-asset `digest` field is preferred, with a
  `SHA256SUMS` asset as fallback. If neither yields a hash the release is *reported but not
  offered*: executing an unverified download is the one failure this package cannot walk back.
- **`Status.Checked` is what makes the check button honest.** Without a timestamp, a check that
  found nothing looks exactly like a button that did nothing — and `CheckNow` returns the fetch
  error rather than only logging it for the same reason. A failed check deliberately leaves the
  stamp alone, so a stale answer is never presented as freshly confirmed.
- **Dev builds never poll.** `parseSemver` rejects `local_<hash>`, so a working tree is never told
  it is out of date — and `newer` returns false whenever *either* side is unparseable. The Docker
  image's non-release builds rely on the same property under a different name, `edge_<sha>`
  (see [`.github/CLAUDE.md`](../../.github/CLAUDE.md)); keep any new build-version scheme
  unparseable unless it really is a release.
- **Apply runs on the server's lifetime context, not the request's.** `POST /api/update` returns
  202 immediately; a closing browser tab must not abort a swap already touching files on disk.
- `blockedReason()` probes writability by **creating and removing a file**, not by reading mode
  bits — an ACL, a read-only mount, or a different owner all produce a directory that *looks*
  writable and is not.

## Connections

- `internal/server/update.go` — `POST /api/update`, `GET /api/update/status`,
  `POST /api/update/check`, `RelaunchPending()`. All three routes are on `adminChain`.
- `internal/server/apigroup.go` — `/api/version` also carries `update_blocked`, `update_restart`,
  `update_phase` so the sidebar can render the state without a second request.
- `cmd/quartermaster/quartermaster.go` — runs `Checker.Run`, and after teardown calls `update.Spawn()` when
  `RelaunchPending()`.
- `ui-svelte/src/stores/update.ts` — the whole client state machine (apply, poll, on-demand
  check), as a store rather than component state because **two** views show one process:
  `Sidebar.svelte`'s button (only present when an update exists) and `SoftwareUpdate.svelte` in
  Settings → System (always present — where "am I current?" gets answered, and where the check
  button lives).
