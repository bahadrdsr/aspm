param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [Parameter(Mandatory = $true)] [string] $OutputName
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79) { throw 'Use a fresh bounded calibration label.' }
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'Existing cached Go 1.27.1 is required; no installation authorized.' }
$output = Join-Path $root '.artifacts\ai-configuration-v1'
$copies = Join-Path $root ".cache\ai-configuration-a3\$OutputName"
$work = Join-Path $root '.cache\ai-configuration-v1\work'
foreach ($extension in @('jsonl', 'log', 'receipt.json')) {
    if (Test-Path (Join-Path $output "$OutputName.$extension")) { throw 'Refusing to overwrite earlier evidence.' }
}
if (Test-Path $copies) { throw 'Calibration working-copy label already exists.' }
try {
    $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable
    $database = [string] $runtime.databaseUrl
    $uri = [Uri] $database
    if ($uri.Scheme -notin @('postgres', 'postgresql') -or $uri.Host -ne '127.0.0.1' -or $uri.Port -lt 1 -or -not $uri.UserInfo.Contains(':')) { throw 'Invalid PG boundary' }
} catch { throw 'Explicit private owned loopback PG runtime is required; values withheld.' }
$redactions = @($database, $uri.UserInfo, [Uri]::UnescapeDataString($uri.UserInfo.Split(':', 2)[1]))
foreach ($value in @($redactions)) {
    if ($value) {
        $redactions += [Uri]::EscapeDataString($value)
        $json = ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1, $json.Length - 2)
    }
}
$redactions = @($redactions | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
New-Item -ItemType Directory -Path $output, $copies, $work -Force | Out-Null
$copied = @()
foreach ($name in @('contract_test.go', 'fixtures_test.go', 'production_bindings_test.go')) {
    $source = Join-Path $root "tests\ai_configuration\$name"
    $target = Join-Path $copies $name
    Copy-Item -LiteralPath $source -Destination $target
    $sourceHash = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant()
    $copyHash = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($sourceHash -ne $copyHash) { throw 'Copied calibration fixture bytes differ from original controls.' }
    $copied += [ordered]@{ source = [IO.Path]::GetRelativePath($root, $source); copy = [IO.Path]::GetRelativePath($root, $target); sha256 = $sourceHash }
}
$template = Join-Path $PSScriptRoot 'calibrate-time-A3.go.txt'
$test = Join-Path $copies 'time_instant_calibration_test.go'
Copy-Item -LiteralPath $template -Destination $test
$templateHash = (Get-FileHash -LiteralPath $template -Algorithm SHA256).Hash.ToLowerInvariant()
if ($templateHash -ne (Get-FileHash -LiteralPath $test -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Calibration template copy mismatch.' }
$arguments = @('test', '-json', '-tags=integration', '-mod=readonly', '-count=1', '-timeout=60s', '-run=^TestAIConfigurationTimeInstantMeasurementCalibration$', ".\.cache\ai-configuration-a3\$OutputName")
$start = [Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go; $start.WorkingDirectory = $root; $start.UseShellExecute = $false
$start.RedirectStandardOutput = $true; $start.RedirectStandardError = $true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') { $start.Environment.Remove($name) | Out-Null }
}
$start.Environment['GOTOOLCHAIN'] = 'local'; $start.Environment['GOENV'] = 'off'; $start.Environment['GOWORK'] = 'off'
$start.Environment['GOFLAGS'] = ''; $start.Environment['GOPROXY'] = 'off'; $start.Environment['GOSUMDB'] = 'off'
$start.Environment['GOMODCACHE'] = Join-Path $root '.cache\modules'
$start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
$start.Environment['GOPATH'] = Join-Path $root '.cache\ai-configuration-v1\gopath'
foreach ($name in @('GOTMPDIR', 'TEMP', 'TMP', 'TMPDIR')) { $start.Environment[$name] = $work }
$start.Environment['ASPM_AI_CONFIG_DATABASE_URL'] = $database
$start.Environment['AWS_EC2_METADATA_DISABLED'] = 'true'
$process = [Diagnostics.Process]::new(); $process.StartInfo = $start
$began = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Calibration Go process did not start.' }
$stdout = $process.StandardOutput.ReadToEndAsync(); $stderr = $process.StandardError.ReadToEndAsync()
$process.WaitForExit(); $code = $process.ExitCode
$text = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
foreach ($value in $redactions) { $text = $text.Replace($value, '[REDACTED]') }
$text | Set-Content (Join-Path $output "$OutputName.jsonl") -Encoding utf8NoBOM
$readable = foreach ($line in ($text -split "`r?`n")) {
    if ($line.StartsWith('{')) { $event = $line | ConvertFrom-Json -AsHashtable; if ($event.Output) { $event.Output.TrimEnd() } }
    elseif ($line) { $line }
}
$readable | Set-Content (Join-Path $output "$OutputName.log") -Encoding utf8NoBOM
$process.Dispose()
foreach ($file in @($copied | ForEach-Object { Join-Path $root $_.copy }) + @($test)) { Remove-Item -LiteralPath $file }
Remove-Item -LiteralPath $copies
[ordered]@{
    schemaVersion = 1; run = $OutputName; startedAt = $began.ToString('o'); finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    exitCode = $code; arguments = $arguments; templateSHA256 = $templateHash; unchangedFixtureCopies = $copied
    workingCopiesRemoved = -not (Test-Path $copies); originalFixtureOrBindingModified = $false
    timezoneEnvironmentChanged = $false; privateRuntimeValuesCopied = $false; productAcceptance = $false
    scope = 'Two actual owned PG/API PATCH observations plus bounded in-memory measurement vectors; no provider/storage HTTP.'
} | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable | Write-Output
exit $code
