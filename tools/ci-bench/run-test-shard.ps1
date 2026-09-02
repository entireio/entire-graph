[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $PlanPath,
    [Parameter(Mandatory)][ValidateRange(0, 1023)][int] $ShardIndex,
    [Parameter(Mandatory)][string] $RepositoryPath,
    [Parameter(Mandatory)][string] $BinaryDirectory,
    [Parameter(Mandatory)][string] $OutputDirectory,
    [Parameter(Mandatory)][string] $ExpectedRepositorySha,
    [string] $GoCommand = 'go',
    [string] $Timeout = '30m',
    [string] $ResourceMonitorPath = (Join-Path $PSScriptRoot 'monitor-resources.ps1'),
    [ValidateRange(0.25, 3600.0)][double] $ResourceSampleIntervalSeconds = 2.0,
    [switch] $SkipResourceMonitor
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$conservativeCommandLineLimit = 30000
$win32AbsoluteCommandLineLimit = 32767

$nativePreferenceExists = Test-Path -LiteralPath 'variable:PSNativeCommandUseErrorActionPreference'
$originalNativePreference = $null
if ($nativePreferenceExists) {
    $originalNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

function Write-Utf8File {
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)][AllowEmptyString()][string] $Content
    )
    [System.IO.File]::WriteAllText($Path, $Content, [System.Text.UTF8Encoding]::new($false))
}

function Write-JsonAtomically {
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)][object] $Value
    )
    $temporary = $Path + '.tmp'
    Write-Utf8File -Path $temporary -Content (ConvertTo-Json -InputObject $Value -Depth 20)
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function ConvertTo-WindowsCommandLineArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string] $Argument)

    if ($Argument.Length -gt 0 -and $Argument -notmatch '[\s"]') {
        return $Argument
    }
    $builder = [System.Text.StringBuilder]::new()
    [void] $builder.Append('"')
    $backslashes = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes++
            continue
        }
        if ($character -eq '"') {
            [void] $builder.Append(('\' * (($backslashes * 2) + 1)))
            [void] $builder.Append('"')
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void] $builder.Append(('\' * $backslashes))
            $backslashes = 0
        }
        [void] $builder.Append($character)
    }
    if ($backslashes -gt 0) {
        [void] $builder.Append(('\' * ($backslashes * 2)))
    }
    [void] $builder.Append('"')
    return $builder.ToString()
}

function Get-WindowsSerializedCommandLine {
    param(
        [Parameter(Mandatory)][string] $Executable,
        [Parameter(Mandatory)][string[]] $Arguments
    )
    return (@(ConvertTo-WindowsCommandLineArgument $Executable) + @(
        $Arguments | ForEach-Object { ConvertTo-WindowsCommandLineArgument ([string] $_) }
    )) -join ' '
}

function Invoke-NativeCapture {
    param(
        [Parameter(Mandatory)][string] $Command,
        [Parameter(Mandatory)][string[]] $Arguments,
        [Parameter(Mandatory)][string] $WorkingDirectory,
        [Parameter(Mandatory)][string] $StdoutPath,
        [Parameter(Mandatory)][string] $StderrPath
    )
    Write-Utf8File -Path $StdoutPath -Content ''
    Write-Utf8File -Path $StderrPath -Content ''
    $previousLocation = Get-Location
    $startedAt = [DateTimeOffset]::UtcNow
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $exitCode = 127
    $invocationError = $null
    try {
        Set-Location -LiteralPath $WorkingDirectory
        & $Command @Arguments 1> $StdoutPath 2> $StderrPath
        # For `go tool test2json <binary> ...`, test2json propagates the child
        # test binary status. Capture it before any other native command runs.
        $exitCode = $LASTEXITCODE
    }
    catch {
        $invocationError = $_.Exception.Message
        Write-Utf8File -Path $StderrPath -Content ($invocationError + [Environment]::NewLine)
    }
    finally {
        $stopwatch.Stop()
        Set-Location -LiteralPath $previousLocation.Path
    }
    return [ordered]@{
        startTimeUtc = $startedAt.ToString('o')
        endTimeUtc = [DateTimeOffset]::UtcNow.ToString('o')
        durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 6)
        exitCode = $exitCode
        stdoutPath = $StdoutPath
        stderrPath = $StderrPath
        invocationError = $invocationError
    }
}

if (-not $IsWindows) {
    throw 'run-test-shard.ps1 must be executed by PowerShell 7 on Windows.'
}
if ($Timeout -notmatch '^\d+(ns|us|µs|ms|s|m|h)$') {
    throw "invalid Go test timeout: $Timeout"
}

$resolvedPlan = (Resolve-Path -LiteralPath $PlanPath).ProviderPath
$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
$resolvedBinaryDirectory = (Resolve-Path -LiteralPath $BinaryDirectory).ProviderPath
$resolvedOutput = [System.IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $resolvedOutput -Force | Out-Null
$metadataPath = Join-Path $resolvedOutput 'shard-metadata.json'
$combinedEventsPath = Join-Path $resolvedOutput 'shard-events.jsonl'
$monitorStopFile = Join-Path $resolvedOutput '.resource-monitor.stop'
$resourcePath = Join-Path $resolvedOutput 'resource-samples.jsonl'
Write-Utf8File -Path $combinedEventsPath -Content ''

$plan = Get-Content -LiteralPath $resolvedPlan -Raw | ConvertFrom-Json
if ($plan.schema -ne 'ci-bench.test-shard-plan.v1') {
    throw "unexpected plan schema: $($plan.schema)"
}
if ($ShardIndex -ge [int] $plan.shardCount) {
    throw "shard index $ShardIndex is outside plan shard count $($plan.shardCount)"
}
$shard = @($plan.shards | Where-Object { [int] $_.index -eq $ShardIndex })
if ($shard.Count -ne 1) {
    throw "plan does not contain exactly one shard with index $ShardIndex"
}
$shard = $shard[0]

$go = Get-Command -Name $GoCommand -CommandType Application -ErrorAction Stop
$resolvedGo = $go.Source
$metadata = [ordered]@{
    schema = 'ci-bench.test-shard-run.v1'
    state = 'initializing'
    shardIndex = $ShardIndex
    shardCount = [int] $plan.shardCount
    planPath = $resolvedPlan
    repositoryPath = $resolvedRepository
    binaryDirectory = $resolvedBinaryDirectory
    expectedRepositorySha = $ExpectedRepositorySha
    repositorySha = $null
    goos = 'windows'
    goarch = 'amd64'
    cgoEnabled = '1'
    timeout = $Timeout
    startTimeUtc = [DateTimeOffset]::UtcNow.ToString('o')
    endTimeUtc = $null
    durationSeconds = $null
    exitCode = $null
    cleanTestCacheExitCode = $null
    invocations = [System.Collections.Generic.List[object]]::new()
    eventsPath = $combinedEventsPath
    resourceSamplesPath = if ($SkipResourceMonitor) { $null } else { $resourcePath }
    commandLineLimit = $conservativeCommandLineLimit
    win32AbsoluteCommandLineLimit = $win32AbsoluteCommandLineLimit
    batching = [ordered]@{
        enabled = $false
        processCountPerPackageInThisShard = 1
        note = 'No command-line batching: compressed regexes must fit or the run fails closed.'
    }
    processSemantics = 'Each package TestMain/init runs once in this shard; a package assigned to multiple shards runs TestMain/init once per shard.'
    errors = [System.Collections.Generic.List[string]]::new()
}
Write-JsonAtomically -Path $metadataPath -Value $metadata

$originalGoEnvironment = [ordered]@{
    GOOS = [Environment]::GetEnvironmentVariable('GOOS', 'Process')
    GOARCH = [Environment]::GetEnvironmentVariable('GOARCH', 'Process')
    CGO_ENABLED = [Environment]::GetEnvironmentVariable('CGO_ENABLED', 'Process')
}
$monitorJob = $null
$startedAt = [DateTimeOffset]::Parse($metadata.startTimeUtc)
$finalExitCode = 0
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '1'

    $sha = @(& git '-C' $resolvedRepository 'rev-parse' 'HEAD' 2>&1 | ForEach-Object { [string] $_ })
    $shaExitCode = $LASTEXITCODE
    if ($shaExitCode -ne 0) {
        throw "git rev-parse failed with exit code $shaExitCode"
    }
    $metadata.repositorySha = ($sha -join '').Trim()
    if ($metadata.repositorySha -ne $ExpectedRepositorySha) {
        throw "repository SHA '$($metadata.repositorySha)' does not match '$ExpectedRepositorySha'"
    }

    & $resolvedGo 'clean' '-testcache' 1> (Join-Path $resolvedOutput 'clean-testcache.stdout.log') 2> (Join-Path $resolvedOutput 'clean-testcache.stderr.log')
    $metadata.cleanTestCacheExitCode = $LASTEXITCODE
    if ($metadata.cleanTestCacheExitCode -ne 0) {
        throw "go clean -testcache failed with exit code $($metadata.cleanTestCacheExitCode)"
    }

    if (-not $SkipResourceMonitor) {
        $resolvedMonitor = (Resolve-Path -LiteralPath $ResourceMonitorPath).ProviderPath
        if (Test-Path -LiteralPath $monitorStopFile) {
            Remove-Item -LiteralPath $monitorStopFile -Force
        }
        $monitorJob = Start-Job -FilePath $resolvedMonitor -ArgumentList @(
            $resourcePath, $monitorStopFile, $ResourceSampleIntervalSeconds, 0, 0
        )
    }
    $metadata.state = 'running'
    Write-JsonAtomically -Path $metadataPath -Value $metadata

    $assignmentNumber = 0
    foreach ($assignment in @($shard.assignments)) {
        $package = [string] $assignment.importPath
        $relativeDirectory = ([string] $assignment.packageDirectoryRelative).Replace('/', [System.IO.Path]::DirectorySeparatorChar)
        if ([System.IO.Path]::IsPathRooted($relativeDirectory) -or
            $relativeDirectory -eq '..' -or
            $relativeDirectory.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar)) {
            throw "unsafe package directory in plan: $relativeDirectory"
        }
        $packageDirectory = [System.IO.Path]::GetFullPath((Join-Path $resolvedRepository $relativeDirectory))
        $binaryName = [System.IO.Path]::GetFileName([string] $assignment.binaryName)
        if ($binaryName -ne [string] $assignment.binaryName) {
            throw "unsafe binary name in plan: $($assignment.binaryName)"
        }
        $binary = (Resolve-Path -LiteralPath (Join-Path $resolvedBinaryDirectory $binaryName)).ProviderPath
        $runExpression = [string] $assignment.runRegex
        if ([string]::IsNullOrWhiteSpace($runExpression)) {
            throw "empty run regex for package $package"
        }
        $arguments = @(
            'tool', 'test2json', '-t', '-p', $package, $binary,
            "-test.timeout=$Timeout", "-test.run=$runExpression", '-test.v=test2json'
        )
        $serialized = Get-WindowsSerializedCommandLine -Executable $resolvedGo -Arguments $arguments
        # Both values include the absolute executable, all arguments, quoting
        # and backslash expansion, separators, and the terminating NUL. Keep a
        # conservative safety margin below CreateProcessW's absolute limit.
        $serializedLengthWithNull = $serialized.Length + 1
        if ($serializedLengthWithNull -gt $conservativeCommandLineLimit) {
            throw "serialized Windows argv is $serializedLengthWithNull characters for $package, above the conservative $conservativeCommandLineLimit-character limit (Win32 absolute $win32AbsoluteCommandLineLimit); refusing implicit batching because it would repeat TestMain/init"
        }
        $stem = ('{0:d2}-{1}' -f $assignmentNumber, ($package -replace '[^A-Za-z0-9_.-]', '_'))
        $stdoutPath = Join-Path $resolvedOutput ($stem + '.jsonl')
        $stderrPath = Join-Path $resolvedOutput ($stem + '.stderr.log')
        $result = Invoke-NativeCapture -Command $resolvedGo -Arguments $arguments -WorkingDirectory $packageDirectory -StdoutPath $stdoutPath -StderrPath $stderrPath
        $result.package = $package
        $result.packageDirectory = $packageDirectory
        $result.binaryPath = $binary
        $result.runRegex = $runExpression
        $result.testCount = @($assignment.tests).Count
        $result.commandLine = $serialized
        $result.serializedLengthWithNull = $serializedLengthWithNull
        $metadata.invocations.Add($result)
        if (Test-Path -LiteralPath $stdoutPath) {
            $content = Get-Content -LiteralPath $stdoutPath -Raw
            if (-not [string]::IsNullOrEmpty($content)) {
                [System.IO.File]::AppendAllText($combinedEventsPath, $content, [System.Text.UTF8Encoding]::new($false))
                if (-not $content.EndsWith("`n")) {
                    [System.IO.File]::AppendAllText($combinedEventsPath, "`n", [System.Text.UTF8Encoding]::new($false))
                }
            }
        }
        if ($result.exitCode -ne 0 -and $finalExitCode -eq 0) {
            $finalExitCode = [int] $result.exitCode
        }
        Write-JsonAtomically -Path $metadataPath -Value $metadata
        $assignmentNumber++
    }
}
catch {
    $metadata.errors.Add($_.Exception.Message)
    if ($finalExitCode -eq 0) {
        $finalExitCode = 2
    }
}
finally {
    if ($null -ne $monitorJob) {
        Write-Utf8File -Path $monitorStopFile -Content ([DateTimeOffset]::UtcNow.ToString('o'))
        Wait-Job -Job $monitorJob -Timeout 30 | Out-Null
        if ($monitorJob.State -eq 'Running') {
            Stop-Job -Job $monitorJob
            $metadata.errors.Add('resource monitor did not stop within 30 seconds')
            if ($finalExitCode -eq 0) { $finalExitCode = 2 }
        }
        Receive-Job -Job $monitorJob -ErrorAction Continue | Out-Null
        Remove-Job -Job $monitorJob -Force
    }
    [Environment]::SetEnvironmentVariable('GOOS', $originalGoEnvironment.GOOS, 'Process')
    [Environment]::SetEnvironmentVariable('GOARCH', $originalGoEnvironment.GOARCH, 'Process')
    [Environment]::SetEnvironmentVariable('CGO_ENABLED', $originalGoEnvironment.CGO_ENABLED, 'Process')
    if ($nativePreferenceExists) {
        $PSNativeCommandUseErrorActionPreference = $originalNativePreference
    }
    $endedAt = [DateTimeOffset]::UtcNow
    $metadata.endTimeUtc = $endedAt.ToString('o')
    $metadata.durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
    $metadata.exitCode = $finalExitCode
    $metadata.state = if ($finalExitCode -eq 0) { 'completed' } else { 'failed' }
    Write-JsonAtomically -Path $metadataPath -Value $metadata
}

exit $finalExitCode
