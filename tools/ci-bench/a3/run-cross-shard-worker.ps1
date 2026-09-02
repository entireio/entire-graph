[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $PlanPath,
    [Parameter(Mandatory)] [string] $OutputDirectory,
    [Parameter(Mandatory)] [string] $GoCommand
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
    # One separator between every token and the terminating NUL are part of
    # CreateProcessW's command-line buffer.
    return (($serialized -join ' ').Length + 1)
}

$plan = Get-Content -LiteralPath (Resolve-Path -LiteralPath $PlanPath) -Raw | ConvertFrom-Json
$output = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $output -Force | Out-Null
$goExe = (Get-Command $GoCommand -ErrorAction Stop).Source
$combined = Join-Path $output 'go-test.shard.jsonl'
[IO.File]::WriteAllText($combined, '', [Text.UTF8Encoding]::new($false))
$results = [Collections.Generic.List[object]]::new()
$started = [DateTimeOffset]::UtcNow
$workerExit = 0

foreach ($package in $plan.packages) {
    $binary = [IO.Path]::GetFullPath([string] $package.binary)
    $packageDirectory = [IO.Path]::GetFullPath([string] $package.packageDirectory)
    $safeName = 'p{0:d2}' -f ($results.Count + 1)
    $jsonPath = Join-Path $output "$safeName.jsonl"
    $stderrPath = Join-Path $output "$safeName.stderr.log"
    $argv = [Collections.Generic.List[string]]::new()
    foreach ($argument in @('tool', 'test2json', '-t', '-p', [string] $package.package, $binary,
        '-test.v=test2json', '-test.timeout=30m')) {
        $argv.Add($argument)
    }
    if ($package.mode -eq 'selected') {
        $tests = @($package.tests)
        if ($tests.Count -eq 0) { throw "selected package has no tests: $($package.package)" }
        $escaped = @($tests | ForEach-Object { [regex]::Escape([string] $_) })
        $argv.Add('-test.run=^(?:' + ($escaped -join '|') + ')$')
    }
    elseif ($package.mode -ne 'full') {
        throw "unknown package execution mode: $($package.mode)"
    }

    $argvCharacters = Get-WindowsCommandLineCharacters -Executable $goExe -Arguments @($argv)
    if ($argvCharacters -gt 30000) {
        throw "Windows argv limit preflight failed for $($package.package): $argvCharacters"
    }

    $processStarted = [DateTimeOffset]::UtcNow
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $goExe -ArgumentList @($argv) `
        -WorkingDirectory $packageDirectory -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $jsonPath -RedirectStandardError $stderrPath
    $clock.Stop()
    [IO.File]::AppendAllText(
        $combined,
        [IO.File]::ReadAllText($jsonPath, [Text.UTF8Encoding]::new($false)),
        [Text.UTF8Encoding]::new($false)
    )
    $results.Add([ordered]@{
        package = $package.package
        mode = $package.mode
        selectedTestCount = @($package.tests).Count
        absoluteExecutable = $binary
        packageWorkingDirectory = $packageDirectory
        arguments = @($argv)
        argvCharacters = $argvCharacters
        startTimeUtc = $processStarted.ToString('o')
        durationSeconds = [Math]::Round($clock.Elapsed.TotalSeconds, 6)
        exitCode = $process.ExitCode
        jsonPath = $jsonPath
        stderrPath = $stderrPath
    }) | Out-Null
    if ($process.ExitCode -ne 0) {
        $workerExit = $process.ExitCode
        break
    }
}

$ended = [DateTimeOffset]::UtcNow
Write-JsonFile -Path (Join-Path $output 'worker-summary.json') -Value ([ordered]@{
    shardIndex = $plan.index
    nonempty = @($plan.packages).Count -gt 0
    startTimeUtc = $started.ToString('o')
    endTimeUtc = $ended.ToString('o')
    durationSeconds = ($ended - $started).TotalSeconds
    exitCode = $workerExit
    packageInvocations = $results
    combinedJson = $combined
})
exit $workerExit
