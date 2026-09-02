[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $RepositoryPath,
    [Parameter(Mandatory)] [string] $CrossArtifactRoot,
    [Parameter(Mandatory)] [string] $OutputDirectory,
    [Parameter(Mandatory)] [string] $BaselineJsonPath,
    [string] $GoCommand = 'go',
    [string] $PythonCommand = 'python',
    [string] $PowerShellCommand = 'pwsh',
    [string] $EvidenceHelperPath = (Join-Path $PSScriptRoot 'compare-evidence.py'),
    [string] $PartitionHelperPath = (Join-Path $PSScriptRoot 'partition-tests.py'),
    [string] $WorkerPath = (Join-Path $PSScriptRoot 'run-cross-shard-worker.ps1'),
    [string] $ResourceMonitorPath = (Join-Path (Split-Path $PSScriptRoot -Parent) 'monitor-resources.ps1'),
    [string] $AzCopyCommand = '',
    [string] $CheckpointUploadRoot = '',
    [ValidateRange(1300, 3600)] [int] $CandidateWatchdogSeconds = 1600,
    [int[]] $ScreenShardCounts = @(4, 6, 8),
    [int] $ScreenOrderSeed = 0,
    [ValidateRange(3, 10)] [int] $RepeatCount = 3
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

function Write-AtomicJsonFile {
    param([string] $Path, [object] $Value)
    $temporary = "$Path.$PID.tmp"
    [IO.File]::WriteAllText(
        $temporary,
        (($Value | ConvertTo-Json -Depth 40) + [Environment]::NewLine),
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::Move($temporary, $Path, $true)
}

function ConvertTo-WindowsCommandLineToken {
    param(
        [AllowEmptyString()] [string] $Value,
        [switch] $ForceQuotes
    )
    if (-not $ForceQuotes -and $Value.Length -gt 0 -and $Value -notmatch '[\s"]') { return $Value }
    $builder = [Text.StringBuilder]::new()
    [void] $builder.Append('"')
    $backslashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq [char] '\') {
            $backslashes++
        }
        elseif ($character -eq [char] '"') {
            [void] $builder.Append([char] '\', (2 * $backslashes) + 1)
            [void] $builder.Append('"')
            $backslashes = 0
        }
        else {
            if ($backslashes -gt 0) { [void] $builder.Append([char] '\', $backslashes) }
            [void] $builder.Append($character)
            $backslashes = 0
        }
    }
    if ($backslashes -gt 0) { [void] $builder.Append([char] '\', 2 * $backslashes) }
    [void] $builder.Append('"')
    return $builder.ToString()
}

function Get-WindowsCommandLineCharacters {
    param([string] $Executable, [string[]] $Arguments)
    $serialized = @((ConvertTo-WindowsCommandLineToken $Executable -ForceQuotes))
    $serialized += @($Arguments | ForEach-Object { ConvertTo-WindowsCommandLineToken $_ })
    return (($serialized -join ' ').Length + 1)
}

function Invoke-CheckedProcess {
    param(
        [string] $FilePath,
        [string[]] $ArgumentList,
        [string] $WorkingDirectory,
        [string] $StdoutPath,
        [string] $StderrPath
    )
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList `
        -WorkingDirectory $WorkingDirectory -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath
    $clock.Stop()
    if ($process.ExitCode -ne 0) {
        throw "process failed with exit $($process.ExitCode): $FilePath $($ArgumentList -join ' ')"
    }
    return [ordered]@{ durationSeconds = $clock.Elapsed.TotalSeconds; exitCode = $process.ExitCode }
}

function Read-TestListFromJson {
    param(
        [string] $Path,
        [string] $Pattern
    )
    $names = [Collections.Generic.List[string]]::new()
    foreach ($line in Get-Content -LiteralPath $Path -Encoding utf8) {
        if (-not $line.Trim()) { continue }
        $event = $line | ConvertFrom-Json
        if ($event.PSObject.Properties.Match('Output').Count -eq 0) { continue }
        $name = ([string] $event.Output).TrimEnd("`r", "`n")
        if ($name -match $Pattern) { $names.Add($name) }
    }
    return @($names)
}

if (-not $IsWindows) { throw 'weighted cross shards require PowerShell 7 on Windows' }
$repo = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
$artifactRoot = (Resolve-Path -LiteralPath $CrossArtifactRoot).ProviderPath
$baseline = (Resolve-Path -LiteralPath $BaselineJsonPath).ProviderPath
$evidenceHelper = (Resolve-Path -LiteralPath $EvidenceHelperPath).ProviderPath
$partitionHelper = (Resolve-Path -LiteralPath $PartitionHelperPath).ProviderPath
$worker = (Resolve-Path -LiteralPath $WorkerPath).ProviderPath
$monitor = (Resolve-Path -LiteralPath $ResourceMonitorPath).ProviderPath
$azcopyExe = if ($AzCopyCommand) { (Get-Command $AzCopyCommand -ErrorAction Stop).Source } else { '' }
if ([bool] $CheckpointUploadRoot -ne [bool] $azcopyExe) {
    throw 'AzCopyCommand and CheckpointUploadRoot must be supplied together'
}
$output = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $output) { Remove-Item -LiteralPath $output -Recurse -Force }
New-Item -ItemType Directory -Path $output -Force | Out-Null

$goExe = (Get-Command $GoCommand -ErrorAction Stop).Source
$pythonExe = (Get-Command $PythonCommand -ErrorAction Stop).Source
$pwshExe = (Get-Command $PowerShellCommand -ErrorAction Stop).Source
if ((git -C $repo rev-parse HEAD).Trim() -ne 'ee6468a6a49d9b2a1a828bd276792f415f392185') {
    throw 'product checkout is not pinned to ee6468a6'
}
if ((& $goExe env GOVERSION).Trim() -ne 'go1.26.7') { throw 'Go must be go1.26.7' }
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'

$manifestFiles = @(Get-ChildItem -LiteralPath $artifactRoot -Filter manifest.json -Recurse |
    Sort-Object FullName)
if ($manifestFiles.Count -lt $RepeatCount) {
    throw "need at least $RepeatCount independent cross artifact manifests"
}
$manifests = @($manifestFiles | ForEach-Object {
    $manifest = Get-Content -LiteralPath $_.FullName -Raw | ConvertFrom-Json
    if ($manifest.runtimeLinkage -ne 'static-mingw-support' -or @($manifest.runtimeDlls).Count -ne 0) {
        throw "manifest is not child-reexec-safe static MinGW linkage: $($_.FullName)"
    }
    foreach ($package in $manifest.packages) {
        $binary = [IO.Path]::GetFullPath((Join-Path $_.Directory.FullName $package.binary))
        $actual = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $package.sha256) { throw "cross artifact checksum mismatch: $binary" }
    }
    [pscustomobject]@{ file = $_; value = $manifest }
})

# Generate the fail-closed inventory from the compiled binary itself. The list
# probe is outside candidate timing and is explicitly included in the TestMain
# multiplicity audit below.
$inventory = [ordered]@{}
$fullInventory = [ordered]@{}
$inventoryDir = Join-Path $output 'compiled-inventory'
New-Item -ItemType Directory -Path $inventoryDir -Force | Out-Null
$first = $manifests[0]
foreach ($package in $first.value.packages) {
    $binary = [IO.Path]::GetFullPath((Join-Path $first.file.Directory.FullName $package.binary))
    $fileName = Split-Path $binary -Leaf
    $stdout = Join-Path $inventoryDir "$fileName.tests.jsonl"
    $stderr = Join-Path $inventoryDir "$fileName.tests.stderr.log"
    Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('tool', 'test2json', '-t', '-p', $package.importPath, $binary,
            '-test.v=test2json', '-test.list=^Test', '-test.timeout=30m') `
        -WorkingDirectory (Join-Path $repo $package.packageDirectory) `
        -StdoutPath $stdout -StderrPath $stderr | Out-Null
    $inventory[$package.importPath] = @(Read-TestListFromJson -Path $stdout -Pattern '^Test')
    $fullStdout = Join-Path $inventoryDir "$fileName.full.jsonl"
    $fullStderr = Join-Path $inventoryDir "$fileName.full.stderr.log"
    Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('tool', 'test2json', '-t', '-p', $package.importPath, $binary,
            '-test.v=test2json', '-test.list=.', '-test.timeout=30m') `
        -WorkingDirectory (Join-Path $repo $package.packageDirectory) `
        -StdoutPath $fullStdout -StderrPath $fullStderr | Out-Null
    $fullInventory[$package.importPath] = @(Read-TestListFromJson `
        -Path $fullStdout -Pattern '^(Test|Benchmark|Fuzz|Example)')
}
$inventoryPath = Join-Path $output 'compiled-test-inventory.json'
Write-JsonFile -Path $inventoryPath -Value $inventory
$fullInventoryPath = Join-Path $output 'compiled-full-list-audit.json'
Write-JsonFile -Path $fullInventoryPath -Value $fullInventory

$heavyPackages = @(
    'github.com/entireio/entire-graph/internal/sem',
    'github.com/entireio/entire-graph/internal/cli',
    'github.com/entireio/entire-graph/internal/gitutil'
)
$plans = @{}
foreach ($count in $ScreenShardCounts) {
    if ($count -notin @(4, 6, 8)) { throw "unsupported shard screen count: $count" }
    $planPath = Join-Path $output "weighted-plan-$count.json"
    $arguments = @($partitionHelper, '--baseline', $baseline, '--inventory', $inventoryPath,
        '--full-inventory', $fullInventoryPath,
        '--output', $planPath, '--shards', [string] $count)
    foreach ($package in $heavyPackages) { $arguments += @('--heavy-package', $package) }
    Invoke-CheckedProcess -FilePath $pythonExe -ArgumentList $arguments `
        -WorkingDirectory $repo `
        -StdoutPath (Join-Path $output "partition-$count.stdout.log") `
        -StderrPath (Join-Path $output "partition-$count.stderr.log") | Out-Null
    $plans[$count] = $planPath
}
$effectiveScreenOrderSeed = $ScreenOrderSeed
if ($effectiveScreenOrderSeed -eq 0) {
    $effectiveScreenOrderSeed = [Security.Cryptography.RandomNumberGenerator]::GetInt32(1, [int]::MaxValue)
}
$screenOrder = @($ScreenShardCounts)
$screenRandom = [Random]::new($effectiveScreenOrderSeed)
for ($index = $screenOrder.Count - 1; $index -gt 0; $index--) {
    $swapIndex = $screenRandom.Next($index + 1)
    $temporary = $screenOrder[$index]
    $screenOrder[$index] = $screenOrder[$swapIndex]
    $screenOrder[$swapIndex] = $temporary
}
$parallelPlanPath = Join-Path $output 'parallel-unsharded-plan.json'
$parallelBins = [Collections.Generic.List[object]]::new()
$parallelIndex = 0
foreach ($package in $first.value.packages | Sort-Object importPath) {
    $parallelIndex++
    $parallelBins.Add([ordered]@{
        index = $parallelIndex
        estimatedSeconds = 0
        packages = @([ordered]@{
            package = $package.importPath
            mode = 'full'
            tests = @()
            estimatedSeconds = 0
        })
    }) | Out-Null
}
if ($parallelBins.Count -gt [Environment]::ProcessorCount) {
    throw "parallel-unsharded diagnostic would exceed default-like package concurrency: $($parallelBins.Count) packages on $([Environment]::ProcessorCount) processors"
}
Write-JsonFile -Path $parallelPlanPath -Value ([ordered]@{
    schema = 'entire-graph.windows-ci.a3.parallel-unsharded-plan.v1'
    shardCount = $parallelBins.Count
    concurrencyModel = 'one process per package, bounded by the eight test packages on the eight-vCPU executor'
    processorCount = [Environment]::ProcessorCount
    coverageEquivalent = $true
    bins = $parallelBins
})

function Invoke-ShardedCandidate {
    param(
        [string] $Label,
        [string] $PlanPath,
        [object] $ManifestRecord
    )
    $candidateOutput = Join-Path $output $Label
    New-Item -ItemType Directory -Path $candidateOutput -Force | Out-Null
    $plan = Get-Content -LiteralPath $PlanPath -Raw | ConvertFrom-Json
    $packageMap = @{}
    foreach ($package in $ManifestRecord.value.packages) { $packageMap[$package.importPath] = $package }

    Invoke-CheckedProcess -FilePath $goExe -ArgumentList @('clean', '-testcache') `
        -WorkingDirectory $repo `
        -StdoutPath (Join-Path $candidateOutput 'clean-testcache.stdout.log') `
        -StderrPath (Join-Path $candidateOutput 'clean-testcache.stderr.log') | Out-Null

    $workerProcesses = [Collections.Generic.List[object]]::new()
    $stopFile = Join-Path $candidateOutput '.monitor.stop'
    $resourcePath = Join-Path $candidateOutput 'resource-samples.jsonl'
    $monitorProcess = Start-Process -FilePath $pwshExe `
        -ArgumentList @('-NoProfile', '-File', $monitor, '-OutputPath', $resourcePath,
            '-IntervalSeconds', '1', '-StopFile', $stopFile) -NoNewWindow -PassThru
    $started = [DateTimeOffset]::UtcNow
    try {
        foreach ($bin in $plan.bins) {
            if (@($bin.packages).Count -eq 0) { throw "empty shard $($bin.index)" }
            $workerOutput = Join-Path $candidateOutput ('shard-{0:d2}' -f $bin.index)
            New-Item -ItemType Directory -Path $workerOutput -Force | Out-Null
            $workerPackages = @($bin.packages | ForEach-Object {
                $package = $packageMap[$_.package]
                if ($null -eq $package) { throw "package absent from manifest: $($_.package)" }
                [ordered]@{
                    package = $_.package
                    mode = $_.mode
                    tests = @($_.tests)
                    estimatedSeconds = $_.estimatedSeconds
                    binary = [IO.Path]::GetFullPath((Join-Path $ManifestRecord.file.Directory.FullName $package.binary))
                    packageDirectory = [IO.Path]::GetFullPath((Join-Path $repo $package.packageDirectory))
                }
            })
            foreach ($workerPackage in $workerPackages) {
                $preflightArgs = @('tool', 'test2json', '-t', '-p', $workerPackage.package,
                    $workerPackage.binary, '-test.v=test2json', '-test.timeout=30m')
                if ($workerPackage.mode -eq 'selected') {
                    $escapedTests = @($workerPackage.tests | ForEach-Object { [regex]::Escape([string] $_) })
                    $preflightArgs += '-test.run=^(?:' + ($escapedTests -join '|') + ')$'
                }
                $preflightCharacters = Get-WindowsCommandLineCharacters `
                    -Executable $goExe -Arguments $preflightArgs
                if ($preflightCharacters -gt 30000) {
                    throw "Windows argv limit preflight failed before shard launch for $($workerPackage.package): $preflightCharacters"
                }
                $workerPackage['argvCharacters'] = $preflightCharacters
            }
            $workerPlan = Join-Path $workerOutput 'worker-plan.json'
            Write-JsonFile -Path $workerPlan -Value ([ordered]@{
                index = $bin.index
                estimatedSeconds = $bin.estimatedSeconds
                packages = $workerPackages
            })
            $stdout = Join-Path $workerOutput 'worker.stdout.log'
            $stderr = Join-Path $workerOutput 'worker.stderr.log'
            $process = Start-Process -FilePath $pwshExe -ArgumentList @(
                '-NoProfile', '-File', $worker, '-PlanPath', $workerPlan,
                '-OutputDirectory', $workerOutput, '-GoCommand', $goExe
            ) -WorkingDirectory $repo -NoNewWindow -PassThru `
                -RedirectStandardOutput $stdout -RedirectStandardError $stderr
            $workerProcesses.Add([ordered]@{
                index = $bin.index; process = $process; output = $workerOutput
                stdout = $stdout; stderr = $stderr
            }) | Out-Null
        }
        $deadline = [DateTimeOffset]::UtcNow.AddSeconds($CandidateWatchdogSeconds)
        foreach ($entry in $workerProcesses) {
            $remaining = $deadline - [DateTimeOffset]::UtcNow
            $remainingMilliseconds = [Math]::Floor($remaining.TotalMilliseconds)
            if ($remainingMilliseconds -le 0 -or
                -not $entry.process.WaitForExit([Math]::Min([int]::MaxValue, [int64] $remainingMilliseconds))) {
                foreach ($running in $workerProcesses | Where-Object { -not $_.process.HasExited }) {
                    $running.process.Kill($true)
                }
                throw "candidate $Label exceeded the $CandidateWatchdogSeconds-second outer watchdog"
            }
        }
    }
    finally {
        New-Item -ItemType File -Path $stopFile -Force | Out-Null
        if (-not $monitorProcess.WaitForExit(30000)) { $monitorProcess.Kill($true) }
    }
    $ended = [DateTimeOffset]::UtcNow

    $combined = Join-Path $candidateOutput 'go-test.sharded.jsonl'
    [IO.File]::WriteAllText($combined, '', [Text.UTF8Encoding]::new($false))
    $workerSummaries = [Collections.Generic.List[object]]::new()
    foreach ($entry in $workerProcesses | Sort-Object index) {
        $summaryPath = Join-Path $entry.output 'worker-summary.json'
        if (-not (Test-Path -LiteralPath $summaryPath)) {
            throw "shard $($entry.index) did not produce a summary (process exit $($entry.process.ExitCode))"
        }
        $summary = Get-Content -LiteralPath $summaryPath -Raw | ConvertFrom-Json
        if (-not $summary.nonempty -or $summary.exitCode -ne 0 -or $entry.process.ExitCode -ne 0) {
            throw "shard $($entry.index) failed or was empty"
        }
        $workerSummaries.Add($summary) | Out-Null
        [IO.File]::AppendAllText(
            $combined,
            [IO.File]::ReadAllText($summary.combinedJson, [Text.UTF8Encoding]::new($false)),
            [Text.UTF8Encoding]::new($false)
        )
    }

    $dynamicCompare = Join-Path $candidateOutput 'dynamic-events-compare.json'
    Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($evidenceHelper, 'compare-dynamic', '--baseline', $baseline,
            '--candidate', $combined, '--output', $dynamicCompare) `
        -WorkingDirectory $repo `
        -StdoutPath (Join-Path $candidateOutput 'compare-dynamic.stdout.log') `
        -StderrPath (Join-Path $candidateOutput 'compare-dynamic.stderr.log') | Out-Null
    $dynamicAssertions = Join-Path $candidateOutput 'dynamic-assertions.json'
    Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($evidenceHelper, 'assert-dynamic-passes', '--input', $combined,
            '--require-pass', 'github.com/entireio/entire-graph/cmd/entire-graph::TestFatalErrorEscapesTerminalControlBytes',
            '--require-pass', 'github.com/entireio/entire-graph/internal/sem::TestTreeSitterParserMultiLanguageEntities',
            '--output', $dynamicAssertions) `
        -WorkingDirectory $repo `
        -StdoutPath (Join-Path $candidateOutput 'assert-dynamic.stdout.log') `
        -StderrPath (Join-Path $candidateOutput 'assert-dynamic.stderr.log') | Out-Null

    $packageInvocations = @($workerSummaries | ForEach-Object { @($_.packageInvocations) })
    $nonHeavyCounts = [ordered]@{}
    foreach ($package in $ManifestRecord.value.packages | Where-Object { $_.importPath -notin $heavyPackages }) {
        $count = @($packageInvocations | Where-Object { $_.package -eq $package.importPath }).Count
        $nonHeavyCounts[$package.importPath] = $count
        if ($count -ne 1) {
            throw "non-heavy package execution count was $count rather than one: $($package.importPath)"
        }
    }
    $result = [ordered]@{
        label = $Label
        shardCount = $plan.shardCount
        artifactRun = $ManifestRecord.value.run
        startTimeUtc = $started.ToString('o')
        endTimeUtc = $ended.ToString('o')
        durationSeconds = ($ended - $started).TotalSeconds
        longestShardSeconds = (@($workerSummaries.durationSeconds) | Measure-Object -Maximum).Maximum
        nonemptyShardCount = $workerSummaries.Count
        packageInvocationCount = $packageInvocations.Count
        nonHeavyPackageInvocationCounts = $nonHeavyCounts
        nonHeavyPackagesExactlyOnce = $true
        allShardExitCodesZero = -not @($workerSummaries | Where-Object { $_.exitCode -ne 0 })
        dynamicEventsEquivalent = (Get-Content -LiteralPath $dynamicCompare -Raw | ConvertFrom-Json).equivalent
        childReexecAndTreeSitterCgoVerified = (Get-Content -LiteralPath $dynamicAssertions -Raw | ConvertFrom-Json).accepted
        workers = $workerSummaries
    }
    $candidateSummaryPath = Join-Path $candidateOutput 'candidate-summary.json'
    Write-JsonFile -Path $candidateSummaryPath -Value $result
    $checkpointPath = Join-Path $candidateOutput 'completion-checkpoint.json'
    Write-AtomicJsonFile -Path $checkpointPath -Value ([ordered]@{
        schema = 'entire-graph.windows-ci.a3.candidate-checkpoint.v1'
        label = $Label
        completedAtUtc = [DateTimeOffset]::UtcNow.ToString('o')
        candidateSummarySha256 = (Get-FileHash -LiteralPath $candidateSummaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
        result = $result
    })
    $checkpointHash = (Get-FileHash -LiteralPath $checkpointPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $checkpointHashPath = "$checkpointPath.sha256"
    $checkpointHashTemporary = "$checkpointHashPath.$PID.tmp"
    [IO.File]::WriteAllText(
        $checkpointHashTemporary,
        "$checkpointHash  completion-checkpoint.json`n",
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::Move($checkpointHashTemporary, $checkpointHashPath, $true)
    if ($CheckpointUploadRoot) {
        Invoke-CheckedProcess -FilePath $azcopyExe `
            -ArgumentList @('copy', $checkpointPath, "$CheckpointUploadRoot/$Label.json", '--overwrite=true') `
            -WorkingDirectory $candidateOutput `
            -StdoutPath (Join-Path $candidateOutput 'checkpoint-upload.stdout.log') `
            -StderrPath (Join-Path $candidateOutput 'checkpoint-upload.stderr.log') | Out-Null
        Invoke-CheckedProcess -FilePath $azcopyExe `
            -ArgumentList @('copy', $checkpointHashPath, "$CheckpointUploadRoot/$Label.json.sha256", '--overwrite=true') `
            -WorkingDirectory $candidateOutput `
            -StdoutPath (Join-Path $candidateOutput 'checkpoint-hash-upload.stdout.log') `
            -StderrPath (Join-Path $candidateOutput 'checkpoint-hash-upload.stderr.log') | Out-Null
    }
    return [pscustomobject] $result
}

$parallelUnshardedDiagnostic = Invoke-ShardedCandidate `
    -Label 'parallel-unsharded-diagnostic' -PlanPath $parallelPlanPath -ManifestRecord $manifests[0]
$screenResults = [Collections.Generic.List[object]]::new()
foreach ($count in $screenOrder) {
    $screenResults.Add((Invoke-ShardedCandidate -Label "screen-$count" `
        -PlanPath $plans[$count] -ManifestRecord $manifests[0])) | Out-Null
}
$winner = $screenResults | Sort-Object durationSeconds, shardCount | Select-Object -First 1
$repeatResults = [Collections.Generic.List[object]]::new()
foreach ($index in 0..($RepeatCount - 1)) {
    $repeatResults.Add((Invoke-ShardedCandidate -Label ('repeat-{0:d2}-shards-{1}' -f ($index + 1), $winner.shardCount) `
        -PlanPath $plans[[int] $winner.shardCount] -ManifestRecord $manifests[$index])) | Out-Null
}

$testMainMatches = @(Get-ChildItem -LiteralPath $repo -Recurse -Filter '*_test.go' |
    Select-String -Pattern '^\s*func\s+TestMain\s*\(')
$audit = [ordered]@{
    shardedWithinPackage = $true
    actualTopLevelTestExecutionsPerCandidate = 1
    nonHeavyPackageExecutionsPerCandidate = 1
    listOnlyBinaryProbesBeforeMeasurement = 2 * $first.value.packageCount
    consequence = 'Subtests remain attached to their selected top-level parent. Each heavy-package nonempty shard starts a fresh process, so TestMain and package initialization repeat; the three TestMain definitions only dispatch opt-in child-helper modes before m.Run. Exact native dynamic-event multisets and three winner repeats guard observable drift, but hidden package-global ordering remains a fidelity risk.'
    testMainDefinitions = @($testMainMatches | ForEach-Object {
        [ordered]@{ path = [IO.Path]::GetRelativePath($repo, $_.Path); line = $_.LineNumber }
    })
}
Write-JsonFile -Path (Join-Path $output 'testmain-init-audit.json') -Value $audit

Write-JsonFile -Path (Join-Path $output 'summary.json') -Value ([ordered]@{
    schema = 'entire-graph.windows-ci.a3.weighted-execution.v1'
    repositorySha = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
    goVersion = 'go1.26.7'
    baseline = $baseline
    cacheState = 'direct precompiled binaries; go test result cache cleaned before every candidate'
    candidateWatchdog = [ordered]@{
        seconds = $CandidateWatchdogSeconds
        basis = 'ceil(1278.984897-second accepted native baseline multiplied by 1.25)'
    }
    parallelUnshardedDiagnostic = $parallelUnshardedDiagnostic
    screenOrderSeed = $effectiveScreenOrderSeed
    screenOrder = $screenOrder
    screen = $screenResults
    winningShardCount = $winner.shardCount
    repeats = $repeatResults
})
