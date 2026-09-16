param(
    [Parameter(Mandatory = $true)]
    [string] $RuntimeConfig
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$mapping = [ordered]@{
    ASPM_STORAGE_TEST_ENDPOINT = 'endpoint'
    ASPM_STORAGE_TEST_REGION = 'region'
    ASPM_STORAGE_TEST_BUCKET = 'bucket'
}
$names = @('GOWORK', 'GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'TEMP', 'TMP', 'AWS_EC2_METADATA_DISABLED') + @($mapping.Keys)
foreach ($role in @('ADMIN', 'AI', 'INGESTION', 'CORE')) {
    $names += "ASPM_STORAGE_TEST_${role}_ACCESS_KEY", "ASPM_STORAGE_TEST_${role}_SECRET_KEY"
}
$previous = @{}
foreach ($name in $names) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$exitCode = 1
$secrets = [System.Collections.Generic.List[string]]::new()
Push-Location $root
try {
    try {
        $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable
    }
    catch {
        throw 'Cannot read the authorized storage fixture configuration; contents are withheld.'
    }
    foreach ($entry in $mapping.GetEnumerator()) {
        if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
            throw "Required storage fixture field $($entry.Value) is missing."
        }
        [Environment]::SetEnvironmentVariable($entry.Key, $runtime[$entry.Value], 'Process')
    }
    foreach ($role in @('admin', 'ai', 'ingestion', 'core')) {
        foreach ($field in @('accessKey', 'secretKey')) {
            $value = $runtime.roles[$role][$field]
            if (-not ($value -is [string]) -or [string]::IsNullOrWhiteSpace($value)) {
                throw "Required fixture role field $role.$field is missing; values withheld."
            }
            $suffix = if ($field -eq 'accessKey') { 'ACCESS_KEY' } else { 'SECRET_KEY' }
            [Environment]::SetEnvironmentVariable("ASPM_STORAGE_TEST_$($role.ToUpperInvariant())_$suffix", $value, 'Process')
            $secrets.Add($value)
            $secrets.Add([Uri]::EscapeDataString($value))
            $encoded = ConvertTo-Json -InputObject $value -Compress
            $secrets.Add($encoded.Substring(1, $encoded.Length - 2))
        }
    }
    $redactions = @($secrets | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    $env:GOWORK = 'off'
    $env:GOTOOLCHAIN = 'auto'
    $env:AWS_EC2_METADATA_DISABLED = 'true'
    foreach ($directory in @('.cache\gopath', '.cache\modules', '.cache\build', '.cache\work', 'tests\storage_security\.run')) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }
    $env:GOPATH = Join-Path $root '.cache\gopath'
    $env:GOMODCACHE = Join-Path $root '.cache\modules'
    $env:GOCACHE = Join-Path $root '.cache\build'
    $env:GOTMPDIR = Join-Path $root '.cache\work'
    $env:TEMP = $env:GOTMPDIR
    $env:TMP = $env:GOTMPDIR
    $raw = @(go test -json -tags=integration -mod=readonly -count=1 -timeout=90s -run '^TestStorageRoleCredentials$' .\tests\storage_security 2>&1)
    $exitCode = $LASTEXITCODE
    $safe = @($raw | ForEach-Object {
        $line = $_.ToString()
        foreach ($secret in $redactions) { $line = $line.Replace($secret, '[REDACTED]') }
        $line
    })
    $safe | Set-Content -LiteralPath 'tests\storage_security\.run\last-run.jsonl' -Encoding utf8
    $readable = foreach ($line in $safe) {
        if ($line.StartsWith('{')) {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) { $event.Output.TrimEnd() }
        }
        else { $line }
    }
    $readable | Set-Content -LiteralPath 'tests\storage_security\.run\last-run.log' -Encoding utf8
    $readable | Write-Output
}
finally {
    Pop-Location
    foreach ($name in $names) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}
exit $exitCode
