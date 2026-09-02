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
$repositoryUrl = 'https://github.com/entireio/entire-graph.git'
$goVersion = '1.26.7'
$gitVersion = '2.55.0'
$powerShellVersion = '7.5.3'
$pythonVersion = '3.13.7'
$expectedScriptHashes = [ordered]@{
    'generate-test-overlays.py' = 'ac14d74ba428dc925b12692ef8a4ad13f63229c1ad9ed6e52a843256b298a4e0'
    'go-test-file-inventory.go' = 'b9ccab9c4364c1225fa06c3a2ad76738d1cac71783996f67740ac74292a22fdd'
    'a4\verify-test-events.py' = '2b6c3ae17b7334ec0c7ee1f43ffcc351ee4fa800c713e8dc5a2699353640eb32'
    'a4\run-experiment.ps1' = '51ac105a7483ed19afeefd0cd2b1471e9826f94a99433610ca4c6a1856ba80c4'
}

$root = 'C:\entire-a4'
$downloads = Join-Path $root 'downloads'
$productDirectory = 'C:\src\entire-graph'
$harnessSourceDirectory = 'C:\src\entire-graph-harness'
$harnessDirectory = 'C:\ci-bench\tools\ci-bench'
$resultDirectory = 'C:\ci-bench-results\a4\bootstrap'
$transportDirectory = 'C:\ci-bench-results\a4\transport'
$toolDirectory = 'C:\tools'
$goDirectory = Join-Path $toolDirectory 'go'
$gitDirectory = Join-Path $toolDirectory 'mingit'
$pwshDirectory = Join-Path $toolDirectory 'pwsh'
$pythonDirectory = Join-Path $toolDirectory 'python'
$cacheRoot = 'C:\ci-bench-cache\a4'
$activeCache = Join-Path $cacheRoot 'active'
$seedArchive = Join-Path $cacheRoot 'seed-build.tar'
$phaseRecords = [Collections.Generic.List[object]]::new()
$startedAt = [DateTimeOffset]::UtcNow
$state = 'failed'
$fatalMessage = $null
$utf8 = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = $utf8
$OutputEncoding = $utf8

function Invoke-Phase {
    param([string] $Name, [scriptblock] $Action)
    $started = [DateTimeOffset]::UtcNow
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    $status = 'pass'
    $message = $null
    try { & $Action }
    catch {
        $status = 'fail'
        $message = $_.Exception.Message
        throw
    }
    finally {
        $stopwatch.Stop()
        $phaseRecords.Add([ordered]@{
            name = $Name
            status = $status
            startTimeUtc = $started.ToString('o')
            durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 6)
            message = $message
        }) | Out-Null
    }
}

function Invoke-NativeChecked {
    param(
        [string] $Command,
        [string[]] $Arguments,
        [string] $LogPath,
        [string] $WorkingDirectory = ''
    )
    $previous = $ErrorActionPreference
    $original = Get-Location
    try {
        $ErrorActionPreference = 'Continue'
        if (-not [string]::IsNullOrWhiteSpace($WorkingDirectory)) {
            Set-Location -LiteralPath $WorkingDirectory
        }
        & $Command @Arguments *> $LogPath
        $code = $LASTEXITCODE
    }
    finally {
        Set-Location -LiteralPath $original
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "Native command failed (${code}): $Command $($Arguments -join ' ')"
    }
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

function Invoke-Download {
    param([string] $Uri, [string] $Path)
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $Path
            return
        }
        catch {
            if ($attempt -eq 5) { throw }
            Start-Sleep -Seconds (5 * $attempt)
        }
    }
}

function Get-StorageToken {
    $uri = 'http://169.254.169.254/metadata/identity/oauth2/token' +
        '?api-version=2018-02-01&resource=https%3A%2F%2Fstorage.azure.com%2F'
    for ($attempt = 1; $attempt -le 18; $attempt++) {
        try {
            return (Invoke-RestMethod -UseBasicParsing -Headers @{ Metadata = 'true' } -Uri $uri).access_token
        }
        catch {
            if ($attempt -eq 18) { throw }
            Start-Sleep -Seconds 10
        }
    }
}

function Receive-ScriptBlob {
    param([string] $BlobName, [string] $Path, [string] $ExpectedSHA256)
    $token = Get-StorageToken
    try {
        $uri = 'https://{0}.blob.core.windows.net/scripts/{1}' -f $StorageAccount, $BlobName
        Invoke-WebRequest -UseBasicParsing -Uri $uri -OutFile $Path -Headers @{
            Authorization = 'Bearer ' + $token
            'x-ms-version' = '2023-11-03'
        }
    }
    finally { Remove-Variable token -ErrorAction SilentlyContinue }
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $ExpectedSHA256) {
        throw "Private script hash mismatch for ${BlobName}: $actual"
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

function Start-CapturedProcess {
    param(
        [string] $Command,
        [string[]] $Arguments,
        [string] $WorkingDirectory,
        [string] $Stdout,
        [string] $Stderr
    )
    Start-Process -FilePath $Command -ArgumentList $Arguments -WorkingDirectory $WorkingDirectory `
        -PassThru -NoNewWindow -RedirectStandardOutput $Stdout -RedirectStandardError $Stderr
}

if (Test-Path -LiteralPath $resultDirectory) {
    Remove-Item -LiteralPath $resultDirectory -Recurse -Force
}
New-Item -ItemType Directory -Path $downloads, $resultDirectory, $transportDirectory, $toolDirectory, $cacheRoot -Force | Out-Null

try {
    Invoke-Phase 'install-powershell' {
        if (-not (Test-Path -LiteralPath (Join-Path $pwshDirectory 'pwsh.exe'))) {
            $archive = Join-Path $downloads 'powershell.zip'
            Invoke-Download ("https://github.com/PowerShell/PowerShell/releases/download/v{0}/PowerShell-{0}-win-x64.zip" -f $powerShellVersion) $archive
            New-Item -ItemType Directory -Path $pwshDirectory -Force | Out-Null
            Expand-Archive -LiteralPath $archive -DestinationPath $pwshDirectory -Force
        }
    }
    Invoke-Phase 'install-go' {
        if (-not (Test-Path -LiteralPath (Join-Path $goDirectory 'bin\go.exe'))) {
            $archive = Join-Path $downloads 'go.zip'
            Invoke-Download ("https://go.dev/dl/go{0}.windows-amd64.zip" -f $goVersion) $archive
            Expand-Archive -LiteralPath $archive -DestinationPath $toolDirectory -Force
        }
    }
    Invoke-Phase 'install-git' {
        if (-not (Test-Path -LiteralPath (Join-Path $gitDirectory 'cmd\git.exe'))) {
            $archive = Join-Path $downloads 'mingit.zip'
            Invoke-Download ("https://github.com/git-for-windows/git/releases/download/v{0}.windows.1/MinGit-{0}-64-bit.zip" -f $gitVersion) $archive
            New-Item -ItemType Directory -Path $gitDirectory -Force | Out-Null
            Expand-Archive -LiteralPath $archive -DestinationPath $gitDirectory -Force
        }
    }
    Invoke-Phase 'install-python' {
        if (-not (Test-Path -LiteralPath (Join-Path $pythonDirectory 'python.exe'))) {
            $archive = Join-Path $downloads 'python.zip'
            Invoke-Download ("https://www.python.org/ftp/python/{0}/python-{0}-embed-amd64.zip" -f $pythonVersion) $archive
            New-Item -ItemType Directory -Path $pythonDirectory -Force | Out-Null
            Expand-Archive -LiteralPath $archive -DestinationPath $pythonDirectory -Force
        }
    }

    $choco = 'C:\ProgramData\chocolatey\bin\choco.exe'
    Invoke-Phase 'install-mingw' {
        if (-not (Test-Path -LiteralPath $choco)) {
            Set-ExecutionPolicy Bypass -Scope Process -Force
            [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072
            Invoke-Expression ((New-Object Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))
        }
        $existing = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -eq $existing) {
            Invoke-NativeChecked $choco @('install', 'mingw', '--yes', '--no-progress', '--limit-output') (Join-Path $resultDirectory 'chocolatey-mingw.log')
        }
    }

    $gcc = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse | Select-Object -First 1 -ExpandProperty FullName
    $sh = Get-ChildItem -LiteralPath $gitDirectory -Filter 'sh.exe' -File -Recurse | Where-Object { $_.FullName -like '*\usr\bin\sh.exe' } | Select-Object -First 1 -ExpandProperty FullName
    if (-not $gcc -or -not $sh) { throw 'Pinned gcc.exe or MinGit usr\bin\sh.exe is missing.' }
    $env:Path = @(
        (Join-Path $goDirectory 'bin'), (Join-Path $gitDirectory 'cmd'),
        (Split-Path -Parent $sh), $pwshDirectory, $pythonDirectory,
        (Split-Path -Parent $gcc), 'C:\ProgramData\chocolatey\bin',
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
    New-Item -ItemType Directory -Path $env:GOCACHE, $env:GOMODCACHE, $env:GOPATH -Force | Out-Null

    $go = Join-Path $goDirectory 'bin\go.exe'
    $git = Join-Path $gitDirectory 'cmd\git.exe'
    $python = Join-Path $pythonDirectory 'python.exe'

    Invoke-Phase 'checkout-product' {
        if (Test-Path -LiteralPath $productDirectory) { Remove-Item -LiteralPath $productDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path $productDirectory -Force | Out-Null
        Invoke-NativeChecked $git @('-C', $productDirectory, 'init') (Join-Path $resultDirectory 'product-init.log')
        Invoke-NativeChecked $git @('-C', $productDirectory, 'remote', 'add', 'origin', $repositoryUrl) (Join-Path $resultDirectory 'product-remote.log')
        Invoke-NativeChecked $git @('-C', $productDirectory, 'fetch', '--depth', '1', 'origin', $productCommit) (Join-Path $resultDirectory 'product-fetch.log')
        Invoke-NativeChecked $git @('-C', $productDirectory, 'checkout', '--detach', 'FETCH_HEAD') (Join-Path $resultDirectory 'product-checkout.log')
        if ((& $git -C $productDirectory rev-parse HEAD).Trim() -ne $productCommit) { throw 'Product SHA mismatch.' }
    }
    Invoke-Phase 'checkout-harness' {
        if (Test-Path -LiteralPath $harnessSourceDirectory) { Remove-Item -LiteralPath $harnessSourceDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path $harnessSourceDirectory -Force | Out-Null
        Invoke-NativeChecked $git @('-C', $harnessSourceDirectory, 'init') (Join-Path $resultDirectory 'harness-init.log')
        Invoke-NativeChecked $git @('-C', $harnessSourceDirectory, 'remote', 'add', 'origin', $repositoryUrl) (Join-Path $resultDirectory 'harness-remote.log')
        Invoke-NativeChecked $git @('-C', $harnessSourceDirectory, 'fetch', '--depth', '1', 'origin', $harnessCommit) (Join-Path $resultDirectory 'harness-fetch.log')
        Invoke-NativeChecked $git @('-C', $harnessSourceDirectory, 'checkout', '--detach', 'FETCH_HEAD') (Join-Path $resultDirectory 'harness-checkout.log')
        if ((& $git -C $harnessSourceDirectory rev-parse HEAD).Trim() -ne $harnessCommit) { throw 'Harness SHA mismatch.' }
        if (Test-Path -LiteralPath $harnessDirectory) { Remove-Item -LiteralPath $harnessDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path (Split-Path -Parent $harnessDirectory) -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $harnessSourceDirectory 'tools\ci-bench') -Destination $harnessDirectory -Recurse
    }
    Invoke-Phase 'download-a4-scripts' {
        foreach ($entry in $expectedScriptHashes.GetEnumerator()) {
            $target = Join-Path $harnessDirectory $entry.Key
            New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
            $blob = 'a4/' + ($entry.Key -replace '\\', '/')
            Receive-ScriptBlob $blob $target $entry.Value
        }
    }
    Invoke-Phase 'validate-toolchain-and-scripts' {
        $toolchain = @(
            (& $go version), (& $git --version), (& $gcc --version | Select-Object -First 1),
            (& $python --version), (& (Join-Path $pwshDirectory 'pwsh.exe') --version),
            "product=$productCommit", "harness=$harnessCommit"
        )
        [IO.File]::WriteAllLines((Join-Path $resultDirectory 'toolchain.txt'), $toolchain)
        if ((& $go env GOVERSION).Trim() -ne 'go1.26.7') { throw 'Go version mismatch.' }
        $parseErrors = [Collections.Generic.List[string]]::new()
        Get-ChildItem -LiteralPath $harnessDirectory -Filter '*.ps1' -Recurse | ForEach-Object {
            $tokens = $null
            $errors = $null
            [Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref] $tokens, [ref] $errors) | Out-Null
            foreach ($error in $errors) { $parseErrors.Add("$($_.FullName): $error") | Out-Null }
        }
        if ($parseErrors.Count -ne 0) { throw ($parseErrors -join '; ') }
    }
    Invoke-Phase 'collect-environment' {
        $now = [DateTimeOffset]::UtcNow.ToString('o')
        Invoke-NativeChecked (Join-Path $pwshDirectory 'pwsh.exe') @(
            '-NoProfile', '-File', (Join-Path $harnessDirectory 'collect-environment.ps1'),
            '-OutputDirectory', (Join-Path $resultDirectory 'environment'),
            '-RepositoryPath', $productDirectory, '-RunId', 'a4-bootstrap',
            '-RunLabel', 'bootstrap-environment', '-CommandLine', 'A4 overlay gate',
            '-StartTimeUtc', $now, '-EndTimeUtc', $now, '-ExitCode', '0'
        ) (Join-Path $resultDirectory 'environment.log')
    }
    Invoke-Phase 'prepare-warm-cache-seed' {
        Invoke-NativeChecked $go @('mod', 'download') (Join-Path $resultDirectory 'go-mod-download.log') $productDirectory
        Invoke-NativeChecked $go @('test', '-run', '^$', '-timeout', '30m', './...') (Join-Path $resultDirectory 'warm-build-cache.log') $productDirectory
        Invoke-NativeChecked $go @('clean', '-testcache') (Join-Path $resultDirectory 'clean-testcache.log') $productDirectory
        if (Test-Path -LiteralPath $seedArchive) { Remove-Item -LiteralPath $seedArchive -Force }
        Invoke-NativeChecked 'C:\Windows\System32\tar.exe' @('-cf', $seedArchive, '-C', $activeCache, '.') (Join-Path $resultDirectory 'archive-cache.log')
    }
    Invoke-Phase 'windows-overlay-gate' {
        $gate = Join-Path $resultDirectory 'gate'
        $plan = Join-Path $gate 'plan'
        New-Item -ItemType Directory -Path $gate -Force | Out-Null
        Invoke-NativeChecked $python @(
            (Join-Path $harnessDirectory 'generate-test-overlays.py'), '--repo', $productDirectory,
            '--package', './internal/sem', '--output', $plan, '--shards', '2',
            '--goos', 'windows', '--goarch', 'amd64', '--cgo-enabled', '1'
        ) (Join-Path $gate 'generate.log')
        $packageDirectory = Join-Path $productDirectory 'internal\sem'
        $listFiles = @()
        for ($index = 1; $index -le 2; $index++) {
            $binary = Join-Path $gate ("shard-{0:00}.test.exe" -f $index)
            $overlay = Join-Path $plan ("shard-{0:00}\overlay.json" -f $index)
            Invoke-NativeChecked $go @('test', '-vet=off', '-overlay', $overlay, '-c', './internal/sem', '-o', $binary) (Join-Path $gate ("compile-{0:00}.log" -f $index)) $productDirectory
            $listPath = Join-Path $gate ("list-{0:00}.txt" -f $index)
            Invoke-TestList $binary $listPath $packageDirectory
            $listFiles += $listPath
            $marker = Join-Path $gate ("testmain-{0:00}.marker" -f $index)
            $probeOut = Join-Path $gate ("testmain-{0:00}.stdout" -f $index)
            $probeErr = Join-Path $gate ("testmain-{0:00}.stderr" -f $index)
            $original = Get-Location
            $previous = $ErrorActionPreference
            try {
                $env:ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER = $marker
                $ErrorActionPreference = 'Continue'
                Set-Location -LiteralPath $packageDirectory
                & $binary '-test.list' '^$' 1> $probeOut 2> $probeErr
                $probeExitCode = $LASTEXITCODE
            }
            finally {
                Set-Location -LiteralPath $original
                $ErrorActionPreference = $previous
                Remove-Item Env:\ENTIRE_GRAPH_TEST_FAKE_GIT_MARKER -ErrorAction SilentlyContinue
            }
            if ($probeExitCode -ne 23 -or (Get-Content -LiteralPath $marker -Raw) -ne 'started') {
                throw "Shard $index did not execute exact TestMain marker semantics."
            }
        }
        $monolithic = Join-Path $gate 'monolithic.test.exe'
        Invoke-NativeChecked $go @('test', '-vet=off', '-c', './internal/sem', '-o', $monolithic) (Join-Path $gate 'compile-monolithic.log') $productDirectory
        $monolithicList = Join-Path $gate 'list-monolithic.txt'
        Invoke-TestList $monolithic $monolithicList $packageDirectory
        $verifyArguments = @(
            (Join-Path $harnessDirectory 'a4\verify-test-events.py'), 'compare-lists',
            '--baseline', $monolithicList, '--output', (Join-Path $gate 'list-equivalence.json')
        )
        foreach ($list in $listFiles) { $verifyArguments += @('--candidate', $list) }
        Invoke-NativeChecked $python $verifyArguments (Join-Path $gate 'verify-list.log')
        Remove-Item -LiteralPath $monolithic, (Join-Path $gate 'shard-01.test.exe'), (Join-Path $gate 'shard-02.test.exe') -Force
        if (@(& $git -C $productDirectory status --porcelain).Count -ne 0) { throw 'Product checkout changed during overlay gate.' }
    }
    $state = 'completed'
}
catch { $fatalMessage = $_.Exception.Message }
finally {
    $endedAt = [DateTimeOffset]::UtcNow
    $metadata = [ordered]@{
        schemaVersion = 1
        state = $state
        phase = 'bootstrap-and-windows-gate'
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        goVersion = 'go1.26.7'
        goos = 'windows'
        goarch = 'amd64'
        cgoEnabled = '1'
        startTimeUtc = $startedAt.ToString('o')
        endTimeUtc = $endedAt.ToString('o')
        durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
        error = $fatalMessage
        phases = $phaseRecords
    }
    $metadata | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $resultDirectory 'bootstrap-metadata.json') -Encoding UTF8
    $archive = Join-Path $transportDirectory 'bootstrap.zip'
    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    Compress-Archive -Path (Join-Path $resultDirectory '*') -DestinationPath $archive -CompressionLevel Optimal
    $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(($archive + '.sha256'), ($hash + '  bootstrap.zip' + "`n"), [Text.Encoding]::ASCII)
    Send-ResultBlob $archive 'bootstrap/bootstrap.zip'
    Send-ResultBlob ($archive + '.sha256') 'bootstrap/bootstrap.zip.sha256'
    Write-Output "A4_BOOTSTRAP_UPLOADED state=$state sha256=$hash"
}

if ($state -ne 'completed') { exit 1 }
