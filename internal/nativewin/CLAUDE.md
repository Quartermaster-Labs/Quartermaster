# internal/nativewin — the desktop window

## Purpose

Turns a go-webview2 window into the **chrome-less app frame** the Svelte UI draws its own title bar
into, and exposes the OS dialogs a web page cannot reach. Two programs show a window — the first-run
wizard (`cmd/quartermaster-setup`) and the server's app window — and both go through here, so they
cannot drift into looking like different applications.

**Everything real is `//go:build windows`.** `doc.go` carries no build tag on purpose: without one
file that always compiles, `go build ./...` on Linux fails the package outright with "build
constraints exclude all Go files", breaking the Docker build for a package it never imports.

## Key files

| File | Role |
|---|---|
| `doc.go` | Package doc. Untagged, and that is the point (above). |
| `window_windows.go` | `Options`, `Attach`, `frameless`, `subclass`/`wndProc`, `Drag`, `ToggleMaximize`, `Show`/`Hide`/`Visible`/`PostClose`, `MessageBox`, `winIndex`. |
| `icon_windows.go` | `ApplyIcon` — the window/taskbar/Alt-Tab icon, a different thing from the file icon. Loads `RT_GROUP_ICON` id 1 from its own module, so it works in any binary carrying a `.syso`. |
| `folder_windows.go` | `PickFolder` — IFileDialog + `FOS_PICKFOLDERS` through raw COM vtable dispatch (`comCall`). |
| `external_windows.go` | `OpenExternal` — hands an http(s) URL to the system browser via `rundll32 url.dll,FileProtocolHandler`. |
| `placement_windows.go` | `Placement`, `GetPlacement`, `ApplyPlacement` — remembering where the window was. |

## Important types & functions

- **`Attach(w, Options) uintptr`** — the whole entry point: icon, frameless reshape, and the seven JS
  bindings (`qmDrag`, `qmMinimize`, `qmMaximize`, `qmClose`, `qmPickFolder`, `qmOpenExternal`,
  `qmCaptionColor`).
  Returns the `hwnd`.
  Must be called **on the thread that created `w` and pumps its loop**. It registers the bindings
  itself rather than leaving them to callers because they have to exist before `Navigate` — a caller
  that forgets gets a page whose title bar silently does nothing.
- **`Options.HideOnClose`** — turns the close button into "hide", leaving the webview warm (~150 MB)
  so a tray reopen is instant instead of a cold WebView2 start. The caller then **owns** getting it
  back on screen; a hidden window with no tray icon is a process the user cannot reach. The wizard
  passes the zero value, so its X really closes.
- **`Options.OnClose`** — replaces the page's `qmClose` action. nil terminates the webview.
- **Package-scope state** (`prevWndProc`, `opts`) — a process has exactly one of these windows. The
  wizard shows one and exits; the server's app window is its single main window. A second would need
  this keyed by hwnd, and neither program wants one.

## Gotchas / conventions

- **Bindings must be registered BEFORE `Navigate`.** go-webview2 turns each one into a document-
  creation init script; one added after navigation starts does not exist for the document already
  loading. This is why `Attach` binds rather than the caller.
- **Stripping the caption does not generate a `WM_SIZE`.** The client area grows while the window
  size does not, the embedded browser stays laid out for the old rect, and the class registers no
  background brush — so the uncovered strip shows a stale pale bar. `frameless` sends a bare
  `WM_SIZE`; the library's handler refits from `GetClientRect` and ignores the parameters.
- **`WS_THICKFRAME` survives the strip on purpose.** It is what keeps edge resizing, Aero snap and
  the maximise animation working; a plain `WS_POPUP` loses all three. It is not free — see next.
- **Clearing `WS_CAPTION` leaves the frame's TOP edge behind.** DWM draws a thick-frame window's top
  border at `SM_CYSIZEFRAME + SM_CXPADDEDBORDER` (4+4) while the other three edges get 1px. Measured
  insets around the client area: caption on `top=31 left=1 right=1 bottom=1`, caption stripped
  `top=7 …` — a 7px pale strip that reads as a leftover title bar, and that `DWMWA_BORDER_COLOR`
  cannot touch (it colours the 1px outline, not the frame). `wndProc` answers `WM_NCCALCSIZE` by
  restoring `rgrc[0].Top` after the default handler has inset it, taking the top to 0. **Maximised is
  deliberately left to the default**: a zoomed window's rect overhangs the work area by the frame
  thickness, so overriding there pushes the page off-screen. The top edge stops being a resize handle
  and cannot be given back via `WM_NCHITTEST` — with no non-client area up there the hit test never
  reaches the window; the WebView2 child owns those pixels.
- **The subclass chains, it does not replace.** `GetWindowLongPtrW(GWLP_WNDPROC)` is stored and
  everything not rewritten is forwarded through `CallWindowProcW`, so go-webview2 keeps handling
  `WM_SIZE`, `WM_DESTROY` and the rest. Dropping the chain kills the window.
- **Negative Win32 indices must pass through a value**, not a constant — `winIndex(int32)`.
  `uintptr(-16)` as a constant expression does not compile.
- **Window icon ≠ file icon.** Explorer reads the `.syso`; the taskbar and Alt-Tab read the window
  class or `WM_SETICON`, and go-webview2 registers its class with `IDI_APPLICATION`. `ApplyIcon`
  covers the second; the `.syso` beside each `main` covers the first.
- **`OpenExternal` is a shell-execution path reachable from JavaScript**, so it validates the scheme
  itself: http/https with a non-empty host, nothing else. A `file:` or `ms-settings:` URL reaching
  `FileProtocolHandler` would launch whatever is registered for it. The page-side half of this lives
  in `ui-svelte/src/lib/native.ts`, which decides *which* links leave the window; a `download`
  attribute is deliberately not one of them.
- **Downloads are left to WebView2's default.** `DownloadStarting` is not declared anywhere in
  go-webview2 (unlike `NewWindowRequested`, which is declared but unwired), so intercepting one means
  hand-rolling an `ICoreWebView2_4` QueryInterface and an event-handler vtable in Go. The Chromium
  default — save to the user's Downloads folder, show a progress flyout — is already the right
  behaviour for the playground's image/audio/transcript save buttons.
- **`GetPlacement` reads `rcNormalPosition`, never `GetWindowRect`.** For a maximised window the
  latter returns the maximised rect, so restoring it would grow the window every session. It also
  ORs `IsZoomed` into `Maximized`: the placement is read after hide-to-tray, and `showCmd` reads
  `SW_HIDE` for a hidden window, which would silently lose the maximised state.
- **`ApplyPlacement` validates position and size separately.** A saved position on a monitor that has
  since been unplugged (`MonitorFromRect` with `MONITOR_DEFAULTTONULL` returning 0) applies the size
  only, with `SWP_NOMOVE`, so the window keeps its centred position instead of opening off-screen. A
  minimised window is never restored minimised — there would be nothing to click.
- **The two white corner dots are the window FRAME, and the page has to dye it.** Windows 11 rounds
  the corners and DWM draws the frame beneath that mask; with the top inset at 0 the client covers
  the whole top edge except where the corner arc meets it, leaving a 1-2px stub of frame in each top
  corner. Established by screenshotting the composited pixels, not by reasoning: `DWMWA_BORDER_COLOR`
  does not reach them (it is already `COLOR_NONE`, and dyeing it red draws a red arc while the dots
  stay), giving the top edge 1px of non-client back turns each dot into a **full white line** across
  the top, and `DWMWCP_DONOTROUND` removes them at the price of square corners. `SetCaptionColor`
  paints the frame the title bar's own colour instead, so the stubs vanish and the rounded corners
  stay. The colour must come from the page (`TitleBar.svelte`) because it depends on the theme, which
  lives in the browser's storage.
- **The process is DPI-unaware, deliberately for now.** No manifest, no
  `SetProcessDpiAwarenessContext`, and none in go-webview2 either, so Windows bitmap-stretches the
  window on a scaled display: right size, blurry text. Making it aware also turns `Options`/`SetSize`
  dimensions into physical pixels and DPI-scales the `WM_NCCALCSIZE` frame insets, so it is a change
  with three moving parts, not a one-liner — written up in `TODO.md` under Feature 12.
- **`go vet -unsafeptr` complains** about the `uintptr → *NCCALCSIZE_PARAMS` and COM vtable
  conversions. Both are the documented Win32 pattern — the pointer comes from the OS, not from Go's
  heap. `unsafeptr` is not in `go test`'s default vet subset, so `make test-dev` stays green.

## Connections

Depends on: `github.com/jchv/go-webview2` only.

Called by: `cmd/quartermaster-setup` (the wizard window) and the server's app window.
