<#
.SYNOPSIS
  Install/uninstall llama-quartermaster as a Windows service via NSSM.

.DESCRIPTION
  The proxy binary has no Service Control Manager handler, so it can't be run by
  `sc create` directly (SCM would kill it for "not responding to start"). This
  script wraps it with NSSM (https://nssm.cc), the de-facto tool for turning a
  console exe into a service. For a no-extra-tool option, use the WinSW xml in
  this folder instead (see llama-quartermaster-service.xml).

  Run from an ELEVATED PowerShell prompt.

.PARAMETER ExePath
  Path to the proxy binary (e.g. build\llama-swap-windows-amd64.exe).

.PARAMETER Config
  Output config path (-config). Generated here when -Generate is set.

.PARAMETER Generate
  Autogen control file (-generate). Omit to load a static -config.

.PARAMETER Listen
  Listen address (-listen). Default 0.0.0.0:1250.

.PARAMETER Uninstall
  Remove the service instead of installing.

.EXAMPLE
  .\install-service.ps1 -ExePath C:\llama-qm\llama-swap-windows-amd64.exe `
    -Config C:\llama-qm\config.yaml -Generate C:\llama-qm\quartermaster-generate.yaml

.EXAMPLE
  .\install-service.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string]$ServiceName = 'llama-quartermaster',
    [string]$ExePath,
    [string]$Config,
    [string]$Generate,
    [string]$Listen = '0.0.0.0:1250',
    [switch]$WatchConfig = $true,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'

function Require-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p = New-Object Security.Principal.WindowsPrincipal($id)
    if (-not $p.IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)) {
        throw "Run this script from an elevated (Administrator) PowerShell prompt."
    }
}

function Get-Nssm {
    $cmd = Get-Command nssm -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    throw "nssm not found on PATH. Install it (https://nssm.cc) or use the WinSW xml in this folder."
}

Require-Admin
$nssm = Get-Nssm

if ($Uninstall) {
    Write-Host "Stopping and removing service '$ServiceName'..."
    & $nssm stop $ServiceName confirm 2>$null | Out-Null
    & $nssm remove $ServiceName confirm
    Write-Host "Removed."
    return
}

if (-not $ExePath -or -not (Test-Path -LiteralPath $ExePath)) {
    throw "-ExePath is required and must exist: '$ExePath'"
}
if (-not $Config) { throw "-Config is required (the config path to load / generate to)." }

$ExePath  = (Resolve-Path -LiteralPath $ExePath).Path
$workDir  = Split-Path -Parent $ExePath

# Build argument string.
$argList = @('-config', "`"$Config`"")
if ($Generate) {
    if (-not (Test-Path -LiteralPath $Generate)) { throw "-Generate file not found: '$Generate'" }
    $argList += @('-generate', "`"$Generate`"")
}
$argList += @('-listen', $Listen)
if ($WatchConfig) { $argList += '-watch-config' }
$arguments = $argList -join ' '

Write-Host "Installing service '$ServiceName'"
Write-Host "  exe : $ExePath"
Write-Host "  args: $arguments"

& $nssm install $ServiceName $ExePath
& $nssm set $ServiceName AppParameters $arguments
& $nssm set $ServiceName AppDirectory $workDir
& $nssm set $ServiceName Start SERVICE_AUTO_START
& $nssm set $ServiceName AppStdout (Join-Path $workDir 'llama-quartermaster.out.log')
& $nssm set $ServiceName AppStderr (Join-Path $workDir 'llama-quartermaster.err.log')
# Autogen scans GGUF headers on first start; allow time before SCM gives up.
& $nssm set $ServiceName AppStopMethodConsole 15000

Write-Host "Starting..."
& $nssm start $ServiceName
Write-Host "Done. Manage with: nssm {start|stop|restart|status} $ServiceName"
