param([string] $OutputName = 'semantic-red-01')

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'Use a plain unique output label.' }
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'Cached Go 1.27.1 is required; no downloads authorized.' }
$output = Join-Path $root '.artifacts\connector-links-v1'
$work = Join-Path $root '.cache\connector-links-v1\work'
foreach ($directory in @($output, $work)) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
foreach ($extension in @('jsonl', 'log', 'receipt.json')) {
    if (Test-Path (Join-Path $output "$OutputName.$extension")) { throw 'Refusing to overwrite existing evidence.' }
}
$arguments = @('test', '-json', '-mod=readonly', '-count=1', '-timeout=30s', '.\tests\connector_links')
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go
$start.WorkingDirectory = $root
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') {
        $start.Environment.Remove($name) | Out-Null
    }
}
$start.Environment['GOTOOLCHAIN'] = 'local'
$start.Environment['GOWORK'] = 'off'
$start.Environment['GOENV'] = 'off'
$start.Environment['GOPROXY'] = 'off'
$start.Environment['GOSUMDB'] = 'off'
$start.Environment['GOMODCACHE'] = Join-Path $root '.cache\modules'
$start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
$start.Environment['GOPATH'] = Join-Path $root '.cache\connector-links-v1\gopath'
$start.Environment['GOTMPDIR'] = $work
$start.Environment['TEMP'] = $work
$start.Environment['TMP'] = $work
$start.Environment['TMPDIR'] = $work
$start.Environment['AWS_EC2_METADATA_DISABLED'] = 'true'
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$began = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Go could not start.' }
$stdout = $process.StandardOutput.ReadToEndAsync()
$stderr = $process.StandardError.ReadToEndAsync()
$process.WaitForExit()
$exitCode = $process.ExitCode
$raw = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
$raw | Set-Content (Join-Path $output "$OutputName.jsonl") -Encoding utf8NoBOM
$readable = foreach ($line in ($raw -split "`r?`n")) {
    if ($line.StartsWith('{')) {
        $event = $line | ConvertFrom-Json -AsHashtable
        if ($event.Output) { $event.Output.TrimEnd() }
    } elseif ($line) { $line }
}
$readable | Set-Content (Join-Path $output "$OutputName.log") -Encoding utf8NoBOM
[ordered]@{
    schemaVersion = 1
    startedAt = $began.ToString('o')
    finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    exitCode = $exitCode
    goExecutable = $go
    arguments = $arguments
    privateRuntimeOrVendorCredentialsLoaded = $false
    boundary = 'Generated identities and one task-owned certificate-validated TLS Slack server per case; no vendor or third-party destination authorized.'
    log = Join-Path $output "$OutputName.log"
    events = Join-Path $output "$OutputName.jsonl"
} | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable | Write-Output
$process.Dispose()
exit $exitCode
