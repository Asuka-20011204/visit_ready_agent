[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^v?\d+\.\d+\.\d+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$')]
    [string]$Version,

    [ValidateSet('live', 'demo')]
    [string]$Mode,

    [string]$RegistryImage = 'docker.io/asuka20011204/visit-ready-agent',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'VisitReady'),
    [ValidateRange(1, 65535)]
    [int]$AppPort = 8097,
    [string]$RepositoryRawUrl = 'https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent',
    [string]$ExpectedImageDigest,
    [ValidatePattern('^[0-9a-f]{40}$')]
    [string]$ExpectedSourceCommit,
    [bool]$AuthCookieSecure = $false,
    [ValidateRange(1, 999)]
    [int]$ReadyTimeoutSeconds = 45
)

$ErrorActionPreference = 'Stop'
$Version = $Version.TrimStart('v')
$envFile = Join-Path $InstallDir '.env'
$markerFile = Join-Path $InstallDir '.deployment-success'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Invoke-Docker([string[]]$Arguments) {
    $output = & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
    return $output
}

function Get-EnvValue([string]$Name, [string]$Path) {
    $line = Get-Content -LiteralPath $Path | Where-Object { $_.StartsWith("$Name=") } | Select-Object -Last 1
    if ($null -eq $line) { return '' }
    return $line.Substring($Name.Length + 1)
}

function Set-EnvValue([string]$Name, [string]$Value, [string]$Path) {
    $lines = [Collections.Generic.List[string]]::new()
    $found = $false
    foreach ($line in [IO.File]::ReadAllLines($Path)) {
        if ($line.StartsWith("$Name=")) {
            if (-not $found) { $lines.Add("$Name=$Value") }
            $found = $true
        } else {
            $lines.Add($line)
        }
    }
    if (-not $found) { $lines.Add("$Name=$Value") }
    $temp = "$Path.tmp"
    [IO.File]::WriteAllLines($temp, $lines, $utf8NoBom)
    Move-Item -LiteralPath $temp -Destination $Path -Force
    Protect-SensitiveFile $Path
}

function Protect-SensitiveFile([string]$Path) {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return }
    $acl = [Security.AccessControl.FileSecurity]::new($Path, [Security.AccessControl.AccessControlSections]::Access)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        if ($null -ne $rule.IdentityReference) { $acl.PurgeAccessRules($rule.IdentityReference) }
    }
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            (New-Object Security.Principal.SecurityIdentifier($sid)),
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule)
    }
    Set-PrivateAccessControl $Path $acl $false
}

function Protect-PrivateDirectory([string]$Path) {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return }
    $acl = [Security.AccessControl.DirectorySecurity]::new($Path, [Security.AccessControl.AccessControlSections]::Access)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        if ($null -ne $rule.IdentityReference) { $acl.PurgeAccessRules($rule.IdentityReference) }
    }
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            (New-Object Security.Principal.SecurityIdentifier($sid)),
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule)
    }
    Set-PrivateAccessControl $Path $acl $true
}

function Set-PrivateAccessControl(
    [string]$Path,
    [Security.AccessControl.FileSystemSecurity]$Acl,
    [bool]$IsDirectory
) {
    $extensions = ([System.Management.Automation.PSTypeName]'System.IO.FileSystemAclExtensions').Type
    if ($null -ne $extensions) {
        $targetType = if ($IsDirectory) { [System.IO.DirectoryInfo] } else { [System.IO.FileInfo] }
        $aclType = if ($IsDirectory) { [Security.AccessControl.DirectorySecurity] } else { [Security.AccessControl.FileSecurity] }
        $method = $extensions.GetMethod('SetAccessControl', [type[]]@($targetType, $aclType))
        if ($null -ne $method) {
            $target = if ($IsDirectory) { [System.IO.DirectoryInfo]::new($Path) } else { [System.IO.FileInfo]::new($Path) }
            $method.Invoke($null, [object[]]@($target, $Acl)) | Out-Null
            return
        }
    }
    if ($IsDirectory) {
        [System.IO.Directory]::SetAccessControl($Path, [Security.AccessControl.DirectorySecurity]$Acl)
    } else {
        [System.IO.File]::SetAccessControl($Path, [Security.AccessControl.FileSecurity]$Acl)
    }
}

function New-Token([int]$ByteCount) {
    $bytes = New-Object byte[] $ByteCount
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Read-Secret([string]$Prompt) {
    $secure = Read-Host $Prompt -AsSecureString
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
}

function Assert-SafeEnvValue([string]$Name, [string]$Value) {
    if ($Value.Contains("`r") -or $Value.Contains("`n") -or $Value.Contains('$')) {
        throw "$Name contains a character that Docker Compose cannot safely load."
    }
}

function Wait-Ready([int]$Port) {
    $deadline = (Get-Date).AddSeconds($ReadyTimeoutSeconds)
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

function Write-InitialEnv([string]$SelectedMode) {
    $llmEndpoint = ''
    $llmApiKey = ''
    $llmModel = ''
    $bochaApiKey = ''
    if ($SelectedMode -eq 'live') {
        $llmEndpoint = Read-Host 'LLM_ENDPOINT (HTTPS Chat Completions URL)'
        $llmApiKey = Read-Secret 'LLM_API_KEY'
        $llmModel = Read-Host 'LLM_MODEL'
        $bochaApiKey = Read-Secret 'BOCHA_API_KEY (optional; press Enter to skip)'
        if (-not $llmEndpoint.StartsWith('https://') -or -not $llmApiKey -or -not $llmModel) {
            throw 'Live mode requires an HTTPS LLM_ENDPOINT, LLM_API_KEY and LLM_MODEL.'
        }
    }
    foreach ($entry in @{
        LLM_ENDPOINT = $llmEndpoint; LLM_API_KEY = $llmApiKey; LLM_MODEL = $llmModel; BOCHA_API_KEY = $bochaApiKey
    }.GetEnumerator()) { Assert-SafeEnvValue $entry.Key $entry.Value }
    $mysqlPassword = New-Token 24
    $encryptionKey = New-Token 32
    $authMode = if ($SelectedMode -eq 'live') { 'required' } else { 'disabled' }
    $cookieSecure = if ($SelectedMode -eq 'live') { $AuthCookieSecure.ToString().ToLowerInvariant() } else { 'false' }
    $sessionStore = if ($SelectedMode -eq 'live') { 'mysql' } else { 'memory' }
    $lines = @(
        "APP_MODE=$SelectedMode", "AUTH_MODE=$authMode", "AUTH_COOKIE_SECURE=$cookieSecure",
        "APP_PORT=$AppPort", "APP_VERSION=$Version", 'APP_COMMIT=bootstrap',
        "LLM_ENDPOINT=$llmEndpoint", "LLM_API_KEY=$llmApiKey", "LLM_MODEL=$llmModel",
        'BOCHA_ENDPOINT=https://api.bochaai.com/v1/web-search', "BOCHA_API_KEY=$bochaApiKey",
        "SESSION_STORE=$sessionStore", 'MYSQL_DSN=', "MYSQL_PASSWORD=$mysqlPassword",
        "SESSION_ENCRYPTION_KEY=$encryptionKey", 'SESSION_TTL=24h',
        'SESSION_CLEANUP_INTERVAL=1m', 'UPSTREAM_TIMEOUT=25s', 'WORKFLOW_TIMEOUT=45s'
    )
    [IO.File]::WriteAllLines($envFile, $lines, $utf8NoBom)
    Protect-SensitiveFile $envFile
}

if (-not $RepositoryRawUrl.StartsWith('https://')) { throw 'RepositoryRawUrl must use HTTPS.' }
if ($ExpectedImageDigest -and $ExpectedImageDigest -notmatch '^sha256:[0-9a-f]{64}$') {
    throw 'ExpectedImageDigest must be a sha256 digest.'
}
Get-Command docker -ErrorAction Stop | Out-Null
Invoke-Docker @('compose', 'version') | Out-Null
Invoke-Docker @('info') | Out-Null

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Protect-PrivateDirectory $InstallDir
$lockPath = Join-Path $InstallDir '.deploy.lock'
$lock = [IO.File]::Open($lockPath, 'OpenOrCreate', 'ReadWrite', 'None')
try {
    if (Test-Path -LiteralPath $envFile) {
        $envItem = Get-Item -LiteralPath $envFile -Force
        if ($envItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw '.env must not be a symbolic link or reparse point.'
        }
        Protect-SensitiveFile $envFile
    }
    if ((Test-Path -LiteralPath $markerFile) -and -not (Test-Path -LiteralPath $envFile -PathType Leaf)) {
        throw 'Existing deployment data was found but .env is missing. Restore the old SESSION_ENCRYPTION_KEY.'
    }
    $projectName = (Split-Path -Leaf $InstallDir).ToLowerInvariant() -replace '[^a-z0-9_-]', ''
    $existingVolumes = Invoke-Docker @('volume', 'ls', '--quiet', '--filter', "label=com.docker.compose.project=$projectName")
    if ($existingVolumes -and -not (Test-Path -LiteralPath $envFile -PathType Leaf)) {
        throw 'A Compose data volume exists but .env is missing. Restore the old SESSION_ENCRYPTION_KEY.'
    }
    if (-not (Test-Path -LiteralPath $envFile -PathType Leaf)) {
        if (-not $Mode) { $Mode = 'live' }
        Write-InitialEnv $Mode
    } else {
        $storedMode = Get-EnvValue 'APP_MODE' $envFile
        $storedPort = [int](Get-EnvValue 'APP_PORT' $envFile)
        if ($Mode -and $Mode -ne $storedMode) { throw 'Use another InstallDir to change an existing deployment between demo and live.' }
        if ($PSBoundParameters.ContainsKey('AppPort') -and $AppPort -ne $storedPort) { throw 'Change APP_PORT in the existing .env before updating.' }
        $Mode = $storedMode
        $AppPort = $storedPort
        $storedCookieSecure = Get-EnvValue 'AUTH_COOKIE_SECURE' $envFile
        $AuthCookieSecure = if ($storedCookieSecure) { [bool]::Parse($storedCookieSecure) } else { $Mode -eq 'live' }
    }
    if ($Mode -eq 'live' -and $AuthCookieSecure -and (-not $ExpectedSourceCommit -or -not $ExpectedImageDigest)) {
        throw 'Public live deployment requires ExpectedSourceCommit and ExpectedImageDigest from the release summary.'
    }

    $tempDir = Join-Path ([IO.Path]::GetTempPath()) ("visit-ready-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    try {
        $remoteImage = if ($ExpectedImageDigest) { "$RegistryImage@$ExpectedImageDigest" } else { "${RegistryImage}:$Version" }
        Invoke-Docker @('pull', $remoteImage) | Out-Null
        $imageCommit = Invoke-Docker @('image', 'inspect', '--format', '{{ index .Config.Labels "org.opencontainers.image.revision" }}', $remoteImage)
        $imageDigest = Invoke-Docker @('image', 'inspect', '--format', '{{ index .RepoDigests 0 }}', $remoteImage)
        if ($imageCommit -notmatch '^[0-9a-f]{40}$') { throw 'Image does not contain a valid source revision label.' }
        if ($imageDigest -notmatch '@sha256:[0-9a-f]{64}$') { throw 'Docker did not report a valid repository digest for the image.' }
        if ($ExpectedSourceCommit -and $imageCommit -ne $ExpectedSourceCommit) { throw "Image revision $imageCommit does not match the requested source commit." }
        $ref = $imageCommit
        Invoke-WebRequest "$RepositoryRawUrl/$ref/compose.yaml" -UseBasicParsing -TimeoutSec 60 -OutFile (Join-Path $tempDir 'compose.yaml')
        Invoke-WebRequest "$RepositoryRawUrl/$ref/compose.mysql.yaml" -UseBasicParsing -TimeoutSec 60 -OutFile (Join-Path $tempDir 'compose.mysql.yaml')
        Invoke-Docker @('tag', $remoteImage, "visit-ready-agent:$Version") | Out-Null

        $previousVersion = Get-EnvValue 'APP_VERSION' $envFile
        $rollbackDir = Join-Path $InstallDir '.rollback'
        New-Item -ItemType Directory -Force -Path $rollbackDir | Out-Null
        Protect-PrivateDirectory $rollbackDir
        $hasPrevious = $previousVersion -and (Test-Path -LiteralPath (Join-Path $InstallDir 'compose.yaml'))
        foreach ($name in @('compose.yaml', 'compose.mysql.yaml')) {
            $existing = Join-Path $InstallDir $name
            if (Test-Path -LiteralPath $existing) { Copy-Item $existing (Join-Path $rollbackDir $name) -Force }
        }
        [IO.File]::WriteAllText((Join-Path $rollbackDir 'version'), $previousVersion, $utf8NoBom)
        foreach ($name in @('image-digest', 'source-commit')) {
            $existing = Join-Path $InstallDir ".$name"
            if (Test-Path -LiteralPath $existing) { Copy-Item $existing (Join-Path $rollbackDir $name) -Force }
        }

        $oldCompose = @('compose', '-f', (Join-Path $InstallDir 'compose.yaml'))
        if ($Mode -eq 'live') { $oldCompose += @('-f', (Join-Path $InstallDir 'compose.mysql.yaml')) }
        if ($Mode -eq 'live' -and $hasPrevious) {
            Invoke-Docker ($oldCompose + @('up', '-d', 'db')) | Out-Null
            $backupDir = Join-Path $InstallDir 'backups'
            New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
            Protect-PrivateDirectory $backupDir
            $backupName = "visitready-before-$Version-$((Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')).sql"
            $backupFile = Join-Path $backupDir $backupName
            $dumpCommand = 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --single-transaction --skip-lock-tables -uvisitready visitready > /tmp/visitready-pre-update.sql'
            Invoke-Docker ($oldCompose + @('exec', '-T', 'db', 'sh', '-c', $dumpCommand)) | Out-Null
            Invoke-Docker ($oldCompose + @('cp', 'db:/tmp/visitready-pre-update.sql', $backupFile)) | Out-Null
            Invoke-Docker ($oldCompose + @('exec', '-T', 'db', 'rm', '-f', '/tmp/visitready-pre-update.sql')) | Out-Null
            if (-not (Test-Path -LiteralPath $backupFile) -or (Get-Item $backupFile).Length -lt 64) {
                throw 'Database backup is empty or incomplete; deployment was not changed.'
            }
            (Get-FileHash -Algorithm SHA256 $backupFile).Hash.ToLowerInvariant() | Set-Content "$backupFile.sha256" -Encoding ascii
            Protect-SensitiveFile $backupFile
            Protect-SensitiveFile "$backupFile.sha256"
        }

        foreach ($name in @('compose.yaml', 'compose.mysql.yaml')) {
            Copy-Item (Join-Path $tempDir $name) (Join-Path $InstallDir $name) -Force
        }

        $compose = @('compose', '-f', (Join-Path $InstallDir 'compose.yaml'))
        if ($Mode -eq 'live') { $compose += @('-f', (Join-Path $InstallDir 'compose.mysql.yaml')) }
        $env:APP_VERSION = $Version
        try {
            Invoke-Docker ($compose + @('config', '--quiet')) | Out-Null
            Invoke-Docker ($compose + @('up', '--no-build', '--force-recreate', '-d')) | Out-Null
            if (-not (Wait-Ready $AppPort)) { throw "Version $Version did not become ready within $ReadyTimeoutSeconds seconds." }
        } catch {
            $deploymentError = $_
            if ($previousVersion -and (Test-Path (Join-Path $rollbackDir 'compose.yaml'))) {
                foreach ($name in @('compose.yaml', 'compose.mysql.yaml')) {
                    $saved = Join-Path $rollbackDir $name
                    if (Test-Path $saved) { Copy-Item $saved (Join-Path $InstallDir $name) -Force }
                }
                $env:APP_VERSION = $previousVersion
                $previousImage = "visit-ready-agent:$previousVersion"
                $digestFile = Join-Path $rollbackDir 'image-digest'
                $sourceFile = Join-Path $rollbackDir 'source-commit'
                $savedDigest = if (Test-Path -LiteralPath $digestFile) { (Get-Content -Raw -LiteralPath $digestFile).Trim() } else { '' }
                $savedCommit = if (Test-Path -LiteralPath $sourceFile) { (Get-Content -Raw -LiteralPath $sourceFile).Trim() } else { '' }
                $mustRestoreImage = $false
                try {
                    $localCommit = Invoke-Docker @('image', 'inspect', '--format', '{{ index .Config.Labels "org.opencontainers.image.revision" }}', $previousImage)
                    $localDigests = @(Invoke-Docker @('image', 'inspect', '--format', '{{ range .RepoDigests }}{{ println . }}{{ end }}', $previousImage))
                    if (($savedCommit -and $localCommit -ne $savedCommit) -or ($savedDigest -and $localDigests -notcontains $savedDigest)) {
                        $mustRestoreImage = $true
                    }
                } catch { $mustRestoreImage = $true }
                if ($mustRestoreImage) {
                    $rollbackRemoteImage = if ($savedDigest) { $savedDigest } else { "$RegistryImage`:$previousVersion" }
                    Invoke-Docker @('pull', $rollbackRemoteImage) | Out-Null
                    if ($savedCommit) {
                        $actualRollbackCommit = Invoke-Docker @('image', 'inspect', '--format', '{{ index .Config.Labels "org.opencontainers.image.revision" }}', $rollbackRemoteImage)
                        if ($actualRollbackCommit -ne $savedCommit) { throw 'Rollback image source revision does not match saved metadata.' }
                    }
                    Invoke-Docker @('tag', $rollbackRemoteImage, $previousImage) | Out-Null
                }
                Invoke-Docker ($compose + @('up', '--no-build', '--force-recreate', '-d')) | Out-Null
                if (-not (Wait-Ready $AppPort)) { throw "Deployment failed and application rollback to $previousVersion is not healthy. Original error: $deploymentError" }
                Write-Warning "Deployment failed; application rollback to $previousVersion is healthy."
            }
            throw $deploymentError
        }
        Set-EnvValue 'APP_VERSION' $Version $envFile
        [IO.File]::WriteAllText($markerFile, $Version, $utf8NoBom)
        [IO.File]::WriteAllText((Join-Path $InstallDir '.image-digest'), $imageDigest, $utf8NoBom)
        [IO.File]::WriteAllText((Join-Path $InstallDir '.source-commit'), $imageCommit, $utf8NoBom)
        Invoke-Docker ($compose + @('ps'))
        Write-Output "Visit Ready $Version ($Mode) is ready at http://127.0.0.1:$AppPort"
    } finally {
        if (Test-Path -LiteralPath $tempDir) { Remove-Item -LiteralPath $tempDir -Recurse -Force }
    }
} finally {
    $lock.Dispose()
}
