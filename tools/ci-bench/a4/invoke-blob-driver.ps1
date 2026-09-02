[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $StorageAccount,
    [Parameter(Mandatory)][string] $ScriptBlob
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

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

$token = Get-StorageToken
$uri = 'https://{0}.blob.core.windows.net/scripts/{1}' -f $StorageAccount, $ScriptBlob
$temporary = Join-Path $env:TEMP ('entire-a4-' + [guid]::NewGuid().ToString('N') + '.ps1')
try {
    Invoke-WebRequest -UseBasicParsing -Uri $uri -OutFile $temporary -Headers @{
        Authorization = 'Bearer ' + $token
        'x-ms-version' = '2023-11-03'
    }
    Remove-Variable token
    & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $temporary -StorageAccount $StorageAccount
    exit $LASTEXITCODE
}
finally { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue }
