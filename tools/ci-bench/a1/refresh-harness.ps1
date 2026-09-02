[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2

$harnessCommit = 'b22be6a73adac8d9c582af4bfc681c5d7a517221'
$harnessSourceDirectory = 'C:\src\entire-graph-harness'
$harnessDirectory = 'C:\ci-bench\tools\ci-bench'
$git = 'C:\tools\mingit\cmd\git.exe'
$resultDirectory = 'C:\ci-bench-results\a1\harness-refresh-b22be6a7'
$metadataPath = Join-Path $resultDirectory 'harness-refresh.json'
$startedAt = [DateTimeOffset]::UtcNow
$parseErrorMessages = [System.Collections.Generic.List[string]]::new()

function Get-StorageToken {
    $tokenUri = 'http://169.254.169.254/metadata/identity/oauth2/token' +
        '?api-version=2018-02-01&resource=https%3A%2F%2Fstorage.azure.com%2F'
    return (Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } -Uri $tokenUri).access_token
}

function Send-ResultBlob {
    param([string] $Path, [string] $BlobName)

    $token = Get-StorageToken
    try {
        $blobUri = 'https://{0}.blob.core.windows.net/results/{1}' -f $StorageAccount, $BlobName
        Invoke-WebRequest -UseBasicParsing -Method Put -Uri $blobUri -InFile $Path -Headers @{
            Authorization = 'Bearer ' + $token
            'x-ms-version' = '2023-11-03'
            'x-ms-blob-type' = 'BlockBlob'
        } | Out-Null
    }
    finally {
        Remove-Variable token -ErrorAction SilentlyContinue
    }
}

function Invoke-GitChecked {
    param(
        [Parameter(Mandatory)][string[]] $Arguments,
        [Parameter(Mandatory)][string] $LogPath
    )

    $previousErrorActionPreference = $ErrorActionPreference
    $nativeExitCode = 127
    try {
        $ErrorActionPreference = 'Continue'
        & $git @Arguments *> $LogPath
        $nativeExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($nativeExitCode -ne 0) {
        throw "Git failed with exit code $nativeExitCode."
    }
}

$env:Path = @('C:\tools\mingit\cmd', [Environment]::GetEnvironmentVariable('Path', 'Machine')) -join ';'
New-Item -ItemType Directory -Path $resultDirectory -Force | Out-Null

Invoke-GitChecked -Arguments @('-C', $harnessSourceDirectory, 'fetch', '--depth', '1', 'origin', $harnessCommit) `
    -LogPath (Join-Path $resultDirectory 'fetch.log')
Invoke-GitChecked -Arguments @('-C', $harnessSourceDirectory, 'checkout', '--detach', 'FETCH_HEAD') `
    -LogPath (Join-Path $resultDirectory 'checkout.log')
$actualCommit = (& $git -C $harnessSourceDirectory rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $actualCommit -ne $harnessCommit) {
    throw "Harness checkout '$actualCommit' does not match '$harnessCommit'."
}

if (Test-Path -LiteralPath $harnessDirectory) {
    Remove-Item -LiteralPath $harnessDirectory -Recurse -Force
}
New-Item -ItemType Directory -Path (Split-Path -Parent $harnessDirectory) -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $harnessSourceDirectory 'tools\ci-bench') -Destination $harnessDirectory -Recurse -Force

Get-ChildItem -LiteralPath $harnessDirectory -Filter '*.ps1' | ForEach-Object {
    $tokens = $null
    $errors = $null
    [Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref] $tokens, [ref] $errors) | Out-Null
    foreach ($parseError in $errors) {
        $parseErrorMessages.Add("$($_.Name): $parseError") | Out-Null
    }
}
if ($parseErrorMessages.Count -ne 0) {
    throw ($parseErrorMessages -join '; ')
}

$endedAt = [DateTimeOffset]::UtcNow
$metadata = [ordered]@{
    schemaVersion = 1
    state = 'completed'
    harnessCommit = $actualCommit
    parseErrorCount = $parseErrorMessages.Count
    startTimeUtc = $startedAt.ToString('o')
    endTimeUtc = $endedAt.ToString('o')
    durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
}
$metadata | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $metadataPath -Encoding UTF8
Send-ResultBlob -Path $metadataPath -BlobName 'harness-refresh-b22be6a7.json'
Write-Output "A1_HARNESS_REFRESHED commit=$actualCommit parse_errors=$($parseErrorMessages.Count)"
