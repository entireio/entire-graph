[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $RepositoryPath,
    [Parameter(Mandatory)] [string] $CrossArtifactRoot,
    [Parameter(Mandatory)] [string] $OutputDirectory,
    [Parameter(Mandatory)] [string] $BaselineJsonPath,
    [Parameter(Mandatory)] [string] $EvidenceHelperPath,
    [Parameter(Mandatory)] [string] $ResourceMonitorPath,
    [Parameter(Mandatory)] [string] $WorkerPath,
    [string] $GoCommand = 'go',
    [string] $PythonCommand = 'python',
    [string] $ObjdumpCommand = 'objdump',
    [string] $PowerShellCommand = 'pwsh',
    [ValidateRange(1300, 3600)] [int] $CandidateWatchdogSeconds = 1600
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-JsonFile {
    param([string] $Path, [object] $Value)
    [IO.File]::WriteAllText(
        $Path,
        (($Value | ConvertTo-Json -Depth 40) + [Environment]::NewLine),
        [Text.UTF8Encoding]::new($false)
    )
}

function Invoke-CheckedProcess {
    param(
        [string] $FilePath,
        [string[]] $ArgumentList,
        [string] $WorkingDirectory,
        [string] $StdoutPath,
        [string] $StderrPath
    )
    $started = [DateTimeOffset]::UtcNow
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList `
        -WorkingDirectory $WorkingDirectory -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath
    $clock.Stop()
    $result = [ordered]@{
        executable = (Get-Command $FilePath -ErrorAction Stop).Source
        arguments = @($ArgumentList)
        workingDirectory = $WorkingDirectory
        startTimeUtc = $started.ToString('o')
        durationSeconds = [Math]::Round($clock.Elapsed.TotalSeconds, 6)
        exitCode = $process.ExitCode
        stdout = $StdoutPath
        stderr = $StderrPath
    }
    if ($process.ExitCode -ne 0) {
        throw "process failed with exit $($process.ExitCode): $FilePath $($ArgumentList -join ' ')"
    }
    return $result
}

function Invoke-TestList {
    param(
        [string] $GoExecutable,
        [string] $Package,
        [string] $Binary,
        [string] $WorkingDirectory,
        [string] $OutputPath,
        [string] $ErrorPath
    )
    $absoluteBinary = [IO.Path]::GetFullPath($Binary)
    $result = Invoke-CheckedProcess -FilePath $GoExecutable `
        -ArgumentList @('tool', 'test2json', '-t', '-p', $Package, $absoluteBinary,
            '-test.v=test2json', '-test.list=.', '-test.timeout=30m') `
        -WorkingDirectory $WorkingDirectory -StdoutPath $OutputPath -StderrPath $ErrorPath
    $names = [Collections.Generic.List[string]]::new()
    foreach ($line in Get-Content -LiteralPath $OutputPath -Encoding utf8) {
        if (-not $line.Trim()) { continue }
        $event = $line | ConvertFrom-Json
        if ($event.PSObject.Properties.Match('Output').Count -eq 0) { continue }
        $name = ([string] $event.Output).TrimEnd("`r", "`n")
        if ($name -match '^(Test|Benchmark|Fuzz|Example)') { $names.Add($name) }
    }
    return [ordered]@{ process = $result; tests = @($names) }
}

if (-not $IsWindows) { throw 'native fidelity requires PowerShell 7 on Windows' }
$repo = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
$artifactRoot = (Resolve-Path -LiteralPath $CrossArtifactRoot).ProviderPath
$baseline = (Resolve-Path -LiteralPath $BaselineJsonPath).ProviderPath
$helper = (Resolve-Path -LiteralPath $EvidenceHelperPath).ProviderPath
$monitor = (Resolve-Path -LiteralPath $ResourceMonitorPath).ProviderPath
$worker = (Resolve-Path -LiteralPath $WorkerPath).ProviderPath
$output = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $output) { Remove-Item -LiteralPath $output -Recurse -Force }
New-Item -ItemType Directory -Path $output -Force | Out-Null

$goExe = (Get-Command $GoCommand -ErrorAction Stop).Source
$pythonExe = (Get-Command $PythonCommand -ErrorAction Stop).Source
$objdumpExe = (Get-Command $ObjdumpCommand -ErrorAction Stop).Source
$pwshExe = (Get-Command $PowerShellCommand -ErrorAction Stop).Source
if ((git -C $repo rev-parse HEAD).Trim() -ne 'ee6468a6a49d9b2a1a828bd276792f415f392185') {
    throw 'product checkout is not pinned to ee6468a6'
}
if ((& $goExe env GOVERSION).Trim() -ne 'go1.26.7') { throw 'Go must be go1.26.7' }
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'
$nativeCompilerVersion = ((& $env:CC --version | Select-Object -First 1) -as [string]).Trim()
$nativeCxxVersion = ((& $env:CXX --version | Select-Object -First 1) -as [string]).Trim()

$manifestFile = Get-ChildItem -LiteralPath $artifactRoot -Filter manifest.json -Recurse |
    Sort-Object FullName | Select-Object -First 1
if ($null -eq $manifestFile) { throw 'cross manifest is absent' }
$manifest = Get-Content -LiteralPath $manifestFile.FullName -Raw | ConvertFrom-Json
if ($manifest.packageCount -ne 8) { throw "expected eight package test binaries, got $($manifest.packageCount)" }

$steps = [Collections.Generic.List[object]]::new()
$stopFile = Join-Path $output '.monitor.stop'
$resourcePath = Join-Path $output 'resource-samples.jsonl'
$monitorProcess = Start-Process -FilePath $pwshExe `
    -ArgumentList @('-NoProfile', '-File', $monitor, '-OutputPath', $resourcePath,
        '-IntervalSeconds', '1', '-StopFile', $stopFile) -NoNewWindow -PassThru
$started = [DateTimeOffset]::UtcNow
try {
    $nativeRaw = Join-Path $output 'target-go-list.native.raw.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $goExe -ArgumentList @('list', '-json', './...') `
        -WorkingDirectory $repo -StdoutPath $nativeRaw `
        -StderrPath (Join-Path $output 'target-go-list.native.stderr.log'))) | Out-Null
    $nativeTarget = Join-Path $output 'target-files.native.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'normalize-target', '--input', $nativeRaw, '--output', $nativeTarget) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $output 'normalize-target.stdout.log') `
        -StderrPath (Join-Path $output 'normalize-target.stderr.log'))) | Out-Null
    $targetCompare = Join-Path $output 'target-files-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-target', '--baseline', $nativeTarget,
            '--candidate', (Join-Path $manifestFile.Directory.FullName 'target-files.cross.json'),
            '--output', $targetCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $output 'compare-target.stdout.log') `
        -StderrPath (Join-Path $output 'compare-target.stderr.log'))) | Out-Null

    $nativeBinaryRoot = Join-Path $output 'native-test-binaries'
    $nativeListRoot = Join-Path $output 'native-test-lists'
    $crossListRoot = Join-Path $output 'cross-test-lists'
    $peRoot = Join-Path $output 'native-pe'
    New-Item -ItemType Directory -Path $nativeBinaryRoot, $nativeListRoot, $crossListRoot, $peRoot -Force | Out-Null
    $nativeLists = [ordered]@{}
    $crossLists = [ordered]@{}
    $linkResults = [Collections.Generic.List[object]]::new()
    $nativeRunPackages = [Collections.Generic.List[object]]::new()
    $nativeDependencies = [Collections.Generic.List[object]]::new()
    foreach ($package in $manifest.packages) {
        $fileName = Split-Path $package.binary -Leaf
        $nativeBinary = Join-Path $nativeBinaryRoot $fileName
        $packageDirectory = [IO.Path]::GetFullPath((Join-Path $repo $package.packageDirectory))
        $link = Invoke-CheckedProcess -FilePath $goExe `
            -ArgumentList @('test', '-vet=off', '-c', $package.importPath, '-o', $nativeBinary) `
            -WorkingDirectory $repo -StdoutPath (Join-Path $nativeListRoot "$fileName.link.stdout.log") `
            -StderrPath (Join-Path $nativeListRoot "$fileName.link.stderr.log")
        $linkResults.Add([ordered]@{ package = $package.importPath; process = $link }) | Out-Null
        $nativeRunPackages.Add([ordered]@{
            package = $package.importPath
            mode = 'full'
            tests = @()
            estimatedSeconds = 0
            binary = [IO.Path]::GetFullPath($nativeBinary)
            packageDirectory = $packageDirectory
        }) | Out-Null
        $steps.Add($link) | Out-Null

        $buildInfoPath = Join-Path $peRoot "$fileName.go-version-m.txt"
        $steps.Add((Invoke-CheckedProcess -FilePath $goExe `
            -ArgumentList @('version', '-m', $nativeBinary) -WorkingDirectory $packageDirectory `
            -StdoutPath $buildInfoPath -StderrPath (Join-Path $peRoot "$fileName.go-version-m.stderr.log"))) | Out-Null
        $buildInfo = Get-Content -LiteralPath $buildInfoPath -Raw
        foreach ($setting in @('GOOS=windows', 'GOARCH=amd64', 'CGO_ENABLED=1')) {
            if (-not $buildInfo.Contains($setting)) { throw "native test link lacks $setting for $($package.importPath)" }
        }
        $objdumpPath = Join-Path $peRoot "$fileName.objdump.txt"
        $steps.Add((Invoke-CheckedProcess -FilePath $objdumpExe -ArgumentList @('-p', $nativeBinary) `
            -WorkingDirectory $packageDirectory -StdoutPath $objdumpPath `
            -StderrPath (Join-Path $peRoot "$fileName.objdump.stderr.log"))) | Out-Null
        $imports = @(Select-String -LiteralPath $objdumpPath -Pattern 'DLL Name:\s*(\S+)' |
            ForEach-Object { $_.Matches[0].Groups[1].Value })
        $nativeDependencies.Add([ordered]@{ package = $package.importPath; imports = $imports }) | Out-Null

        $nativeListed = Invoke-TestList -GoExecutable $goExe -Package $package.importPath `
            -Binary $nativeBinary -WorkingDirectory $packageDirectory `
            -OutputPath (Join-Path $nativeListRoot "$fileName.jsonl") `
            -ErrorPath (Join-Path $nativeListRoot "$fileName.stderr.log")
        $steps.Add($nativeListed.process) | Out-Null
        $nativeLists[$package.importPath] = $nativeListed.tests

        $crossBinary = [IO.Path]::GetFullPath((Join-Path $manifestFile.Directory.FullName $package.binary))
        $crossListed = Invoke-TestList -GoExecutable $goExe -Package $package.importPath `
            -Binary $crossBinary -WorkingDirectory $packageDirectory `
            -OutputPath (Join-Path $crossListRoot "$fileName.jsonl") `
            -ErrorPath (Join-Path $crossListRoot "$fileName.stderr.log")
        $steps.Add($crossListed.process) | Out-Null
        $crossLists[$package.importPath] = $crossListed.tests
    }
    Write-JsonFile -Path (Join-Path $output 'native-pe-dependencies.json') -Value $nativeDependencies
    $nativeListsPath = Join-Path $output 'test-list.native.json'
    $crossListsPath = Join-Path $output 'test-list.cross.json'
    Write-JsonFile -Path $nativeListsPath -Value $nativeLists
    Write-JsonFile -Path $crossListsPath -Value $crossLists
    $listCompare = Join-Path $output 'test-list-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-test-lists', '--baseline', $nativeListsPath,
            '--candidate', $crossListsPath, '--output', $listCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $output 'compare-lists.stdout.log') `
        -StderrPath (Join-Path $output 'compare-lists.stderr.log'))) | Out-Null

    $nativeChecks = [ordered]@{}
    foreach ($check in @(
        @{ name = 'build'; arguments = @('build', './...') },
        @{ name = 'vet'; arguments = @('vet', './...') }
    )) {
        $result = Invoke-CheckedProcess -FilePath $goExe -ArgumentList $check.arguments `
            -WorkingDirectory $repo -StdoutPath (Join-Path $output "$($check.name).stdout.log") `
            -StderrPath (Join-Path $output "$($check.name).stderr.log")
        $nativeChecks[$check.name] = $result
        $steps.Add($result) | Out-Null
    }

    # A matched native diagnostic launches the already-linked package test
    # binaries with the same eight-process shape as the cross parallel
    # diagnostic. No compilation occurs within this timed interval.
    $parallelOutput = Join-Path $output 'parallel-unsharded-native-diagnostic'
    New-Item -ItemType Directory -Path $parallelOutput -Force | Out-Null
    $parallelProcesses = [Collections.Generic.List[object]]::new()
    $parallelStopFile = Join-Path $parallelOutput '.monitor.stop'
    $parallelResources = Join-Path $parallelOutput 'resource-samples.jsonl'
    $parallelMonitor = Start-Process -FilePath $pwshExe `
        -ArgumentList @('-NoProfile', '-File', $monitor, '-OutputPath', $parallelResources,
            '-IntervalSeconds', '1', '-StopFile', $parallelStopFile) -NoNewWindow -PassThru
    $parallelStarted = [DateTimeOffset]::UtcNow
    try {
        $parallelIndex = 0
        foreach ($package in $nativeRunPackages) {
            $parallelIndex++
            $workerOutput = Join-Path $parallelOutput ('shard-{0:d2}' -f $parallelIndex)
            New-Item -ItemType Directory -Path $workerOutput -Force | Out-Null
            $workerPlan = Join-Path $workerOutput 'worker-plan.json'
            Write-JsonFile -Path $workerPlan -Value ([ordered]@{
                index = $parallelIndex
                estimatedSeconds = 0
                packages = @($package)
            })
            $workerStdout = Join-Path $workerOutput 'worker.stdout.log'
            $workerStderr = Join-Path $workerOutput 'worker.stderr.log'
            $process = Start-Process -FilePath $pwshExe -ArgumentList @(
                '-NoProfile', '-File', $worker, '-PlanPath', $workerPlan,
                '-OutputDirectory', $workerOutput, '-GoCommand', $goExe
            ) -WorkingDirectory $repo -NoNewWindow -PassThru `
                -RedirectStandardOutput $workerStdout -RedirectStandardError $workerStderr
            $parallelProcesses.Add([ordered]@{
                index = $parallelIndex
                process = $process
                output = $workerOutput
                stdout = $workerStdout
                stderr = $workerStderr
            }) | Out-Null
        }
        $deadline = [DateTimeOffset]::UtcNow.AddSeconds($CandidateWatchdogSeconds)
        foreach ($entry in $parallelProcesses) {
            $remaining = $deadline - [DateTimeOffset]::UtcNow
            $remainingMilliseconds = [Math]::Floor($remaining.TotalMilliseconds)
            if ($remainingMilliseconds -le 0 -or
                -not $entry.process.WaitForExit([Math]::Min([int]::MaxValue, [int64] $remainingMilliseconds))) {
                foreach ($running in $parallelProcesses | Where-Object { -not $_.process.HasExited }) {
                    $running.process.Kill($true)
                }
                throw "native parallel diagnostic exceeded the $CandidateWatchdogSeconds-second watchdog"
            }
        }
    }
    finally {
        New-Item -ItemType File -Path $parallelStopFile -Force | Out-Null
        if (-not $parallelMonitor.WaitForExit(30000)) { $parallelMonitor.Kill($true) }
    }
    $parallelEnded = [DateTimeOffset]::UtcNow
    $parallelCombined = Join-Path $parallelOutput 'go-test.native-parallel.jsonl'
    [IO.File]::WriteAllText($parallelCombined, '', [Text.UTF8Encoding]::new($false))
    $parallelWorkers = [Collections.Generic.List[object]]::new()
    foreach ($entry in $parallelProcesses | Sort-Object index) {
        $workerSummaryPath = Join-Path $entry.output 'worker-summary.json'
        if (-not (Test-Path -LiteralPath $workerSummaryPath -PathType Leaf)) {
            throw "native parallel shard $($entry.index) did not produce a summary"
        }
        $workerSummary = Get-Content -LiteralPath $workerSummaryPath -Raw | ConvertFrom-Json
        if (-not $workerSummary.nonempty -or $workerSummary.exitCode -ne 0 -or $entry.process.ExitCode -ne 0) {
            throw "native parallel shard $($entry.index) failed or was empty"
        }
        $parallelWorkers.Add($workerSummary) | Out-Null
        [IO.File]::AppendAllText(
            $parallelCombined,
            [IO.File]::ReadAllText($workerSummary.combinedJson, [Text.UTF8Encoding]::new($false)),
            [Text.UTF8Encoding]::new($false)
        )
    }
    $parallelPackageInvocationCount = @($parallelWorkers.packageInvocations).Count
    if ($parallelWorkers.Count -ne 8 -or $parallelPackageInvocationCount -ne 8) {
        throw "native parallel diagnostic did not execute exactly eight package binaries once"
    }
    $parallelCompare = Join-Path $parallelOutput 'dynamic-events-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-dynamic', '--baseline', $baseline,
            '--candidate', $parallelCombined, '--output', $parallelCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $parallelOutput 'compare-dynamic.stdout.log') `
        -StderrPath (Join-Path $parallelOutput 'compare-dynamic.stderr.log'))) | Out-Null
    $parallelAssertions = Join-Path $parallelOutput 'dynamic-assertions.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'assert-dynamic-passes', '--input', $parallelCombined,
            '--require-pass', 'github.com/entireio/entire-graph/cmd/entire-graph::TestFatalErrorEscapesTerminalControlBytes',
            '--require-pass', 'github.com/entireio/entire-graph/internal/sem::TestTreeSitterParserMultiLanguageEntities',
            '--output', $parallelAssertions) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $parallelOutput 'assert-dynamic.stdout.log') `
        -StderrPath (Join-Path $parallelOutput 'assert-dynamic.stderr.log'))) | Out-Null
    $parallelDiagnostic = [ordered]@{
        label = 'parallel-unsharded-native-diagnostic'
        compilationInsideTimedInterval = $false
        startTimeUtc = $parallelStarted.ToString('o')
        endTimeUtc = $parallelEnded.ToString('o')
        durationSeconds = ($parallelEnded - $parallelStarted).TotalSeconds
        longestShardSeconds = (@($parallelWorkers.durationSeconds) | Measure-Object -Maximum).Maximum
        packageInvocationCount = $parallelPackageInvocationCount
        allShardExitCodesZero = -not @($parallelWorkers | Where-Object { $_.exitCode -ne 0 })
        dynamicEventsEquivalent = (Get-Content -LiteralPath $parallelCompare -Raw | ConvertFrom-Json).equivalent
        childReexecAndTreeSitterCgoVerified = (Get-Content -LiteralPath $parallelAssertions -Raw | ConvertFrom-Json).accepted
        workers = $parallelWorkers
    }
    Write-JsonFile -Path (Join-Path $parallelOutput 'candidate-summary.json') -Value $parallelDiagnostic
}
finally {
    New-Item -ItemType File -Path $stopFile -Force | Out-Null
    if (-not $monitorProcess.WaitForExit(30000)) { $monitorProcess.Kill($true) }
}
$ended = [DateTimeOffset]::UtcNow

Write-JsonFile -Path (Join-Path $output 'summary.json') -Value ([ordered]@{
    schema = 'entire-graph.windows-ci.a3.native-fidelity.v1'
    repositorySha = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
    goVersion = 'go1.26.7'
    target = [ordered]@{ goos = 'windows'; goarch = 'amd64'; cgoEnabled = '1' }
    nativeCompiler = [ordered]@{ cc = $nativeCompilerVersion; cxx = $nativeCxxVersion }
    cacheState = 'warm native Windows Go build cache; native test links/build/vet are fidelity gates, not candidate timing'
    startTimeUtc = $started.ToString('o')
    endTimeUtc = $ended.ToString('o')
    durationSeconds = ($ended - $started).TotalSeconds
    nativeTestLinkPackageCount = $linkResults.Count
    nativeTestLinks = $linkResults
    nativeChecks = $nativeChecks
    targetFilesEquivalent = (Get-Content -LiteralPath $targetCompare -Raw | ConvertFrom-Json).equivalent
    topLevelTestListsEquivalent = (Get-Content -LiteralPath $listCompare -Raw | ConvertFrom-Json).equivalent
    parallelUnshardedNativeDiagnostic = $parallelDiagnostic
    steps = $steps
})
