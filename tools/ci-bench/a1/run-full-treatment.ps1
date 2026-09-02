[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount,

    [Parameter(Mandatory)]
    [ValidatePattern('^[a-z0-9-]+$')]
    [string] $RunId,

    [Parameter(Mandatory)]
    [ValidatePattern('^[a-z0-9-]+$')]
    [string] $RunLabel,

    [Parameter(Mandatory)]
    [ValidatePattern('^Standard_D(4|8|16|32)ads_v7$')]
    [string] $ExpectedVmSize,

    [Parameter(Mandatory)]
    [ValidateSet(4, 8, 16, 32)]
    [int] $ExpectedVcpus,

    [ValidateSet('./...', './internal/termsafe', './cmd/graph-bench', './internal/cli')]
    [string] $Packages = './...'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2

$productCommit = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
$harnessCommit = 'b22be6a73adac8d9c582af4bfc681c5d7a517221'
$productDirectory = 'C:\src\entire-graph'
$harnessDirectory = 'C:\ci-bench\tools\ci-bench'
$cacheRoot = 'C:\ci-bench-cache\a1'
$seedArchive = Join-Path $cacheRoot 'seed-build.tar'
$activeCacheDirectory = Join-Path $cacheRoot 'active-build'
$moduleCacheDirectory = Join-Path $cacheRoot 'gomod'
$goPathDirectory = Join-Path $cacheRoot 'gopath'
$resultDirectory = Join-Path 'C:\ci-bench-results\a1\treatments' $RunId
$transportDirectory = 'C:\ci-bench-results\a1\transport'
$artifactPath = Join-Path $transportDirectory ($RunId + '.zip')
$artifactHashPath = $artifactPath + '.sha256'
$transportPath = Join-Path $transportDirectory ($RunId + '.transport.json')
$pwsh = 'C:\tools\pwsh\pwsh.exe'
$git = 'C:\tools\mingit\cmd\git.exe'
$go = 'C:\tools\go\bin\go.exe'
$gccPath = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse -ErrorAction SilentlyContinue |
    Select-Object -First 1 -ExpandProperty FullName
$shPath = Get-ChildItem -LiteralPath 'C:\tools\mingit' -Filter 'sh.exe' -File -Recurse -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -like '*\usr\bin\sh.exe' } |
    Select-Object -First 1 -ExpandProperty FullName
$startedAt = [DateTimeOffset]::UtcNow
$phases = [System.Collections.Generic.List[object]]::new()
$state = 'failed'
$driverExitCode = 1
$suiteExitCode = $null
$suiteMetadataExitCode = $null
$wrapperProcessExitCode = $null
$fatalMessage = $null
$seedHash = $null
$actualVmSize = $null
$actualVcpus = $null

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
        [Parameter(Mandatory)][string] $LogPath
    )

    $previousErrorActionPreference = $ErrorActionPreference
    $nativeExitCode = 127
    try {
        $ErrorActionPreference = 'Continue'
        & $Command @Arguments *> $LogPath
        $nativeExitCode = $LASTEXITCODE
    }
    finally {
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
if ([string]::IsNullOrWhiteSpace($shPath)) {
    throw 'Pinned MinGit installation is missing usr\bin\sh.exe.'
}

$pathComponents = @(
    'C:\tools\go\bin',
    'C:\tools\mingit\cmd',
    'C:\tools\pwsh',
    (Split-Path -Parent $shPath),
    (Split-Path -Parent $gccPath),
    [Environment]::GetEnvironmentVariable('Path', 'Machine')
)
$env:Path = $pathComponents -join ';'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'
$env:GOTOOLCHAIN = 'local'
$env:GIT_TERMINAL_PROMPT = '0'
$env:GCM_INTERACTIVE = 'Never'
$env:GOCACHE = $activeCacheDirectory
$env:GOMODCACHE = $moduleCacheDirectory
$env:GOPATH = $goPathDirectory

New-Item -ItemType Directory -Path $resultDirectory, $transportDirectory -Force | Out-Null
$toolResolutions = [ordered]@{
    pathComponents = $pathComponents
    sh = (Get-Command sh.exe -ErrorAction Stop).Source
    git = (Get-Command git.exe -ErrorAction Stop).Source
    gcc = (Get-Command gcc.exe -ErrorAction Stop).Source
    go = (Get-Command go.exe -ErrorAction Stop).Source
    pwsh = (Get-Command pwsh.exe -ErrorAction Stop).Source
}
$toolResolutions | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $resultDirectory 'driver-environment.json') -Encoding UTF8

try {
    Invoke-Phase -Name 'validate-pins-and-vm' -Action {
        if (-not (Test-Path -LiteralPath $seedArchive)) { throw "Seed archive is missing: $seedArchive" }
        $actualCommit = (& $git -C $productDirectory rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0 -or $actualCommit -ne $productCommit) {
            throw "Product checkout is not pinned to $productCommit."
        }
        $actualHarnessCommit = (& $git -C 'C:\src\entire-graph-harness' rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0 -or $actualHarnessCommit -ne $harnessCommit) {
            throw "Harness checkout is not pinned to $harnessCommit."
        }
        $actualGoVersion = (& $go env GOVERSION).Trim()
        if ($LASTEXITCODE -ne 0 -or $actualGoVersion -ne 'go1.26.7') {
            throw "Go version '$actualGoVersion' does not match go1.26.7."
        }
        $imds = Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } `
            -Uri 'http://169.254.169.254/metadata/instance/compute?api-version=2021-12-13'
        $script:actualVmSize = [string] $imds.vmSize
        $script:actualVcpus = [Environment]::ProcessorCount
        if ($actualVmSize -ne $ExpectedVmSize) {
            throw "IMDS VM size '$actualVmSize' does not match '$ExpectedVmSize'."
        }
        if ($actualVcpus -ne $ExpectedVcpus) {
            throw "Processor count '$actualVcpus' does not match '$ExpectedVcpus'."
        }
    }

    Invoke-Phase -Name 'verify-and-restore-seed-cache' -Action {
        $script:seedHash = (Get-FileHash -LiteralPath $seedArchive -Algorithm SHA256).Hash.ToLowerInvariant()
        if (Test-Path -LiteralPath $activeCacheDirectory) {
            Remove-Item -LiteralPath $activeCacheDirectory -Recurse -Force
        }
        New-Item -ItemType Directory -Path $activeCacheDirectory -Force | Out-Null
        Invoke-NativeChecked -Command 'C:\Windows\System32\tar.exe' -Arguments @('-xf', $seedArchive, '-C', $activeCacheDirectory) -LogPath (Join-Path $resultDirectory 'restore-seed-cache.log')
    }

    Invoke-Phase -Name 'run-exact-suite-and-decomposition' -Action {
        $wrapperStdout = Join-Path $resultDirectory 'wrapper.stdout.log'
        $wrapperStderr = Join-Path $resultDirectory 'wrapper.stderr.log'
        $arguments = @(
            '-NoLogo', '-NoProfile', '-NonInteractive',
            '-File', (Join-Path $harnessDirectory 'run-go-suite.ps1'),
            '-OutputDirectory', (Join-Path $resultDirectory 'suite'),
            '-RepositoryPath', $productDirectory,
            '-RunId', $RunId,
            '-RunLabel', $RunLabel,
            '-ExpectedRepositorySha', $productCommit,
            '-ExpectedGoVersion', 'go1.26.7',
            '-Packages', $Packages,
            '-ResourceSampleIntervalSeconds', '2'
        )
        $process = Start-Process -FilePath $pwsh -ArgumentList $arguments -PassThru `
            -RedirectStandardOutput $wrapperStdout -RedirectStandardError $wrapperStderr -NoNewWindow
        # Start-Process -Wait on Windows waits for the full descendant process
        # tree. Some repository tests intentionally spawn or re-exec helpers;
        # waiting on the direct PowerShell wrapper avoids an idle post-suite
        # hang if a descendant outlives the wrapper.
        $process.WaitForExit()
        $process.Refresh()
        $script:wrapperProcessExitCode = [int] $process.ExitCode

        $completedMetadataPath = Join-Path $resultDirectory 'suite\run-metadata.json'
        if (-not (Test-Path -LiteralPath $completedMetadataPath)) {
            throw 'Suite wrapper exited without final run-metadata.json.'
        }
        $completedMetadata = Get-Content -LiteralPath $completedMetadataPath -Raw | ConvertFrom-Json
        $script:suiteExitCode = [int] $completedMetadata.goTestExitCode
        $script:suiteMetadataExitCode = [int] $completedMetadata.exitCode
        if ($completedMetadata.state -ne 'completed' -or $completedMetadata.phase -ne 'finished') {
            throw "Suite metadata is not completed/finished: $($completedMetadata.state)/$($completedMetadata.phase)."
        }
        if ($suiteExitCode -ne 0 -or $suiteMetadataExitCode -ne 0 -or $wrapperProcessExitCode -ne 0) {
            throw "Exit disagreement or failure: goTest=$suiteExitCode metadata=$suiteMetadataExitCode process=$wrapperProcessExitCode."
        }
    }

    Invoke-Phase -Name 'remove-test-binaries' -Action {
        $binaryDirectory = Join-Path $resultDirectory 'suite\test-binaries'
        if (Test-Path -LiteralPath $binaryDirectory) {
            Remove-Item -LiteralPath $binaryDirectory -Recurse -Force
        }
    }

    $state = 'completed'
    $driverExitCode = 0
}
catch {
    $fatalMessage = $_.Exception.Message
    if ($null -ne $suiteExitCode -and [int] $suiteExitCode -ne 0) {
        $driverExitCode = [int] $suiteExitCode
    }
    elseif ($null -ne $suiteMetadataExitCode -and [int] $suiteMetadataExitCode -ne 0) {
        $driverExitCode = [int] $suiteMetadataExitCode
    }
    elseif ($null -ne $wrapperProcessExitCode -and [int] $wrapperProcessExitCode -ne 0) {
        $driverExitCode = [int] $wrapperProcessExitCode
    }
    else {
        $driverExitCode = 1
    }
}
finally {
    $suiteMetadataPath = Join-Path $resultDirectory 'suite\run-metadata.json'
    $suiteMetadata = $null
    if (Test-Path -LiteralPath $suiteMetadataPath) {
        try { $suiteMetadata = Get-Content -LiteralPath $suiteMetadataPath -Raw | ConvertFrom-Json }
        catch { $fatalMessage = ($fatalMessage, $_.Exception.Message | Where-Object { $_ }) -join '; ' }
    }

    $endedAt = [DateTimeOffset]::UtcNow
    $treatmentMetadata = [ordered]@{
        schemaVersion = 1
        state = $state
        phase = 'finished'
        runId = $RunId
        runLabel = $RunLabel
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        expectedVmSize = $ExpectedVmSize
        actualVmSize = $actualVmSize
        expectedVcpus = $ExpectedVcpus
        actualVcpus = $actualVcpus
        goVersion = 'go1.26.7'
        goos = 'windows'
        goarch = 'amd64'
        cgoEnabled = '1'
        gitTerminalPrompt = '0'
        gitCredentialManagerInteractive = 'Never'
        commandLine = ('go test -json -timeout 30m ' + $Packages)
        measuredTreatment = ($Packages -eq './...')
        buildCachePolicy = 'restore the same SHA-256-addressed warm seed before every treatment; run go clean -testcache inside wrapper'
        seedArchiveSha256 = $seedHash
        startTimeUtc = $startedAt.ToString('o')
        endTimeUtc = $endedAt.ToString('o')
        durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
        suiteState = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.state }
        suitePhase = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.phase }
        suiteDurationSeconds = if ($null -eq $suiteMetadata) { $null } else { $suiteMetadata.durationSeconds }
        goTestExitCode = if ($null -eq $suiteMetadata) { $suiteExitCode } else { $suiteMetadata.goTestExitCode }
        suiteMetadataExitCode = $suiteMetadataExitCode
        wrapperProcessExitCode = $wrapperProcessExitCode
        toolResolutions = $toolResolutions
        exitCode = $driverExitCode
        error = $fatalMessage
        phases = $phases
    }
    $treatmentMetadata | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $resultDirectory 'treatment-metadata.json') -Encoding UTF8

    $packageStarted = [DateTimeOffset]::UtcNow
    if (Test-Path -LiteralPath $artifactPath) { Remove-Item -LiteralPath $artifactPath -Force }
    Compress-Archive -Path (Join-Path $resultDirectory '*') -DestinationPath $artifactPath -CompressionLevel Optimal
    $packageEnded = [DateTimeOffset]::UtcNow
    $artifactHash = (Get-FileHash -LiteralPath $artifactPath -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(
        $artifactHashPath,
        ($artifactHash + '  ' + (Split-Path -Leaf $artifactPath) + "`n"),
        [Text.Encoding]::ASCII
    )

    $uploadStarted = [DateTimeOffset]::UtcNow
    Send-ResultBlob -Path $artifactPath -BlobName ('treatments/' + $RunId + '.zip')
    Send-ResultBlob -Path $artifactHashPath -BlobName ('treatments/' + $RunId + '.zip.sha256')
    $uploadEnded = [DateTimeOffset]::UtcNow

    $transportMetadata = [ordered]@{
        schemaVersion = 1
        runId = $RunId
        exportStatus = 'artifact-and-checksum-uploaded'
        packageStartTimeUtc = $packageStarted.ToString('o')
        packageEndTimeUtc = $packageEnded.ToString('o')
        packageDurationSeconds = [math]::Round(($packageEnded - $packageStarted).TotalSeconds, 6)
        uploadStartTimeUtc = $uploadStarted.ToString('o')
        uploadEndTimeUtc = $uploadEnded.ToString('o')
        uploadDurationSeconds = [math]::Round(($uploadEnded - $uploadStarted).TotalSeconds, 6)
        artifactBlob = ('treatments/' + $RunId + '.zip')
        artifactBytes = (Get-Item -LiteralPath $artifactPath).Length
        artifactSha256 = $artifactHash
    }
    $transportMetadata | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $transportPath -Encoding UTF8
    Send-ResultBlob -Path $transportPath -BlobName ('treatments/' + $RunId + '.transport.json')

    Write-Output "A1_TREATMENT_UPLOADED run_id=$RunId state=$state exit_code=$driverExitCode sha256=$artifactHash"
}

exit $driverExitCode
