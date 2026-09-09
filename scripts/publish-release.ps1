[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^\d+\.\d+\.\d+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$')]
    [string]$Version,

    [string]$RegistryImage = 'ghcr.io/asuka-20011204/visit-ready-agent',
    [switch]$Push,
    [switch]$DeployLocal,
    [switch]$SkipChecks
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$localImage = "visit-ready-agent:$Version"
$remoteImage = "${RegistryImage}:$Version"

function Invoke-Native([string]$FilePath, [string[]]$Arguments) {
    $output = & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
    return $output
}

Push-Location $root
try {
    $commit = (Invoke-Native git @('rev-parse', 'HEAD')).Trim()
    $tagCommit = (Invoke-Native git @('rev-list', '-n', '1', "v$Version")).Trim()
    if ($tagCommit -ne $commit) { throw "Tag v$Version must exist and point to HEAD ($commit)." }

    if ($Push) {
        $changes = Invoke-Native git @('status', '--porcelain', '--untracked-files=all')
        if ($changes) { throw 'Refusing to publish from a dirty working tree. Commit all release inputs first.' }
        $remoteTagLines = @(Invoke-Native git @('ls-remote', 'origin', "refs/tags/v$Version", "refs/tags/v$Version^{}"))
        if (-not $remoteTagLines) { throw "Tag v$Version has not been pushed to origin." }
        $peeled = $remoteTagLines | Where-Object { $_ -match '\^\{\}$' } | Select-Object -Last 1
        $remoteTagLine = if ($peeled) { $peeled } else { $remoteTagLines | Select-Object -First 1 }
        $remoteTagCommit = ($remoteTagLine -split '\s+')[0]
        if ($remoteTagCommit -ne $commit) { throw "Remote tag v$Version points to $remoteTagCommit, not HEAD ($commit)." }
        & docker manifest inspect $remoteImage *> $null
        if ($LASTEXITCODE -eq 0) { throw "Registry image $remoteImage already exists; refusing to overwrite an immutable release." }
    }

    if (-not $SkipChecks) {
        Invoke-Native go @('test', './...') | Out-Null
        Invoke-Native go @('vet', './...') | Out-Null
        Invoke-Native docker @('compose', 'config', '--quiet') | Out-Null
    }

    Invoke-Native docker @(
        'build', '--pull', '--build-arg', "VERSION=$Version", '--build-arg', "COMMIT=$commit",
        '--tag', $localImage, '--tag', $remoteImage, '.'
    ) | Out-Null

    if ($Push) { Invoke-Native docker @('push', $remoteImage) | Out-Null }

    if ($DeployLocal) {
        $env:APP_VERSION = $Version
        Invoke-Native docker @('compose', '-f', 'compose.yaml', '-f', 'compose.mysql.yaml', 'up', '--no-build', '--force-recreate', '-d') | Out-Null
        Invoke-Native docker @('compose', '-f', 'compose.yaml', '-f', 'compose.mysql.yaml', 'ps')
    }

    Write-Output "Source commit: $commit"
    Write-Output "Local image: $localImage"
    Write-Output "Registry image: $remoteImage"
    if (-not $Push) { Write-Output 'The image was not pushed. Re-run with -Push after registry login.' }
} finally {
    Pop-Location
}
