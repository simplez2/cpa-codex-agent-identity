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
$entryPatch = Get-Content -LiteralPath $patchPaths[1] -Raw
if ($entryPatch.Contains('/v0/resource/plugins/codex-agent-identity/open') -or
    $entryPatch -match '(?i)management[-_ ]?key|managementKey|x-management-key|authorization|bearer') {
    throw 'Management entry patch crosses the authentication boundary'
}

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

$builtArtifactPath = Join-Path $WorkDirectory 'dist\index.html'
$artifact = Get-Content -LiteralPath $builtArtifactPath -Raw
foreach ($requiredMarker in @(
    'codex-agent-identity',
    '/agent-identity/',
    'Identity management',
    '身份管理与导入',
    'noopener,noreferrer'
)) {
    if (-not $artifact.Contains($requiredMarker)) {
        throw "Management overlay is missing required marker: $requiredMarker"
    }
}
if ($artifact.Contains('/v0/resource/plugins/codex-agent-identity/open')) {
    throw 'Management overlay restored the legacy public plugin route'
}

$outputDirectory = Split-Path -Parent $OutputPath
if ($outputDirectory) {
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
}
Copy-Item -LiteralPath $builtArtifactPath -Destination $OutputPath -Force

$hash = Get-FileHash -LiteralPath $OutputPath -Algorithm SHA256
$actualHash = $hash.Hash.ToLowerInvariant()
Write-Output "Built $OutputPath"
Write-Output "SHA256 $actualHash"
Write-Output "Build workspace retained at $WorkDirectory"
