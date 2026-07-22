[CmdletBinding()]
param(
    [Parameter()]
    [string]$BunPath = 'bun',

    [Parameter()]
    [string]$OutputPath = (Join-Path $PSScriptRoot 'out\management.html'),

    [Parameter()]
    [string]$WorkDirectory = (Join-Path ([System.IO.Path]::GetTempPath()) (
        'cpa-management-overlay-' + [System.Guid]::NewGuid().ToString('N')
    ))
)

$ErrorActionPreference = 'Stop'
$upstreamRepository = 'https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git'
$upstreamCommit = '6a6a22af85ce8763e8898c0d8641de3137f3ffd9'
$patchPath = Join-Path $PSScriptRoot 'reset-credit-visibility.patch'

if (Test-Path -LiteralPath $WorkDirectory) {
    throw "WorkDirectory already exists: $WorkDirectory"
}

& git clone --filter=blob:none $upstreamRepository $WorkDirectory
if ($LASTEXITCODE -ne 0) { throw 'git clone failed' }

& git -C $WorkDirectory checkout --detach $upstreamCommit
if ($LASTEXITCODE -ne 0) { throw 'git checkout failed' }

& git -C $WorkDirectory apply --unidiff-zero --check $patchPath
if ($LASTEXITCODE -ne 0) { throw 'management overlay patch check failed' }
& git -C $WorkDirectory apply --unidiff-zero $patchPath
if ($LASTEXITCODE -ne 0) { throw 'management overlay patch failed' }

Push-Location $WorkDirectory
try {
    & $BunPath install --frozen-lockfile
    if ($LASTEXITCODE -ne 0) { throw 'bun install failed' }
    & $BunPath run verify
    if ($LASTEXITCODE -ne 0) { throw 'management overlay verification failed' }
} finally {
    Pop-Location
}

$outputDirectory = Split-Path -Parent $OutputPath
if ($outputDirectory) {
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
}
Copy-Item -LiteralPath (Join-Path $WorkDirectory 'dist\index.html') -Destination $OutputPath -Force

$hash = Get-FileHash -LiteralPath $OutputPath -Algorithm SHA256
Write-Output "Built $OutputPath"
Write-Output "SHA256 $($hash.Hash.ToLowerInvariant())"
Write-Output "Build workspace retained at $WorkDirectory"
