[CmdletBinding()]
param(
    [string]$Version = "dev",
    [string]$Commit = "local"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$outputDirectory = Join-Path $root "dist"
$output = Join-Path $outputDirectory "visit-ready.exe"

New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
Push-Location $root
try {
    $env:CGO_ENABLED = "0"
    go build -trimpath -buildvcs=false "-ldflags=-s -w -X main.version=$Version -X main.commit=$Commit" -o $output ./cmd/server
    Get-FileHash -Algorithm SHA256 $output
} finally {
    Pop-Location
}
