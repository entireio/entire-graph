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
$root = 'C:\entire-a1'
$downloadDirectory = Join-Path $root 'downloads'
$resultDirectory = 'C:\ci-bench-results\a1\bootstrap-smoke'
$productDirectory = 'C:\src\entire-graph'
$harnessSourceDirectory = 'C:\src\entire-graph-harness'
$harnessDirectory = 'C:\ci-bench\tools\ci-bench'
$toolDirectory = 'C:\tools'
$goDirectory = Join-Path $toolDirectory 'go'
$gitDirectory = Join-Path $toolDirectory 'mingit'
$pwshDirectory = Join-Path $toolDirectory 'pwsh'
$phaseRecords = [System.Collections.Generic.List[object]]::new()
$bootstrapStarted = [DateTimeOffset]::UtcNow

function Invoke-Phase {
    param(
        [Parameter(Mandatory)][string] $Name,
        [Parameter(Mandatory)][scriptblock] $Action
    )

    $started = [DateTimeOffset]::UtcNow
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
        $phaseRecords.Add([ordered]@{
            name = $Name
            status = $status
            startTimeUtc = $started.ToString('o')
            durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 3)
            message = $message
        }) | Out-Null
    }
}

function Invoke-NativeChecked {
    param(
        [Parameter(Mandatory)][string] $Command,
        [string[]] $Arguments = @(),
        [string] $LogPath = ''
    )

    $previousErrorActionPreference = $ErrorActionPreference
    $nativeExitCode = 127
    try {
        # Windows PowerShell wraps native stderr as ErrorRecord objects. Keep
        # expected diagnostics in the log and judge success by the native code.
        $ErrorActionPreference = 'Continue'
        if ([string]::IsNullOrWhiteSpace($LogPath)) {
            & $Command @Arguments
        }
        else {
            & $Command @Arguments *> $LogPath
        }
        $nativeExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($nativeExitCode -ne 0) {
        throw "Native command failed with exit code ${nativeExitCode}: $Command $($Arguments -join ' ')"
    }
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
    $blobUri = 'https://{0}.blob.core.windows.net/results/{1}' -f $StorageAccount, $BlobName
    Invoke-WebRequest -UseBasicParsing -Method Put -Uri $blobUri -InFile $Path -Headers @{
        Authorization = 'Bearer ' + $token
        'x-ms-version' = '2023-11-03'
        'x-ms-blob-type' = 'BlockBlob'
    } | Out-Null
    Remove-Variable token
}

New-Item -ItemType Directory -Path $downloadDirectory, $resultDirectory, $toolDirectory -Force | Out-Null

try {
    Invoke-Phase -Name 'install-powershell' -Action {
        if (-not (Test-Path -LiteralPath (Join-Path $pwshDirectory 'pwsh.exe'))) {
            $archive = Join-Path $downloadDirectory 'powershell.zip'
            Invoke-Download -Uri ("https://github.com/PowerShell/PowerShell/releases/download/v{0}/PowerShell-{0}-win-x64.zip" -f $powerShellVersion) -Path $archive
            New-Item -ItemType Directory -Path $pwshDirectory -Force | Out-Null
            Expand-Archive -LiteralPath $archive -DestinationPath $pwshDirectory -Force
        }
    }

    Invoke-Phase -Name 'install-go' -Action {
        if (-not (Test-Path -LiteralPath (Join-Path $goDirectory 'bin\go.exe'))) {
            $archive = Join-Path $downloadDirectory 'go.zip'
            Invoke-Download -Uri ("https://go.dev/dl/go{0}.windows-amd64.zip" -f $goVersion) -Path $archive
            Expand-Archive -LiteralPath $archive -DestinationPath $toolDirectory -Force
        }
    }

    Invoke-Phase -Name 'install-git' -Action {
        if (-not (Test-Path -LiteralPath (Join-Path $gitDirectory 'cmd\git.exe'))) {
            $archive = Join-Path $downloadDirectory 'mingit.zip'
            Invoke-Download -Uri ("https://github.com/git-for-windows/git/releases/download/v{0}.windows.1/MinGit-{0}-64-bit.zip" -f $gitVersion) -Path $archive
            New-Item -ItemType Directory -Path $gitDirectory -Force | Out-Null
            Expand-Archive -LiteralPath $archive -DestinationPath $gitDirectory -Force
        }
    }

    $env:Path = @(
        (Join-Path $goDirectory 'bin'),
        (Join-Path $gitDirectory 'cmd'),
        (Join-Path $pwshDirectory ''),
        'C:\ProgramData\chocolatey\bin',
        'C:\ProgramData\mingw64\mingw64\bin',
        [Environment]::GetEnvironmentVariable('Path', 'Machine')
    ) -join ';'

    Invoke-Phase -Name 'install-mingw' -Action {
        $choco = 'C:\ProgramData\chocolatey\bin\choco.exe'
        if (-not (Test-Path -LiteralPath $choco)) {
            Set-ExecutionPolicy Bypass -Scope Process -Force
            [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072
            Invoke-Expression ((New-Object Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))
        }
        $existingGcc = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -eq $existingGcc) {
            Invoke-NativeChecked -Command $choco -Arguments @('install', 'mingw', '--yes', '--no-progress', '--limit-output') -LogPath (Join-Path $resultDirectory 'chocolatey-mingw.log')
        }
    }

    $gccPath = Get-ChildItem -LiteralPath 'C:\ProgramData\mingw64' -Filter 'gcc.exe' -File -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty FullName
    if ([string]::IsNullOrWhiteSpace($gccPath)) { throw 'Chocolatey mingw completed without installing gcc.exe.' }
    $mingwBinDirectory = Split-Path -Parent $gccPath

    $env:Path = @(
        (Join-Path $goDirectory 'bin'),
        (Join-Path $gitDirectory 'cmd'),
        (Join-Path $pwshDirectory ''),
        'C:\ProgramData\chocolatey\bin',
        $mingwBinDirectory,
        [Environment]::GetEnvironmentVariable('Path', 'Machine')
    ) -join ';'

    $git = Join-Path $gitDirectory 'cmd\git.exe'
    $go = Join-Path $goDirectory 'bin\go.exe'
    $pwsh = Join-Path $pwshDirectory 'pwsh.exe'

    Invoke-Phase -Name 'checkout-product' -Action {
        if (Test-Path -LiteralPath $productDirectory) { Remove-Item -LiteralPath $productDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path $productDirectory -Force | Out-Null
        Invoke-NativeChecked -Command $git -Arguments @('-C', $productDirectory, 'init') -LogPath (Join-Path $resultDirectory 'product-init.log')
        Invoke-NativeChecked -Command $git -Arguments @('-C', $productDirectory, 'remote', 'add', 'origin', $repositoryUrl)
        Invoke-NativeChecked -Command $git -Arguments @('-C', $productDirectory, 'fetch', '--depth', '1', 'origin', $productCommit) -LogPath (Join-Path $resultDirectory 'product-fetch.log')
        Invoke-NativeChecked -Command $git -Arguments @('-C', $productDirectory, 'checkout', '--detach', 'FETCH_HEAD') -LogPath (Join-Path $resultDirectory 'product-checkout.log')
        $actualProductCommit = (& $git -C $productDirectory rev-parse HEAD).Trim()
        if ($actualProductCommit -ne $productCommit) { throw "Wrong product commit: $actualProductCommit" }
    }

    Invoke-Phase -Name 'checkout-harness' -Action {
        if (Test-Path -LiteralPath $harnessSourceDirectory) { Remove-Item -LiteralPath $harnessSourceDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path $harnessSourceDirectory -Force | Out-Null
        Invoke-NativeChecked -Command $git -Arguments @('-C', $harnessSourceDirectory, 'init') -LogPath (Join-Path $resultDirectory 'harness-init.log')
        Invoke-NativeChecked -Command $git -Arguments @('-C', $harnessSourceDirectory, 'remote', 'add', 'origin', $repositoryUrl)
        Invoke-NativeChecked -Command $git -Arguments @('-C', $harnessSourceDirectory, 'fetch', '--depth', '1', 'origin', $harnessCommit) -LogPath (Join-Path $resultDirectory 'harness-fetch.log')
        Invoke-NativeChecked -Command $git -Arguments @('-C', $harnessSourceDirectory, 'checkout', '--detach', 'FETCH_HEAD') -LogPath (Join-Path $resultDirectory 'harness-checkout.log')
        $actualHarnessCommit = (& $git -C $harnessSourceDirectory rev-parse HEAD).Trim()
        if ($actualHarnessCommit -ne $harnessCommit) { throw "Wrong harness commit: $actualHarnessCommit" }
        if (Test-Path -LiteralPath $harnessDirectory) { Remove-Item -LiteralPath $harnessDirectory -Recurse -Force }
        New-Item -ItemType Directory -Path (Split-Path -Parent $harnessDirectory) -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $harnessSourceDirectory 'tools\ci-bench') -Destination $harnessDirectory -Recurse -Force
    }

    Invoke-Phase -Name 'record-toolchain' -Action {
        $records = @(
            (& $pwsh --version),
            (& $go version),
            (& $git --version),
            (& $gccPath --version | Select-Object -First 1),
            "product=$productCommit",
            "harness=$harnessCommit"
        )
        [IO.File]::WriteAllLines((Join-Path $resultDirectory 'toolchain.txt'), $records)
    }

    Invoke-Phase -Name 'parse-harness' -Action {
        $parseErrors = [System.Collections.Generic.List[string]]::new()
        Get-ChildItem -LiteralPath $harnessDirectory -Filter '*.ps1' | ForEach-Object {
            $tokens = $null
            $errors = $null
            [Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref] $tokens, [ref] $errors) | Out-Null
            foreach ($parseError in $errors) { $parseErrors.Add("$($_.Name): $parseError") | Out-Null }
        }
        if ($parseErrors.Count -ne 0) { throw ($parseErrors -join '; ') }
    }

    Invoke-Phase -Name 'environment-smoke' -Action {
        $now = [DateTimeOffset]::UtcNow.ToString('o')
        Invoke-NativeChecked -Command $pwsh -Arguments @(
            '-NoProfile', '-File', (Join-Path $harnessDirectory 'collect-environment.ps1'),
            '-OutputDirectory', (Join-Path $resultDirectory 'environment'),
            '-RepositoryPath', $productDirectory,
            '-RunId', 'a1-bootstrap-smoke',
            '-RunLabel', 'environment-smoke',
            '-CommandLine', 'go test -json -timeout 30m ./...',
            '-StartTimeUtc', $now,
            '-EndTimeUtc', $now,
            '-ExitCode', '0'
        ) -LogPath (Join-Path $resultDirectory 'environment-smoke.log')
    }

    Invoke-Phase -Name 'monitor-smoke' -Action {
        Invoke-NativeChecked -Command $pwsh -Arguments @(
            '-NoProfile', '-File', (Join-Path $harnessDirectory 'monitor-resources.ps1'),
            '-OutputPath', (Join-Path $resultDirectory 'resource-smoke.jsonl'),
            '-IntervalSeconds', '1',
            '-MaxSamples', '5'
        ) -LogPath (Join-Path $resultDirectory 'monitor-smoke.log')
    }

    Invoke-Phase -Name 'suite-wrapper-smoke' -Action {
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '1'
        Invoke-NativeChecked -Command $pwsh -Arguments @(
            '-NoProfile', '-File', (Join-Path $harnessDirectory 'run-go-suite.ps1'),
            '-OutputDirectory', (Join-Path $resultDirectory 'suite-wrapper'),
            '-RepositoryPath', $productDirectory,
            '-RunId', 'a1-suite-wrapper-smoke',
            '-RunLabel', 'bounded-harness-smoke',
            '-ExpectedRepositorySha', $productCommit,
            '-ExpectedGoVersion', ('go' + $goVersion),
            '-Packages', './internal/termsafe'
        ) -LogPath (Join-Path $resultDirectory 'suite-wrapper-smoke.log')
    }
}
finally {
    $bootstrapEnded = [DateTimeOffset]::UtcNow
    $metadata = [ordered]@{
        schemaVersion = 1
        phase = 'bootstrap-and-smoke'
        productCommit = $productCommit
        harnessCommit = $harnessCommit
        goVersion = $goVersion
        gitVersion = $gitVersion
        powerShellVersion = $powerShellVersion
        startTimeUtc = $bootstrapStarted.ToString('o')
        endTimeUtc = $bootstrapEnded.ToString('o')
        durationSeconds = [math]::Round(($bootstrapEnded - $bootstrapStarted).TotalSeconds, 3)
        phases = $phaseRecords
    }
    $metadata | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $resultDirectory 'bootstrap-metadata.json') -Encoding UTF8

    $archive = 'C:\ci-bench-results\a1\bootstrap-smoke.zip'
    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    Compress-Archive -Path (Join-Path $resultDirectory '*') -DestinationPath $archive -CompressionLevel Optimal
    $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(
        ($archive + '.sha256'),
        ($hash + '  bootstrap-smoke.zip' + "`n"),
        [Text.Encoding]::ASCII
    )
    Send-ResultBlob -Path $archive -BlobName 'bootstrap-smoke.zip'
    Send-ResultBlob -Path ($archive + '.sha256') -BlobName 'bootstrap-smoke.zip.sha256'
    Write-Output "A1_BOOTSTRAP_SMOKE_UPLOADED sha256=$hash"
}
