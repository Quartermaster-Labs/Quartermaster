<#
.SYNOPSIS
  Authenticode-sign one or more files with a PFX supplied via env vars.

.DESCRIPTION
  CI calls this for the binary and the installer. No-op (exit 0) when PFX_B64 is
  unset, so unsigned builds still succeed — drop in the secrets to start signing,
  nothing else changes. To switch to SignPath/Azure Trusted Signing later, swap
  this step for their action; the rest of the pipeline is unaffected.

  Env: PFX_B64 (base64 of the .pfx), PFX_PASS (its password).
#>
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Files)
$ErrorActionPreference = 'Stop'

if (-not $env:PFX_B64) { Write-Host 'No signing cert configured; skipping.'; exit 0 }

$pfx = Join-Path $env:TEMP 'codesign.pfx'
[IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:PFX_B64))

$signtool = Get-ChildItem 'C:\Program Files (x86)\Windows Kits\10\bin\*\x64\signtool.exe' -ErrorAction SilentlyContinue |
    Sort-Object FullName | Select-Object -Last 1
if (-not $signtool) { throw 'signtool.exe not found (Windows SDK missing)' }

try {
    foreach ($f in $Files) {
        $path = (Resolve-Path -LiteralPath $f).Path
        Write-Host "signing $path"
        & $signtool.FullName sign /f $pfx /p $env:PFX_PASS /fd sha256 `
            /tr http://timestamp.digicert.com /td sha256 $path
        if ($LASTEXITCODE -ne 0) { throw "signtool failed ($LASTEXITCODE) for $path" }
    }
} finally {
    Remove-Item -Force $pfx -ErrorAction SilentlyContinue
}
