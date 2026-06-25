# Windows installer (local build)

`make release` builds a per-user Windows installer
(`llama-quartermaster-setup-vX.Y.Z.exe`) via Inno Setup **locally** and uploads
it to a GitHub Release (draft). `make release-public` publishes it. Needs go,
npm, `gh` (authed), and Inno Setup 6 (`choco install innosetup -y`). Bare
`make release` uses the latest `vX.Y.Z` tag; `make release VERSION=v0.5.1`
creates that tag first. See `windows/installer.iss` + `windows/build-release.ps1`.

Build runs locally because the repo is private (CI Actions minutes are metered).

The wizard optionally downloads `llama-server` (llama.cpp) and `sd-server`
(stable-diffusion.cpp) for a chosen backend (Vulkan/CUDA/CPU) into `bin\` and
points `quartermaster-generate.yaml` at them — see `windows/fetch-backend.ps1`
(self-test: `fetch-backend.ps1 -Test`). It can also add a logon-autostart
shortcut. Installs per-user (no UAC) under `%LocalAppData%\Programs`.

**Code signing** is off until you add repo secrets `SIGN_PFX_BASE64` (base64 of
a `.pfx`) and `SIGN_PFX_PASSWORD`; CI then signs the binary + installer
(`windows/sign.ps1`). For a free OSS cert, apply to SignPath Foundation and swap
the sign step for their action. Unsigned builds trip SmartScreen until then.

# Running llama-quartermaster as a service

The proxy is a plain console binary. Linux uses a native systemd unit; Windows
needs a tiny wrapper (NSSM or WinSW) because the binary has no SCM handler.

All examples use `-generate`, which builds the config from the local GGUF tree on
startup (hash-gated — an unchanged models folder skips the scan). Drop `-generate`
to load a static `-config` instead.

## Linux (systemd)

```sh
# 1. Build + place the binary
make build                                  # or: go build -o llama-quartermaster .
sudo install -D -m755 build/llama-quartermaster-linux-amd64 /opt/llama-quartermaster/llama-quartermaster

# 2. Config dir + control file
sudo mkdir -p /etc/llama-quartermaster
sudo cp quartermaster-generate.yaml /etc/llama-quartermaster/

# 3. Dedicated user
sudo useradd --system --no-create-home llama || true

# 4. Install + enable the unit (edit paths/ports/models root first)
sudo cp packaging/systemd/llama-quartermaster.service /etc/systemd/system/
sudoedit /etc/systemd/system/llama-quartermaster.service
sudo systemctl daemon-reload
sudo systemctl enable --now llama-quartermaster

# Logs / status
systemctl status llama-quartermaster
journalctl -u llama-quartermaster -f
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
  -ExePath  C:\llama-qm\llama-quartermaster-windows-amd64.exe `
  -Config   C:\llama-qm\config.yaml `
  -Generate C:\llama-qm\quartermaster-generate.yaml `
  -Listen   0.0.0.0:1250

# Remove
.\packaging\windows\install-service.ps1 -Uninstall
```

### Option B — WinSW (no PATH dependency)

1. Download `WinSW-x64.exe` from https://github.com/winsw/winsw/releases.
2. Rename it `llama-quartermaster-service.exe`; put it next to the proxy binary
   and `packaging/windows/llama-quartermaster-service.xml`.
3. Edit the xml's `<arguments>` (ports/paths), then from an elevated prompt:

```powershell
.\llama-quartermaster-service.exe install
.\llama-quartermaster-service.exe start
# Uninstall: .\llama-quartermaster-service.exe stop ; .\llama-quartermaster-service.exe uninstall
```

Both wrappers set the service to auto-start and capture stdout/stderr to log
files beside the binary.
