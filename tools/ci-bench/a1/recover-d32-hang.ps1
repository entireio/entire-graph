[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2

$runId = 'screen-01-d32'
$resultDirectory = Join-Path 'C:\ci-bench-results\a1\treatments' $runId
$suiteDirectory = Join-Path $resultDirectory 'suite'
$transportDirectory = 'C:\ci-bench-results\a1\transport'
$artifactPath = Join-Path $transportDirectory ($runId + '-recovered.zip')
$artifactHashPath = $artifactPath + '.sha256'
$recoveryMetadataPath = Join-Path $resultDirectory 'recovery-metadata.json'
$startedAt = [DateTimeOffset]::UtcNow

function Get-StorageToken {
    $tokenUri = 'http://169.254.169.254/metadata/identity/oauth2/token' +
        '?api-version=2018-02-01&resource=https%3A%2F%2Fstorage.azure.com%2F'
    for ($attempt = 1; $attempt -le 18; $attempt++) {
        try {
            return (Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } -Uri $tokenUri).access_token
        }
        catch {
            if ($attempt -eq 18) { throw }
            Start-Sleep -Seconds 10
        }
    }
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

if (-not (Test-Path -LiteralPath $resultDirectory)) {
    throw "Treatment directory is missing: $resultDirectory"
}

New-Item -ItemType Directory -Path $transportDirectory -Force | Out-Null
$suiteMetadata = $null
$suiteMetadataPath = Join-Path $suiteDirectory 'run-metadata.json'
if (Test-Path -LiteralPath $suiteMetadataPath) {
    $suiteMetadata = Get-Content -LiteralPath $suiteMetadataPath -Raw | ConvertFrom-Json
}

$binaryDirectory = Join-Path $suiteDirectory 'test-binaries'
if (Test-Path -LiteralPath $binaryDirectory) {
    Remove-Item -LiteralPath $binaryDirectory -Recurse -Force
}

$endedAt = [DateTimeOffset]::UtcNow
$recoveryMetadata = [ordered]@{
    schemaVersion = 1
    runId = $runId
    recoveryReason = 'Azure Run Command stayed open while Azure metrics showed the VM idle for more than ten minutes; VM was stopped and deallocated to terminate the hung invocation.'
    timingValidity = 'Retained for timing; go-test event names may be encoding-tainted because this run used pre-b22be6a7 harness output encoding.'
    suiteMetadataPresent = ($null -ne $suiteMetadata)
    suiteState = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.state }
    suitePhase = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.phase }
    suiteDurationSeconds = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.durationSeconds }
    goTestExitCode = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.goTestExitCode }
    wrapperExitCode = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.exitCode }
    startTimeUtc = $startedAt.ToString('o')
    endTimeUtc = $endedAt.ToString('o')
}
$recoveryMetadata | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $recoveryMetadataPath -Encoding UTF8

if (Test-Path -LiteralPath $artifactPath) { Remove-Item -LiteralPath $artifactPath -Force }
Compress-Archive -Path (Join-Path $resultDirectory '*') -DestinationPath $artifactPath -CompressionLevel Optimal
$artifactHash = (Get-FileHash -LiteralPath $artifactPath -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText(
    $artifactHashPath,
    ($artifactHash + '  ' + (Split-Path -Leaf $artifactPath) + "`n"),
    [Text.Encoding]::ASCII
)
Send-ResultBlob -Path $artifactPath -BlobName ('treatments/' + $runId + '-recovered.zip')
Send-ResultBlob -Path $artifactHashPath -BlobName ('treatments/' + $runId + '-recovered.zip.sha256')
Write-Output "A1_RECOVERY_UPLOADED run_id=$runId sha256=$artifactHash suite_state=$($recoveryMetadata.suiteState) go_test_exit=$($recoveryMetadata.goTestExitCode)"
