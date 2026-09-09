[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$')]
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

Push-Location $root
try {
    if (-not $SkipChecks) {
        go test ./...
        go vet ./...
        docker compose config --quiet
    }

    $commit = (git rev-parse --short HEAD).Trim()
    docker build --pull `
        --build-arg "VERSION=$Version" `
        --build-arg "COMMIT=$commit" `
        --tag $localImage `
        --tag $remoteImage `
        .

    if ($Push) {
        docker push $remoteImage
    }

    if ($DeployLocal) {
        $env:APP_VERSION = $Version
        docker compose -f compose.yaml -f compose.mysql.yaml up --no-build --force-recreate -d
        docker compose -f compose.yaml -f compose.mysql.yaml ps
    }

    Write-Output "Local image: $localImage"
    Write-Output "Registry image: $remoteImage"
    if (-not $Push) {
        Write-Output 'The image was not pushed. Re-run with -Push after docker login ghcr.io.'
    }
} finally {
    Pop-Location
}
