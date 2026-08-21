# Windows installer (local build)

`make release` builds a per-user Windows installer
(`quartermaster-setup-vX.Y.Z.exe`) via Inno Setup **locally** and uploads
it to a GitHub Release (draft). `make release-public` publishes it. Needs go,
npm, `gh` (authed), and Inno Setup 6 (`choco install innosetup -y`). Bare
`make release` uses the latest `vX.Y.Z` tag; `make release VERSION=v0.5.1`
creates that tag first. See `windows/installer.iss` + `windows/build-release.ps1`.

Build runs locally because the repo is private (CI Actions minutes are metered).

What users download is `cmd/quartermaster-setup`: a native-window wizard
(WebView2, rendering the project's own Svelte UI) that asks where to install,
where the models are, and which backends to fetch — then drives the Inno
installer with `/VERYSILENT` for the Start Menu entry, the uninstall record and
the in-place upgrade. The Inno `.exe` is embedded inside it and is not published
on its own.

Backends are downloaded by `internal/backends`, the same code the Settings page
uses, so a first install and a later one behave identically: GPU-detected
variant, versioned side-by-side install, staged download with rollback, and a
PE-imports preflight that names a missing GPU runtime rather than failing at
first launch. `windows/fetch-backend.ps1` did this job before and is no longer
run by anything — it is kept only as a manual escape hatch. Installs per-user
(no UAC) under `%LocalAppData%\Programs`.

**Code signing** is off until you add repo secrets `SIGN_PFX_BASE64` (base64 of
a `.pfx`) and `SIGN_PFX_PASSWORD`; CI then signs the binary + installer
(`windows/sign.ps1`). For a free OSS cert, apply to SignPath Foundation and swap
the sign step for their action. Unsigned builds trip SmartScreen until then.

# Running quartermaster as a service

The proxy is a plain console binary. Linux uses a native systemd unit; Windows
needs a tiny wrapper (NSSM or WinSW) because the binary has no SCM handler.

All examples use `-generate`, which builds the config from the local GGUF tree on
startup (hash-gated — an unchanged models folder skips the scan). Drop `-generate`
to load a static `-config` instead.

## Linux (systemd)

```sh
# 1. Build + place the binary
make linux-amd64                            # or: go build -o quartermaster .
sudo install -D -m755 build/quartermaster-linux-amd64 /opt/quartermaster/quartermaster

# 2. Config dir + control file
sudo mkdir -p /etc/quartermaster
sudo cp quartermaster-generate.yaml /etc/quartermaster/

# 3. Dedicated user
sudo useradd --system --no-create-home llama || true

# 4. Let that user own its state dir (quartermaster writes bin/, .cache/ and
#    playground-data/ beside its own binary)
sudo chown -R llama:llama /opt/quartermaster

# 5. Install + enable the unit (edit paths/ports/models root first)
sudo cp packaging/systemd/quartermaster.service /etc/systemd/system/
sudoedit /etc/systemd/system/quartermaster.service
sudo systemctl daemon-reload
sudo systemctl enable --now quartermaster

# Logs / status
systemctl status quartermaster
journalctl -u quartermaster -f
```

Set the models root in `quartermaster-generate.yaml` (`settings.modelsRoot`) or
pass `-models-dir` in `ExecStart`. The unit's `TimeoutStartSec=180` covers the
first-start GGUF scan.

## Windows

Pick one wrapper.

### Option A — NSSM (script provided)

```powershell
# Elevated PowerShell. nssm must be on PATH (https://nssm.cc).
.\packaging\windows\install-service.ps1 `
  -ExePath  C:\llama-qm\quartermaster-windows-amd64.exe `
  -Config   C:\llama-qm\config.yaml `
  -Generate C:\llama-qm\quartermaster-generate.yaml `
  -Listen   0.0.0.0:1250

# Remove
.\packaging\windows\install-service.ps1 -Uninstall
```

### Option B — WinSW (no PATH dependency)

1. Download `WinSW-x64.exe` from https://github.com/winsw/winsw/releases.
2. Rename it `quartermaster-service.exe`; put it next to the proxy binary
   and `packaging/windows/quartermaster-service.xml`.
3. Edit the xml's `<arguments>` (ports/paths), then from an elevated prompt:

```powershell
.\quartermaster-service.exe install
.\quartermaster-service.exe start
# Uninstall: .\quartermaster-service.exe stop ; .\quartermaster-service.exe uninstall
```

Both wrappers set the service to auto-start and capture stdout/stderr to log
files beside the binary.
