<#
.SYNOPSIS
  Build every release artifact and upload them to a GitHub release. Runs either
  from a developer's machine (make release) or from .github/workflows/release.yml,
  which installs the toolchain and calls this same script rather than restating
  the steps in YAML: two builders would drift, and the one that ships would be
  the one nobody tested. It predates that workflow, from when metered Actions
  minutes on a private repo made a local build the cheap option.

.DESCRIPTION
  Produces two kinds of artifact, for two different audiences:

    1. Bare per-platform binaries + SHA256SUMS. These are what the IN-APP
       UPDATER downloads (see internal/update): the app is one self-contained
       binary, so a new version is just a new file, and updating is a download
       + rename. The names here MUST match internal/update.assetName() exactly
       — an update that cannot find its asset silently never offers itself.

    2. The setup programs (quartermaster-setup-<os>-<arch>-vX.Y.Z), one per
       platform, FIRST INSTALL only. Every published name carries both the
       platform and the version: a file that has been in a Downloads folder for
       six months has to say what it is and how old it is. This is a native-window wizard (cmd/quartermaster-setup)
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
  new user will first-install from, and for testing the updater path. Implies
  -PublishBinaries, since otherwise there would be nothing to upload.

.PARAMETER SkipBinaries
  Do NOT upload the four bare server binaries. Almost never what you want, and
  the damage is silent: the in-app updater (internal/update) looks for its
  platform's binary by exact name and treats a miss as "no update available", so
  every existing install stops updating with nothing logged anywhere. README's
  "Linux and macOS binaries" section also sends people to the release page for
  exactly these files, so a release without them documents a download that is
  not there.

  The reason to want them gone is that a release page is a download page, and
  the server binary sitting next to the setup program is the file a visitor is
  most likely to take by mistake. That is handled in the notes instead: they
  name the setup programs first and put everything else behind a collapsed
  "who is this for" section, which renders above the asset list.

.PARAMETER PublishBinaries
  Kept for callers that pass it. Binaries are published unless -SkipBinaries.
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
    [switch]$SkipInstaller,
    [switch]$PublishBinaries,
    [switch]$SkipBinaries
)

$ErrorActionPreference = 'Stop'
$isDraft = ($Draft -eq 'true')
# Without the wizards there is nothing else to publish, so this combination
# would create an empty release rather than a binaries-only one.
if ($SkipInstaller) { $PublishBinaries = $true }
# Default ON. The switch above stays for callers that pass it explicitly; this
# is what makes "make release" publish an updatable release without remembering
# a flag, and -SkipBinaries the deliberate way to opt out.
if (-not $SkipBinaries) { $PublishBinaries = $true }
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
# PFX_B64 / PFX_PASS, the names sign.ps1 itself reads. They used to be gated on
# SIGN_PFX_BASE64 here, which matched nothing: setting it signed nothing (the
# script no-ops on an unset PFX_B64) and setting PFX_B64 alone never reached the
# script at all, so the whole path was dead in both directions.
if ($env:PFX_B64) {
    & (Join-Path $PSScriptRoot 'sign.ps1') $winExe
}

# 4. The upload set. SHA256SUMS is written in step 5c, once every artifact in it
# is final: the Windows wizard does not exist yet (it embeds an installer ISCC
# has not built), and signing changes bytes, so hashing anything now would
# publish a digest for a file that is about to change.
$sumsPath = Join-Path $staging 'SHA256SUMS'
$uploads = @()
if ($PublishBinaries) { $uploads += $binaries }
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
    if ($env:PFX_B64) { & (Join-Path $PSScriptRoot 'sign.ps1') $inno }

    # The embed target is a committed zero-byte placeholder so the package still
    # compiles in a dev tree (place_windows.go checks the size and falls back to
    # copying files). It is restored below so a release build does not leave a
    # multi-megabyte blob staged in git.
    $embed = Join-Path $root 'cmd\quartermaster-setup\inno\setup.exe'
    Copy-Item $inno $embed -Force
    try {
        $setupName = "quartermaster-setup-windows-amd64-$Tag.exe"
        $setup = Join-Path $outdir $setupName
        Write-Host "  building $setupName" -ForegroundColor DarkGray
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
    if ($env:PFX_B64) { & (Join-Path $PSScriptRoot 'sign.ps1') $setup }
    Write-Host "installer: $setup" -ForegroundColor Green
    $uploads += $setup
    $setups += $setup
}

# 5b. The unix wizards, each carrying the server binary for its own platform.
#
# The Windows wizard embeds an Inno package; these embed the binary directly,
# because a tarball unpacked into a directory is the unix install convention and
# there is no package worth embedding instead. The payload matters more than it
# used to: the release publishes setup programs only, so a wizard with nothing
# inside it would have no binary to install and no asset to download either.
#
# The copy-over-the-placeholder dance is the same one the Inno embed uses above,
# and for the same reason -- //go:embed reads a path, not a variable -- except
# that it runs once per target, since each wizard has to carry its own arch.
#
# Names carry the platform AND the version, matching the Windows one. Nothing
# resolves a setup program by a fixed name -- the site reads the release's asset
# list at build time (ui-svelte/site/build.mjs) and the updater only ever looks
# for the bare server binaries -- so the naming is free to serve the person
# looking at a Downloads folder instead.
if (-not $SkipInstaller) {
    $nixSetups = @(
        @{ os = 'linux'; arch = 'amd64'; name = "quartermaster-setup-linux-amd64-$Tag" }
        @{ os = 'linux'; arch = 'arm64'; name = "quartermaster-setup-linux-arm64-$Tag" }
        @{ os = 'darwin'; arch = 'arm64'; name = "quartermaster-setup-darwin-arm64-$Tag" }
    )
    $payload = Join-Path $root 'cmd\quartermaster-setup\payload\server'
    try {
        foreach ($t in $nixSetups) {
            $out = Join-Path $staging $t.name
            $srv = Join-Path $staging "quartermaster-$($t.os)-$($t.arch)"
            if (-not (Test-Path $srv)) { Die "no server binary at $srv to embed in $($t.name)" }
            Copy-Item $srv $payload -Force
            Write-Host "  building $($t.name)" -ForegroundColor DarkGray
            $env:GOOS = $t.os
            $env:GOARCH = $t.arch
            go build -ldflags $ldBase -o $out .\cmd\quartermaster-setup
            if ($LASTEXITCODE -ne 0) { Die "go build failed for the $($t.os)/$($t.arch) setup program" }
            $uploads += $out
            $setups += $out
        }
    }
    finally {
        Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
        # Restore the committed placeholder, so a release build never leaves a
        # multi-megabyte blob staged in git. Same guarantee as the Inno embed.
        New-Item -ItemType File -Path $payload -Force | Out-Null
    }
}

# 5c. SHA256SUMS, in sha256sum's own format ("<hex>  <name>"), over exactly the
# files being uploaded and no others: a line for an asset that is not on the
# release is worse than no line, because "verify what you downloaded" then fails
# for a file nobody can download. Written last, after every signature.
#
# The updater prefers the GitHub API's per-asset `digest` field and falls back to
# this file, which covers older API responses and lets a user check a manual
# download by hand.
$sumLines = foreach ($f in $uploads) {
    $h = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLower()
    "$h  $(Split-Path -Leaf $f)"
}
# ASCII + LF: this file is parsed on linux too, and a BOM would corrupt the first
# entry's hash.
[System.IO.File]::WriteAllText($sumsPath, (($sumLines -join "`n") + "`n"), [System.Text.Encoding]::ASCII)
Write-Host "SHA256SUMS:" -ForegroundColor DarkGray
$sumLines | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkGray }
$uploads += $sumsPath

# 6. Push the tag, create the release if missing, upload everything.
#
# The body says which of the eight assets to click. Every one of them has to be
# on the release -- the bare binaries are what the in-app updater renames over
# the running exe, what a lone unix setup file downloads for itself
# (cmd/quartermaster-setup/place_other.go), and the headless install README
# documents -- but only three are the download a first-time visitor wants. The
# body renders directly above the asset list, so it is the only place that can
# say so before someone scrolls into the files and guesses: setup programs by
# name first, everything else behind a collapsed section that says who it is
# for. Hiding an asset is not an option GitHub offers, and renaming the
# binaries would cut installed copies off from updates.
$notes = @"
Download the setup program for your platform:

- **Windows** -- ``quartermaster-setup-windows-amd64-$Tag.exe``
- **Linux** -- ``quartermaster-setup-linux-amd64-$Tag`` (or ``-linux-arm64-$Tag``)
- **macOS, Apple silicon** -- ``quartermaster-setup-darwin-arm64-$Tag``

The unix ones need ``chmod +x`` first. There is no signed macOS build yet, so
clear the quarantine flag before the first launch:

``xattr -d com.apple.quarantine ./quartermaster-setup-darwin-arm64-$Tag``

``SHA256SUMS`` covers every file here.

<details>
<summary><b>The other files, and who they are for</b></summary>

``quartermaster-windows-amd64.exe`` and ``quartermaster-linux-amd64`` /
``-linux-arm64`` / ``-darwin-arm64`` are the bare server binary, not an
installer. Nothing about them creates a Start Menu entry, a desktop shortcut or
an uninstall record. They are published for two reasons:

- the in-app updater downloads one to replace the running binary, so a release
  without them silently stops every existing install from updating;
- a headless Linux or macOS box can run one directly: ``chmod +x``, point it at
  a models folder, and install backends from Settings.

**Installing for the first time? Take a ``quartermaster-setup-...`` file above.**
</details>
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
