[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2

$productCommit = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
$goVersion = '1.26.7'
$productDirectory = 'C:\src\entire-graph'
$cacheRoot = 'C:\ci-bench-cache\a1'
$seedDirectory = Join-Path $cacheRoot 'seed-build'
$moduleCacheDirectory = Join-Path $cacheRoot 'gomod'
$goPathDirectory = Join-Path $cacheRoot 'gopath'
$seedArchive = Join-Path $cacheRoot 'seed-build.tar'
$resultDirectory = 'C:\ci-bench-results\a1\cache-seed'
$resultArchive = 'C:\ci-bench-results\a1\cache-seed.zip'
$metadataPath = Join-Path $resultDirectory 'cache-seed-metadata.json'
$go = 'C:\tools\go\bin\go.exe'
$git = 'C:\tools\mingit\cmd\git.exe'
$gccPath = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse -ErrorAction SilentlyContinue |
    Select-Object -First 1 -ExpandProperty FullName
$startedAt = [DateTimeOffset]::UtcNow
$phases = [System.Collections.Generic.List[object]]::new()
$state = 'failed'
$exitCode = 1
$fatalMessage = $null
$seedHash = $null
$seedFileCount = $null
$seedBytes = $null

function Invoke-Phase {
    param(
        [Parameter(Mandatory)][string] $Name,
        [Parameter(Mandatory)][scriptblock] $Action
    )

    $phaseStart = [DateTimeOffset]::UtcNow
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    $status = 'pass'
    $message = $null
    try {
        & $Action
    }
    catch {
        $status = 'fail'
        $message = $_.Exception.Message
        throw
    }
    finally {
        $stopwatch.Stop()
        $phases.Add([ordered]@{
            name = $Name
            status = $status
            startTimeUtc = $phaseStart.ToString('o')
            durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 6)
            message = $message
        }) | Out-Null
    }
}

function Invoke-NativeChecked {
    param(
        [Parameter(Mandatory)][string] $Command,
        [string[]] $Arguments = @(),
        [Parameter(Mandatory)][string] $LogPath,
        [string] $WorkingDirectory = $productDirectory
    )

    $previousErrorActionPreference = $ErrorActionPreference
    $previousLocation = Get-Location
    $nativeExitCode = 127
    try {
        $ErrorActionPreference = 'Continue'
        Set-Location -LiteralPath $WorkingDirectory
        & $Command @Arguments *> $LogPath
        $nativeExitCode = $LASTEXITCODE
    }
    finally {
        Set-Location -LiteralPath $previousLocation.Path
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($nativeExitCode -ne 0) {
        throw "Native command failed with exit code ${nativeExitCode}: $Command $($Arguments -join ' ')"
    }
}

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

if ([string]::IsNullOrWhiteSpace($gccPath)) {
    throw 'Pinned MinGW installation is missing gcc.exe.'
}

$env:Path = @(
    'C:\tools\go\bin',
    'C:\tools\mingit\cmd',
    'C:\tools\pwsh',
    (Split-Path -Parent $gccPath),
    [Environment]::GetEnvironmentVariable('Path', 'Machine')
) -join ';'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'
$env:GOTOOLCHAIN = 'local'
$env:GOCACHE = $seedDirectory
$env:GOMODCACHE = $moduleCacheDirectory
$env:GOPATH = $goPathDirectory

New-Item -ItemType Directory -Path $resultDirectory, $cacheRoot -Force | Out-Null

try {
    Invoke-Phase -Name 'validate-pins' -Action {
        $actualCommit = (& $git -C $productDirectory rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0 -or $actualCommit -ne $productCommit) {
            throw "Product checkout is not pinned to $productCommit."
        }
        $actualGoVersion = (& $go env GOVERSION).Trim()
        if ($LASTEXITCODE -ne 0 -or $actualGoVersion -ne ('go' + $goVersion)) {
            throw "Go version '$actualGoVersion' does not match go$goVersion."
        }
    }

    Invoke-Phase -Name 'initialize-cache-directories' -Action {
        foreach ($path in @($seedDirectory, $moduleCacheDirectory, $goPathDirectory, $seedArchive)) {
            if (Test-Path -LiteralPath $path) {
                Remove-Item -LiteralPath $path -Recurse -Force
            }
        }
        New-Item -ItemType Directory -Path $seedDirectory, $moduleCacheDirectory, $goPathDirectory -Force | Out-Null
    }

    Invoke-Phase -Name 'download-modules' -Action {
        Invoke-NativeChecked -Command $go -Arguments @('mod', 'download') -LogPath (Join-Path $resultDirectory 'go-mod-download.log')
    }

    Invoke-Phase -Name 'warm-build-cache' -Action {
        Invoke-NativeChecked -Command $go -Arguments @('test', '-run', '^$', '-timeout', '30m', './...') -LogPath (Join-Path $resultDirectory 'warm-build-cache.log')
    }

    Invoke-Phase -Name 'clean-test-cache' -Action {
        Invoke-NativeChecked -Command $go -Arguments @('clean', '-testcache') -LogPath (Join-Path $resultDirectory 'clean-testcache.log')
    }

    Invoke-Phase -Name 'archive-seed' -Action {
        $seedFiles = @(Get-ChildItem -LiteralPath $seedDirectory -File -Recurse)
        $script:seedFileCount = $seedFiles.Count
        $script:seedBytes = [long] (($seedFiles | Measure-Object -Property Length -Sum).Sum)
        Invoke-NativeChecked -Command 'C:\Windows\System32\tar.exe' -Arguments @('-cf', $seedArchive, '-C', $seedDirectory, '.') -LogPath (Join-Path $resultDirectory 'archive-seed.log')
        $script:seedHash = (Get-FileHash -LiteralPath $seedArchive -Algorithm SHA256).Hash.ToLowerInvariant()
    }

    $state = 'completed'
    $exitCode = 0
}
catch {
    $fatalMessage = $_.Exception.Message
}
finally {
    $endedAt = [DateTimeOffset]::UtcNow
    $metadata = [ordered]@{
        schemaVersion = 1
        state = $state
        phase = 'finished'
        productCommit = $productCommit
        goVersion = ('go' + $goVersion)
        goos = 'windows'
        goarch = 'amd64'
        cgoEnabled = '1'
        seedPolicy = 'dedicated Go build cache warmed by go test -run ^$ -timeout 30m ./...; test cache then cleaned'
        moduleCachePolicy = 'dedicated module cache populated by go mod download and held constant on the persisted OS disk'
        warmCommandLine = "go test -run '^$' -timeout 30m ./..."
        measuredCommandLine = 'go test -json -timeout 30m ./...'
        seedArchivePath = $seedArchive
        seedArchiveSha256 = $seedHash
        seedFileCount = $seedFileCount
        seedUncompressedBytes = $seedBytes
        startTimeUtc = $startedAt.ToString('o')
        endTimeUtc = $endedAt.ToString('o')
        durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
        exitCode = $exitCode
        error = $fatalMessage
        phases = $phases
    }
    $metadata | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $metadataPath -Encoding UTF8

    if (Test-Path -LiteralPath $resultArchive) { Remove-Item -LiteralPath $resultArchive -Force }
    Compress-Archive -Path (Join-Path $resultDirectory '*') -DestinationPath $resultArchive -CompressionLevel Optimal
    $resultHash = (Get-FileHash -LiteralPath $resultArchive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(
        ($resultArchive + '.sha256'),
        ($resultHash + '  cache-seed.zip' + "`n"),
        [Text.Encoding]::ASCII
    )
    Send-ResultBlob -Path $resultArchive -BlobName 'cache-seed.zip'
    Send-ResultBlob -Path ($resultArchive + '.sha256') -BlobName 'cache-seed.zip.sha256'
    Write-Output "A1_CACHE_SEED_UPLOADED state=$state seed_sha256=$seedHash artifact_sha256=$resultHash"
}

exit $exitCode
