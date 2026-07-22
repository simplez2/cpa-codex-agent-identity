param(
    [Parameter(Mandatory = $true)]
    [string]$ManagementKey,

    [string]$SidecarUrl = "http://127.0.0.1:18787",

    [string]$Token = $env:CODEX_ACCESS_TOKEN
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($Token)) {
    throw "CODEX_ACCESS_TOKEN is empty"
}

$headers = @{ Authorization = "Bearer $ManagementKey" }
$body = @{ codex_access_token = $Token } | ConvertTo-Json -Compress
$result = Invoke-RestMethod `
    -Method Post `
    -Uri "$($SidecarUrl.TrimEnd('/'))/admin/v1/identities/import" `
    -Headers $headers `
    -ContentType "application/json" `
    -Body $body

$result
