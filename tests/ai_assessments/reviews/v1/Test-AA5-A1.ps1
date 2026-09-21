param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [Parameter(Mandatory = $true)]
    [ValidateSet('AA5','OrderingOld','Ordering','Absent','Queued')] [string] $Mode,
    [Parameter(Mandatory = $true)] [string] $OutputName
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79) { throw 'Use a fresh bounded label.' }
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'Cached Go 1.27.1 is required; no installation authorized.' }
$output = Join-Path $root '.artifacts\ai-assessments-v1'
$work = Join-Path $root '.cache\ai-assessments-v1\work'
$copies = Join-Path $root ".cache\ai-assessments-a1\$OutputName"
foreach ($extension in @('jsonl','log','receipt.json')) {
    if (Test-Path (Join-Path $output "$OutputName.$extension")) { throw 'Refusing to overwrite prior evidence.' }
}
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'Cannot read selected owned private runtime; values withheld.' }
$mapping = [ordered]@{
    ASPM_ASSESSMENT_DATABASE_URL='databaseUrl'
    ASPM_ASSESSMENT_S3_ENDPOINT='s3Endpoint'
    ASPM_ASSESSMENT_S3_ACCESS_KEY='s3AccessKey'
    ASPM_ASSESSMENT_S3_SECRET_KEY='s3SecretKey'
    ASPM_ASSESSMENT_S3_BUCKET='s3Bucket'
}
foreach ($entry in $mapping.GetEnumerator()) {
    if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) { throw 'Required owned runtime input is missing; values withheld.' }
}
try {
    $db=[Uri]$runtime.databaseUrl; $store=[Uri]$runtime.s3Endpoint
    if ($db.Host -ne '127.0.0.1' -or $db.Port -lt 1 -or $db.Scheme -notin @('postgres','postgresql') -or
        -not $db.UserInfo.Contains(':') -or $store.Host -ne '127.0.0.1' -or $store.Port -lt 1 -or
        $store.Scheme -notin @('http','https') -or $store.UserInfo -ne '' -or $store.Query -ne '' -or $store.Fragment -ne '') { throw 'Invalid boundary' }
} catch { throw 'Only explicit authenticated owned loopback PG/S3 fixtures are permitted.' }
$redactions=@($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$db.UserInfo,[Uri]::UnescapeDataString($db.UserInfo.Split(':',2)[1]))
if ($runtime.postgresPassword -is [string]) { $redactions += $runtime.postgresPassword }
foreach ($value in @($redactions)) {
    if ($value) {
        $redactions += [Uri]::EscapeDataString($value)
        $json=ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1,$json.Length-2)
    }
}
$redactions=@($redactions | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
New-Item -ItemType Directory -Path $output,$work -Force | Out-Null
$sourceRoot = Join-Path $root 'tests\ai_assessments'
$nativeSource = Join-Path $sourceRoot 'native_test.go'
$copied = @()
$target = '.\tests\ai_assessments'
$selector = '^TestAA5HeldNativeIOReleasesSingleSQLPoolAndCancellationIsPotentiallySent$'
if ($Mode -ne 'AA5') {
    if (Test-Path $copies) { throw 'Calibration working label already exists.' }
    New-Item -ItemType Directory -Path $copies -Force | Out-Null
    if ($Mode -eq 'OrderingOld') { $nativeSource = Join-Path $PSScriptRoot 'history\a1\native_test.go.v1' }
    foreach ($name in @('contract_test.go','fixtures_test.go','production_bindings_test.go','native_test.go')) {
        $source = if ($name -eq 'native_test.go') { $nativeSource } else { Join-Path $sourceRoot $name }
        $destination = Join-Path $copies $name
        Copy-Item -LiteralPath $source -Destination $destination
        $hash=(Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($hash -ne (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Calibration copy is not byte-identical.' }
        $copied += [ordered]@{ source=[IO.Path]::GetRelativePath($root,$source); copy=[IO.Path]::GetRelativePath($root,$destination); sha256=$hash }
    }
    $template=Join-Path $PSScriptRoot 'calibrate-aa5-A1.go.txt'
    $destination=Join-Path $copies 'observation_ordering_test.go'
    Copy-Item -LiteralPath $template -Destination $destination
    $copied += [ordered]@{ source=[IO.Path]::GetRelativePath($root,$template); copy=[IO.Path]::GetRelativePath($root,$destination); sha256=(Get-FileHash $template -Algorithm SHA256).Hash.ToLowerInvariant() }
    $target=".\.cache\ai-assessments-a1\$OutputName"
    $selector=if ($Mode -in @('Absent','Queued')) { '^TestAA5GenuineMarkerRejectionCalibration$' } else { '^TestAA5ObservationReadinessCalibration$' }
}
$arguments=@('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=240s',"-run=$selector",$target)
$start=[Diagnostics.ProcessStartInfo]::new()
$start.FileName=$go; $start.WorkingDirectory=$root; $start.UseShellExecute=$false
$start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') { $start.Environment.Remove($name) | Out-Null }
}
$start.Environment['GOTOOLCHAIN']='local'; $start.Environment['GOENV']='off'; $start.Environment['GOWORK']='off'
$start.Environment['GOFLAGS']=''; $start.Environment['GOPROXY']='off'; $start.Environment['GOSUMDB']='off'
$start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules'; $start.Environment['GOCACHE']=Join-Path $root '.cache\build'
$start.Environment['GOPATH']=Join-Path $root '.cache\ai-assessments-v1\gopath'
foreach ($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')) { $start.Environment[$name]=$work }
foreach ($entry in $mapping.GetEnumerator()) { $start.Environment[$entry.Key]=$runtime[$entry.Value] }
$start.Environment['AWS_EC2_METADATA_DISABLED']='true'
$start.Environment['ASPM_AA5_ADVERSE_MARKER']=$Mode.ToLowerInvariant()
$process=[Diagnostics.Process]::new(); $process.StartInfo=$start; $began=[DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Go process did not start.' }
$stdout=$process.StandardOutput.ReadToEndAsync(); $stderr=$process.StandardError.ReadToEndAsync()
$process.WaitForExit(); $code=$process.ExitCode
$text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
foreach ($value in $redactions) { $text=$text.Replace($value,'[REDACTED]') }
$text | Set-Content (Join-Path $output "$OutputName.jsonl") -Encoding utf8NoBOM
$readable=foreach ($line in ($text -split "`r?`n")) {
    if ($line.StartsWith('{')) { $event=$line | ConvertFrom-Json -AsHashtable; if ($event.Output) { $event.Output.TrimEnd() } }
    elseif ($line) { $line }
}
$readable | Set-Content (Join-Path $output "$OutputName.log") -Encoding utf8NoBOM
$process.Dispose()
if ($Mode -ne 'AA5') {
    foreach ($file in $copied) { Remove-Item -LiteralPath (Join-Path $root $file.copy) }
    Remove-Item -LiteralPath $copies
}
[ordered]@{
    schemaVersion=1; mode=$Mode; run=$OutputName; startedAt=$began.ToString('o'); finishedAt=[DateTimeOffset]::UtcNow.ToString('o')
    exitCode=$code; arguments=$arguments; nativeFixture=[IO.Path]::GetRelativePath($root,$nativeSource)
    nativeFixtureSHA256=(Get-FileHash $nativeSource -Algorithm SHA256).Hash.ToLowerInvariant()
    unchangedCopies=$copied; workingCopiesRemoved=($Mode -eq 'AA5' -or -not (Test-Path $copies))
    originalEightAssertionsOrBindingsEdited=$false; poolLimitsOrDeadlinesChanged=$false; privateRuntimeValuesCopied=$false
    calibrationNotFullAcceptance=($Mode -ne 'AA5'); negativeModesReturnTheirActualFailure=$true
} | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable | Write-Output
exit $code
