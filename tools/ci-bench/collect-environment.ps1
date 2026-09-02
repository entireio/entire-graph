[CmdletBinding()]
param(
    [Alias('ResultsDirectory', 'ResultsDir')]
    [string] $OutputDirectory = (Join-Path (Get-Location) 'ci-bench-results'),

    [string] $RepositoryPath = (Get-Location).Path,

    [string] $RunId = '',

    [string] $RunLabel = '',

    [string] $CommandLine = '',

    [string] $StartTimeUtc = '',

    [string] $EndTimeUtc = '',

    [AllowNull()]
    [Nullable[int]] $ExitCode = $null,

    [string] $OutputFileName = 'environment.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$nativePreferenceExists = Test-Path -LiteralPath 'variable:PSNativeCommandUseErrorActionPreference'
$originalNativePreference = $null
if ($nativePreferenceExists) {
    $originalNativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
}

$captureErrors = [System.Collections.Generic.List[object]]::new()
$fatalError = $null

function Add-CaptureError {
    param(
        [Parameter(Mandatory)]
        [string] $Section,

        [Parameter(Mandatory)]
        [System.Management.Automation.ErrorRecord] $ErrorRecord
    )

    $captureErrors.Add([ordered]@{
        section = $Section
        message = $ErrorRecord.Exception.Message
        category = $ErrorRecord.CategoryInfo.Category.ToString()
        fullyQualifiedErrorId = $ErrorRecord.FullyQualifiedErrorId
    }) | Out-Null
}

function Invoke-SafeCapture {
    param(
        [Parameter(Mandatory)]
        [string] $Section,

        [Parameter(Mandatory)]
        [scriptblock] $Operation
    )

    try {
        return & $Operation
    }
    catch {
        Add-CaptureError -Section $Section -ErrorRecord $_
        return $null
    }
}

function Protect-Text {
    param([AllowNull()][object] $Value)

    if ($null -eq $Value) {
        return $null
    }

    $text = [string] $Value

    # A GOPROXY or another environment value may contain URL user-info. The
    # benchmark needs the setting, not credentials embedded in it.
    return [regex]::Replace(
        $text,
        '(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@',
        '$1[REDACTED]@'
    )
}

function Invoke-NativeCapture {
    param(
        [Parameter(Mandatory)]
        [string] $Section,

        [Parameter(Mandatory)]
        [string] $Command,

        [string[]] $Arguments = @(),

        [string] $WorkingDirectory = ''
    )

    $previousLocation = $null
    try {
        if (-not [string]::IsNullOrWhiteSpace($WorkingDirectory)) {
            $previousLocation = Get-Location
            Set-Location -LiteralPath $WorkingDirectory
        }

        $lines = @(& $Command @Arguments 2>&1 | ForEach-Object { [string] $_ })
        $commandExitCode = $LASTEXITCODE
        return [ordered]@{
            command = (@($Command) + $Arguments) -join ' '
            exitCode = $commandExitCode
            output = @($lines | ForEach-Object { Protect-Text $_ })
        }
    }
    catch {
        Add-CaptureError -Section $Section -ErrorRecord $_
        return [ordered]@{
            command = (@($Command) + $Arguments) -join ' '
            exitCode = $null
            output = @()
        }
    }
    finally {
        if ($null -ne $previousLocation) {
            Set-Location -LiteralPath $previousLocation.Path
        }
    }
}

function Convert-GoEnv {
    param([AllowNull()][object] $CommandResult)

    if ($null -eq $CommandResult -or $CommandResult.exitCode -ne 0) {
        return $null
    }

    try {
        $raw = ($CommandResult.output -join [Environment]::NewLine)
        $parsed = $raw | ConvertFrom-Json -AsHashtable
        $sanitized = [ordered]@{}
        foreach ($key in ($parsed.Keys | Sort-Object)) {
            $sanitized[$key] = Protect-Text $parsed[$key]
        }
        return $sanitized
    }
    catch {
        Add-CaptureError -Section 'go env JSON' -ErrorRecord $_
        return $null
    }
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

$resolvedOutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$resolvedRepositoryPath = $RepositoryPath

$payload = [ordered]@{
    schemaVersion = 1
    captureStatus = 'failed'
    capturedAtUtc = [DateTimeOffset]::UtcNow.ToString('o')
    run = [ordered]@{
        id = $RunId
        label = $RunLabel
        commandLine = Protect-Text $CommandLine
        startTimeUtc = $StartTimeUtc
        endTimeUtc = $EndTimeUtc
        exitCode = $ExitCode
    }
    runner = $null
    azure = $null
    operatingSystem = $null
    computerSystem = $null
    processors = @()
    physicalDisks = @()
    partitions = @()
    logicalDisks = @()
    volumes = @()
    tools = $null
    goEnv = $null
    defender = $null
    repository = $null
    errors = $captureErrors
}

try {
    New-Item -ItemType Directory -Path $resolvedOutputDirectory -Force | Out-Null

    $resolvedRepositoryPath = Invoke-SafeCapture -Section 'repository path' -Operation {
        (Resolve-Path -LiteralPath $RepositoryPath).ProviderPath
    }
    if ($null -eq $resolvedRepositoryPath) {
        $resolvedRepositoryPath = [System.IO.Path]::GetFullPath($RepositoryPath)
    }

    if ([string]::IsNullOrWhiteSpace($CommandLine)) {
        $payload.run.commandLine = Protect-Text ([Environment]::CommandLine)
    }

    $systemInfo = Invoke-NativeCapture -Section 'systeminfo' -Command 'systeminfo.exe'
    Write-Utf8File -Path (Join-Path $resolvedOutputDirectory 'systeminfo.txt') -Content (($systemInfo.output -join [Environment]::NewLine) + [Environment]::NewLine)

    $os = Invoke-SafeCapture -Section 'Win32_OperatingSystem' -Operation {
        Get-CimInstance -ClassName Win32_OperatingSystem |
            Select-Object Caption, Version, BuildNumber, OSArchitecture,
                InstallationDate, LastBootUpTime, TotalVisibleMemorySize,
                FreePhysicalMemory
    }

    $windowsRegistry = Invoke-SafeCapture -Section 'Windows version registry' -Operation {
        Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion' |
            Select-Object ProductName, DisplayVersion, EditionID, InstallationType,
                CurrentBuild, UBR, BuildLabEx
    }

    $payload.operatingSystem = [ordered]@{
        cim = $os
        registry = $windowsRegistry
        systemInfoExitCode = $systemInfo.exitCode
        imageOS = $env:ImageOS
        imageVersion = $env:ImageVersion
    }

    $payload.computerSystem = Invoke-SafeCapture -Section 'Win32_ComputerSystem' -Operation {
        Get-CimInstance -ClassName Win32_ComputerSystem |
            Select-Object Manufacturer, Model, SystemType, Domain, PartOfDomain,
                NumberOfProcessors, NumberOfLogicalProcessors, TotalPhysicalMemory
    }

    $payload.processors = @(
        Invoke-SafeCapture -Section 'Win32_Processor' -Operation {
            Get-CimInstance -ClassName Win32_Processor |
                Select-Object DeviceID, Name, Manufacturer, Description,
                    SocketDesignation, NumberOfCores, NumberOfLogicalProcessors,
                    MaxClockSpeed
        }
    )

    $payload.physicalDisks = @(
        Invoke-SafeCapture -Section 'Win32_DiskDrive' -Operation {
            Get-CimInstance -ClassName Win32_DiskDrive |
                Select-Object Index, DeviceID, Model, Manufacturer, InterfaceType,
                    MediaType, Partitions, Size
        }
    )

    $payload.partitions = @(
        Invoke-SafeCapture -Section 'Win32_DiskPartition' -Operation {
            Get-CimInstance -ClassName Win32_DiskPartition |
                Select-Object DiskIndex, Index, DeviceID, Name, Type, Bootable,
                    BootPartition, PrimaryPartition, StartingOffset, Size
        }
    )

    $payload.logicalDisks = @(
        Invoke-SafeCapture -Section 'Win32_LogicalDisk' -Operation {
            Get-CimInstance -ClassName Win32_LogicalDisk |
                Select-Object DeviceID, VolumeName, FileSystem, Description,
                    DriveType, ProviderName, Size, FreeSpace
        }
    )

    $payload.volumes = @(
        Invoke-SafeCapture -Section 'Get-Volume' -Operation {
            Get-Volume |
                Select-Object DriveLetter, FileSystemLabel, FileSystem, DriveType,
                    HealthStatus, OperationalStatus, AllocationUnitSize,
                    Size, SizeRemaining, Path
        }
    )

    $azureMetadata = Invoke-SafeCapture -Section 'Azure instance metadata' -Operation {
        $imdsArguments = @{
            Headers = @{ Metadata = 'true' }
            Method = 'Get'
            Uri = 'http://169.254.169.254/metadata/instance?api-version=2021-02-01'
            TimeoutSec = 2
        }
        Invoke-RestMethod @imdsArguments
    }
    if ($null -ne $azureMetadata) {
        $payload.azure = [ordered]@{
            # Keep the benchmark-relevant image and machine facts without
            # persisting subscription, tenant, resource IDs, or VM IDs in
            # result artifacts that may later be committed to the repository.
            compute = $azureMetadata.compute |
                Select-Object name, location, vmSize, osType, publisher, offer,
                    sku, version, platformFaultDomain, platformSubFaultDomain
            network = @(
                $azureMetadata.network.interface | ForEach-Object {
                    [ordered]@{
                        macAddress = $_.macAddress
                        ipv4Subnet = @($_.ipv4.subnet | Select-Object address, prefix)
                        ipv6Subnet = @($_.ipv6.subnet | Select-Object address, prefix)
                    }
                }
            )
        }
    }

    $payload.runner = [ordered]@{
        name = $env:RUNNER_NAME
        os = $env:RUNNER_OS
        architecture = $env:RUNNER_ARCH
        environment = $env:RUNNER_ENVIRONMENT
        githubActions = $env:GITHUB_ACTIONS
        imageOS = $env:ImageOS
        imageVersion = $env:ImageVersion
        azureVmSize = if ($null -ne $azureMetadata) { $azureMetadata.compute.vmSize } else { $null }
    }

    $goVersion = Invoke-NativeCapture -Section 'go version' -Command 'go' -Arguments @('version')
    $gitVersion = Invoke-NativeCapture -Section 'git version' -Command 'git' -Arguments @('--version')
    $gccVersion = Invoke-NativeCapture -Section 'gcc version' -Command 'gcc' -Arguments @('--version')
    $goEnvCommand = Invoke-NativeCapture -Section 'go env' -Command 'go' -Arguments @('env', '-json')

    $payload.tools = [ordered]@{
        go = $goVersion
        git = $gitVersion
        gcc = $gccVersion
    }
    $payload.goEnv = Convert-GoEnv $goEnvCommand

    $defenderStatus = Invoke-SafeCapture -Section 'Get-MpComputerStatus' -Operation {
        Get-MpComputerStatus |
            Select-Object AMServiceEnabled, AntispywareEnabled, AntivirusEnabled,
                BehaviorMonitorEnabled, IoavProtectionEnabled, NISEnabled,
                OnAccessProtectionEnabled, RealTimeProtectionEnabled,
                IsTamperProtected, AntivirusSignatureVersion,
                AntivirusSignatureLastUpdated, QuickScanAge, FullScanAge
    }
    $defenderPreference = Invoke-SafeCapture -Section 'Get-MpPreference' -Operation {
        Get-MpPreference |
            Select-Object DisableArchiveScanning, DisableBehaviorMonitoring,
                DisableIOAVProtection, DisableRealtimeMonitoring,
                DisableScriptScanning, ExclusionExtension, ExclusionPath,
                ExclusionProcess
    }
    $payload.defender = [ordered]@{
        status = $defenderStatus
        preference = $defenderPreference
    }

    $repoSha = Invoke-NativeCapture -Section 'repository SHA' -Command 'git' -Arguments @('-C', $resolvedRepositoryPath, 'rev-parse', 'HEAD')
    $repoBranch = Invoke-NativeCapture -Section 'repository branch' -Command 'git' -Arguments @('-C', $resolvedRepositoryPath, 'branch', '--show-current')
    $repoStatus = Invoke-NativeCapture -Section 'repository status' -Command 'git' -Arguments @('-C', $resolvedRepositoryPath, 'status', '--porcelain=v1')

    $payload.repository = [ordered]@{
        path = $resolvedRepositoryPath
        sha = if ($repoSha.exitCode -eq 0) { $repoSha.output | Select-Object -First 1 } else { $null }
        branch = if ($repoBranch.exitCode -eq 0) { $repoBranch.output | Select-Object -First 1 } else { $null }
        dirty = if ($repoStatus.exitCode -eq 0) { $repoStatus.output.Count -gt 0 } else { $null }
        status = $repoStatus.output
    }

    $payload.captureStatus = if ($captureErrors.Count -eq 0) { 'complete' } else { 'partial' }
}
catch {
    $fatalError = $_
    Add-CaptureError -Section 'environment capture' -ErrorRecord $_
    $payload.captureStatus = 'failed'
}
finally {
    $payload.capturedAtUtc = [DateTimeOffset]::UtcNow.ToString('o')
    $payload.errors = $captureErrors

    try {
        New-Item -ItemType Directory -Path $resolvedOutputDirectory -Force | Out-Null
        $outputPath = Join-Path $resolvedOutputDirectory $OutputFileName
        $temporaryPath = $outputPath + '.tmp'
        Write-Utf8File -Path $temporaryPath -Content ($payload | ConvertTo-Json -Depth 12)
        Move-Item -LiteralPath $temporaryPath -Destination $outputPath -Force
    }
    finally {
        if ($nativePreferenceExists) {
            $PSNativeCommandUseErrorActionPreference = $originalNativePreference
        }
    }
}

if ($null -ne $fatalError) {
    throw $fatalError
}
