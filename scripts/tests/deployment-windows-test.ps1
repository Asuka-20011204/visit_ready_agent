$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ("visit-ready-test-" + [Guid]::NewGuid().ToString('N'))
$sourceCommit = '0123456789abcdef0123456789abcdef01234567'
$global:MockCurrentVersion = ''
$global:MockContainers = @{}
$global:MockFailFinalPS = $false

$scriptText = Get-Content -Raw -LiteralPath (Join-Path $repoRoot 'scripts/bootstrap-windows.ps1')
if ($scriptText -match '\[IO\.FileSystemAclExtensions\]') {
    throw 'Bootstrap script directly references a type unavailable in Windows PowerShell 5.1.'
}
if ($scriptText -notmatch 'System\.IO\.File\]::SetAccessControl' -or $scriptText -notmatch 'System\.IO\.Directory\]::SetAccessControl') {
    throw 'Bootstrap script is missing the Windows PowerShell 5.1 ACL fallback.'
}

function global:docker {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:LASTEXITCODE = 0
    if ($arguments.Count -ge 2 -and $arguments[0] -eq 'image' -and $arguments[1] -eq 'inspect') {
        if (($arguments -join ' ') -match 'org.opencontainers.image.revision') { return $sourceCommit }
        return 'example@sha256:' + ('0' * 64)
    }
    if ($arguments.Count -ge 2 -and $arguments[0] -eq 'volume' -and $arguments[1] -eq 'ls') { return }
    if ($arguments.Count -ge 2 -and $arguments[0] -eq 'ps' -and $arguments[1] -eq '-a') {
        $filter = $arguments[3]
        $name = $filter -replace '^name=\^/', '' -replace '\$$', ''
        if ($global:MockContainers.ContainsKey($name)) { return $name }
        return
    }
    if ($arguments[0] -eq 'rename') {
        $global:MockContainers[$arguments[2]] = $global:MockContainers[$arguments[1]]
        $global:MockContainers.Remove($arguments[1])
        return
    }
    if ($arguments[0] -eq 'rm') {
        $name = $arguments[-1]
        $global:MockContainers.Remove($name)
        return
    }
    if ($arguments[0] -eq 'run') {
        $name = $arguments[[Array]::IndexOf($arguments, '--name') + 1]
        $versionArg = $arguments | Where-Object { $_ -like 'APP_VERSION=*' }
        $version = $versionArg.Substring('APP_VERSION='.Length)
        $global:MockContainers[$name] = $version
        $global:MockCurrentVersion = $version
        return 'mock-container-id'
    }
    if ($arguments[0] -eq 'start') {
        $global:MockCurrentVersion = $global:MockContainers[$arguments[1]]
        return
    }
    if ($arguments[0] -eq 'compose' -and $arguments -contains 'up') {
        $global:MockCurrentVersion = $env:APP_VERSION
        return
    }
    if ($arguments[0] -eq 'compose' -and $arguments -contains 'cp') {
        $destination = $arguments[-1]
        [IO.File]::WriteAllText($destination, '-- mock SQL backup ' + ('x' * 128))
        return
    }
    if ($arguments[0] -eq 'ps' -and $global:MockFailFinalPS) {
        $global:LASTEXITCODE = 1
        return
    }
}

function global:Invoke-WebRequest {
    param([string]$Uri, [switch]$UseBasicParsing, [int]$TimeoutSec, [string]$OutFile)
    if ($Uri.EndsWith('/compose.yaml')) { Copy-Item (Join-Path $repoRoot 'compose.yaml') $OutFile; return }
    if ($Uri.EndsWith('/compose.mysql.yaml')) { Copy-Item (Join-Path $repoRoot 'compose.mysql.yaml') $OutFile; return }
    throw "Unexpected URL: $Uri"
}

function global:Invoke-RestMethod {
    param([string]$Uri, [int]$TimeoutSec)
    if ($global:MockCurrentVersion -eq '1.0.2') { throw 'mock unhealthy version' }
    return @{ status = 'ready' }
}

function Get-TestEnvValue([string]$Name, [string]$Path) {
    $line = Get-Content $Path | Where-Object { $_.StartsWith("$Name=") } | Select-Object -Last 1
    return $line.Substring($Name.Length + 1)
}

function Assert-PrivateAcl([string]$Path) {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return }
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) { throw "$Path inherits broad filesystem permissions." }
    $expectedSids = @(
        'S-1-5-18',
        'S-1-5-32-544',
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    ) | Sort-Object -Unique
    $actualSids = @()
    foreach ($rule in @($acl.Access)) {
        if ($rule.IsInherited) { throw "$Path contains an inherited access rule." }
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
            throw "$Path contains a non-Allow access rule."
        }
        if (($rule.FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -ne [Security.AccessControl.FileSystemRights]::FullControl) {
            throw "$Path contains an access rule without FullControl."
        }
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($expectedSids -notcontains $sid) { throw "$Path grants access to unexpected principal $sid." }
        $actualSids += $sid
    }
    $actualSids = @($actualSids | Sort-Object -Unique)
    if ($actualSids.Count -ne $expectedSids.Count) { throw "$Path does not grant the expected private principal set." }
    foreach ($sid in $expectedSids) {
        if ($actualSids -notcontains $sid) { throw "$Path is missing required principal $sid." }
    }
}

try {
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    $installDir = Join-Path $testRoot 'install'
    & (Join-Path $repoRoot 'scripts/bootstrap-windows.ps1') `
        -Version 1.0.1 -Mode demo -InstallDir $installDir -ReadyTimeoutSeconds 1
    $envFile = Join-Path $installDir '.env'
    if ((Get-TestEnvValue APP_PORT $envFile) -ne '8097') { throw 'Default port was not persisted.' }
    if ((Get-TestEnvValue APP_VERSION $envFile) -ne '1.0.1') { throw 'Initial version was not persisted.' }
    if ((Get-Content (Join-Path $installDir '.image-digest')) -notmatch '^example@sha256:') { throw 'Image digest metadata was not persisted.' }
    if ((Get-Content (Join-Path $installDir '.source-commit')) -ne $sourceCommit) { throw 'Source commit metadata was not persisted.' }
    Assert-PrivateAcl $installDir
    Assert-PrivateAcl $envFile

    & (Join-Path $repoRoot 'scripts/bootstrap-windows.ps1') `
        -Version 1.0.3 -InstallDir $installDir -ReadyTimeoutSeconds 1
    if ((Get-TestEnvValue APP_VERSION $envFile) -ne '1.0.3') { throw 'Updated version was not persisted.' }
    if ((Get-Content (Join-Path $installDir '.rollback\version')) -ne '1.0.1') { throw 'Rollback version metadata was not preserved.' }
    if (-not (Test-Path (Join-Path $installDir '.rollback\image-digest'))) { throw 'Rollback digest metadata was not preserved.' }
    if (-not (Test-Path (Join-Path $installDir '.rollback\source-commit'))) { throw 'Rollback source metadata was not preserved.' }
    Assert-PrivateAcl (Join-Path $installDir '.rollback')

    try {
        & (Join-Path $repoRoot 'scripts/bootstrap-windows.ps1') `
            -Version 1.0.2 -InstallDir $installDir -ReadyTimeoutSeconds 1
        throw 'Unhealthy update unexpectedly succeeded.'
    } catch {
        if ($_.Exception.Message -eq 'Unhealthy update unexpectedly succeeded.') { throw }
    }
    if ((Get-TestEnvValue APP_VERSION $envFile) -ne '1.0.3') { throw 'Failed update changed the persisted version.' }
    if ($global:MockCurrentVersion -ne '1.0.3') { throw 'Failed update did not restore the previous version.' }

    $liveDir = Join-Path $testRoot 'live'
    New-Item -ItemType Directory -Path $liveDir | Out-Null
    Copy-Item (Join-Path $repoRoot 'compose.yaml') (Join-Path $liveDir 'compose.yaml')
    Copy-Item (Join-Path $repoRoot 'compose.mysql.yaml') (Join-Path $liveDir 'compose.mysql.yaml')
    [IO.File]::WriteAllLines((Join-Path $liveDir '.env'), @(
        'APP_MODE=live', 'AUTH_MODE=required', 'AUTH_COOKIE_SECURE=true', 'APP_PORT=8098',
        'APP_VERSION=1.0.1', 'MYSQL_PASSWORD=mock_password_123',
        'SESSION_ENCRYPTION_KEY=QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI'
    ))
    [IO.File]::WriteAllText((Join-Path $liveDir '.deployment-success'), '1.0.1')
    & (Join-Path $repoRoot 'scripts/bootstrap-windows.ps1') `
        -Version 1.0.3 -InstallDir $liveDir -ReadyTimeoutSeconds 1 `
        -ExpectedSourceCommit $sourceCommit -ExpectedImageDigest ('sha256:' + ('0' * 64))
    $backup = Get-ChildItem (Join-Path $liveDir 'backups') -Filter 'visitready-before-1.0.3-*.sql' | Select-Object -First 1
    if (-not $backup -or $backup.Length -lt 64 -or -not (Test-Path "$($backup.FullName).sha256")) {
        throw 'Windows live update did not create a verified database backup.'
    }
    Assert-PrivateAcl $liveDir
    Assert-PrivateAcl (Join-Path $liveDir '.env')
    Assert-PrivateAcl (Join-Path $liveDir '.rollback')
    Assert-PrivateAcl (Join-Path $liveDir 'backups')
    Assert-PrivateAcl $backup.FullName
    Assert-PrivateAcl "$($backup.FullName).sha256"

    $standaloneEnv = Join-Path $testRoot 'standalone.env'
    [IO.File]::WriteAllText($standaloneEnv, "APP_MODE=demo`n")
    $global:MockContainers = @{ 'visit-ready-agent' = '1.0.1' }
    $global:MockCurrentVersion = '1.0.1'
    $global:MockFailFinalPS = $true
    try {
        & (Join-Path $repoRoot 'scripts/update-local.ps1') `
            -Version 1.0.3 -EnvFile $standaloneEnv -ReplaceStandaloneContainer
    } catch {
        # A diagnostic docker ps failure must not trigger rollback after old-container cleanup.
    }
    if (-not $global:MockContainers.ContainsKey('visit-ready-agent')) { throw 'Healthy replacement container was removed.' }
    if ($global:MockContainers.ContainsKey('visit-ready-agent-rollback')) { throw 'Rollback container was not cleaned up.' }
    if ($global:MockCurrentVersion -ne '1.0.3') { throw 'Diagnostic failure incorrectly rolled back the healthy container.' }

    Write-Output 'PowerShell deployment behavior OK'
} finally {
    Remove-Item Function:\docker -ErrorAction SilentlyContinue
    Remove-Item Function:\Invoke-WebRequest -ErrorAction SilentlyContinue
    Remove-Item Function:\Invoke-RestMethod -ErrorAction SilentlyContinue
    if (Test-Path $testRoot) { Remove-Item $testRoot -Recurse -Force }
}
