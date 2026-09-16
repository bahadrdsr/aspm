param(
    [string] $RuntimeConfig,
    [string] $Run = '^Test(M0[3-7]_|Acceptance_)',
    [switch] $PlanOnly,
    [string] $Go = 'go',
    [string] $ModuleCache
)

$ErrorActionPreference = 'Stop'
if ($RuntimeConfig) { $RuntimeConfig = (Resolve-Path -LiteralPath $RuntimeConfig).Path }
if ($ModuleCache) { $ModuleCache = (Resolve-Path -LiteralPath $ModuleCache).Path }
$mapping = [ordered]@{
    ASPM_TEST_DATABASE_URL = 'databaseUrl'
    ASPM_TEST_S3_ENDPOINT = 's3Endpoint'
    ASPM_TEST_S3_ACCESS_KEY = 's3AccessKey'
    ASPM_TEST_S3_SECRET_KEY = 's3SecretKey'
    ASPM_TEST_S3_BUCKET = 's3Bucket'
}
$names = @('GOTOOLCHAIN', 'GOWORK', 'GOENV', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR',
    'TEMP', 'TMP', 'GOPROXY', 'GOSUMDB', 'AWS_EC2_METADATA_DISABLED') + @($mapping.Keys)
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$exitCode = 1
Push-Location $PSScriptRoot
try {
    if ($RuntimeConfig) {
        try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
        catch { throw 'BLOCKED: cannot read the authorized runtime configuration; contents withheld.' }
        foreach ($entry in $mapping.GetEnumerator()) {
            if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
                throw "BLOCKED: runtime field $($entry.Value) is missing; no values logged."
            }
            [Environment]::SetEnvironmentVariable($entry.Key, $runtime[$entry.Value], 'Process')
        }
    }
    $redactions = @($env:ASPM_TEST_DATABASE_URL, $env:ASPM_TEST_S3_ACCESS_KEY, $env:ASPM_TEST_S3_SECRET_KEY)
    if ($env:ASPM_TEST_DATABASE_URL) {
        try {
            $userInfo = ([Uri]$env:ASPM_TEST_DATABASE_URL).UserInfo
            $redactions += $userInfo
            if ($userInfo.Contains(':')) { $redactions += [Uri]::UnescapeDataString($userInfo.Split(':', 2)[1]) }
        } catch { throw 'BLOCKED: invalid fixture database URL; value withheld.' }
    }
    $redactions = @($redactions | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    foreach ($value in @($redactions)) {
        $redactions += [Uri]::EscapeDataString($value)
        $json = ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1, $json.Length - 2)
    }
    $redactions = @($redactions | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    foreach ($directory in @('.run\build', '.run\work', '.run\modules', '.run\gopath')) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }
    $env:GOTOOLCHAIN = 'local'
    $env:GOWORK = 'off'
    $env:GOENV = 'off'
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    $env:AWS_EC2_METADATA_DISABLED = 'true'
    $env:GOPATH = Join-Path $PSScriptRoot '.run\gopath'
    $env:GOMODCACHE = if ($ModuleCache) { $ModuleCache } else { Join-Path $PSScriptRoot '.run\modules' }
    $env:GOCACHE = Join-Path $PSScriptRoot '.run\build'
    $env:GOTMPDIR = Join-Path $PSScriptRoot '.run\work'
    $env:TEMP = $env:GOTMPDIR
    $env:TMP = $env:GOTMPDIR
    if ($PlanOnly) { $Run = '^TestM03_' }
    $arguments = @('test', '-json', '-mod=readonly', '-count=1', '-timeout=300s', '-run', $Run)
    if (-not $PlanOnly) { $arguments += '-tags=integration' }
    $raw = & $Go @arguments '.' 2>&1
    $exitCode = $LASTEXITCODE
    $safe = foreach ($line in $raw) {
        $text = $line.ToString()
        foreach ($value in $redactions) { $text = $text.Replace($value, '[REDACTED]') }
        $text
    }
    $safe | Set-Content -LiteralPath '.run\last-run.jsonl' -Encoding utf8
    foreach ($line in $safe) {
        if ($line.StartsWith('{')) {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) { Write-Output $event.Output.TrimEnd() }
        } else { Write-Output $line }
    }
}
finally {
    Pop-Location
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
}
exit $exitCode
