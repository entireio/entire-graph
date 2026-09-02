[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2

$productCommit = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
$harnessCommit = 'b22be6a73adac8d9c582af4bfc681c5d7a517221'
$semImportPath = 'github.com/entireio/entire-graph/internal/sem'
$productDirectory = 'C:\src\entire-graph'
$packageDirectory = Join-Path $productDirectory 'internal\sem'
$harnessDirectory = 'C:\ci-bench\tools\ci-bench'
$a4Directory = Join-Path $harnessDirectory 'a4'
$cacheRoot = 'C:\ci-bench-cache\a4'
$activeCache = Join-Path $cacheRoot 'active'
$seedArchive = Join-Path $cacheRoot 'seed-build.tar'
$resultRoot = 'C:\ci-bench-results\a4\experiment'
$transportDirectory = 'C:\ci-bench-results\a4\transport'
$go = 'C:\tools\go\bin\go.exe'
$git = 'C:\tools\mingit\cmd\git.exe'
$python = 'C:\tools\python\python.exe'
$pwsh = 'C:\tools\pwsh\pwsh.exe'
$gcc = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse | Select-Object -First 1 -ExpandProperty FullName
$sh = Get-ChildItem -LiteralPath 'C:\tools\mingit' -Filter 'sh.exe' -File -Recurse | Where-Object { $_.FullName -like '*\usr\bin\sh.exe' } | Select-Object -First 1 -ExpandProperty FullName
$experimentStart = [DateTimeOffset]::UtcNow
$experimentState = 'failed'
$experimentError = $null
$treatments = [Collections.Generic.List[object]]::new()
$candidateCounts = @(4, 8, 12)

if (-not $gcc -or -not $sh) { throw 'Pinned gcc.exe or MinGit usr\bin\sh.exe is missing.' }
$env:Path = @(
    'C:\tools\go\bin', 'C:\tools\mingit\cmd', (Split-Path -Parent $sh),
    'C:\tools\pwsh', 'C:\tools\python', (Split-Path -Parent $gcc),
    [Environment]::GetEnvironmentVariable('Path', 'Machine')
) -join ';'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'
$env:GOTOOLCHAIN = 'local'
$env:GIT_TERMINAL_PROMPT = '0'
$env:GCM_INTERACTIVE = 'Never'
$env:GOCACHE = $activeCache
$env:GOMODCACHE = Join-Path $cacheRoot 'gomod'
$env:GOPATH = Join-Path $cacheRoot 'gopath'
$utf8 = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = $utf8
$OutputEncoding = $utf8

function Get-StorageToken {
    $uri = 'http://169.254.169.254/metadata/identity/oauth2/token' +
        '?api-version=2018-02-01&resource=https%3A%2F%2Fstorage.azure.com%2F'
    for ($attempt = 1; $attempt -le 18; $attempt++) {
        try { return (Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } -Uri $uri).access_token }
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
        $uri = 'https://{0}.blob.core.windows.net/results/{1}' -f $StorageAccount, $BlobName
        Invoke-WebRequest -UseBasicParsing -Method Put -Uri $uri -InFile $Path -Headers @{
            Authorization = 'Bearer ' + $token
            'x-ms-version' = '2023-11-03'
            'x-ms-blob-type' = 'BlockBlob'
        } | Out-Null
    }
    finally { Remove-Variable token -ErrorAction SilentlyContinue }
}

function Invoke-NativeChecked {
    param([string] $Command, [string[]] $Arguments, [string] $LogPath, [string] $WorkingDirectory = $productDirectory)
    $previous = $ErrorActionPreference
    $original = Get-Location
    try {
        $ErrorActionPreference = 'Continue'
        Set-Location -LiteralPath $WorkingDirectory
        & $Command @Arguments *> $LogPath
        $code = $LASTEXITCODE
    }
    finally {
        Set-Location -LiteralPath $original
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "Native command failed (${code}): $Command $($Arguments -join ' ')" }
}

function Invoke-TestList {
    param(
        [string] $Binary,
        [string] $OutputPath,
        [string] $WorkingDirectory
    )
    $previous = $ErrorActionPreference
    $original = Get-Location
    try {
        $ErrorActionPreference = 'Continue'
        Set-Location -LiteralPath $WorkingDirectory
        [string[]] $lines = @(& $Binary '-test.list' '.')
        $code = $LASTEXITCODE
    }
    finally {
        Set-Location -LiteralPath $original
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "Native test list failed (${code}): $Binary" }
    [IO.File]::WriteAllLines($OutputPath, $lines, [Text.UTF8Encoding]::new($false))
}

function Start-CapturedProcess {
    param(
        [string] $Command,
        [string[]] $Arguments,
        [string] $WorkingDirectory,
        [string] $Stdout,
        [string] $Stderr
    )
    $argumentCharacters = ($Arguments -join ' ').Length
    if ($argumentCharacters -ge 16000) {
        throw "Conservative Windows argv guard rejected $argumentCharacters characters for $Command."
    }
    $started = [DateTimeOffset]::UtcNow
    $process = Start-Process -FilePath $Command -ArgumentList $Arguments -WorkingDirectory $WorkingDirectory `
        -PassThru -NoNewWindow -RedirectStandardOutput $Stdout -RedirectStandardError $Stderr
    [pscustomobject]@{
        process = $process
        command = $Command
        arguments = $Arguments
        argumentCharacters = $argumentCharacters
        startTimeUtc = $started
        stdout = $Stdout
        stderr = $Stderr
    }
}

function Wait-CapturedProcess {
    param([object] $Record)
    $Record.process.WaitForExit()
    $Record.process.Refresh()
    $ended = [DateTimeOffset]::UtcNow
    [pscustomobject][ordered]@{
        command = $Record.command
        arguments = $Record.arguments
        argumentCharacters = $Record.argumentCharacters
        startTimeUtc = $Record.startTimeUtc.ToString('o')
        endTimeUtc = $ended.ToString('o')
        durationSeconds = [math]::Round(($ended - $Record.startTimeUtc).TotalSeconds, 6)
        exitCode = [int] $Record.process.ExitCode
        stdout = $Record.stdout
        stderr = $Record.stderr
    }
}

function Invoke-TestMainProbe {
    param([string] $Binary, [string] $Shard, [string] $Directory)
    $marker = Join-Path $Directory ($Shard + '.testmain.marker')
    $stdout = Join-Path $Directory ($Shard + '.testmain.stdout.log')
    $stderr = Join-Path $Directory ($Shard + '.testmain.stderr.log')
    $started = [DateTimeOffset]::UtcNow
    $original = Get-Location
    $previous = $ErrorActionPreference
    try {
        $env:ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER = $marker
        $ErrorActionPreference = 'Continue'
        Set-Location -LiteralPath $packageDirectory
        & $Binary '-test.list' '^$' 1> $stdout 2> $stderr
        $exitCode = $LASTEXITCODE
    }
    finally {
        Set-Location -LiteralPath $original
        $ErrorActionPreference = $previous
        Remove-Item Env:\ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER -ErrorAction SilentlyContinue
    }
    $ended = [DateTimeOffset]::UtcNow
    $markerValue = if (Test-Path -LiteralPath $marker) { Get-Content -LiteralPath $marker -Raw } else { $null }
    [pscustomobject][ordered]@{
        shard = $Shard
        command = $Binary
        arguments = @('-test.list', '^$')
        argumentCharacters = 14
        startTimeUtc = $started.ToString('o')
        endTimeUtc = $ended.ToString('o')
        durationSeconds = [math]::Round(($ended - $started).TotalSeconds, 6)
        exitCode = [int] $exitCode
        stdout = $stdout
        stderr = $stderr
        marker = $markerValue
        exactSemantics = $exitCode -eq 23 -and $markerValue -eq 'started'
    }
}

function Restore-Seed {
    param([string] $Directory)
    $started = [DateTimeOffset]::UtcNow
    if (-not (Test-Path -LiteralPath $seedArchive)) { throw "Missing cache seed $seedArchive" }
    if (Test-Path -LiteralPath $activeCache) { Remove-Item -LiteralPath $activeCache -Recurse -Force }
    New-Item -ItemType Directory -Path $activeCache -Force | Out-Null
    Invoke-NativeChecked 'C:\Windows\System32\tar.exe' @('-xf', $seedArchive, '-C', $activeCache) (Join-Path $Directory 'restore-cache.log')
    Invoke-NativeChecked $go @('clean', '-testcache') (Join-Path $Directory 'clean-testcache.log')
    [math]::Round(([DateTimeOffset]::UtcNow - $started).TotalSeconds, 6)
}

function Stop-ResourceMonitor {
    param([object] $Record, [string] $StopFile)
    [IO.File]::WriteAllText($StopFile, 'stop', [Text.Encoding]::ASCII)
    $result = Wait-CapturedProcess $Record
    if ($result.exitCode -ne 0) { throw "Resource monitor failed with $($result.exitCode)." }
    $result
}

function Publish-Checkpoint {
    param([string] $TreatmentDirectory, [string] $Name)
    $metadataPath = Join-Path $TreatmentDirectory 'treatment-metadata.json'
    if (Test-Path -LiteralPath $metadataPath) {
        Send-ResultBlob $metadataPath ("checkpoints/{0}.json" -f $Name)
    }
}

function Invoke-Baseline {
    $directory = Join-Path $resultRoot 'baseline-01'
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $preparationSeconds = Restore-Seed $directory
    $wrapperStdout = Join-Path $directory 'wrapper.stdout.log'
    $wrapperStderr = Join-Path $directory 'wrapper.stderr.log'
    $arguments = @(
        '-NoLogo', '-NoProfile', '-NonInteractive', '-File', (Join-Path $harnessDirectory 'run-go-suite.ps1'),
        '-OutputDirectory', (Join-Path $directory 'suite'), '-RepositoryPath', $productDirectory,
        '-RunId', 'a4-baseline-01', '-RunLabel', 'a4-accepted-b22-monolithic-baseline',
        '-ExpectedRepositorySha', $productCommit, '-ExpectedGoVersion', 'go1.26.7',
        '-SkipCompileDecomposition', '-ResourceSampleIntervalSeconds', '2'
    )
    $wrapper = Start-CapturedProcess $pwsh $arguments $productDirectory $wrapperStdout $wrapperStderr
    $wrapperResult = Wait-CapturedProcess $wrapper
    $metadataPath = Join-Path $directory 'suite\run-metadata.json'
    if (-not (Test-Path -LiteralPath $metadataPath)) { throw 'Baseline wrapper omitted run-metadata.json.' }
    $suite = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    $accepted = $wrapperResult.exitCode -eq 0 -and $suite.goTestExitCode -eq 0 -and $suite.exitCode -eq 0 -and $suite.state -eq 'completed'
    $metadata = [ordered]@{
        schemaVersion = 1
        treatment = 'baseline-01'
        kind = 'monolithic-full-suite'
        accepted = $accepted
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        preparationSeconds = $preparationSeconds
        wrapper = $wrapperResult
        suiteDurationSeconds = $suite.durationSeconds
        goTestExitCode = $suite.goTestExitCode
        wrapperMetadataExitCode = $suite.exitCode
        suiteState = $suite.state
        command = 'go test -json -timeout 30m ./...'
    }
    $metadata | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $directory 'treatment-metadata.json') -Encoding UTF8
    if (-not $accepted) { throw 'A4 monolithic baseline did not pass every exit/state check.' }

    $events = Join-Path $directory 'suite\go-test.jsonl'
    Invoke-NativeChecked $python @(
        (Join-Path $a4Directory 'verify-test-events.py'), 'weights', '--input', $events,
        '--package', $semImportPath, '--output', (Join-Path $resultRoot 'sem-weights.json')
    ) (Join-Path $directory 'weights.log')

    $monolithicBinary = Join-Path $directory 'sem-monolithic.test.exe'
    Invoke-NativeChecked $go @('test', '-vet=off', '-c', './internal/sem', '-o', $monolithicBinary) (Join-Path $directory 'sem-list-compile.log')
    Invoke-TestList $monolithicBinary (Join-Path $resultRoot 'sem-monolithic-list.txt') $packageDirectory
    Remove-Item -LiteralPath $monolithicBinary -Force
    Publish-Checkpoint $directory 'baseline-01'
    $metadata
}

function Invoke-Candidate {
    param([int] $ShardCount, [string] $Name)
    $directory = Join-Path $resultRoot $Name
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $preparationSeconds = Restore-Seed $directory
    $planDirectory = Join-Path $directory 'plan'
    Invoke-NativeChecked $python @(
        (Join-Path $harnessDirectory 'generate-test-overlays.py'), '--repo', $productDirectory,
        '--package', './internal/sem', '--output', $planDirectory, '--shards', [string] $ShardCount,
        '--weights', (Join-Path $resultRoot 'sem-weights.json'), '--goos', 'windows',
        '--goarch', 'amd64', '--cgo-enabled', '1'
    ) (Join-Path $directory 'generate.log')

    $allPackages = @(& $go -C $productDirectory list ./...)
    if ($LASTEXITCODE -ne 0) { throw 'go list ./... failed while deriving non-sem package inventory.' }
    $otherPackages = @($allPackages | Where-Object { $_ -ne $semImportPath })
    if ($allPackages.Count -ne ($otherPackages.Count + 1) -or $semImportPath -notin $allPackages) {
        throw 'Exact sem exclusion from target package inventory failed.'
    }

    $monitorStop = Join-Path $directory 'resource-monitor.stop'
    $monitor = Start-CapturedProcess $pwsh @(
        '-NoLogo', '-NoProfile', '-NonInteractive', '-File', (Join-Path $harnessDirectory 'monitor-resources.ps1'),
        '-OutputPath', (Join-Path $directory 'resource-samples.jsonl'), '-StopFile', $monitorStop,
        '-IntervalSeconds', '2'
    ) $productDirectory (Join-Path $directory 'monitor.stdout.log') (Join-Path $directory 'monitor.stderr.log')

    $measuredStart = [DateTimeOffset]::UtcNow
    $state = 'failed'
    $errorMessage = $null
    $otherResult = $null
    $vetResult = $null
    $compileResults = [Collections.Generic.List[object]]::new()
    $testResults = [Collections.Generic.List[object]]::new()
    $testMainResults = [Collections.Generic.List[object]]::new()
    $monitorResult = $null
    $equivalenceExit = $null
    $listEquivalenceExit = $null

    try {
        $otherArguments = @('test', '-json', '-timeout', '30m') + $otherPackages
        $other = Start-CapturedProcess $go $otherArguments $productDirectory (Join-Path $directory 'other-packages.jsonl') (Join-Path $directory 'other-packages.stderr.log')
        $vet = Start-CapturedProcess $go @('vet', './internal/sem') $productDirectory (Join-Path $directory 'vet.stdout.log') (Join-Path $directory 'vet.stderr.log')

        $pendingCompiles = [Collections.Generic.List[object]]::new()
        for ($index = 1; $index -le $ShardCount; $index++) {
            $shard = "shard-{0:00}" -f $index
            $binary = Join-Path $directory ($shard + '.test.exe')
            $overlay = Join-Path $planDirectory ($shard + '\overlay.json')
            $record = Start-CapturedProcess $go @(
                'test', '-vet=off', '-overlay', $overlay, '-c', './internal/sem', '-o', $binary
            ) $productDirectory (Join-Path $directory ($shard + '.compile.stdout.log')) (Join-Path $directory ($shard + '.compile.stderr.log'))
            Add-Member -InputObject $record -NotePropertyName shard -NotePropertyValue $shard
            Add-Member -InputObject $record -NotePropertyName binary -NotePropertyValue $binary
            $pendingCompiles.Add($record) | Out-Null
        }

        $runningTests = [Collections.Generic.List[object]]::new()
        while ($pendingCompiles.Count -gt 0) {
            for ($position = $pendingCompiles.Count - 1; $position -ge 0; $position--) {
                $record = $pendingCompiles[$position]
                if (-not $record.process.HasExited) { continue }
                $compileResult = Wait-CapturedProcess $record
                Add-Member -InputObject $compileResult -NotePropertyName shard -NotePropertyValue $record.shard
                Add-Member -InputObject $compileResult -NotePropertyName binaryBytes -NotePropertyValue $(if (Test-Path -LiteralPath $record.binary) { (Get-Item -LiteralPath $record.binary).Length } else { $null })
                $compileResults.Add($compileResult) | Out-Null
                $pendingCompiles.RemoveAt($position)
                if ($compileResult.exitCode -ne 0) { throw "$($record.shard) compile failed with $($compileResult.exitCode)." }

                $listPath = Join-Path $directory ($record.shard + '.list.txt')
                Invoke-TestList $record.binary $listPath $packageDirectory
                $probeResult = Invoke-TestMainProbe $record.binary $record.shard $directory
                $testMainResults.Add($probeResult) | Out-Null
                if (-not $probeResult.exactSemantics) { throw "$($record.shard) TestMain marker semantics failed." }

                $testArguments = @(
                    'tool', 'test2json', '-t', '-p', $semImportPath, $record.binary,
                    '-test.v=test2json', '-test.timeout=30m'
                )
                $test = Start-CapturedProcess $go $testArguments $packageDirectory (Join-Path $directory ($record.shard + '.jsonl')) (Join-Path $directory ($record.shard + '.stderr.log'))
                Add-Member -InputObject $test -NotePropertyName shard -NotePropertyValue $record.shard
                $runningTests.Add($test) | Out-Null
            }
            if ($pendingCompiles.Count -gt 0) { Start-Sleep -Milliseconds 200 }
        }

        foreach ($record in $runningTests) {
            $result = Wait-CapturedProcess $record
            Add-Member -InputObject $result -NotePropertyName shard -NotePropertyValue $record.shard
            $testResults.Add($result) | Out-Null
        }
        $otherResult = Wait-CapturedProcess $other
        $vetResult = Wait-CapturedProcess $vet
        if ($otherResult.exitCode -ne 0) { throw "Non-sem package tests failed with $($otherResult.exitCode)." }
        if ($vetResult.exitCode -ne 0) { throw "Sem vet failed with $($vetResult.exitCode)." }
        if (@($testResults | Where-Object { $_.exitCode -ne 0 }).Count -ne 0) { throw 'One or more sem shard processes failed.' }

        $eventArguments = @(
            (Join-Path $a4Directory 'verify-test-events.py'), 'compare-events',
            '--baseline', (Join-Path $resultRoot 'baseline-01\suite\go-test.jsonl'),
            '--candidate', (Join-Path $directory 'other-packages.jsonl'),
            '--output', (Join-Path $directory 'event-equivalence.json')
        )
        $listArguments = @(
            (Join-Path $a4Directory 'verify-test-events.py'), 'compare-lists',
            '--baseline', (Join-Path $resultRoot 'sem-monolithic-list.txt'),
            '--output', (Join-Path $directory 'list-equivalence.json')
        )
        for ($index = 1; $index -le $ShardCount; $index++) {
            $shard = "shard-{0:00}" -f $index
            $eventArguments += @('--candidate', (Join-Path $directory ($shard + '.jsonl')))
            $listArguments += @('--candidate', (Join-Path $directory ($shard + '.list.txt')))
        }
        $previous = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            & $python @eventArguments *> (Join-Path $directory 'event-equivalence.log')
            $equivalenceExit = $LASTEXITCODE
            & $python @listArguments *> (Join-Path $directory 'list-equivalence.log')
            $listEquivalenceExit = $LASTEXITCODE
        }
        finally { $ErrorActionPreference = $previous }
        if ($equivalenceExit -ne 0 -or $listEquivalenceExit -ne 0) {
            throw "Coverage equivalence failed: dynamic=$equivalenceExit list=$listEquivalenceExit."
        }
        if (@(& $git -C $productDirectory status --porcelain).Count -ne 0) { throw 'Product source changed during candidate.' }
        $state = 'completed'
    }
    catch { $errorMessage = $_.Exception.Message }
    finally {
        Remove-Item Env:\ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER -ErrorAction SilentlyContinue
        try { $monitorResult = Stop-ResourceMonitor $monitor $monitorStop }
        catch { $errorMessage = ($errorMessage, $_.Exception.Message | Where-Object { $_ }) -join '; ' }
    }

    $measuredEnd = [DateTimeOffset]::UtcNow
    $eventProof = if (Test-Path -LiteralPath (Join-Path $directory 'event-equivalence.json')) { Get-Content -LiteralPath (Join-Path $directory 'event-equivalence.json') -Raw | ConvertFrom-Json } else { $null }
    $listProof = if (Test-Path -LiteralPath (Join-Path $directory 'list-equivalence.json')) { Get-Content -LiteralPath (Join-Path $directory 'list-equivalence.json') -Raw | ConvertFrom-Json } else { $null }
    $accepted = $state -eq 'completed' -and $null -ne $eventProof -and $eventProof.exactDynamicEventMultiset -and $eventProof.packageInventoryExact -and $null -ne $listProof -and $listProof.exactListMultiset
    $metadata = [ordered]@{
        schemaVersion = 1
        treatment = $Name
        kind = 'full-suite-with-native-sem-overlay-binaries'
        shardCount = $ShardCount
        state = $state
        accepted = $accepted
        error = $errorMessage
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        goVersion = 'go1.26.7'
        goos = 'windows'
        goarch = 'amd64'
        cgoEnabled = '1'
        preparationSeconds = $preparationSeconds
        measuredStartTimeUtc = $measuredStart.ToString('o')
        measuredEndTimeUtc = $measuredEnd.ToString('o')
        measuredCriticalPathSeconds = [math]::Round(($measuredEnd - $measuredStart).TotalSeconds, 6)
        compileResults = @($compileResults | Sort-Object shard)
        testResults = @($testResults | Sort-Object shard)
        testMainResults = @($testMainResults | Sort-Object shard)
        longestCompileSeconds = if ($compileResults.Count) { ($compileResults | Measure-Object durationSeconds -Maximum).Maximum } else { $null }
        longestShardExecutionSeconds = if ($testResults.Count) { ($testResults | Measure-Object durationSeconds -Maximum).Maximum } else { $null }
        otherPackages = $otherResult
        semVet = $vetResult
        monitor = $monitorResult
        dynamicEventEquivalenceExitCode = $equivalenceExit
        listEquivalenceExitCode = $listEquivalenceExit
        exactDynamicEventMultiset = if ($null -eq $eventProof) { $false } else { $eventProof.exactDynamicEventMultiset }
        exactPackageInventory = if ($null -eq $eventProof) { $false } else { $eventProof.packageInventoryExact }
        exactRunnableListMultiset = if ($null -eq $listProof) { $false } else { $listProof.exactListMultiset }
        expectedPackageCount = $allPackages.Count
        nonSemPackageCount = $otherPackages.Count
        semPackageProcessCount = $ShardCount
        argvLimitGuardCharacters = 16000
    }
    $metadata | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $directory 'treatment-metadata.json') -Encoding UTF8
    Publish-Checkpoint $directory $Name
    foreach ($binary in Get-ChildItem -LiteralPath $directory -Filter '*.test.exe' -File -ErrorAction SilentlyContinue) {
        Remove-Item -LiteralPath $binary.FullName -Force
    }
    $treatments.Add($metadata) | Out-Null
    $metadata
}

if (Test-Path -LiteralPath $resultRoot) {
    Remove-Item -LiteralPath $resultRoot -Recurse -Force
}
New-Item -ItemType Directory -Path $resultRoot, $transportDirectory -Force | Out-Null

try {
    $actualCommit = (& $git -C $productDirectory rev-parse HEAD).Trim()
    $actualHarness = (& $git -C 'C:\src\entire-graph-harness' rev-parse HEAD).Trim()
    $actualGo = (& $go env GOVERSION).Trim()
    $imds = Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } -Uri 'http://169.254.169.254/metadata/instance/compute?api-version=2021-12-13'
    if ($actualCommit -ne $productCommit -or $actualHarness -ne $harnessCommit -or $actualGo -ne 'go1.26.7') { throw 'Pinned product/harness/Go validation failed.' }
    if ($imds.vmSize -ne 'Standard_D32ads_v7' -or [Environment]::ProcessorCount -ne 32) { throw "Unexpected VM $($imds.vmSize)/$([Environment]::ProcessorCount) vCPU." }
    if (-not (Test-Path -LiteralPath $seedArchive)) { throw 'Bootstrap cache seed is missing.' }

    $baseline = Invoke-Baseline
    foreach ($count in $candidateCounts) { Invoke-Candidate $count ("screen-{0:00}" -f $count) | Out-Null }
    $credibleScreens = @($treatments | Where-Object { $_.accepted } | Sort-Object measuredCriticalPathSeconds)
    if ($credibleScreens.Count -eq 0) { throw 'No coverage-equivalent screen candidate completed.' }
    $bestCount = [int] $credibleScreens[0].shardCount
    Invoke-Candidate $bestCount ("best-{0:00}-rep-02" -f $bestCount) | Out-Null
    Invoke-Candidate $bestCount ("best-{0:00}-rep-03" -f $bestCount) | Out-Null
    $experimentState = 'completed'
}
catch { $experimentError = $_.Exception.Message }
finally {
    $experimentEnd = [DateTimeOffset]::UtcNow
    $summary = [ordered]@{
        schemaVersion = 1
        state = $experimentState
        error = $experimentError
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        vmSize = 'Standard_D32ads_v7'
        vcpus = 32
        goVersion = 'go1.26.7'
        goos = 'windows'
        goarch = 'amd64'
        cgoEnabled = '1'
        startTimeUtc = $experimentStart.ToString('o')
        endTimeUtc = $experimentEnd.ToString('o')
        durationSeconds = [math]::Round(($experimentEnd - $experimentStart).TotalSeconds, 6)
        baseline = if (Test-Path -LiteralPath (Join-Path $resultRoot 'baseline-01\treatment-metadata.json')) { Get-Content -LiteralPath (Join-Path $resultRoot 'baseline-01\treatment-metadata.json') -Raw | ConvertFrom-Json } else { $null }
        treatments = $treatments
    }
    $summary | ConvertTo-Json -Depth 25 | Set-Content -LiteralPath (Join-Path $resultRoot 'experiment-summary.json') -Encoding UTF8
    $archive = Join-Path $transportDirectory 'experiment.zip'
    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    Compress-Archive -Path (Join-Path $resultRoot '*') -DestinationPath $archive -CompressionLevel Optimal
    $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(($archive + '.sha256'), ($hash + '  experiment.zip' + "`n"), [Text.Encoding]::ASCII)
    Send-ResultBlob $archive 'experiment/experiment.zip'
    Send-ResultBlob ($archive + '.sha256') 'experiment/experiment.zip.sha256'
    Send-ResultBlob (Join-Path $resultRoot 'experiment-summary.json') 'experiment/experiment-summary.json'
    Write-Output "A4_EXPERIMENT_UPLOADED state=$experimentState sha256=$hash"
}

if ($experimentState -ne 'completed') { exit 1 }
