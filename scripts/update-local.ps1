[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^v?\d+\.\d+\.\d+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$')]
    [string]$Version,

    [string]$RegistryImage = 'docker.io/asuka20011204/visit-ready-agent',
    [string]$ContainerName = 'visit-ready-agent',
    [ValidateRange(1, 65535)]
    [int]$AppPort = 8097,
    [string]$EnvFile = '.env',
    [switch]$ReplaceStandaloneContainer,
    [switch]$UseCompose
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$Version = $Version.TrimStart('v')
$remoteImage = "${RegistryImage}:$Version"
$rollbackContainer = "$ContainerName-rollback"

function Invoke-Docker([string[]]$Arguments) {
    $output = & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
    return $output
}

function Find-Container([string]$Name) {
    $found = Invoke-Docker @('ps', '-a', '--filter', "name=^/$Name$", '--format', '{{.Names}}')
    return $found -eq $Name
}

function Wait-Ready([int]$Port) {
    $deadline = (Get-Date).AddSeconds(45)
    do {
        try {
            Invoke-RestMethod "http://127.0.0.1:$Port/readyz" -TimeoutSec 3 | Out-Null
            return $true
        } catch {
            Start-Sleep -Seconds 2
        }
    } while ((Get-Date) -lt $deadline)
    return $false
}

function Restore-Container {
    if (-not (Find-Container $rollbackContainer)) {
        throw "Rollback container '$rollbackContainer' is missing; the current container was left untouched."
    }
    if (Find-Container $ContainerName) {
        Invoke-Docker @('rm', '-f', $ContainerName) | Out-Null
    }
    Invoke-Docker @('rename', $rollbackContainer, $ContainerName) | Out-Null
    Invoke-Docker @('start', $ContainerName) | Out-Null
    if (-not (Wait-Ready $AppPort)) { throw 'The previous container was restored but is not healthy.' }
}

Push-Location $root
try {
    if ($UseCompose) {
        $bootstrapArgs = @{
            Version = $Version
            RegistryImage = $RegistryImage
            InstallDir = $root
        }
        if ($PSBoundParameters.ContainsKey('AppPort')) { $bootstrapArgs.AppPort = $AppPort }
        & (Join-Path $PSScriptRoot 'bootstrap-windows.ps1') @bootstrapArgs
        if (-not $?) { throw 'Compose update failed.' }
        return
    }

    Invoke-Docker @('pull', $remoteImage) | Out-Null
    Invoke-Docker @('image', 'inspect', $remoteImage) | Out-Null
    if (-not $ReplaceStandaloneContainer) {
        Write-Output "Downloaded $remoteImage. Re-run with -ReplaceStandaloneContainer to switch containers."
        return
    }
    if (-not (Test-Path -LiteralPath $EnvFile -PathType Leaf)) { throw "Environment file not found: $EnvFile" }
    if (-not (Find-Container $ContainerName)) { throw "Container '$ContainerName' was not found; refusing to create a replacement." }
    if (Find-Container $rollbackContainer) { throw "Remove or inspect stale rollback container '$rollbackContainer' first." }

    Invoke-Docker @('stop', $ContainerName) | Out-Null
    Invoke-Docker @('rename', $ContainerName, $rollbackContainer) | Out-Null
    try {
        $run = @(
            'run', '-d', '--name', $ContainerName, '--restart', 'unless-stopped', '--init',
            '--read-only', '--tmpfs', '/tmp:size=16m,mode=1777', '--cap-drop', 'ALL',
            '--security-opt', 'no-new-privileges:true', '--pids-limit', '128', '--stop-timeout', '130',
            '--env-file', $EnvFile, '-e', "APP_VERSION=$Version", '-e', 'APP_ADDR=:8080',
            '-p', "127.0.0.1:${AppPort}:8080", $remoteImage
        )
        Invoke-Docker $run | Out-Null
        if (-not (Wait-Ready $AppPort)) { throw "Version $Version did not become ready within 45 seconds." }
    } catch {
        $updateError = $_
        Restore-Container
        throw "Update failed; the previous container is healthy again. Original error: $updateError"
    }
    Invoke-Docker @('rm', $rollbackContainer) | Out-Null
    Invoke-Docker @('ps', '--filter', "name=^/$ContainerName$")
} finally {
    Pop-Location
}
