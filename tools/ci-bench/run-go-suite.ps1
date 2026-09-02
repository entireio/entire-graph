[CmdletBinding()]
param(
    [Alias('ResultsDirectory', 'ResultsDir')]
    [string] $OutputDirectory = (Join-Path (Get-Location) 'ci-bench-results'),

    [string] $RepositoryPath = (Get-Location).Path,

    [string] $RunId = ([DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ')),

    [string] $RunLabel = 'windows-baseline',

    [string] $GoCommand = 'go',

    [string] $Timeout = '30m',

    [string[]] $Packages = @('./...'),

    [string[]] $AdditionalGoTestArguments = @(),

    [string] $ExpectedRepositorySha = '',

    [string] $ExpectedGoVersion = '',

    [ValidateSet('0', '1')]
    [string] $CgoEnabled = '1',

    [switch] $ColdBuildCache,

    [Alias('SkipCompile')]
    [switch] $SkipCompileDecomposition,

    [switch] $SkipResourceMonitor,

    [ValidateRange(0.25, 3600.0)]
    [double] $ResourceSampleIntervalSeconds = 2.0,

    [string] $EnvironmentCollectorPath = (Join-Path $PSScriptRoot 'collect-environment.ps1'),

    [string] $ResourceMonitorPath = (Join-Path $PSScriptRoot 'monitor-resources.ps1')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$nativePreferenceExists = Test-Path -LiteralPath 'variable:PSNativeCommandUseErrorActionPreference'
$originalNativePreference = $null
if ($nativePreferenceExists) {
    $originalNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

function Protect-Text {
    param([AllowNull()][object] $Value)

    if ($null -eq $Value) {
        return $null
    }

    return [regex]::Replace(
        [string] $Value,
        '(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@',
        '$1[REDACTED]@'
    )
}

function Format-CommandLine {
    param(
        [Parameter(Mandatory)]
        [string] $Command,

        [string[]] $Arguments = @()
    )

    $formatted = @($Command)
    foreach ($argument in $Arguments) {
        $safeArgument = Protect-Text $argument
        if ($safeArgument -match '[\s"]') {
            $safeArgument = '"' + ($safeArgument -replace '"', '\"') + '"'
        }
        $formatted += $safeArgument
    }
    return $formatted -join ' '
}

function Write-Utf8File {
    param(
        [Parameter(Mandatory)]
        [string] $Path,

        [AllowEmptyString()]
        [Parameter(Mandatory)]
        [string] $Content
    )

    [System.IO.File]::WriteAllText(
        $Path,
        $Content,
        [System.Text.UTF8Encoding]::new($false)
    )
}

function Write-JsonAtomically {
    param(
        [Parameter(Mandatory)]
        [string] $Path,

        [Parameter(Mandatory)]
        [AllowNull()]
        [object] $Value,

        [ValidateRange(2, 100)]
        [int] $Depth = 12
    )

    $temporaryPath = $Path + '.tmp'
    Write-Utf8File -Path $temporaryPath -Content (ConvertTo-Json -InputObject $Value -Depth $Depth)
    Move-Item -LiteralPath $temporaryPath -Destination $Path -Force
}

function Invoke-NativeTimed {
    param(
        [Parameter(Mandatory)]
        [string] $Command,

        [string[]] $Arguments = @(),

        [Parameter(Mandatory)]
        [string] $WorkingDirectory,

        [Parameter(Mandatory)]
        [string] $StdoutPath,

        [Parameter(Mandatory)]
        [string] $StderrPath,

        [switch] $TeeToHost
    )

    Write-Utf8File -Path $StdoutPath -Content ''
    Write-Utf8File -Path $StderrPath -Content ''

    $startedAt = [DateTimeOffset]::UtcNow
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $nativeExitCode = 127
    $invocationError = $null
    $previousLocation = Get-Location

    try {
        Set-Location -LiteralPath $WorkingDirectory
        if ($TeeToHost) {
            & $Command @Arguments 2> $StderrPath | Tee-Object -FilePath $StdoutPath

            # Capture this immediately. Tee-Object is the final pipeline command,
            # but LASTEXITCODE still belongs to the native Go process.
            $nativeExitCode = $LASTEXITCODE
        }
        else {
            & $Command @Arguments 1> $StdoutPath 2> $StderrPath
            $nativeExitCode = $LASTEXITCODE
        }
    }
    catch {
        $invocationError = $_.Exception.Message
        Write-Utf8File -Path $StderrPath -Content ($invocationError + [Environment]::NewLine)
    }
    finally {
        $stopwatch.Stop()
        Set-Location -LiteralPath $previousLocation.Path
    }

    $endedAt = [DateTimeOffset]::UtcNow
    return [ordered]@{
        commandLine = Format-CommandLine -Command $Command -Arguments $Arguments
        startTimeUtc = $startedAt.ToString('o')
        endTimeUtc = $endedAt.ToString('o')
        durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 6)
        exitCode = $nativeExitCode
        stdoutPath = $StdoutPath
        stderrPath = $StderrPath
        invocationError = $invocationError
    }
}

function Add-RunError {
    param(
        [Parameter(Mandatory)]
        [System.Collections.Generic.List[object]] $List,

        [Parameter(Mandatory)]
        [string] $Phase,

        [Parameter(Mandatory)]
        [string] $Message
    )

    $List.Add([ordered]@{
        phase = $Phase
        timestampUtc = [DateTimeOffset]::UtcNow.ToString('o')
        message = $Message
    }) | Out-Null
}

$resolvedOutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$metadataPath = Join-Path $resolvedOutputDirectory 'run-metadata.json'
$compileMetricsPath = Join-Path $resolvedOutputDirectory 'compile-metrics.json'
$resourceSamplesPath = Join-Path $resolvedOutputDirectory 'resource-samples.jsonl'
$monitorStopFile = Join-Path $resolvedOutputDirectory '.resource-monitor.stop'
$runErrors = [System.Collections.Generic.List[object]]::new()
$steps = [System.Collections.Generic.List[object]]::new()
$compileMetrics = [System.Collections.Generic.List[object]]::new()
$suiteExitCode = $null
$finalExitCode = 1
$fatalError = $null
$monitorJob = $null
$monitorStarted = $false
$resolvedRepositoryPath = $RepositoryPath
$startedAt = [DateTimeOffset]::UtcNow
$endedAt = $null

$testArguments = @('test', '-json', '-timeout', $Timeout)
$testArguments += $AdditionalGoTestArguments
$testArguments += $Packages
$testCommandLine = Format-CommandLine -Command $GoCommand -Arguments $testArguments

$metadata = [ordered]@{
    schemaVersion = 1
    runId = $RunId
    runLabel = $RunLabel
    state = 'initializing'
    phase = 'initializing'
    repositoryPath = $resolvedRepositoryPath
    repositorySha = $null
    expectedRepositorySha = $ExpectedRepositorySha
    expectedGoVersion = $ExpectedGoVersion
    goos = 'windows'
    goarch = 'amd64'
    cgoEnabled = $CgoEnabled
    coldBuildCache = [bool] $ColdBuildCache
    commandLine = $testCommandLine
    startTimeUtc = $startedAt.ToString('o')
    endTimeUtc = $null
    durationSeconds = $null
    goTestExitCode = $null
    exitCode = $null
    outputs = [ordered]@{
        goTestJson = 'go-test.jsonl'
        goTestStderr = 'go-test.stderr.log'
        compileMetrics = 'compile-metrics.json'
        resourceSamples = if ($SkipResourceMonitor) { $null } else { 'resource-samples.jsonl' }
        environment = 'environment.json'
        systemInfo = 'systeminfo.txt'
    }
    steps = $steps
    errors = $runErrors
}

$originalGoEnvironment = [ordered]@{
    GOOS = [Environment]::GetEnvironmentVariable('GOOS', 'Process')
    GOARCH = [Environment]::GetEnvironmentVariable('GOARCH', 'Process')
    CGO_ENABLED = [Environment]::GetEnvironmentVariable('CGO_ENABLED', 'Process')
}

try {
    New-Item -ItemType Directory -Path $resolvedOutputDirectory -Force | Out-Null
    if (Test-Path -LiteralPath $monitorStopFile) {
        Remove-Item -LiteralPath $monitorStopFile -Force
    }

    $resolvedRepositoryPath = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
    $metadata.repositoryPath = $resolvedRepositoryPath
    Write-Utf8File -Path (Join-Path $resolvedOutputDirectory 'run-command.txt') -Content ($testCommandLine + [Environment]::NewLine)
    Write-JsonAtomically -Path $metadataPath -Value $metadata

    if (-not $IsWindows) {
        throw 'run-go-suite.ps1 must be executed by PowerShell 7 on Windows.'
    }

    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = $CgoEnabled

    $metadata.phase = 'validate-environment'
    Write-JsonAtomically -Path $metadataPath -Value $metadata

    $shaResult = Invoke-NativeTimed -Command 'git' -Arguments @('-C', $resolvedRepositoryPath, 'rev-parse', 'HEAD') -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $resolvedOutputDirectory 'repository-sha.txt') -StderrPath (Join-Path $resolvedOutputDirectory 'repository-sha.stderr.log')
    $steps.Add($shaResult) | Out-Null
    if ($shaResult.exitCode -ne 0) {
        throw "Unable to read repository SHA (exit $($shaResult.exitCode))."
    }
    $repositorySha = (Get-Content -LiteralPath $shaResult.stdoutPath -Raw).Trim()
    $metadata.repositorySha = $repositorySha
    if (-not [string]::IsNullOrWhiteSpace($ExpectedRepositorySha) -and
        $repositorySha -ne $ExpectedRepositorySha) {
        throw "Repository SHA '$repositorySha' does not match expected SHA '$ExpectedRepositorySha'."
    }

    $goVersionResult = Invoke-NativeTimed -Command $GoCommand -Arguments @('env', 'GOVERSION') -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $resolvedOutputDirectory 'go-version.txt') -StderrPath (Join-Path $resolvedOutputDirectory 'go-version.stderr.log')
    $steps.Add($goVersionResult) | Out-Null
    if ($goVersionResult.exitCode -ne 0) {
        throw "Unable to read Go version (exit $($goVersionResult.exitCode))."
    }
    $goVersion = (Get-Content -LiteralPath $goVersionResult.stdoutPath -Raw).Trim()
    if (-not [string]::IsNullOrWhiteSpace($ExpectedGoVersion) -and
        $goVersion -ne $ExpectedGoVersion) {
        throw "Go version '$goVersion' does not match expected version '$ExpectedGoVersion'."
    }

    if (-not $SkipResourceMonitor) {
        $resolvedMonitorPath = (Resolve-Path -LiteralPath $ResourceMonitorPath).ProviderPath
        $monitorArguments = @(
            $resourceSamplesPath,
            $monitorStopFile,
            $ResourceSampleIntervalSeconds,
            0,
            0
        )
        $monitorJob = Start-Job -FilePath $resolvedMonitorPath -ArgumentList $monitorArguments
        $monitorStarted = $true
    }

    $metadata.phase = 'clean-test-cache'
    Write-JsonAtomically -Path $metadataPath -Value $metadata
    $testCacheClean = Invoke-NativeTimed -Command $GoCommand -Arguments @('clean', '-testcache') -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $resolvedOutputDirectory 'clean-testcache.stdout.log') -StderrPath (Join-Path $resolvedOutputDirectory 'clean-testcache.stderr.log')
    $steps.Add($testCacheClean) | Out-Null
    if ($testCacheClean.exitCode -ne 0) {
        throw "go clean -testcache failed with exit code $($testCacheClean.exitCode)."
    }

    if ($ColdBuildCache) {
        $metadata.phase = 'clean-build-cache'
        Write-JsonAtomically -Path $metadataPath -Value $metadata
        $buildCacheClean = Invoke-NativeTimed -Command $GoCommand -Arguments @('clean', '-cache') -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $resolvedOutputDirectory 'clean-buildcache.stdout.log') -StderrPath (Join-Path $resolvedOutputDirectory 'clean-buildcache.stderr.log')
        $steps.Add($buildCacheClean) | Out-Null
        if ($buildCacheClean.exitCode -ne 0) {
            throw "go clean -cache failed with exit code $($buildCacheClean.exitCode)."
        }
    }

    $metadata.phase = 'go-test'
    $metadata.state = 'running'
    Write-JsonAtomically -Path $metadataPath -Value $metadata

    $testResult = Invoke-NativeTimed -Command $GoCommand -Arguments $testArguments -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $resolvedOutputDirectory 'go-test.jsonl') -StderrPath (Join-Path $resolvedOutputDirectory 'go-test.stderr.log') -TeeToHost
    $steps.Add($testResult) | Out-Null
    $suiteExitCode = [int] $testResult.exitCode
    $metadata.goTestExitCode = $suiteExitCode
    $finalExitCode = $suiteExitCode

    if ((Get-Item -LiteralPath $testResult.stderrPath).Length -gt 0) {
        Get-Content -LiteralPath $testResult.stderrPath | ForEach-Object {
            [Console]::Error.WriteLine([string] $_)
        }
    }

    if (-not $SkipCompileDecomposition) {
        $binaryDirectory = Join-Path $resolvedOutputDirectory 'test-binaries'
        $compileLogDirectory = Join-Path $resolvedOutputDirectory 'compile-logs'
        New-Item -ItemType Directory -Path $binaryDirectory -Force | Out-Null
        New-Item -ItemType Directory -Path $compileLogDirectory -Force | Out-Null

        $compileTargets = @(
            [ordered]@{ name = 'sem'; package = './internal/sem'; binary = 'sem.test.exe' },
            [ordered]@{ name = 'cli'; package = './internal/cli'; binary = 'cli.test.exe' },
            [ordered]@{ name = 'gitutil'; package = './internal/gitutil'; binary = 'gitutil.test.exe' }
        )

        foreach ($target in $compileTargets) {
            $metadata.phase = "compile-$($target.name)"
            Write-JsonAtomically -Path $metadataPath -Value $metadata

            $binaryPath = Join-Path $binaryDirectory $target.binary
            $compileArguments = @('test', '-vet=off', '-c', $target.package, '-o', $binaryPath)
            $compileResult = Invoke-NativeTimed -Command $GoCommand -Arguments $compileArguments -WorkingDirectory $resolvedRepositoryPath -StdoutPath (Join-Path $compileLogDirectory "$($target.name).stdout.log") -StderrPath (Join-Path $compileLogDirectory "$($target.name).stderr.log")
            $steps.Add($compileResult) | Out-Null

            $binarySize = $null
            if (Test-Path -LiteralPath $binaryPath) {
                $binarySize = (Get-Item -LiteralPath $binaryPath).Length
            }

            $compileMetrics.Add([ordered]@{
                name = $target.name
                package = $target.package
                commandLine = $compileResult.commandLine
                startTimeUtc = $compileResult.startTimeUtc
                endTimeUtc = $compileResult.endTimeUtc
                wallTimeSeconds = $compileResult.durationSeconds
                exitCode = $compileResult.exitCode
                binaryPath = [System.IO.Path]::GetRelativePath($resolvedOutputDirectory, $binaryPath)
                binarySizeBytes = $binarySize
                stdoutPath = [System.IO.Path]::GetRelativePath($resolvedOutputDirectory, $compileResult.stdoutPath)
                stderrPath = [System.IO.Path]::GetRelativePath($resolvedOutputDirectory, $compileResult.stderrPath)
            }) | Out-Null

            if ($compileResult.exitCode -ne 0 -and $finalExitCode -eq 0) {
                $finalExitCode = [int] $compileResult.exitCode
            }
        }
    }

    $metadata.state = if ($finalExitCode -eq 0) { 'completed' } else { 'failed' }
}
catch {
    $fatalError = $_
    Add-RunError -List $runErrors -Phase $metadata.phase -Message $_.Exception.Message
    $metadata.state = 'failed'
    if ($null -eq $suiteExitCode) {
        $finalExitCode = 1
    }
}
finally {
    if ($monitorStarted) {
        try {
            Write-Utf8File -Path $monitorStopFile -Content ([DateTimeOffset]::UtcNow.ToString('o'))
            $completedMonitorJob = Wait-Job -Job $monitorJob -Timeout 30
            if ($null -eq $completedMonitorJob) {
                Stop-Job -Job $monitorJob
                Add-RunError -List $runErrors -Phase 'resource-monitor' -Message 'Resource monitor did not stop within 30 seconds and was terminated.'
            }
            Receive-Job -Job $monitorJob -ErrorAction Continue | Out-Null
        }
        catch {
            Add-RunError -List $runErrors -Phase 'resource-monitor' -Message $_.Exception.Message
        }
        finally {
            if ($null -ne $monitorJob) {
                Remove-Job -Job $monitorJob -Force -ErrorAction SilentlyContinue
            }
        }
    }

    try {
        Write-JsonAtomically -Path $compileMetricsPath -Value @($compileMetrics)
    }
    catch {
        Add-RunError -List $runErrors -Phase 'compile-metadata' -Message $_.Exception.Message
    }

    $endedAt = [DateTimeOffset]::UtcNow
    $metadata.phase = 'finished'
    $metadata.endTimeUtc = $endedAt.ToString('o')
    $metadata.durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
    $metadata.goTestExitCode = $suiteExitCode
    $metadata.exitCode = $finalExitCode
    $metadata.steps = $steps
    $metadata.errors = $runErrors

    try {
        $resolvedCollectorPath = (Resolve-Path -LiteralPath $EnvironmentCollectorPath).ProviderPath
        $collectorArguments = @{
            OutputDirectory = $resolvedOutputDirectory
            RepositoryPath = $resolvedRepositoryPath
            RunId = $RunId
            RunLabel = $RunLabel
            CommandLine = $testCommandLine
            StartTimeUtc = $startedAt.ToString('o')
            EndTimeUtc = $endedAt.ToString('o')
            ExitCode = $finalExitCode
        }
        & $resolvedCollectorPath @collectorArguments
    }
    catch {
        Add-RunError -List $runErrors -Phase 'environment-capture' -Message $_.Exception.Message
    }

    try {
        Write-JsonAtomically -Path $metadataPath -Value $metadata
    }
    finally {
        foreach ($entry in $originalGoEnvironment.GetEnumerator()) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
        }
        if ($nativePreferenceExists) {
            $PSNativeCommandUseErrorActionPreference = $originalNativePreference
        }
    }
}

if ($null -ne $fatalError) {
    [Console]::Error.WriteLine($fatalError.Exception.Message)
}

exit $finalExitCode
