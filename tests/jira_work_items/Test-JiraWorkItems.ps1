param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Compile', 'Calibration', 'Contract')] [string] $Mode = 'Contract',
    [string] $PublishedReceipt,
    [Parameter(Mandatory = $true)] [string] $OutputName
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'A fresh plain artifact label is required.' }
$artifacts = Join-Path $root '.artifacts\jira-work-items'
$cache = Join-Path $root '.cache\jira-work-items'
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
$modules = Join-Path $root '.cache\modules'
if (-not (Test-Path -LiteralPath $go -PathType Leaf) -or -not (Test-Path -LiteralPath $modules -PathType Container)) {
    throw 'BLOCKED: cached Go1.27.1/modules required; no install or restore is authorized.'
}
if ($Mode -eq 'Contract' -and (-not $PublishedReceipt -or -not (Test-Path -LiteralPath $PublishedReceipt -PathType Leaf))) {
    throw 'BLOCKED: prepare the exact pinned V9 app fixture first and pass its build receipt.'
}
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'BLOCKED: private runtime metadata unavailable; values withheld.' }
foreach ($field in @('databaseUrl', 's3Endpoint', 's3AccessKey', 's3SecretKey', 's3Bucket')) {
    if (-not ($runtime[$field] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$field])) {
        throw "BLOCKED: required private field $field is missing; values withheld."
    }
}
$secrets = @($runtime.databaseUrl, $runtime.s3AccessKey, $runtime.s3SecretKey)
try {
    $db = [Uri]$runtime.databaseUrl
    $store = [Uri]$runtime.s3Endpoint
    if ($db.Host -ne '127.0.0.1' -or $db.Port -ne 15432 -or $db.Scheme -notin @('postgres', 'postgresql') -or
        -not $db.UserInfo.Contains(':') -or $store.Host -ne '127.0.0.1' -or $store.Port -ne 18333 -or $store.Scheme -ne 'http') {
        throw 'Wrong owned fixture boundary'
    }
    $secrets += [Uri]::UnescapeDataString($db.UserInfo.Split(':', 2)[1])
}
catch { throw 'BLOCKED: only explicit authenticated PG15432/S318333 fixtures are permitted.' }
foreach ($secret in @($secrets)) {
    $secrets += [Uri]::EscapeDataString($secret)
    $secrets += [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($secret))
}
$secrets = @($secrets | Where-Object { $_ } | Sort-Object -Unique | Sort-Object Length -Descending)
foreach ($path in @($artifacts, (Join-Path $cache 'work'), (Join-Path $cache 'gopath'))) {
    New-Item -ItemType Directory -Path $path -Force | Out-Null
}
$log = Join-Path $artifacts "$OutputName.log"
$events = Join-Path $artifacts "$OutputName.jsonl"
$receipt = Join-Path $artifacts "$OutputName.receipt.json"
foreach ($path in @($log, $events, $receipt)) {
    if (Test-Path -LiteralPath $path) { throw 'Refusing to overwrite prior evidence; choose another label.' }
}
$arguments = @('test', '-mod=readonly', '-tags=integration')
if ($Mode -eq 'Compile') {
    $arguments += @('-c', '-o', (Join-Path $artifacts "$OutputName.test.exe"), '.\tests\jira_work_items')
} else {
    $arguments += @('-json', '-count=1', '-timeout=90s')
    if ($Mode -eq 'Calibration') { $arguments += '-run=^TestJiraNativeTLSCalibration$' }
    $arguments += '.\tests\jira_work_items'
}
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go
$start.WorkingDirectory = $root
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_|JIRA_)' -or
        $name -match '^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') {
        $start.Environment.Remove($name) | Out-Null
    }
}
$settings = @{
    GOTOOLCHAIN = 'local'; GOWORK = 'off'; GOENV = 'off'; GOFLAGS = ''; GOPROXY = 'off'; GOSUMDB = 'off'
    GOMODCACHE = $modules; GOCACHE = (Join-Path $root '.cache\build'); GOPATH = (Join-Path $cache 'gopath')
    GOTMPDIR = (Join-Path $cache 'work'); TEMP = (Join-Path $cache 'work')
    TMP = (Join-Path $cache 'work'); TMPDIR = (Join-Path $cache 'work')
    AWS_EC2_METADATA_DISABLED = 'true'; ASPM_JIRA_RUNTIME = $RuntimeConfig
}
if ($PublishedReceipt) { $settings['ASPM_JIRA_V9_RECEIPT'] = $PublishedReceipt }
foreach ($entry in $settings.GetEnumerator()) { $start.Environment[$entry.Key] = $entry.Value }
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$startedAt = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'BLOCKED: selected Go process did not start.' }
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
        } catch { '[unparseable redacted Go event]' }
    } elseif ($line) { $line }
}
$readable | Set-Content -LiteralPath $log -Encoding utf8NoBOM
[ordered]@{
    schemaVersion = 1; mode = $Mode; startedAt = $startedAt.ToString('o'); finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    exitCode = $exitCode; arguments = $arguments; runtimeBoundary = 'owned PG15432/S318333 only; private values withheld'
    normalFreshOwnedTLS = $true; oldSuitesRun = $false; uiTestsRun = $false; productionChanged = $false
    vendorCallsAuthorized = $false; redactedLog = $log; redactedEvents = $events
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receipt -Encoding utf8NoBOM
$readable | Write-Output
$process.Dispose()
exit $exitCode
