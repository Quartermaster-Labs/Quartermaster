<#
.SYNOPSIS
  Build every release artifact LOCALLY and upload them to a GitHub release.
  Replaces the CI runner (private repo Actions minutes are metered; local build
  is free).

.DESCRIPTION
  Produces two kinds of artifact, for two different audiences:

    1. Bare per-platform binaries + SHA256SUMS. These are what the IN-APP
       UPDATER downloads (see internal/update): the app is one self-contained
       binary, so a new version is just a new file, and updating is a download
       + rename. The names here MUST match internal/update.assetName() exactly
       — an update that cannot find its asset silently never offers itself.

    2. The setup program (quartermaster-setup-vX.Y.Z.exe), Windows FIRST
       INSTALL only. This is a native-window wizard (cmd/quartermaster-setup)
       with the Inno installer EMBEDDED inside it: the wizard asks the
       questions and downloads the backends, and drives Inno silently for the
       Start Menu entry and the uninstall record. Existing installs never run
       it again; they self-update via (1).

  Everything is cross-compiled from this one box: CGO is off across the whole
  project, so linux/amd64, linux/arm64 and darwin/arm64 all build here with no
  toolchain beyond Go itself.

.PARAMETER Tag
  Release tag (vX.Y.Z). Required, and it must already point at HEAD (or not
  exist yet, in which case it is created on HEAD). Never inferred: see below.

.PARAMETER Draft
  Create/keep the GitHub release as a draft (default). Pass -Draft false to publish.

.PARAMETER Repo
  owner/name for gh (avoids "no default remote" with multiple remotes).

.PARAMETER SkipUi
  Reuse the already-built UI in internal/server/ui_dist (skip npm).

.PARAMETER SkipInstaller
  Build and upload only the bare binaries. Useful for a patch release that no
  new user will first-install from, and for testing the updater path.
#>
[CmdletBinding()]
param(
    [string]$Tag,
    # 'true' (draft) or 'false' (publish). String, not bool: -File mode passes
    # args verbatim and can't evaluate $true/$false.
    [ValidateSet('true', 'false')]
    [string]$Draft = 'true',
    [string]$Repo = 'Quartermaster-Labs/Quartermaster',
    [switch]$SkipUi,
    [switch]$SkipInstaller
)

$ErrorActionPreference = 'Stop'
$isDraft = ($Draft -eq 'true')
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $root

function Die($m) { Write-Error $m; exit 1 }

# The tag is never inferred. Defaulting to the highest existing tag built
# whatever HEAD happened to be and published it under a version that tag may
# not point at; a release has to name the version it is releasing.
if (-not $Tag) { Die "-Tag vX.Y.Z is required (make release VERSION=vX.Y.Z)" }
if ($Tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { Die "Tag must be vMAJOR.MINOR.PATCH (e.g. v0.5.1)" }

# $Tag is stamped into every binary (-X main.version) and the updater hands that
# string to users as the version they are running, so the build has to come from
# exactly the source the tag names. Two ways it would not: uncommitted edits, or
# a tag that already exists somewhere other than HEAD. Both are refused here
# rather than discovered later from a binary that lies about its provenance.
$dirty = (git status --porcelain)
if ($dirty) {
    $dirty | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }
    Die "working tree is dirty; commit or stash before releasing"
}
$head = (git rev-parse HEAD).Trim()
git rev-parse -q --verify "refs/tags/$Tag" *> $null
if ($LASTEXITCODE -eq 0) {
    $at = (git rev-list -n 1 $Tag).Trim()
    if ($at -ne $head) { Die "$Tag points at $at, not HEAD ($head); check out the tag, or move it" }
} else {
    Write-Host "  $Tag is new; it will be created on $($head.Substring(0, 7))" -ForegroundColor DarkGray
}

# origin's copy of the tag is the one the RELEASE names, and it is checked here
# rather than at the push in step 6, because a tag push that origin rejects is
# not a reason to stop: the assets still go up, and the release ends up naming a
# commit that is not the one its binaries were built from. Refusing before the
# build also saves the twenty minutes it would otherwise waste.
$remoteRef = (git ls-remote --tags origin "refs/tags/$Tag" | Select-Object -First 1)
if ($LASTEXITCODE -ne 0) { Die "git ls-remote failed; cannot tell where origin has $Tag" }
if ($remoteRef) {
    $remoteAt = ($remoteRef -split '\s+')[0]
    if ($remoteAt -ne $head) {
        Die ("origin already has $Tag at $remoteAt, not HEAD ($head)." + [Environment]::NewLine +
             "Move it (nothing consumes a tag on an unpublished release):" + [Environment]::NewLine +
             "  git push origin :refs/tags/$Tag" + [Environment]::NewLine +
             "  git push origin $Tag")
    }
}

$iscc = $null
if (-not $SkipInstaller) {
    $iscc = (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source
    if (-not $iscc) { $iscc = "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe" }
    if (-not (Test-Path $iscc)) { Die "ISCC not found. Install Inno Setup 6 (choco install innosetup -y), or pass -SkipInstaller." }
}

Write-Host "== building $Tag ==" -ForegroundColor Cyan

# 1. UI -> embedded into every binary, so it is built once, before any of them.
if (-not $SkipUi) {
    Push-Location ui-svelte
    npm ci
    if ($LASTEXITCODE -ne 0) { Pop-Location; Die "npm ci failed" }
    npm run build
    if ($LASTEXITCODE -ne 0) { Pop-Location; Die "npm run build failed" }
    # Second, much smaller bundle: the first-run wizard's own UI, embedded into
    # cmd/quartermaster-setup rather than into the server binary.
    npm run build:setup
    if ($LASTEXITCODE -ne 0) { Pop-Location; Die "npm run build:setup failed" }
    Pop-Location
}

# 2. Binaries. One staging dir for everything: the Windows binary the installer
# bundles is the SAME FILE that is published for the updater, so a fresh install
# and a self-updated one are byte-identical builds.
$commit = (git rev-parse --short HEAD).Trim()
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$staging = Join-Path $root 'staging'
if (Test-Path $staging) { Remove-Item -Recurse -Force $staging }
New-Item -ItemType Directory -Force $staging | Out-Null

# CGO off everywhere: it is what makes cross-compiling all four targets from one
# Windows box work at all.
$env:CGO_ENABLED = '0'
$ldBase = "-X main.commit=$commit -X main.version=$Tag -X main.date=$date"

# Keep this table in sync with internal/update.assetName(). A name that drifts
# means that platform silently stops seeing updates.
$targets = @(
    # -H=windowsgui: no console window when launched from Explorer, the tray, or
    # a relaunch after a self-update. Without it every update flashes a console.
    @{ os = 'windows'; arch = 'amd64'; name = 'quartermaster-windows-amd64.exe'; ld = "-H=windowsgui $ldBase" }
    @{ os = 'linux'; arch = 'amd64'; name = 'quartermaster-linux-amd64'; ld = $ldBase }
    @{ os = 'linux'; arch = 'arm64'; name = 'quartermaster-linux-arm64'; ld = $ldBase }
    @{ os = 'darwin'; arch = 'arm64'; name = 'quartermaster-darwin-arm64'; ld = $ldBase }
)

$binaries = @()
foreach ($t in $targets) {
    $out = Join-Path $staging $t.name
    Write-Host "  building $($t.name)" -ForegroundColor DarkGray
    $env:GOOS = $t.os
    $env:GOARCH = $t.arch
    go build -ldflags $t.ld -o $out .\cmd\quartermaster
    if ($LASTEXITCODE -ne 0) { Die "go build failed for $($t.os)/$($t.arch)" }
    $binaries += $out
}
Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue

$winExe = Join-Path $staging 'quartermaster-windows-amd64.exe'

# 3. Optional sign (Windows binary) — gated on the pfx env var, same as CI.
# Signed BEFORE hashing, or the published digest would not match the shipped file.
if ($env:SIGN_PFX_BASE64) {
    & (Join-Path $PSScriptRoot 'sign.ps1') $winExe
}

# 4. SHA256SUMS, in sha256sum's own format ("<hex>  <name>").
#
# The updater prefers the GitHub API's per-asset `digest` field and falls back to
# this file, so it covers older API responses and lets a user verify a manual
# download by hand. Written LAST among the binaries, and only over files that are
# final (signing already happened above).
$sumsPath = Join-Path $staging 'SHA256SUMS'
$sumLines = foreach ($b in $binaries) {
    $h = (Get-FileHash -Algorithm SHA256 $b).Hash.ToLower()
    "$h  $(Split-Path -Leaf $b)"
}
# ASCII + LF: this file is parsed on linux too, and a BOM would corrupt the first
# entry's hash.
[System.IO.File]::WriteAllText($sumsPath, (($sumLines -join "`n") + "`n"), [System.Text.Encoding]::ASCII)
Write-Host "SHA256SUMS:" -ForegroundColor DarkGray
$sumLines | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }

$uploads = @($binaries) + @($sumsPath)
# Filled in below and hashed into SHA256SUMS once every one of them is final.
# The wizards cannot be hashed with the server binaries above because the
# Windows one does not exist yet: it embeds an installer that ISCC has not built.
$setups = @()

# 5. Installer bundle + ISCC. Only the first-install path needs this; the extra
# files staged here (configs, launcher, packaging tree) are what the wizard lays
# down, and are deliberately NOT part of an update.
if (-not $SkipInstaller) {
    $stagingConfig = Join-Path $staging 'config'
    New-Item -ItemType Directory -Force $stagingConfig | Out-Null
    Copy-Item config.example.yaml, quartermaster-generate.example.yaml $stagingConfig
    Copy-Item LICENSE.md, README.md $staging
    # The installed binary is Quartermaster.exe, but the RELEASE ASSET keeps the
    # old name: internal/update.assetName() matches it exactly, so renaming the
    # upload would cut every existing install off from updates. Staging holds
    # both; installer.iss excludes the asset-named copy from {app}, and the
    # updater swaps by path rather than by name, so the two are free to differ.
    Copy-Item $winExe (Join-Path $staging 'Quartermaster.exe')
    Copy-Item -Recurse packaging (Join-Path $staging 'packaging')
    "quartermaster $Tag built $date" | Out-File -Encoding utf8 (Join-Path $staging 'VERSION.txt')

    $outdir = Join-Path $root 'Output'
    & $iscc "/DMyAppVersion=$Tag" "/DStagingDir=$staging" "/DOutputDir=$outdir" packaging\windows\installer.iss
    if ($LASTEXITCODE -ne 0) { Die "ISCC failed ($LASTEXITCODE)" }

    # The Inno output is not shipped on its own any more — it becomes the
    # payload of the setup program. Signed BEFORE it is embedded: signing it
    # afterwards is impossible, and an unsigned inner installer would trip
    # SmartScreen the moment the wizard extracted and ran it.
    $inno = (Resolve-Path (Join-Path $outdir "quartermaster-inno-$Tag.exe")).Path
    if ($env:SIGN_PFX_BASE64) { & (Join-Path $PSScriptRoot 'sign.ps1') $inno }

    # The embed target is a committed zero-byte placeholder so the package still
    # compiles in a dev tree (place_windows.go checks the size and falls back to
    # copying files). It is restored below so a release build does not leave a
    # multi-megabyte blob staged in git.
    $embed = Join-Path $root 'cmd\quartermaster-setup\inno\setup.exe'
    Copy-Item $inno $embed -Force
    try {
        $setup = Join-Path $outdir "quartermaster-setup-$Tag.exe"
        Write-Host "  building quartermaster-setup-$Tag.exe" -ForegroundColor DarkGray
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        # -H=windowsgui for the same reason as the server binary, and more so:
        # this one is double-clicked from Explorer, and a console flashing up
        # behind an installer window is exactly the tell we are avoiding.
        go build -ldflags "-H=windowsgui $ldBase" -o $setup .\cmd\quartermaster-setup
        if ($LASTEXITCODE -ne 0) { Die "go build failed for the setup program" }
    }
    finally {
        Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
        New-Item -ItemType File -Path $embed -Force | Out-Null
    }

    $setup = (Resolve-Path $setup).Path
    if ($env:SIGN_PFX_BASE64) { & (Join-Path $PSScriptRoot 'sign.ps1') $setup }
    Write-Host "installer: $setup" -ForegroundColor Green
    $uploads += $setup
    $setups += $setup
}

# 5b. The unix wizards. Same program, no embedded payload: place() there copies
# the binary sitting beside it, and downloads the verified release asset when a
# lone setup file has nothing to copy. That is why these ship even though there
# is no Inno package behind them, and why they are built here rather than in the
# $targets loop, which produces server binaries only.
#
# Names are unversioned, unlike the Windows setup: the site links at
# /releases/latest, and a stable name is one a script or a README can hard-code.
if (-not $SkipInstaller) {
    $nixSetups = @(
        @{ os = 'linux'; arch = 'amd64'; name = 'quartermaster-setup-linux-amd64' }
        @{ os = 'linux'; arch = 'arm64'; name = 'quartermaster-setup-linux-arm64' }
        @{ os = 'darwin'; arch = 'arm64'; name = 'quartermaster-setup-darwin-arm64' }
    )
    foreach ($t in $nixSetups) {
        $out = Join-Path $staging $t.name
        Write-Host "  building $($t.name)" -ForegroundColor DarkGray
        $env:GOOS = $t.os
        $env:GOARCH = $t.arch
        go build -ldflags $ldBase -o $out .\cmd\quartermaster-setup
        if ($LASTEXITCODE -ne 0) { Die "go build failed for the $($t.os)/$($t.arch) setup program" }
        $uploads += $out
        $setups += $out
    }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

# The wizards are appended to SHA256SUMS rather than left out of it, so "verify
# what you downloaded" is one instruction covering every asset on the page. The
# updater reads this file by exact asset name, so extra lines are invisible to
# it. Appended AFTER signing, for the same reason the binaries were hashed after
# it: a signature changes the bytes.
if ($setups.Count -gt 0) {
    $extra = foreach ($f in $setups) {
        $h = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLower()
        "$h  $(Split-Path -Leaf $f)"
    }
    [System.IO.File]::AppendAllText($sumsPath, (($extra -join "`n") + "`n"), [System.Text.Encoding]::ASCII)
    $extra | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }
}

# 6. Push the tag, create the release if missing, upload everything.
#
# The body says which of the eight assets to click. Every one of them has to be
# on the release -- the bare binaries are what the in-app updater renames over
# the running exe, and what a lone unix setup file downloads for itself
# (cmd/quartermaster-setup/place_other.go) -- but only three are a download a
# person makes, and a release page listing them all with no explanation invites
# someone to grab the server binary and wonder why nothing installs.
$notes = @"
Download the setup program for your platform:

- **Windows** -- ``quartermaster-setup-$Tag.exe``
- **Linux** -- ``quartermaster-setup-linux-amd64`` (or ``-linux-arm64``)
- **macOS, Apple silicon** -- ``quartermaster-setup-darwin-arm64``

There is no signed macOS build yet, so clear the quarantine flag before the
first launch:

``xattr -d com.apple.quarantine ./quartermaster-setup-darwin-arm64``

The bare ``quartermaster-*`` binaries are not a download. They are the payload
the in-app updater fetches, and what the unix setup program pulls when it is run
on its own. ``SHA256SUMS`` covers every file here.
"@

git push origin $Tag
# Unchecked, this is how a release gets assets from one commit and a tag from
# another: git reports the rejection on stderr and returns non-zero, and the
# upload below happily continued. The pre-flight above should have caught the
# common cause already; anything reaching here is a surprise worth stopping for.
if ($LASTEXITCODE -ne 0) { Die "git push origin $Tag failed; not uploading assets under a tag origin does not have" }
$exists = $false
try { gh release view $Tag -R $Repo *> $null; $exists = ($LASTEXITCODE -eq 0) } catch { $exists = $false }
if (-not $exists) {
    # Build the arg list rather than interpolating a flag variable: an empty
    # string is still passed as an argument, and `gh release create ""` fails.
    $createArgs = @('release', 'create', $Tag, '-R', $Repo, '--title', $Tag, '--notes', $notes)
    if ($isDraft) { $createArgs += '--draft' }
    gh @createArgs
    if ($LASTEXITCODE -ne 0) { Die "gh release create failed" }
} else {
    # A re-run is usually a fixed build of the same tag, so the body is rewritten
    # rather than left at whatever the first attempt wrote.
    gh release edit $Tag -R $Repo --notes $notes
    if ($LASTEXITCODE -ne 0) { Die "gh release edit (notes) failed" }
}
gh release upload $Tag @uploads -R $Repo --clobber
if ($LASTEXITCODE -ne 0) { Die "gh release upload failed" }

# Publishing LAST, after every asset is up: the updater polls /releases/latest,
# and a release that goes public with only some of its binaries attached would
# hand a partial set to whoever checks in that window.
if (-not $isDraft) {
    gh release edit $Tag -R $Repo --draft=false
    if ($LASTEXITCODE -ne 0) { Die "gh release edit failed" }
}

Write-Host "Done. $Tag (draft=$isDraft) -> https://github.com/$Repo/releases" -ForegroundColor Green
