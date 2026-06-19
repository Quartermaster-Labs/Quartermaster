<#
.SYNOPSIS
  Stop and remove the llama-quartermaster NSSM service. Self-elevating.

.DESCRIPTION
  Double-click or run from any PowerShell prompt: relaunches itself elevated
  (UAC) if not already admin, then stops and removes the service via NSSM.

.PARAMETER ServiceName
  Service to remove. Default llama-quartermaster.

.PARAMETER RemoveLegacyTask
  Also unregister the old 'llama-quartermaster' scheduled task (the pre-fork startup
  service) and kill any running llama-quartermaster.exe.

.EXAMPLE
  .\uninstall-service.ps1

.EXAMPLE
  .\uninstall-service.ps1 -RemoveLegacyTask
#>
[CmdletBinding()]
param(
    [string]$ServiceName = 'llama-quartermaster',
    [string]$LegacyTaskName = 'llama-quartermaster',
    [switch]$RemoveLegacyTask,
    [switch]$NoPause
)

$ErrorActionPreference = 'Stop'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltinRole]::Administrator)
}

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

if ($RemoveLegacyTask) {
    if (Get-ScheduledTask -TaskName $LegacyTaskName -ErrorAction SilentlyContinue) {
        Write-Host "Removing legacy scheduled task '$LegacyTaskName'..." -ForegroundColor DarkGray
        Unregister-ScheduledTask -TaskName $LegacyTaskName -Confirm:$false
    } else {
        Write-Host "Legacy task '$LegacyTaskName' not present." -ForegroundColor DarkGray
    }
    Stop-Process -Name 'llama-quartermaster' -Force -ErrorAction SilentlyContinue
}

$nssm = Get-Command nssm -ErrorAction SilentlyContinue
if (-not $nssm) { throw "nssm not found on PATH." }
$nssm = $nssm.Source

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Write-Host "Stopping and removing service '$ServiceName'..."
    & $nssm stop $ServiceName confirm 2>$null | Out-Null
    & $nssm remove $ServiceName confirm
    # Ensure the proxy is actually dead (nssm stop can leave a detached child).
    Stop-Process -Name 'llama-quartermaster-windows-amd64' -Force -ErrorAction SilentlyContinue
    Stop-Process -Name 'llama-quartermaster' -Force -ErrorAction SilentlyContinue
    Write-Host "Removed and stopped." -ForegroundColor Green
} else {
    Write-Host "Service '$ServiceName' not installed." -ForegroundColor DarkGray
}

if (-not $NoPause) { Write-Host "`nPress any key to close..."; [void][System.Console]::ReadKey($true) }
