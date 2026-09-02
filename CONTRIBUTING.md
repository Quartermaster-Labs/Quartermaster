# Contributing

Thanks for looking. quartermaster is a small project, and issues and PRs are both
welcome, and the bar is "does it work and does it fit", not ceremony.

## Before you write code

- **Bug?** Open a [bug report](.github/ISSUE_TEMPLATE/bug-report.md). The
  template asks for your config plus proxy and upstream logs (copy from `/logs`)
  because nearly every real bug here is a launch-command or VRAM-fit problem, and
  those two things answer it immediately.
- **Question?** Use Discussions, not an issue.
- **Security bug?** Don't open an issue: see [`SECURITY.md`](SECURITY.md).
- **Feature or refactor?** Open an issue first if it is more than an afternoon of
  work. quartermaster has opinions (below) that are cheaper to discuss than to
  discover in review.

## Getting set up

You need **Go** (version from [`go.mod`](go.mod)) and **Node 20+** for the UI.

```sh
git clone https://github.com/Quartermaster-Labs/Quartermaster
cd quartermaster
make ui          # build the Svelte app into internal/server/ui_dist
go build ./...
```

`internal/server/ui_dist` is generated and untracked, but it is `go:embed`ed,
so `make ui` (or at least one `npm run build`) has to run before the Go build
will succeed on a fresh clone.

For UI work, run the Vite dev server against a running quartermaster instead of
rebuilding the bundle each time:

```sh
cd ui-svelte && npm install && npm run dev
```

You do **not** need GPUs or model weights to work on most of the codebase; the
tests use a stub upstream (`cmd/simple-responder`) rather than real inference.

## Tests

```sh
make test-dev    # fast: go test -short + staticcheck
make test-all    # full, with -race; run before opening a PR
make test-ui     # svelte-check + vitest, after changes under ui-svelte/
```

Fix every staticcheck finding you introduce. Test naming follows the existing
convention: `TestProxyManager_<name>`, `TestProcessGroup_<name>`, and so on.

Run `gofmt -w` on Go files before committing. CI runs the Go tests on Linux and
Windows and the UI tests separately; all three must be green.

## Finding your way around

Each subsystem has its own `CLAUDE.md` with the file layout, the types that
matter and the gotchas: start from the table in the root
[`CLAUDE.md`](CLAUDE.md) and read the one for the area you're touching. Those
files are documentation for humans too, despite the name; if your change makes
one of them wrong, update it in the same PR.

## Things that will get a PR sent back

These aren't style preferences: each one has broken something before:

- **Splitting into multiple processes.** Multi-listener operation and
  cross-port eviction require ONE process with N listeners sharing ONE
  router/scheduler. Two instances means two schedulers, no shared VRAM
  accounting, and models colliding on the GPU. Per-listener behaviour belongs in
  the *request context*, never in a handler and never in a second instance.
- **Putting a new admin or config-editor route on the wrong middleware chain.**
  API keys gate the inference API only; the admin surface is gated by remote
  address. A new `/api/*` ops or editor route goes on `adminChain`. Getting this
  wrong publishes the config editor to whatever address the port is bound to.
- **Interrogating a launch command with `strings.Contains`.** Use
  `config.ParseCmd`: substring tests break on line-wrapped flags and match
  prefixes of longer flags.
- **Adding volatile text to the KV-stable prompt prefix.** Tool descriptions and
  system-prompt lines are cache state; putting today's date in a tool
  *description* invalidates every conversation. It belongs in the tool *result*.
- **Trusting a tool argument.** Every one of them is model-generated text: URLs
  go through the `fetch_page` SSRF guard, identifiers are validated or rebuilt
  before reaching a URL or argv.
- **Committing large binaries.** Model weights and backend executables are
  fetched at runtime or by the installer, never checked in.

## Pull requests

- Branch off `main`; keep one logical change per PR.
- Commit messages:

  ```
  scope: short subject

  Longer description of X and Y.

  - key change 1
  - key change 2
  ```

  `scope` is the subsystem: `server`, `autogen`, `backends`, `ui`, and so on.

- Describe what changed and why. Skip the test plan; say how you verified it if
  it isn't obvious.
- New dependencies need a reason. If you add one, add it to
  [`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) too.

## License

By contributing you agree your work ships under the MIT license in
[`LICENSE.md`](LICENSE.md).
