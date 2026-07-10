<#
.SYNOPSIS
  Download prebuilt llama-server / sd-server backends from GitHub releases and
  wire them into the bundle's quartermaster-generate.yaml.

.DESCRIPTION
  Run post-install by the Inno installer (or by hand). For each requested
  component it queries the upstream repo's LATEST release, picks the asset
  matching the chosen backend (vulkan / cuda / cpu), unzips it under
  <AppDir>\bin\<component>, then points serverExe / sdServerExe at the extracted
  exe in quartermaster-generate.yaml.

  Failures for one component warn and continue — a missing sd-server asset must
  not abort a working llama-server install.

  llama-server -> ggml-org/llama.cpp
  sd-server    -> leejet/stable-diffusion.cpp

.PARAMETER Backend
  vulkan | cuda | cpu

.PARAMETER Components
  Comma-separated: llama-server,sd-server (any subset).

.PARAMETER AppDir
  Bundle root (holds the exe + quartermaster-generate.yaml). Defaults to the
  folder two levels up from this script.

.PARAMETER Test
  Run the offline self-check of the asset-matching logic and exit.
#>
[CmdletBinding()]
param(
    [ValidateSet('vulkan', 'cuda', 'cpu')]
    [string]$Backend = 'vulkan',
    # Empty by default so a -ModelsRoot-only call doesn't trigger downloads.
    [string]$Components = '',
    [string]$AppDir,
    # Existing-install mode: when either is given, skip downloading and just point
    # the generate yaml at these exes (Backend/Components are ignored).
    [string]$LlamaExe,
    [string]$SdExe,
    # Set settings.modelsRoot in the generate yaml (independent of server setup).
    [string]$ModelsRoot,
    [switch]$NoPause,
    [switch]$Test
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# Asset-name preference lists per repo+backend. First regex that matches an asset
# in the latest release wins. ponytail: hardcoded patterns — if upstream renames
# release assets, update these lists; that's the one maintenance point.
$ASSET_PATTERNS = @{
    'llama-server' = @{
        repo   = 'ggml-org/llama.cpp'
        exe    = 'llama-server.exe'
        vulkan = @('llama-.*-bin-win-vulkan-x64\.zip$')
        cuda   = @('llama-.*-bin-win-cuda-.*x64\.zip$')
        cpu    = @('llama-.*-bin-win-cpu-x64\.zip$', 'llama-.*-bin-win-avx2-x64\.zip$')
        # CUDA build needs the matching cudart runtime zip extracted alongside.
        extra  = @{ cuda = @('cudart-llama.*-x64\.zip$', 'cudart-.*cuda.*\.zip$') }
    }
    'sd-server'    = @{
        repo   = 'leejet/stable-diffusion.cpp'
        exe    = 'sd-server.exe'
        vulkan = @('.*-bin-win-vulkan-x64\.zip$', '.*vulkan.*\.zip$')
        cuda   = @('.*-bin-win-cuda.*-x64\.zip$', '.*cuda.*\.zip$')
        cpu    = @('.*-bin-win-avx2-x64\.zip$', '.*-bin-win-cpu-x64\.zip$', '.*avx2.*\.zip$')
    }
}

# Pick the first asset whose name matches any pattern, in order.
function Select-Asset {
    param([string[]]$AssetNames, [string[]]$Patterns)
    foreach ($p in $Patterns) {
        $hit = $AssetNames | Where-Object { $_ -match $p } | Select-Object -First 1
        if ($hit) { return $hit }
    }
    return $null
}

if ($Test) {
    # Offline self-check: the matcher must prefer the right backend asset.
    $llama = @(
        'llama-b6543-bin-win-cpu-x64.zip',
        'llama-b6543-bin-win-vulkan-x64.zip',
        'llama-b6543-bin-win-cuda-12.4-x64.zip',
        'cudart-llama-bin-win-cuda-12.4-x64.zip'
    )
    $cfg = $ASSET_PATTERNS['llama-server']
    if ((Select-Asset $llama $cfg.vulkan) -ne 'llama-b6543-bin-win-vulkan-x64.zip') { throw 'vulkan match failed' }
    if ((Select-Asset $llama $cfg.cuda)   -ne 'llama-b6543-bin-win-cuda-12.4-x64.zip') { throw 'cuda match failed' }
    if ((Select-Asset $llama $cfg.cpu)    -ne 'llama-b6543-bin-win-cpu-x64.zip') { throw 'cpu match failed' }
    if ((Select-Asset $llama $cfg.extra.cuda) -ne 'cudart-llama-bin-win-cuda-12.4-x64.zip') { throw 'cudart match failed' }
    if ($null -ne (Select-Asset @('nope.zip') $cfg.vulkan)) { throw 'expected no match' }
    Write-Host 'self-check OK' -ForegroundColor Green
    return
}

if (-not $AppDir) {
    $scriptDir = $PSScriptRoot
    if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition }
    $AppDir = Split-Path -Parent (Split-Path -Parent $scriptDir)
}
$AppDir = (Resolve-Path -LiteralPath $AppDir).Path

# Latest-release asset list for a repo (name -> download URL).
function Get-LatestAssets {
    param([string]$Repo)
    $headers = @{ 'User-Agent' = 'quartermaster-installer'; 'Accept' = 'application/vnd.github+json' }
    if ($env:GITHUB_TOKEN) { $headers['Authorization'] = "Bearer $env:GITHUB_TOKEN" }
    $rel = Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$Repo/releases/latest"
    $map = @{}
    foreach ($a in $rel.assets) { $map[$a.name] = $a.browser_download_url }
    return $map
}

function Get-And-Extract {
    param([string]$Url, [string]$DestDir)
    if (Test-Path -LiteralPath $DestDir) { Remove-Item -Recurse -Force $DestDir }
    New-Item -ItemType Directory -Force $DestDir | Out-Null
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName() + '.zip')
    Write-Host "  downloading $Url"
    Invoke-WebRequest -Uri $Url -OutFile $tmp -UseBasicParsing
    Expand-Archive -LiteralPath $tmp -DestinationPath $DestDir -Force
    Remove-Item -Force $tmp
}

# Find the named exe anywhere under a dir (zips nest in a subfolder).
function Find-Exe {
    param([string]$Root, [string]$Name)
    $f = Get-ChildItem -LiteralPath $Root -Recurse -Filter $Name -File -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($f) { return $f.FullName } else { return $null }
}

# Rewrite a `key: value` line in the generate yaml (yaml wants forward slashes).
function Set-YamlValue {
    param([string]$File, [string]$Key, [string]$Value)
    if (-not (Test-Path -LiteralPath $File)) { return }
    $val = $Value -replace '\\', '/'
    # -Encoding UTF8: the example is UTF-8; the default ANSI read mojibakes its
    # em-dashes/smart-quotes and a round-trip would persist the garbage.
    $lines = Get-Content -LiteralPath $File -Encoding UTF8
    $re = "^(\s*)$([regex]::Escape($Key)):.*$"
    $exists = [bool]($lines | Where-Object { $_ -match $re })
    $out = New-Object System.Collections.Generic.List[string]
    foreach ($l in $lines) {
        if ($l -match $re) { $out.Add("$($Matches[1])${Key}: $val") }
        else {
            $out.Add($l)
            # Insert a missing key right under settings:, not at EOF — EOF lands
            # inside the trailing overrides: block and breaks the YAML.
            if (-not $exists -and $l -match '^settings:\s*$') { $out.Add("  ${Key}: $val"); $exists = $true }
        }
    }
    if (-not $exists) { $out.Add("  ${Key}: $val") }
    # UTF-8 without BOM (PS 5.1 -Encoding utf8 writes a BOM).
    [IO.File]::WriteAllLines($File, $out, (New-Object Text.UTF8Encoding $false))
}

$genYaml = Join-Path (Join-Path $AppDir 'config') 'quartermaster-generate.yaml'

# Models folder (independent of server setup; runs may pass this alone).
if ($ModelsRoot) {
    Set-YamlValue $genYaml 'modelsRoot' $ModelsRoot
    Write-Host "modelsRoot -> $ModelsRoot" -ForegroundColor Green
}

# Existing-install mode: point the yaml at user-supplied exes, no download.
if ($LlamaExe -or $SdExe) {
    if ($LlamaExe) {
        if (Test-Path -LiteralPath $LlamaExe) {
            $p = (Resolve-Path -LiteralPath $LlamaExe).Path
            Set-YamlValue $genYaml 'serverExe' $p; Write-Host "serverExe -> $p" -ForegroundColor Green
        } else { Write-Warning "llama-server not found: $LlamaExe" }
    }
    if ($SdExe) {
        if (Test-Path -LiteralPath $SdExe) {
            $p = (Resolve-Path -LiteralPath $SdExe).Path
            Set-YamlValue $genYaml 'sdServerExe' $p; Write-Host "sdServerExe -> $p" -ForegroundColor Green
        } else { Write-Warning "sd-server not found: $SdExe" }
    }
    Write-Host "Done." -ForegroundColor Green
    if (-not $NoPause -and $Host.Name -eq 'ConsoleHost') { Write-Host "`nPress any key..."; [void][System.Console]::ReadKey($true) }
    return
}

$wanted = $Components.Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ }

foreach ($comp in $wanted) {
    $cfg = $ASSET_PATTERNS[$comp]
    if (-not $cfg) { Write-Warning "unknown component '$comp' - skipping"; continue }
    try {
        Write-Host "== $comp ($Backend) ==" -ForegroundColor Cyan
        $assets = Get-LatestAssets $cfg.repo
        $names = @($assets.Keys)
        $pick = Select-Asset $names $cfg[$Backend]
        if (-not $pick) {
            Write-Warning "${comp}: no '$Backend' asset in latest $($cfg.repo) release. Available: $($names -join ', ')"
            continue
        }
        $dest = Join-Path (Join-Path $AppDir 'bin') $comp
        Get-And-Extract $assets[$pick] $dest

        # CUDA: also drop the cudart runtime next to the exe.
        if ($Backend -eq 'cuda' -and $cfg.extra -and $cfg.extra.cuda) {
            $rt = Select-Asset $names $cfg.extra.cuda
            if ($rt) { Get-And-Extract $assets[$rt] $dest }
        }

        $exe = Find-Exe $dest $cfg.exe
        if (-not $exe) { Write-Warning "${comp}: '$($cfg.exe)' not found after extract"; continue }
        Write-Host "  installed: $exe" -ForegroundColor Green

        if ($comp -eq 'llama-server') { Set-YamlValue $genYaml 'serverExe' $exe }
        elseif ($comp -eq 'sd-server') { Set-YamlValue $genYaml 'sdServerExe' $exe }
    } catch {
        Write-Warning "$comp failed: $($_.Exception.Message)"
    }
}

Write-Host "Done." -ForegroundColor Green
if (-not $NoPause -and $Host.Name -eq 'ConsoleHost') {
    Write-Host "`nPress any key to close..."; [void][System.Console]::ReadKey($true)
}
