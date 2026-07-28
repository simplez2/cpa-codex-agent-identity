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
$patchPaths = @(
    (Join-Path $PSScriptRoot 'reset-credit-visibility.patch'),
    (Join-Path $PSScriptRoot 'agent-identity-management-entry.patch')
)
$expectedHashPath = Join-Path $PSScriptRoot 'management.html.sha256'

if (Test-Path -LiteralPath $WorkDirectory) {
    throw "WorkDirectory already exists: $WorkDirectory"
}

& git clone --filter=blob:none $upstreamRepository $WorkDirectory
if ($LASTEXITCODE -ne 0) { throw 'git clone failed' }

& git -C $WorkDirectory checkout --detach $upstreamCommit
if ($LASTEXITCODE -ne 0) { throw 'git checkout failed' }

foreach ($patchPath in $patchPaths) {
    & git -C $WorkDirectory apply --unidiff-zero --check $patchPath
    if ($LASTEXITCODE -ne 0) {
        throw "management overlay patch check failed: $patchPath"
    }
    & git -C $WorkDirectory apply --unidiff-zero $patchPath
    if ($LASTEXITCODE -ne 0) {
        throw "management overlay patch failed: $patchPath"
    }
}

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
$actualHash = $hash.Hash.ToLowerInvariant()
$expectedHash = ((Get-Content -LiteralPath $expectedHashPath -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
if ($expectedHash -notmatch '^[0-9a-f]{64}$') {
    throw "Invalid expected management overlay hash: $expectedHashPath"
}
if ($actualHash -ne $expectedHash) {
    throw "Management overlay checksum mismatch: expected $expectedHash, got $actualHash"
}
Write-Output "Built $OutputPath"
Write-Output "SHA256 $actualHash"
Write-Output "Build workspace retained at $WorkDirectory"
