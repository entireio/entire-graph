[CmdletBinding()]
param(
    [Parameter(Mandatory, Position = 0)]
    [string] $OutputPath,

    [Parameter(Position = 1)]
    [string] $StopFile = '',

    [Parameter(Position = 2)]
    [ValidateRange(0.25, 3600.0)]
    [double] $IntervalSeconds = 2.0,

    [Parameter(Position = 3)]
    [ValidateRange(0, 2147483647)]
    [int] $MonitoredProcessId = 0,

    [Parameter(Position = 4)]
    [ValidateRange(0, 2147483647)]
    [int] $MaxSamples = 0
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Convert-NullableDouble {
    param([AllowNull()][object] $Value)

    if ($null -eq $Value) {
        return $null
    }

    return [double] $Value
}

function Convert-NullableInt64 {
    param([AllowNull()][object] $Value)

    if ($null -eq $Value) {
        return $null
    }

    return [long] $Value
}

function New-SampleError {
    param(
        [Parameter(Mandatory)]
        [string] $Section,

        [Parameter(Mandatory)]
        [System.Management.Automation.ErrorRecord] $ErrorRecord
    )

    return [ordered]@{
        section = $Section
        message = $ErrorRecord.Exception.Message
        category = $ErrorRecord.CategoryInfo.Category.ToString()
    }
}

$resolvedOutputPath = [System.IO.Path]::GetFullPath($OutputPath)
$outputParent = Split-Path -Parent $resolvedOutputPath
if ([string]::IsNullOrWhiteSpace($outputParent)) {
    $outputParent = (Get-Location).Path
}
New-Item -ItemType Directory -Path $outputParent -Force | Out-Null

$writer = $null
$startedAt = [DateTimeOffset]::UtcNow
$sampleCount = 0
$failedSampleCount = 0
$terminalError = $null

try {
    $writer = [System.IO.StreamWriter]::new(
        $resolvedOutputPath,
        $false,
        [System.Text.UTF8Encoding]::new($false)
    )
    $writer.AutoFlush = $true

    $writer.WriteLine((([ordered]@{
        type = 'resource-monitor-start'
        schemaVersion = 1
        timestampUtc = $startedAt.ToString('o')
        intervalSeconds = $IntervalSeconds
        monitoredProcessId = $MonitoredProcessId
        stopFile = if ([string]::IsNullOrWhiteSpace($StopFile)) { $null } else { [System.IO.Path]::GetFullPath($StopFile) }
        maxSamples = $MaxSamples
    }) | ConvertTo-Json -Compress -Depth 5))

    while ($true) {
        if (-not [string]::IsNullOrWhiteSpace($StopFile) -and
            (Test-Path -LiteralPath $StopFile)) {
            break
        }

        if ($MaxSamples -gt 0 -and $sampleCount -ge $MaxSamples) {
            break
        }

        if ($MonitoredProcessId -gt 0 -and
            $sampleCount -gt 0 -and
            $null -eq (Get-Process -Id $MonitoredProcessId -ErrorAction SilentlyContinue)) {
            break
        }

        $sampleStartedAt = [DateTimeOffset]::UtcNow
        $sampleErrors = [System.Collections.Generic.List[object]]::new()
        $cpu = $null
        $memory = $null
        $disks = @()
        $process = $null

        try {
            $cpuRecord = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Processor -Filter "Name='_Total'"
            $cpu = [ordered]@{
                name = $cpuRecord.Name
                percentProcessorTime = Convert-NullableDouble $cpuRecord.PercentProcessorTime
                percentUserTime = Convert-NullableDouble $cpuRecord.PercentUserTime
                percentPrivilegedTime = Convert-NullableDouble $cpuRecord.PercentPrivilegedTime
                interruptRatePerSecond = Convert-NullableDouble $cpuRecord.InterruptsPersec
            }
        }
        catch {
            $sampleErrors.Add((New-SampleError -Section 'cpu' -ErrorRecord $_)) | Out-Null
        }

        try {
            $memoryRecord = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Memory
            $memory = [ordered]@{
                availableBytes = Convert-NullableInt64 $memoryRecord.AvailableBytes
                cacheBytes = Convert-NullableInt64 $memoryRecord.CacheBytes
                committedBytes = Convert-NullableInt64 $memoryRecord.CommittedBytes
                commitLimitBytes = Convert-NullableInt64 $memoryRecord.CommitLimit
                pagesPerSecond = Convert-NullableDouble $memoryRecord.PagesPersec
            }
        }
        catch {
            $sampleErrors.Add((New-SampleError -Section 'memory' -ErrorRecord $_)) | Out-Null
        }

        try {
            $disks = @(
                Get-CimInstance -ClassName Win32_PerfFormattedData_PerfDisk_PhysicalDisk |
                    Sort-Object Name |
                    ForEach-Object {
                        [ordered]@{
                            name = $_.Name
                            percentDiskTime = Convert-NullableDouble $_.PercentDiskTime
                            percentDiskReadTime = Convert-NullableDouble $_.PercentDiskReadTime
                            percentDiskWriteTime = Convert-NullableDouble $_.PercentDiskWriteTime
                            currentDiskQueueLength = Convert-NullableDouble $_.CurrentDiskQueueLength
                            averageDiskQueueLength = Convert-NullableDouble $_.AvgDiskQueueLength
                            diskBytesPerSecond = Convert-NullableDouble $_.DiskBytesPersec
                            diskReadBytesPerSecond = Convert-NullableDouble $_.DiskReadBytesPersec
                            diskWriteBytesPerSecond = Convert-NullableDouble $_.DiskWriteBytesPersec
                            diskReadsPerSecond = Convert-NullableDouble $_.DiskReadsPersec
                            diskWritesPerSecond = Convert-NullableDouble $_.DiskWritesPersec
                        }
                    }
            )
        }
        catch {
            $sampleErrors.Add((New-SampleError -Section 'disk' -ErrorRecord $_)) | Out-Null
        }

        if ($MonitoredProcessId -gt 0) {
            try {
                $processRecord = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfProc_Process -Filter "IDProcess=$MonitoredProcessId" -ErrorAction Stop |
                        Select-Object -First 1
                if ($null -ne $processRecord) {
                    $process = [ordered]@{
                        id = $MonitoredProcessId
                        name = $processRecord.Name
                        percentProcessorTime = Convert-NullableDouble $processRecord.PercentProcessorTime
                        workingSetBytes = Convert-NullableInt64 $processRecord.WorkingSet
                        privateBytes = Convert-NullableInt64 $processRecord.PrivateBytes
                        ioDataBytesPerSecond = Convert-NullableDouble $processRecord.IODataBytesPersec
                    }
                }
            }
            catch {
                $sampleErrors.Add((New-SampleError -Section 'process' -ErrorRecord $_)) | Out-Null
            }
        }

        if ($sampleErrors.Count -gt 0) {
            $failedSampleCount++
        }

        $writer.WriteLine((([ordered]@{
            type = 'resource-sample'
            schemaVersion = 1
            sequence = $sampleCount
            timestampUtc = $sampleStartedAt.ToString('o')
            cpu = $cpu
            memory = $memory
            disks = $disks
            process = $process
            errors = $sampleErrors
        }) | ConvertTo-Json -Compress -Depth 8))

        $sampleCount++

        if (-not [string]::IsNullOrWhiteSpace($StopFile) -and
            (Test-Path -LiteralPath $StopFile)) {
            break
        }

        Start-Sleep -Milliseconds ([math]::Max(1, [int][math]::Round($IntervalSeconds * 1000.0)))
    }
}
catch {
    $terminalError = $_
}
finally {
    if ($null -ne $writer) {
        $endedAt = [DateTimeOffset]::UtcNow
        $summary = [ordered]@{
            type = 'resource-monitor-end'
            schemaVersion = 1
            timestampUtc = $endedAt.ToString('o')
            durationSeconds = [math]::Round(($endedAt - $startedAt).TotalSeconds, 6)
            sampleCount = $sampleCount
            samplesWithErrors = $failedSampleCount
            error = if ($null -eq $terminalError) { $null } else { $terminalError.Exception.Message }
        }

        try {
            $writer.WriteLine(($summary | ConvertTo-Json -Compress -Depth 5))
        }
        finally {
            $writer.Dispose()
        }
    }
}

if ($null -ne $terminalError) {
    throw $terminalError
}
