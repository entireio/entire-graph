[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $StorageAccount,

    [Parameter(Mandatory)]
    [string] $ScriptBlob,

    [string] $RunId = '',

    [string] $RunLabel = '',

    [string] $ExpectedVmSize = '',

    [int] $ExpectedVcpus = 0,

    [string] $Packages = ''
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

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

$token = Get-StorageToken
$scriptUri = 'https://{0}.blob.core.windows.net/scripts/{1}' -f $StorageAccount, $ScriptBlob
$temporaryScript = Join-Path $env:TEMP ('entire-a1-' + [guid]::NewGuid().ToString('N') + '.ps1')

try {
    Invoke-WebRequest -UseBasicParsing -Uri $scriptUri -OutFile $temporaryScript -Headers @{
        Authorization = 'Bearer ' + $token
        'x-ms-version' = '2023-11-03'
    }
    Remove-Variable token

    $driverArguments = @(
        '-NoLogo',
        '-NoProfile',
        '-NonInteractive',
        '-ExecutionPolicy', 'Bypass',
        '-File', $temporaryScript,
        '-StorageAccount', $StorageAccount
    )
    if (-not [string]::IsNullOrWhiteSpace($RunId)) {
        $driverArguments += @('-RunId', $RunId)
    }
    if (-not [string]::IsNullOrWhiteSpace($RunLabel)) {
        $driverArguments += @('-RunLabel', $RunLabel)
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedVmSize)) {
        $driverArguments += @('-ExpectedVmSize', $ExpectedVmSize)
    }
    if ($ExpectedVcpus -gt 0) {
        $driverArguments += @('-ExpectedVcpus', [string] $ExpectedVcpus)
    }
    if (-not [string]::IsNullOrWhiteSpace($Packages)) {
        $driverArguments += @('-Packages', $Packages)
    }

    & powershell.exe @driverArguments
    $driverExitCode = $LASTEXITCODE
    if ($null -eq $driverExitCode) { $driverExitCode = 1 }
    exit $driverExitCode
}
finally {
    Remove-Item -LiteralPath $temporaryScript -Force -ErrorAction SilentlyContinue
}
