param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [Parameter(Mandatory = $true)] [string] $StorageConfig
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$mapping = [ordered]@{
    ASPM_RUNTIME_ROLE_ENDPOINT = 'endpoint'
    ASPM_RUNTIME_ROLE_REGION = 'region'
    ASPM_RUNTIME_ROLE_BUCKET = 'bucket'
}
$names = @('GOWORK', 'GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'TEMP', 'TMP', 'AWS_EC2_METADATA_DISABLED', 'ASPM_RUNTIME_ROLE_DATABASE_URL') + @($mapping.Keys)
foreach ($role in @('ADMIN', 'CORE', 'INGESTION', 'AI')) {
    $names += "ASPM_RUNTIME_ROLE_${role}_ACCESS_KEY", "ASPM_RUNTIME_ROLE_${role}_SECRET_KEY"
}
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$secrets = [System.Collections.Generic.List[string]]::new()
$exitCode = 1
Push-Location $root
try {
    try {
        $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable
        $storage = Get-Content -LiteralPath $StorageConfig -Raw | ConvertFrom-Json -AsHashtable
    }
    catch { throw 'Cannot read authorized fixture metadata; contents are withheld.' }
    if (-not ($runtime.databaseUrl -is [string]) -or [string]::IsNullOrWhiteSpace($runtime.databaseUrl)) {
        throw 'Private PostgreSQL fixture databaseUrl is missing.'
    }
    $env:ASPM_RUNTIME_ROLE_DATABASE_URL = $runtime.databaseUrl
    $secrets.Add($runtime.databaseUrl)
    if ($runtime.postgresPassword -is [string]) { $secrets.Add($runtime.postgresPassword) }
    try {
        $userinfo = ([Uri]$runtime.databaseUrl).UserInfo
        $secrets.Add($userinfo)
        $parts = $userinfo.Split(':', 2)
        if ($parts.Count -eq 2) { $secrets.Add([Uri]::UnescapeDataString($parts[1])) }
    }
    catch { throw 'PostgreSQL URL could not be validated safely.' }
    foreach ($entry in $mapping.GetEnumerator()) {
        if (-not ($storage[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($storage[$entry.Value])) {
            throw "Required storage field $($entry.Value) is missing."
        }
        [Environment]::SetEnvironmentVariable($entry.Key, $storage[$entry.Value], 'Process')
    }
    foreach ($role in @('admin', 'core', 'ingestion', 'ai')) {
        foreach ($field in @('accessKey', 'secretKey')) {
            $value = $storage.roles[$role][$field]
            if (-not ($value -is [string]) -or [string]::IsNullOrWhiteSpace($value)) {
                throw "Required role field $role.$field is missing; values withheld."
            }
            $suffix = if ($field -eq 'accessKey') { 'ACCESS_KEY' } else { 'SECRET_KEY' }
            [Environment]::SetEnvironmentVariable("ASPM_RUNTIME_ROLE_$($role.ToUpperInvariant())_$suffix", $value, 'Process')
            $secrets.Add($value)
        }
    }
    $redactions = @($secrets | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    foreach ($secret in @($redactions)) {
        $redactions += [Uri]::EscapeDataString($secret)
        $encoded = ConvertTo-Json -InputObject $secret -Compress
        $redactions += $encoded.Substring(1, $encoded.Length - 2)
    }
    $redactions = @($redactions | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    foreach ($directory in @('.cache\gopath', '.cache\modules', '.cache\build', '.cache\work', 'tests\runtime_roles\.run')) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }
    $env:GOWORK = 'off'; $env:GOTOOLCHAIN = 'auto'; $env:AWS_EC2_METADATA_DISABLED = 'true'
    $env:GOPATH = Join-Path $root '.cache\gopath'; $env:GOMODCACHE = Join-Path $root '.cache\modules'
    $env:GOCACHE = Join-Path $root '.cache\build'; $env:GOTMPDIR = Join-Path $root '.cache\work'
    $env:TEMP = $env:GOTMPDIR; $env:TMP = $env:GOTMPDIR
    $raw = @(go test -json -tags=integration -mod=readonly -count=1 -timeout=150s -run '^TestRuntimeRoles' .\tests\runtime_roles 2>&1)
    $exitCode = $LASTEXITCODE
    $safe = @($raw | ForEach-Object {
        $line = $_.ToString()
        foreach ($secret in $redactions) { $line = $line.Replace($secret, '[REDACTED]') }
        $line
    })
    $safe | Set-Content -LiteralPath 'tests\runtime_roles\.run\last-run.jsonl' -Encoding utf8
    $readable = foreach ($line in $safe) {
        if ($line.StartsWith('{')) {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) { $event.Output.TrimEnd() }
        } else { $line }
    }
    $readable | Set-Content -LiteralPath 'tests\runtime_roles\.run\last-run.log' -Encoding utf8
    $readable | Write-Output
}
finally {
    Pop-Location
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
}
exit $exitCode
