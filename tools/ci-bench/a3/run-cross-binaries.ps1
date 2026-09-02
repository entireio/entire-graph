[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $RepositoryPath,
    [Parameter(Mandatory)] [string] $CrossArtifactRoot,
    [Parameter(Mandatory)] [string] $OutputDirectory,
    [Parameter(Mandatory)] [string] $BaselineJsonPath,
    [string] $GoCommand = 'go',
    [string] $PythonCommand = 'python',
    [string] $ObjdumpCommand = 'objdump',
    [string] $EvidenceHelperPath = (Join-Path $PSScriptRoot 'compare-evidence.py'),
    [string] $ResourceMonitorPath = (Join-Path (Split-Path $PSScriptRoot -Parent) 'monitor-resources.ps1'),
    [ValidateRange(0, 3)] [int] $ManifestLimit = 0
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-JsonFile {
    param([string] $Path, [object] $Value)
    [IO.File]::WriteAllText(
        $Path,
        (($Value | ConvertTo-Json -Depth 30) + [Environment]::NewLine),
        [Text.UTF8Encoding]::new($false)
    )
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

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)] [string] $FilePath,
        [Parameter(Mandatory)] [string[]] $ArgumentList,
        [Parameter(Mandatory)] [string] $WorkingDirectory,
        [Parameter(Mandatory)] [string] $StdoutPath,
        [Parameter(Mandatory)] [string] $StderrPath
    )
    $resolvedFile = (Get-Command $FilePath -ErrorAction Stop).Source
    $started = [DateTimeOffset]::UtcNow
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $resolvedFile -ArgumentList $ArgumentList `
        -WorkingDirectory $WorkingDirectory -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath
    $clock.Stop()
    return [ordered]@{
        executable = $resolvedFile
        arguments = @($ArgumentList)
        workingDirectory = $WorkingDirectory
        startTimeUtc = $started.ToString('o')
        durationSeconds = [Math]::Round($clock.Elapsed.TotalSeconds, 6)
        exitCode = $process.ExitCode
        stdout = $StdoutPath
        stderr = $StderrPath
    }
}

function Invoke-CheckedProcess {
    param(
        [Parameter(Mandatory)] [string] $FilePath,
        [Parameter(Mandatory)] [string[]] $ArgumentList,
        [Parameter(Mandatory)] [string] $WorkingDirectory,
        [Parameter(Mandatory)] [string] $StdoutPath,
        [Parameter(Mandatory)] [string] $StderrPath
    )
    $result = Invoke-CapturedProcess @PSBoundParameters
    if ($result.exitCode -ne 0) {
        throw "Process failed with exit $($result.exitCode): $($result.executable) $($result.arguments -join ' ')"
    }
    return $result
}

function Get-TestList {
    param(
        [Parameter(Mandatory)] [string] $Executable,
        [Parameter(Mandatory)] [string] $PackageImportPath,
        [Parameter(Mandatory)] [string] $PackageDirectory,
        [Parameter(Mandatory)] [string] $OutputPath,
        [Parameter(Mandatory)] [string] $ErrorPath
    )
    $absoluteExecutable = [IO.Path]::GetFullPath($Executable)
    $result = Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('tool', 'test2json', '-t', '-p', $PackageImportPath,
            $absoluteExecutable, '-test.v=test2json', '-test.list=.', '-test.timeout=30m') `
        -WorkingDirectory $PackageDirectory -StdoutPath $OutputPath -StderrPath $ErrorPath
    $tests = [Collections.Generic.List[string]]::new()
    foreach ($line in Get-Content -LiteralPath $OutputPath -Encoding utf8) {
        if (-not $line.Trim()) { continue }
        $event = $line | ConvertFrom-Json
        if ($event.PSObject.Properties.Match('Output').Count -eq 0) { continue }
        $name = ([string] $event.Output).TrimEnd("`r", "`n")
        if ($name -match '^(Test|Benchmark|Fuzz|Example)') { $tests.Add($name) }
    }
    return [ordered]@{ result = $result; tests = $tests }
}

if (-not $IsWindows) { throw 'run-cross-binaries.ps1 requires PowerShell 7 on Windows.' }

$repo = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
$artifactRoot = (Resolve-Path -LiteralPath $CrossArtifactRoot).ProviderPath
$baselineJson = (Resolve-Path -LiteralPath $BaselineJsonPath).ProviderPath
$helper = (Resolve-Path -LiteralPath $EvidenceHelperPath).ProviderPath
$monitor = (Resolve-Path -LiteralPath $ResourceMonitorPath).ProviderPath
$output = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $output -Force | Out-Null

if ((git -C $repo rev-parse HEAD).Trim() -ne 'ee6468a6a49d9b2a1a828bd276792f415f392185') {
    throw 'product checkout is not pinned to ee6468a6'
}
if ((& $GoCommand env GOVERSION).Trim() -ne 'go1.26.7') { throw 'Go must be go1.26.7' }
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '1'

$steps = [Collections.Generic.List[object]]::new()
$goExe = (Get-Command $GoCommand -ErrorAction Stop).Source
$pythonExe = (Get-Command $PythonCommand -ErrorAction Stop).Source
$objdumpExe = (Get-Command $ObjdumpCommand -ErrorAction Stop).Source

# Native target selection evidence. The cross manifest carries the equivalent
# Linux-targeted inventory. Absolute directories are deliberately normalized
# away by compare-evidence.py.
$nativeRaw = Join-Path $output 'target-go-list.native.raw.json'
$nativeRawErr = Join-Path $output 'target-go-list.native.stderr.log'
$steps.Add((Invoke-CheckedProcess -FilePath $goExe -ArgumentList @('list', '-json', './...') `
    -WorkingDirectory $repo -StdoutPath $nativeRaw -StderrPath $nativeRawErr)) | Out-Null
$nativeInventory = Join-Path $output 'target-files.native.json'
$steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
    -ArgumentList @($helper, 'normalize-target', '--input', $nativeRaw, '--output', $nativeInventory) `
    -WorkingDirectory $repo -StdoutPath (Join-Path $output 'normalize-native.stdout.log') `
    -StderrPath (Join-Path $output 'normalize-native.stderr.log'))) | Out-Null

# Native test-link equivalence means actually linking every native Windows test
# package. It is intentionally not substituted with `go build ./...` and it
# never runs TestMain or package initialization.
$firstManifestPath = Get-ChildItem -LiteralPath $artifactRoot -Filter manifest.json -Recurse |
    Sort-Object FullName | Select-Object -First 1 -ExpandProperty FullName
if (-not $firstManifestPath) { throw 'no cross artifact manifest found' }
$firstManifest = Get-Content -LiteralPath $firstManifestPath -Raw | ConvertFrom-Json
$nativeBinaryDir = Join-Path $output 'native-test-binaries'
$nativeListDir = Join-Path $output 'native-test-lists'
$nativePeDir = Join-Path $output 'native-pe'
New-Item -ItemType Directory -Path $nativeBinaryDir, $nativeListDir, $nativePeDir -Force | Out-Null
$nativeTests = [ordered]@{}
$nativeLinkStarted = [DateTimeOffset]::UtcNow
foreach ($package in $firstManifest.packages) {
    $fileName = Split-Path $package.binary -Leaf
    $nativeExe = Join-Path $nativeBinaryDir $fileName
    $packageDirectory = Join-Path $repo $package.packageDirectory
    $link = Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('test', '-vet=off', '-c', $package.importPath, '-o', $nativeExe) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $nativeListDir "$fileName.link.stdout.log") `
        -StderrPath (Join-Path $nativeListDir "$fileName.link.stderr.log")
    $steps.Add($link) | Out-Null
    $nativeBuildInfoPath = Join-Path $nativePeDir "$fileName.go-version-m.txt"
    $steps.Add((Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('version', '-m', $nativeExe) -WorkingDirectory $packageDirectory `
        -StdoutPath $nativeBuildInfoPath `
        -StderrPath (Join-Path $nativePeDir "$fileName.go-version-m.stderr.log"))) | Out-Null
    $nativeBuildInfo = Get-Content -LiteralPath $nativeBuildInfoPath -Raw
    foreach ($requiredSetting in @('GOOS=windows', 'GOARCH=amd64', 'CGO_ENABLED=1')) {
        if (-not $nativeBuildInfo.Contains($requiredSetting)) {
            throw "native build metadata lacks $requiredSetting for $($package.importPath)"
        }
    }
    $steps.Add((Invoke-CheckedProcess -FilePath $objdumpExe `
        -ArgumentList @('-p', $nativeExe) -WorkingDirectory $packageDirectory `
        -StdoutPath (Join-Path $nativePeDir "$fileName.objdump.txt") `
        -StderrPath (Join-Path $nativePeDir "$fileName.objdump.stderr.log"))) | Out-Null
    $listed = Get-TestList -Executable $nativeExe -PackageImportPath $package.importPath `
        -PackageDirectory $packageDirectory `
        -OutputPath (Join-Path $nativeListDir "$fileName.txt") `
        -ErrorPath (Join-Path $nativeListDir "$fileName.stderr.log")
    $steps.Add($listed.result) | Out-Null
    $nativeTests[$package.importPath] = $listed.tests
}
$nativeLinkEnded = [DateTimeOffset]::UtcNow
Write-JsonFile -Path (Join-Path $output 'test-list.native.json') -Value $nativeTests

# Keep all native Windows compiler checks. They are separate evidence from the
# cross-built test path and can be separate concurrent jobs in CI.
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

$runResults = [Collections.Generic.List[object]]::new()
$manifestPaths = @(Get-ChildItem -LiteralPath $artifactRoot -Filter manifest.json -Recurse | Sort-Object FullName)
if ($ManifestLimit -gt 0) { $manifestPaths = @($manifestPaths | Select-Object -First $ManifestLimit) }
foreach ($manifestFile in $manifestPaths) {
    $manifest = Get-Content -LiteralPath $manifestFile.FullName -Raw | ConvertFrom-Json
    $runOutput = Join-Path $output $manifest.run
    $listOutput = Join-Path $runOutput 'test-lists'
    $jsonOutput = Join-Path $runOutput 'test-json'
    $peOutput = Join-Path $runOutput 'pe'
    New-Item -ItemType Directory -Path $runOutput, $listOutput, $jsonOutput, $peOutput -Force | Out-Null

    $crossTests = [ordered]@{}
    $runtimeByName = @{}
    foreach ($runtimeDll in $manifest.runtimeDlls) {
        $runtimePath = [IO.Path]::GetFullPath((Join-Path $manifestFile.Directory.FullName $runtimeDll.path))
        $actualRuntimeHash = (Get-FileHash -LiteralPath $runtimePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualRuntimeHash -ne $runtimeDll.sha256) { throw "runtime DLL checksum mismatch: $runtimePath" }
        $runtimeByName[$runtimeDll.name] = $runtimeDll
        $steps.Add((Invoke-CheckedProcess -FilePath $objdumpExe `
            -ArgumentList @('-p', $runtimePath) -WorkingDirectory $manifestFile.Directory.FullName `
            -StdoutPath (Join-Path $peOutput "$($runtimeDll.name).objdump.txt") `
            -StderrPath (Join-Path $peOutput "$($runtimeDll.name).objdump.stderr.log"))) | Out-Null
    }
    $dependencyEvidence = [Collections.Generic.List[object]]::new()
    foreach ($package in $manifest.packages) {
        $binary = [IO.Path]::GetFullPath((Join-Path $manifestFile.Directory.FullName $package.binary))
        $actualHash = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $package.sha256) { throw "artifact checksum mismatch: $binary" }
        $packageDirectory = Join-Path $repo $package.packageDirectory
        $fileName = Split-Path $binary -Leaf
        $listed = Get-TestList -Executable $binary -PackageImportPath $package.importPath `
            -PackageDirectory $packageDirectory `
            -OutputPath (Join-Path $listOutput "$fileName.txt") `
            -ErrorPath (Join-Path $listOutput "$fileName.stderr.log")
        $steps.Add($listed.result) | Out-Null
        $crossTests[$package.importPath] = $listed.tests

        $crossBuildInfoPath = Join-Path $peOutput "$fileName.go-version-m.txt"
        $steps.Add((Invoke-CheckedProcess -FilePath $goExe `
            -ArgumentList @('version', '-m', $binary) -WorkingDirectory $packageDirectory `
            -StdoutPath $crossBuildInfoPath `
            -StderrPath (Join-Path $peOutput "$fileName.go-version-m.stderr.log"))) | Out-Null
        $crossBuildInfo = Get-Content -LiteralPath $crossBuildInfoPath -Raw
        foreach ($requiredSetting in @('GOOS=windows', 'GOARCH=amd64', 'CGO_ENABLED=1')) {
            if (-not $crossBuildInfo.Contains($requiredSetting)) {
                throw "cross build metadata lacks $requiredSetting for $($package.importPath)"
            }
        }
        $crossObjdumpPath = Join-Path $peOutput "$fileName.objdump.txt"
        $steps.Add((Invoke-CheckedProcess -FilePath $objdumpExe `
            -ArgumentList @('-p', $binary) -WorkingDirectory $packageDirectory `
            -StdoutPath $crossObjdumpPath `
            -StderrPath (Join-Path $peOutput "$fileName.objdump.stderr.log"))) | Out-Null
        $imports = @(Select-String -LiteralPath $crossObjdumpPath -Pattern 'DLL Name:\s*(\S+)' |
            ForEach-Object { $_.Matches[0].Groups[1].Value })
        $mingwImports = @($imports | Where-Object { $_ -match '^lib.*\.dll$' })
        foreach ($import in $mingwImports) {
            if (-not $runtimeByName.ContainsKey($import)) {
                throw "unpackaged MinGW runtime dependency $import in $($package.importPath)"
            }
            $adjacentDll = Join-Path (Split-Path -Parent $binary) $import
            if (-not (Test-Path -LiteralPath $adjacentDll -PathType Leaf)) {
                throw "MinGW runtime dependency is not adjacent to executable: $adjacentDll"
            }
            $adjacentHash = (Get-FileHash -LiteralPath $adjacentDll -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($adjacentHash -ne $runtimeByName[$import].sha256) {
                throw "adjacent MinGW runtime hash mismatch: $adjacentDll"
            }
        }
        if ($manifest.runtimeLinkage -eq 'static-mingw-support' -and $mingwImports.Count -ne 0) {
            throw "static MinGW support linkage retained external runtime imports in $($package.importPath)"
        }
        if ($package.importPath -eq 'github.com/entireio/entire-graph/internal/sem' -and
            $manifest.runtimeLinkage -ne 'static-mingw-support') {
            throw 'tree-sitter/CGO binary was not built with child-reexec-safe static MinGW support linkage'
        }
        $dependencyEvidence.Add([ordered]@{
            package = $package.importPath
            imports = $imports
            packagedMinGWImports = $mingwImports
        }) | Out-Null
    }
    Write-JsonFile -Path (Join-Path $runOutput 'pe-dependencies.json') -Value $dependencyEvidence
    Write-JsonFile -Path (Join-Path $runOutput 'test-list.cross.json') -Value $crossTests

    $targetCompare = Join-Path $runOutput 'target-files-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-target', '--baseline', $nativeInventory, '--candidate', (Join-Path $manifestFile.Directory.FullName 'target-files.cross.json'), '--output', $targetCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $runOutput 'compare-target.stdout.log') `
        -StderrPath (Join-Path $runOutput 'compare-target.stderr.log'))) | Out-Null
    $listCompare = Join-Path $runOutput 'test-list-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-test-lists', '--baseline', (Join-Path $output 'test-list.native.json'), '--candidate', (Join-Path $runOutput 'test-list.cross.json'), '--output', $listCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $runOutput 'compare-list.stdout.log') `
        -StderrPath (Join-Path $runOutput 'compare-list.stderr.log'))) | Out-Null

    $steps.Add((Invoke-CheckedProcess -FilePath $goExe `
        -ArgumentList @('clean', '-testcache') -WorkingDirectory $repo `
        -StdoutPath (Join-Path $runOutput 'clean-testcache.stdout.log') `
        -StderrPath (Join-Path $runOutput 'clean-testcache.stderr.log'))) | Out-Null

    $stopFile = Join-Path $runOutput '.monitor.stop'
    $resourcePath = Join-Path $runOutput 'resource-samples.jsonl'
    $monitorProcess = Start-Process -FilePath (Get-Command pwsh).Source `
        -ArgumentList @('-NoProfile', '-File', $monitor, '-OutputPath', $resourcePath, '-IntervalSeconds', '1', '-StopFile', $stopFile) `
        -NoNewWindow -PassThru
    $executionStarted = [DateTimeOffset]::UtcNow
    $packageRuns = [Collections.Generic.List[object]]::new()
    $combinedJson = Join-Path $runOutput 'go-test.cross.jsonl'
    [IO.File]::WriteAllText($combinedJson, '', [Text.UTF8Encoding]::new($false))
    try {
        foreach ($package in $manifest.packages) {
            $binary = [IO.Path]::GetFullPath((Join-Path $manifestFile.Directory.FullName $package.binary))
            $packageDirectory = Join-Path $repo $package.packageDirectory
            $fileName = Split-Path $binary -Leaf
            $jsonPath = Join-Path $jsonOutput "$fileName.jsonl"
            $stderrPath = Join-Path $jsonOutput "$fileName.stderr.log"

            # test2json starts the absolute test executable. All Go test flags
            # are prefixed, and Start-Process exposes test2json's propagated
            # native exit code without relying on PowerShell pipeline state.
            $argv = @('tool', 'test2json', '-t', '-p', $package.importPath, $binary,
                '-test.v=test2json', '-test.timeout=30m')
            $argvCharacters = Get-WindowsCommandLineCharacters -Executable $goExe -Arguments $argv
            if ($argvCharacters -gt 30000) { throw "Windows argv limit preflight failed for $($package.importPath)" }
            $result = Invoke-CapturedProcess -FilePath $goExe -ArgumentList $argv `
                -WorkingDirectory $packageDirectory -StdoutPath $jsonPath -StderrPath $stderrPath
            $packageRuns.Add([ordered]@{ package = $package.importPath; binary = $binary; argvCharacters = $argvCharacters; process = $result }) | Out-Null
            Get-Content -LiteralPath $jsonPath | Add-Content -LiteralPath $combinedJson -Encoding utf8
            if ($result.exitCode -ne 0) { throw "cross test package failed: $($package.importPath), exit $($result.exitCode)" }
        }
    }
    finally {
        New-Item -ItemType File -Path $stopFile -Force | Out-Null
        if (-not $monitorProcess.WaitForExit(30000)) { $monitorProcess.Kill($true) }
    }
    $executionEnded = [DateTimeOffset]::UtcNow

    $dynamicCompare = Join-Path $runOutput 'dynamic-events-compare.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'compare-dynamic', '--baseline', $baselineJson, '--candidate', $combinedJson, '--output', $dynamicCompare) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $runOutput 'compare-dynamic.stdout.log') `
        -StderrPath (Join-Path $runOutput 'compare-dynamic.stderr.log'))) | Out-Null

    # These passing events make the two riskiest runtime properties explicit:
    # the command test re-executes its own PE child, and sem reaches the
    # tree-sitter parser through the CGO-linked MinGW runtime.
    $dynamicAssertions = Join-Path $runOutput 'dynamic-assertions.json'
    $steps.Add((Invoke-CheckedProcess -FilePath $pythonExe `
        -ArgumentList @($helper, 'assert-dynamic-passes', '--input', $combinedJson,
            '--require-pass', 'github.com/entireio/entire-graph/cmd/entire-graph::TestFatalErrorEscapesTerminalControlBytes',
            '--require-pass', 'github.com/entireio/entire-graph/internal/sem::TestTreeSitterParserMultiLanguageEntities',
            '--output', $dynamicAssertions) `
        -WorkingDirectory $repo -StdoutPath (Join-Path $runOutput 'assert-dynamic.stdout.log') `
        -StderrPath (Join-Path $runOutput 'assert-dynamic.stderr.log'))) | Out-Null

    $runResults.Add([ordered]@{
        run = $manifest.run
        crossCompileDurationMilliseconds = $manifest.compileDurationMilliseconds
        executionStartTimeUtc = $executionStarted.ToString('o')
        executionEndTimeUtc = $executionEnded.ToString('o')
        executionDurationSeconds = ($executionEnded - $executionStarted).TotalSeconds
        packageCount = $manifest.packageCount
        packageRuns = $packageRuns
        targetFilesEquivalent = (Get-Content $targetCompare -Raw | ConvertFrom-Json).equivalent
        topLevelTestListsEquivalent = (Get-Content $listCompare -Raw | ConvertFrom-Json).equivalent
        dynamicEventsEquivalent = (Get-Content $dynamicCompare -Raw | ConvertFrom-Json).equivalent
        childReexecAndTreeSitterCgoVerified = (Get-Content $dynamicAssertions -Raw | ConvertFrom-Json).accepted
    }) | Out-Null
}

$testMainMatches = @(Get-ChildItem -LiteralPath $repo -Recurse -Filter '*_test.go' |
    Select-String -Pattern '^\s*func\s+TestMain\s*\(')
$audit = [ordered]@{
    shardedWithinPackage = $false
    actualTestExecutionsPerPackagePerRun = 1
    listOnlyBinaryProbesPerPackagePerRun = 1
    packageProcessStartsPerPackagePerRun = 2
    consequence = 'No test is sharded or duplicated. A separate -test.list probe necessarily starts each package once before its single actual test run, so package init and TestMain execute once extra; the three selected TestMain definitions only check opt-in helper environment variables before m.Run.'
    testMainDefinitions = @($testMainMatches | ForEach-Object {
        [ordered]@{ path = [IO.Path]::GetRelativePath($repo, $_.Path); line = $_.LineNumber }
    })
}
Write-JsonFile -Path (Join-Path $output 'testmain-init-audit.json') -Value $audit

$summary = [ordered]@{
    schema = 'entire-graph.windows-ci.a3.windows-execution.v1'
    repositorySha = 'ee6468a6a49d9b2a1a828bd276792f415f392185'
    goVersion = 'go1.26.7'
    target = [ordered]@{ goos = 'windows'; goarch = 'amd64'; cgoEnabled = '1' }
    manifestLimit = $ManifestLimit
    nativeTestLink = [ordered]@{
        startTimeUtc = $nativeLinkStarted.ToString('o')
        endTimeUtc = $nativeLinkEnded.ToString('o')
        durationSeconds = ($nativeLinkEnded - $nativeLinkStarted).TotalSeconds
        packageCount = $firstManifest.packageCount
    }
    nativeChecks = $nativeChecks
    runs = $runResults
    steps = $steps
}
Write-JsonFile -Path (Join-Path $output 'summary.json') -Value $summary
