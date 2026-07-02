<#
.SYNOPSIS
  Install llama-quartermaster as a Windows service via NSSM. Self-elevating.

.DESCRIPTION
  The proxy binary has no Service Control Manager handler, so it can't be run by
  `sc create` directly (SCM would kill it for "not responding to start"). This
  script wraps it with NSSM (https://nssm.cc), the de-facto tool for turning a
  console exe into a service.

  Double-click or run from any PowerShell prompt: the script relaunches itself
  elevated (UAC) if not already admin. With no arguments it uses the bundle
  layout: exe sits two levels up from this script (the release-bundle root),
  with config\config.yaml and config\quartermaster-generate.yaml under it.

  To remove: run uninstall-service.ps1 in this folder.

.PARAMETER ExePath
  Path to the proxy binary. Defaults to <bundle>\llama-quartermaster-windows-amd64.exe.

.PARAMETER Config
  Config path (-config). Defaults to <bundle>\config\config.yaml.

.PARAMETER Generate
  Autogen control file (-generate). Defaults to <bundle>\config\quartermaster-generate.yaml
  when present; omit/clear to load a static -config only.

.PARAMETER Listen
  Listen address (-listen). Default 0.0.0.0:1250.

.EXAMPLE
  .\install-service.ps1

.EXAMPLE
  .\install-service.ps1 -Listen 0.0.0.0:1300 -Generate ''
#>
[CmdletBinding()]
param(
    [string]$ServiceName = 'llama-quartermaster',
    [string]$ExePath,
    [string]$Config,
    [string]$Generate,
    [string]$Listen = '0.0.0.0:1250',
    [switch]$WatchConfig = $true,
    [switch]$NoPause
)

$ErrorActionPreference = 'Stop'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltinRole]::Administrator)
}

# Self-elevate: relaunch this exact script with the same bound parameters under UAC.
if (-not (Test-Admin)) {
    Write-Host "Requesting administrator elevation..." -ForegroundColor Cyan
    $argList = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`"")
    foreach ($k in $PSBoundParameters.Keys) {
        $v = $PSBoundParameters[$k]
        if ($v -is [System.Management.Automation.SwitchParameter]) {
            if ($v.IsPresent) { $argList += "-$k" }
        } else {
            $argList += "-$k"; $argList += "`"$v`""
        }
    }
    try {
        Start-Process powershell.exe -Verb RunAs -ArgumentList $argList
    } catch {
        throw "Elevation cancelled or failed: $($_.Exception.Message)"
    }
    return
}

function Get-Nssm {
    $cmd = Get-Command nssm -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    throw "nssm not found on PATH. Install it (https://nssm.cc) or use the WinSW xml in this folder."
}

# Resolve bundle root (two levels up: <root>\packaging\windows\install-service.ps1).
$scriptDir = $PSScriptRoot
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition }
$root = Split-Path -Parent (Split-Path -Parent $scriptDir)

# Default paths from the bundle layout when not supplied.
if (-not $ExePath)  { $ExePath = Join-Path $root 'llama-quartermaster-windows-amd64.exe' }
if (-not $Config)   { $Config  = Join-Path $root 'config\config.yaml' }
if (-not $PSBoundParameters.ContainsKey('Generate')) {
    $g = Join-Path $root 'config\quartermaster-generate.yaml'
    if (Test-Path -LiteralPath $g) { $Generate = $g }
}

$nssm = Get-Nssm

if (-not (Test-Path -LiteralPath $ExePath)) { throw "-ExePath not found: '$ExePath'" }
$ExePath = (Resolve-Path -LiteralPath $ExePath).Path
$workDir = Split-Path -Parent $ExePath

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

# Replace any existing instance cleanly.
if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Write-Host "Existing service found; removing first..." -ForegroundColor DarkGray
    & $nssm stop $ServiceName confirm 2>$null | Out-Null
    & $nssm remove $ServiceName confirm | Out-Null
}

& $nssm install $ServiceName $ExePath
& $nssm set $ServiceName AppParameters $arguments
& $nssm set $ServiceName AppDirectory $workDir
& $nssm set $ServiceName Start SERVICE_AUTO_START
# NSSM won't create the log dir; make it first.
$logDir = Join-Path $workDir 'logs'
New-Item -ItemType Directory -Force $logDir | Out-Null
& $nssm set $ServiceName AppStdout (Join-Path $logDir 'llama-quartermaster.out.log')
& $nssm set $ServiceName AppStderr (Join-Path $logDir 'llama-quartermaster.err.log')
# Autogen scans GGUF headers on first start; allow time before SCM gives up.
& $nssm set $ServiceName AppStopMethodConsole 15000

Write-Host "Starting..."
& $nssm start $ServiceName
Write-Host "Done. Manage with: nssm {start|stop|restart|status} $ServiceName" -ForegroundColor Green

if (-not $NoPause) { Write-Host "`nPress any key to close..."; [void][System.Console]::ReadKey($true) }
