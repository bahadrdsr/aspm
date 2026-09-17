param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Contract', 'ServiceContract', 'CommandBuild', 'HarnessCompile')] [string] $Mode = 'Contract',
    [string] $OutputName = 'runtime-red-01'
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'Use a plain unique artifact label.' }
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'Existing cached Go 1.27.1 is required; downloads are not authorized.' }
$output = Join-Path $root '.artifacts\delivery-runtime-v1'
$work = Join-Path $root '.cache\delivery-runtime-v1\work'
$binary = Join-Path $output "$OutputName.delivery-worker.test-owned.exe"
foreach ($directory in @($output, $work)) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
if (Test-Path (Join-Path $output "$OutputName.receipt.json")) { throw 'Refusing to overwrite an earlier runtime receipt.' }
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'Cannot read authorized private fixture metadata; values withheld.' }
$mapping = [ordered]@{
    ASPM_DELIVERY_TEST_DATABASE_URL = 'databaseUrl'
    ASPM_DELIVERY_TEST_S3_ENDPOINT = 's3Endpoint'
    ASPM_DELIVERY_TEST_S3_ACCESS_KEY = 's3AccessKey'
    ASPM_DELIVERY_TEST_S3_SECRET_KEY = 's3SecretKey'
    ASPM_DELIVERY_TEST_S3_BUCKET = 's3Bucket'
}
foreach ($entry in $mapping.GetEnumerator()) {
    if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
        throw "Required private fixture field $($entry.Value) is missing; no values logged."
    }
}
try {
    $db = [Uri]$runtime.databaseUrl
    $s3 = [Uri]$runtime.s3Endpoint
    if ($db.Host -ne '127.0.0.1' -or $db.Port -lt 1 -or -not $db.UserInfo.Contains(':') -or
        $db.Scheme -notin @('postgres', 'postgresql') -or $s3.Host -ne '127.0.0.1' -or $s3.Port -lt 1 -or
        $s3.Scheme -notin @('http', 'https') -or $s3.UserInfo -ne '' -or $s3.Query -ne '' -or $s3.Fragment -ne '') {
        throw 'Invalid fixture boundary'
    }
}
catch { throw 'Only the explicit authenticated loopback PG/local-S3 runtime is permitted.' }
$redactions = @($runtime.databaseUrl, $runtime.s3AccessKey, $runtime.s3SecretKey, $db.UserInfo,
    [Uri]::UnescapeDataString($db.UserInfo.Split(':', 2)[1]))
if ($runtime.postgresPassword -is [string]) { $redactions += $runtime.postgresPassword }
foreach ($value in @($redactions)) {
    if ($value) {
        $redactions += [Uri]::EscapeDataString($value)
        $json = ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1, $json.Length - 2)
    }
}
$redactions = @($redactions | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
$stages = [System.Collections.Generic.List[object]]::new()

function Invoke-OwnedGo([string[]] $Arguments, [string] $Stage) {
    $eventFile = Join-Path $output "$OutputName-$Stage.jsonl"
    $logFile = Join-Path $output "$OutputName-$Stage.log"
    if ((Test-Path $eventFile) -or (Test-Path $logFile)) { throw 'Refusing to replace earlier stage evidence.' }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $go
    $start.WorkingDirectory = $root
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    foreach ($name in @($start.Environment.Keys)) {
        if ($name -match '^(ASPM_|AWS_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') { $start.Environment.Remove($name) | Out-Null }
    }
    $start.Environment['GOTOOLCHAIN'] = 'local'
    $start.Environment['GOENV'] = 'off'
    $start.Environment['GOWORK'] = 'off'
    $start.Environment['GOFLAGS'] = ''
    $start.Environment['GOPROXY'] = 'off'
    $start.Environment['GOSUMDB'] = 'off'
    $start.Environment['GOMODCACHE'] = Join-Path $root '.cache\modules'
    $start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
    $start.Environment['GOPATH'] = Join-Path $root '.cache\delivery-runtime-v1\gopath'
    foreach ($name in @('GOTMPDIR', 'TEMP', 'TMP', 'TMPDIR')) { $start.Environment[$name] = $work }
    $start.Environment['AWS_EC2_METADATA_DISABLED'] = 'true'
    $start.Environment['ASPM_DELIVERY_TEST_ARTIFACT_DIR'] = $output
    $start.Environment['ASPM_DELIVERY_TEST_BINARY'] = $binary
    foreach ($entry in $mapping.GetEnumerator()) { $start.Environment[$entry.Key] = $runtime[$entry.Value] }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    $began = [DateTimeOffset]::UtcNow
    if (-not $process.Start()) { throw 'Go process could not start.' }
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $code = $process.ExitCode
    $text = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
    foreach ($value in $redactions) { $text = $text.Replace($value, '[REDACTED]') }
    $text | Set-Content -LiteralPath $eventFile -Encoding utf8NoBOM
    $readable = foreach ($line in ($text -split "`r?`n")) {
        if ($line.StartsWith('{')) {
            $event = $line | ConvertFrom-Json -AsHashtable
            if ($event.Output) { $event.Output.TrimEnd() }
        } elseif ($line) { $line }
    }
    $readable | Set-Content -LiteralPath $logFile -Encoding utf8NoBOM
    $readable | Write-Host
    $stages.Add([ordered]@{
        name = $Stage
        startedAt = $began.ToString('o')
        finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
        arguments = $Arguments
        exitCode = $code
        events = $eventFile
        log = $logFile
    })
    $process.Dispose()
    return $code
}

$code = 0
if ($Mode -eq 'HarnessCompile') {
    $files = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File |
        Where-Object { $_.Name -ne 'production_bindings_test.go' } | Sort-Object Name | Select-Object -ExpandProperty FullName)
    $code = Invoke-OwnedGo (@('test', '-c', '-mod=readonly', '-tags=integration', '-o', (Join-Path $output "$OutputName.helpers.test.exe")) + $files) 'helper-compile'
} else {
    if ($Mode -in @('Contract', 'CommandBuild')) {
        $code = Invoke-OwnedGo @('build', '-mod=readonly', '-o', $binary, '.\cmd\delivery-worker') 'command-build'
    }
    if ($code -eq 0 -and $Mode -in @('Contract', 'ServiceContract')) {
        $code = Invoke-OwnedGo @('test', '-json', '-mod=readonly', '-tags=integration', '-count=1', '-timeout=180s', '.\tests\delivery_runtime') 'service-contract'
    }
}
[ordered]@{
    schemaVersion = 1
    mode = $Mode
    exitCode = $code
    goExecutable = $go
    stages = @($stages)
    helperOnlyIsNotAcceptance = ($Mode -eq 'HarnessCompile')
    runtime = 'explicit owned loopback PG/local-S3; values withheld'
    nativeBoundary = 'owned certificate-validated Slack TLS fixture; no vendor/cloud accounts'
    commandBinary = $binary
} | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
exit $code
