<#
.SYNOPSIS
  Build the Windows binary + Inno Setup installer LOCALLY and upload the .exe to
  a GitHub release. Replaces the CI runner (private repo Actions minutes are
  metered; local build is free).

.DESCRIPTION
  Mirrors the old `windows-installer` CI job: build UI, build the binary with
  version ldflags, stage the bundle, compile the installer with ISCC, optionally
  sign, then `gh release create`/`upload` the setup .exe. Run from the repo root
  or anywhere — paths resolve against this script.

.PARAMETER Tag
  Release tag (vX.Y.Z). Defaults to the highest existing semver git tag.

.PARAMETER Draft
  Create/keep the GitHub release as a draft (default). Pass -Draft:$false to publish.

.PARAMETER Repo
  owner/name for gh (avoids "no default remote" with multiple remotes).

.PARAMETER SkipUi
  Reuse the already-built UI in internal/server/ui_dist (skip npm).
#>
[CmdletBinding()]
param(
    [string]$Tag,
    # 'true' (draft) or 'false' (publish). String, not bool: -File mode passes
    # args verbatim and can't evaluate $true/$false.
    [ValidateSet('true', 'false')]
    [string]$Draft = 'true',
    [string]$Repo = 'Quartermaster-Labs/quartermaster',
    [switch]$SkipUi
)

$ErrorActionPreference = 'Stop'
$isDraft = ($Draft -eq 'true')
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $root

function Die($m) { Write-Error $m; exit 1 }

# Tag: explicit, else highest vX.Y.Z.
if (-not $Tag) {
    $Tag = (git tag --sort=-v:refname | Where-Object { $_ -match '^v[0-9]+\.[0-9]+\.[0-9]+$' } | Select-Object -First 1)
    if (-not $Tag) { Die "no vX.Y.Z tag found; create one or pass -Tag vX.Y.Z" }
}
if ($Tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { Die "Tag must be vMAJOR.MINOR.PATCH (e.g. v0.5.1)" }

$iscc = (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source
if (-not $iscc) { $iscc = "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe" }
if (-not (Test-Path $iscc)) { Die "ISCC not found. Install Inno Setup 6 (choco install innosetup -y)." }

Write-Host "== building $Tag ==" -ForegroundColor Cyan

# 1. UI -> embedded into the binary.
if (-not $SkipUi) {
    Push-Location ui-svelte
    npm ci
    npm run build
    Pop-Location
}

# 2. Binary with version ldflags.
$commit  = (git rev-parse --short HEAD).Trim()
$date    = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$staging = Join-Path $root 'staging'
if (Test-Path $staging) { Remove-Item -Recurse -Force $staging }
New-Item -ItemType Directory -Force $staging | Out-Null

go build -ldflags "-X main.commit=$commit -X main.version=$Tag -X main.date=$date" `
    -o (Join-Path $staging 'quartermaster-windows-amd64.exe') .

# 3. Stage bundle (same set the installer expects). Config yaml lives under config\.
$stagingConfig = Join-Path $staging 'config'
New-Item -ItemType Directory -Force $stagingConfig | Out-Null
Copy-Item config.example.yaml,quartermaster-generate.example.yaml $stagingConfig
Copy-Item LICENSE.md,README.md $staging
Copy-Item packaging\windows\start.cmd (Join-Path $staging 'start.cmd')
Copy-Item -Recurse packaging (Join-Path $staging 'packaging')
"quartermaster $Tag built $date" | Out-File -Encoding utf8 (Join-Path $staging 'VERSION.txt')

# 4. Optional sign (binary) — gated on the pfx env var, same as CI.
if ($env:SIGN_PFX_BASE64) {
    & (Join-Path $PSScriptRoot 'sign.ps1') (Join-Path $staging 'quartermaster-windows-amd64.exe')
}

# 5. Compile installer (ISCC wants absolute paths).
$outdir = Join-Path $root 'Output'
& $iscc "/DMyAppVersion=$Tag" "/DStagingDir=$staging" "/DOutputDir=$outdir" packaging\windows\installer.iss
if ($LASTEXITCODE -ne 0) { Die "ISCC failed ($LASTEXITCODE)" }

$setup = (Resolve-Path (Join-Path $outdir "quartermaster-setup-$Tag.exe")).Path
if ($env:SIGN_PFX_BASE64) { & (Join-Path $PSScriptRoot 'sign.ps1') $setup }
Write-Host "installer: $setup" -ForegroundColor Green

# 6. Push the tag, create the release if missing, upload the .exe.
git push origin $Tag
$exists = $false
try { gh release view $Tag -R $Repo *> $null; $exists = ($LASTEXITCODE -eq 0) } catch { $exists = $false }
if (-not $exists) {
    $draftArg = if ($isDraft) { '--draft' } else { '' }
    gh release create $Tag -R $Repo --title $Tag --notes "quartermaster $Tag" $draftArg
}
gh release upload $Tag $setup -R $Repo --clobber
if (-not $isDraft) { gh release edit $Tag -R $Repo --draft=false }

Write-Host "Done. $Tag (draft=$isDraft) -> https://github.com/$Repo/releases" -ForegroundColor Green
