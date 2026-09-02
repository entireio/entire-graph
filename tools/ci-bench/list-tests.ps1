[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $RepositoryPath,

    [Parameter(Mandatory)]
    [string] $BinaryPath,

    [Parameter(Mandatory)]
    [string] $PackageArgument,

    [Parameter(Mandatory)]
    [string] $OutputPath,

    [string] $GoCommand = 'go'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

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
    Write-Utf8File -Path $temporary -Content (ConvertTo-Json -InputObject $Value -Depth 12)
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

$resolvedRepository = (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
$resolvedBinary = (Resolve-Path -LiteralPath $BinaryPath).ProviderPath
$resolvedOutput = [System.IO.Path]::GetFullPath($OutputPath)
$outputParent = Split-Path -Parent $resolvedOutput
New-Item -ItemType Directory -Path $outputParent -Force | Out-Null
$stdoutPath = $resolvedOutput + '.stdout.log'
$stderrPath = $resolvedOutput + '.stderr.log'
$inventory = [ordered]@{
    schema = 'ci-bench.compiled-test-inventory.v1'
    repositoryPath = $resolvedRepository
    binaryPath = $resolvedBinary
    binaryName = [System.IO.Path]::GetFileName($resolvedBinary)
    packageArgument = $PackageArgument
    importPath = $null
    packageDirectory = $null
    packageDirectoryRelative = $null
    commandLine = $null
    startTimeUtc = [DateTimeOffset]::UtcNow.ToString('o')
    endTimeUtc = $null
    durationSeconds = $null
    exitCode = $null
    tests = @()
    diagnostics = [System.Collections.Generic.List[string]]::new()
}

$finalExitCode = 2
$stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
$previousLocation = Get-Location
try {
    Set-Location -LiteralPath $resolvedRepository
    $goListRaw = @(& $GoCommand 'list' '-json' $PackageArgument 2>&1 | ForEach-Object { [string] $_ })
    $goListExitCode = $LASTEXITCODE
    if ($goListExitCode -ne 0) {
        throw "go list failed with exit code $goListExitCode`: $($goListRaw -join [Environment]::NewLine)"
    }
    $package = ($goListRaw -join [Environment]::NewLine) | ConvertFrom-Json
    if ([string]::IsNullOrWhiteSpace([string] $package.ImportPath) -or
        [string]::IsNullOrWhiteSpace([string] $package.Dir)) {
        throw 'go list did not return ImportPath and Dir.'
    }
    $resolvedPackageDirectory = (Resolve-Path -LiteralPath ([string] $package.Dir)).ProviderPath
    $relativePackageDirectory = [System.IO.Path]::GetRelativePath(
        $resolvedRepository,
        $resolvedPackageDirectory
    )
    if ([System.IO.Path]::IsPathRooted($relativePackageDirectory) -or
        $relativePackageDirectory -eq '..' -or
        $relativePackageDirectory.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar)) {
        throw "package directory is outside the repository: $resolvedPackageDirectory"
    }

    $inventory.importPath = [string] $package.ImportPath
    $inventory.packageDirectory = $resolvedPackageDirectory
    $inventory.packageDirectoryRelative = $relativePackageDirectory.Replace('\', '/')
    $inventory.commandLine = "`"$resolvedBinary`" -test.list=^Test"

    Write-Utf8File -Path $stdoutPath -Content ''
    Write-Utf8File -Path $stderrPath -Content ''
    Set-Location -LiteralPath $resolvedPackageDirectory
    & $resolvedBinary '-test.list=^Test' 1> $stdoutPath 2> $stderrPath
    $listExitCode = $LASTEXITCODE
    $inventory.exitCode = $listExitCode
    $finalExitCode = $listExitCode
    $lines = @(
        Get-Content -LiteralPath $stdoutPath |
            ForEach-Object { $_.Trim() } |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    $tests = @($lines | Where-Object { $_ -match '^Test\w+$' })
    $unexpected = @($lines | Where-Object { $_ -notmatch '^Test\w+$' })
    if ($unexpected.Count -gt 0) {
        $inventory.diagnostics.Add("unexpected stdout lines: $($unexpected -join ', ')")
    }
    if ($listExitCode -ne 0) {
        $inventory.diagnostics.Add("compiled test listing exited $listExitCode")
    }
    if ($tests.Count -eq 0) {
        $inventory.diagnostics.Add('compiled binary listed no top-level tests')
    }
    if (@($tests | Sort-Object -Unique).Count -ne $tests.Count) {
        $inventory.diagnostics.Add('compiled binary listed duplicate top-level tests')
    }
    $inventory.tests = @($tests | Sort-Object -Unique)
    if ($inventory.diagnostics.Count -gt 0 -and $finalExitCode -eq 0) {
        $finalExitCode = 2
    }
}
catch {
    $inventory.diagnostics.Add($_.Exception.Message)
    if ($null -eq $inventory.exitCode) {
        $inventory.exitCode = 2
    }
    $finalExitCode = 2
}
finally {
    Set-Location -LiteralPath $previousLocation.Path
    $stopwatch.Stop()
    $inventory.endTimeUtc = [DateTimeOffset]::UtcNow.ToString('o')
    $inventory.durationSeconds = [math]::Round($stopwatch.Elapsed.TotalSeconds, 6)
    Write-JsonAtomically -Path $resolvedOutput -Value $inventory
    if ($nativePreferenceExists) {
        $PSNativeCommandUseErrorActionPreference = $originalNativePreference
    }
}

if ($finalExitCode -ne 0) {
    Write-Error "compiled test inventory failed; see $resolvedOutput" -ErrorAction Continue
}
exit $finalExitCode
