param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Contract', 'Preflight', 'HarnessCompile', 'ExistingAdapters')] [string] $Mode = 'Contract',
    [string] $OutputName = 'contract-red-01',
    [string] $Go,
    [string] $ModuleCache
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'OutputName must be a plain unique artifact label.' }
$artifacts = Join-Path $root '.artifacts\remediation-v1'
$cache = Join-Path $root '.cache\remediation-v1'
if (-not $Go) { $Go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe' }
if (-not $ModuleCache) { $ModuleCache = Join-Path $root '.cache\modules' }
if (-not (Test-Path -LiteralPath $Go -PathType Leaf)) { throw 'BLOCKED: selected cached Go toolchain is missing; no toolchain download is authorized.' }
if (-not (Test-Path -LiteralPath $ModuleCache -PathType Container)) { throw 'BLOCKED: existing offline module cache is missing.' }
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'BLOCKED: cannot read authorized private runtime metadata; contents withheld.' }
$mapping = [ordered]@{
    ASPM_REMEDIATION_DATABASE_URL = 'databaseUrl'
    ASPM_REMEDIATION_S3_ENDPOINT = 's3Endpoint'
    ASPM_REMEDIATION_S3_ACCESS_KEY = 's3AccessKey'
    ASPM_REMEDIATION_S3_SECRET_KEY = 's3SecretKey'
    ASPM_REMEDIATION_S3_BUCKET = 's3Bucket'
}
foreach ($entry in $mapping.GetEnumerator()) {
    if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
        throw "BLOCKED: required runtime field $($entry.Value) is missing; no values logged."
    }
}
try {
    $db = [Uri]$runtime.databaseUrl
    $store = [Uri]$runtime.s3Endpoint
    if ($db.Scheme -notin @('postgres', 'postgresql') -or $db.Host -ne '127.0.0.1' -or $db.Port -lt 1 -or
        -not $db.UserInfo.Contains(':') -or $store.Host -ne '127.0.0.1' -or $store.Port -lt 1 -or
        $store.Scheme -notin @('http', 'https') -or $store.UserInfo -ne '' -or $store.Query -ne '' -or $store.Fragment -ne '') {
        throw 'Invalid fixture boundary'
    }
}
catch { throw 'BLOCKED: only explicit authenticated loopback PG and the selected local object store are permitted.' }
$secrets = @($runtime.databaseUrl, $runtime.s3AccessKey, $runtime.s3SecretKey, $db.UserInfo,
    [Uri]::UnescapeDataString($db.UserInfo.Split(':', 2)[1]))
if ($runtime.postgresPassword -is [string]) { $secrets += $runtime.postgresPassword }
foreach ($secret in @($secrets)) {
    if ($secret) {
        $secrets += [Uri]::EscapeDataString($secret)
        $json = ConvertTo-Json -InputObject $secret -Compress
        $secrets += $json.Substring(1, $json.Length - 2)
    }
}
$secrets = @($secrets | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
foreach ($directory in @($artifacts, (Join-Path $cache 'work'), (Join-Path $cache 'gopath'))) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}
$log = Join-Path $artifacts "$OutputName.log"
$events = Join-Path $artifacts "$OutputName.jsonl"
$receipt = Join-Path $artifacts "$OutputName.receipt.json"
if ((Test-Path $log) -or (Test-Path $events) -or (Test-Path $receipt)) { throw 'Refusing to overwrite prior run evidence; choose a fresh OutputName.' }
$arguments = @('test', '-mod=readonly', '-tags=integration')
$helperFiles = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File |
    Where-Object { $_.Name -ne 'production_bindings_test.go' } | Sort-Object Name | ForEach-Object { $_.FullName })
switch ($Mode) {
    'HarnessCompile' {
        $arguments += @('-c', '-o', (Join-Path $artifacts "$OutputName.test.exe")) + $helperFiles
    }
    'Preflight' {
        $arguments += @('-json', '-count=1', '-timeout=90s', '-run=^TestRemediationOwnedRuntimePreflight$') + $helperFiles
    }
    'ExistingAdapters' {
        $arguments = @('test', '-json', '-mod=readonly', '-count=1', '-timeout=60s', '.\tests\connectors', '.\internal\connectors')
    }
    default {
        $arguments += @('-json', '-count=1', '-timeout=240s', '.\tests\remediation')
    }
}
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $Go
$start.WorkingDirectory = $root
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|SLACK_)' -or $name -match '^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') {
        $start.Environment.Remove($name) | Out-Null
    }
}
$start.Environment['GOTOOLCHAIN'] = 'local'
$start.Environment['GOWORK'] = 'off'
$start.Environment['GOENV'] = 'off'
$start.Environment['GOPROXY'] = 'off'
$start.Environment['GOSUMDB'] = 'off'
$start.Environment['GOPATH'] = Join-Path $cache 'gopath'
$start.Environment['GOMODCACHE'] = $ModuleCache
$start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
$start.Environment['GOTMPDIR'] = Join-Path $cache 'work'
$start.Environment['TEMP'] = Join-Path $cache 'work'
$start.Environment['TMP'] = Join-Path $cache 'work'
$start.Environment['TMPDIR'] = Join-Path $cache 'work'
$start.Environment['AWS_EC2_METADATA_DISABLED'] = 'true'
$start.Environment['ASPM_REMEDIATION_ARTIFACT_DIR'] = $artifacts
foreach ($entry in $mapping.GetEnumerator()) { $start.Environment[$entry.Key] = $runtime[$entry.Value] }
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$startedAt = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'BLOCKED: Go process could not start.' }
$stdout = $process.StandardOutput.ReadToEndAsync()
$stderr = $process.StandardError.ReadToEndAsync()
$process.WaitForExit()
$exitCode = $process.ExitCode
$raw = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
foreach ($secret in $secrets) { $raw = $raw.Replace($secret, '[REDACTED]') }
$raw | Set-Content -LiteralPath $events -Encoding utf8NoBOM
$readable = foreach ($line in ($raw -split "`r?`n")) {
    if ($line.StartsWith('{')) {
        try {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) { $event.Output.TrimEnd() }
        }
        catch { '[unparseable redacted Go event]' }
    } elseif ($line) { $line }
}
$readable | Set-Content -LiteralPath $log -Encoding utf8NoBOM
[ordered]@{
    schemaVersion = 1
    mode = $Mode
    startedAt = $startedAt.ToString('o')
    finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    exitCode = $exitCode
    goExecutable = $Go
    arguments = $arguments
    runtimeBoundary = 'explicit loopback PG and existing local S3; values withheld'
    externalVendorActivityAuthorized = $false
    bindingIncluded = ($Mode -eq 'Contract')
    helperOnlyIsNotProductAcceptance = ($Mode -in @('HarnessCompile', 'Preflight'))
    redactedEvents = $events
    redactedLog = $log
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receipt -Encoding utf8NoBOM
$readable | Write-Output
$process.Dispose()
exit $exitCode
