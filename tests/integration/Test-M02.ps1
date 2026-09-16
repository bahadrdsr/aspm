param(
    [string] $RuntimeConfig,
    [string] $Run = 'TestM02'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$mapping = [ordered]@{
    ASPM_TEST_DATABASE_URL = 'databaseUrl'
    ASPM_TEST_S3_ENDPOINT = 's3Endpoint'
    ASPM_TEST_S3_ACCESS_KEY = 's3AccessKey'
    ASPM_TEST_S3_SECRET_KEY = 's3SecretKey'
    ASPM_TEST_S3_BUCKET = 's3Bucket'
}
$names = @('GOTOOLCHAIN', 'GOWORK', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'TEMP', 'TMP', 'AWS_EC2_METADATA_DISABLED') + @($mapping.Keys)
$previous = @{}
foreach ($name in $names) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$exitCode = 1
$secrets = [System.Collections.Generic.List[string]]::new()
Push-Location $root
try {
    if ($RuntimeConfig) {
        try {
            $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable
        }
        catch {
            throw 'Cannot read authorized runtime metadata; contents are not logged.'
        }
        foreach ($entry in $mapping.GetEnumerator()) {
            if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
                throw "Runtime metadata is missing the required $($entry.Value) field; no values are logged."
            }
            [Environment]::SetEnvironmentVariable($entry.Key, $runtime[$entry.Value], 'Process')
        }
        if ($runtime.postgresPassword -is [string]) {
            $secrets.Add($runtime.postgresPassword)
        }
    }
    foreach ($name in $mapping.Keys) {
        if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name, 'Process'))) {
            throw "Required process-local fixture variable $name is missing."
        }
    }
    foreach ($name in @('ASPM_TEST_DATABASE_URL', 'ASPM_TEST_S3_ACCESS_KEY', 'ASPM_TEST_S3_SECRET_KEY')) {
        $secrets.Add([Environment]::GetEnvironmentVariable($name, 'Process'))
    }
    try {
        $database = [Uri]$env:ASPM_TEST_DATABASE_URL
        $userInfo = $database.UserInfo
        if ($userInfo) {
            $secrets.Add($userInfo)
            $parts = $userInfo.Split(':', 2)
            if ($parts.Count -eq 2) {
                $secrets.Add([Uri]::UnescapeDataString($parts[1]))
            }
        }
    }
    catch {
        throw 'The database fixture requires a valid PostgreSQL URL; its value is withheld.'
    }
    $redactions = @($secrets | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    foreach ($secret in @($redactions)) {
        $redactions += [Uri]::EscapeDataString($secret)
        $encoded = ConvertTo-Json -InputObject $secret -Compress
        $redactions += $encoded.Substring(1, $encoded.Length - 2)
    }
    $redactions = @($redactions | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    foreach ($directory in @('.cache\gopath', '.cache\modules', '.cache\build', '.cache\work', '.cache\m02')) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }
    $env:GOTOOLCHAIN = 'auto'
    $env:GOWORK = 'off'
    $env:AWS_EC2_METADATA_DISABLED = 'true'
    $env:GOPATH = Join-Path $root '.cache\gopath'
    $env:GOMODCACHE = Join-Path $root '.cache\modules'
    $env:GOCACHE = Join-Path $root '.cache\build'
    $env:GOTMPDIR = Join-Path $root '.cache\work'
    $env:TEMP = $env:GOTMPDIR
    $env:TMP = $env:GOTMPDIR
    $raw = go test -json -tags=integration -mod=readonly -count=1 -timeout=120s -run $Run .\tests\integration 2>&1
    $exitCode = $LASTEXITCODE
    $safeLines = @($raw | ForEach-Object {
        $line = $_.ToString()
        foreach ($secret in $redactions) {
            $line = $line.Replace($secret, '[REDACTED]')
        }
        $line
    })
    $safeLines | Set-Content -LiteralPath '.cache\m02\last-run.jsonl' -Encoding utf8
    $readable = @()
    foreach ($line in $safeLines) {
        if ($line.StartsWith('{')) {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) {
                $readable += $event.Output.TrimEnd()
            }
        }
        else {
            $readable += $line
        }
    }
    $readable | Set-Content -LiteralPath '.cache\m02\last-run.log' -Encoding utf8
    $readable | Write-Output
}
finally {
    Pop-Location
    foreach ($name in $names) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}
exit $exitCode
