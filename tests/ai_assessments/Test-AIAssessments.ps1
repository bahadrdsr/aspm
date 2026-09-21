param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Contract','HarnessCompile','FixtureCalibration','UpgradePrerequisite')] [string] $Mode = 'Contract',
    [Parameter(Mandatory = $true)] [string] $OutputName
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79) { throw 'Use a fresh bounded output label.' }
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'Cached Go 1.27.1 required; no installation authorized.' }
$output = Join-Path $root '.artifacts\ai-assessments-v1'
$work = Join-Path $root '.cache\ai-assessments-v1\work'
New-Item -ItemType Directory -Path $output,$work -Force | Out-Null
foreach ($extension in @('jsonl','log','receipt.json','published-v7-build.log','published-v7-build.json')) {
    if (Test-Path (Join-Path $output "$OutputName.$extension")) { throw 'Refusing to overwrite earlier evidence.' }
}
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'Cannot read selected private runtime; values withheld.' }
$mapping = [ordered]@{
    ASPM_ASSESSMENT_DATABASE_URL='databaseUrl'
    ASPM_ASSESSMENT_S3_ENDPOINT='s3Endpoint'
    ASPM_ASSESSMENT_S3_ACCESS_KEY='s3AccessKey'
    ASPM_ASSESSMENT_S3_SECRET_KEY='s3SecretKey'
    ASPM_ASSESSMENT_S3_BUCKET='s3Bucket'
}
foreach ($item in $mapping.GetEnumerator()) {
    if (-not ($runtime[$item.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$item.Value])) { throw "Missing private owned runtime field $($item.Value); values withheld." }
}
try {
    $database = [Uri] $runtime.databaseUrl; $storage = [Uri] $runtime.s3Endpoint
    if ($database.Scheme -notin @('postgres','postgresql') -or $database.Host -ne '127.0.0.1' -or $database.Port -lt 1 -or
        -not $database.UserInfo.Contains(':') -or $storage.Host -ne '127.0.0.1' -or $storage.Port -lt 1 -or
        $storage.Scheme -notin @('http','https') -or $storage.UserInfo -ne '' -or $storage.Query -ne '' -or $storage.Fragment -ne '') { throw 'Invalid boundary' }
} catch { throw 'Only the explicit authenticated owned loopback PG/S3 boundary is permitted.' }
$redactions = @($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$database.UserInfo,[Uri]::UnescapeDataString($database.UserInfo.Split(':',2)[1]))
if ($runtime.postgresPassword -is [string]) { $redactions += $runtime.postgresPassword }
foreach ($value in @($redactions)) {
    if ($value) {
        $redactions += [Uri]::EscapeDataString($value)
        $json = ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1,$json.Length-2)
    }
}
$redactions = @($redactions | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
& node (Join-Path $PSScriptRoot 'prepare-published-v7.mjs') $OutputName
if ($LASTEXITCODE -ne 0) { throw 'Pinned V7 fixture build failed; no test success claimed.' }
$helpers = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File | Where-Object { $_.Name -ne 'production_bindings_test.go' } | Sort-Object Name | Select-Object -ExpandProperty FullName)
switch ($Mode) {
    'HarnessCompile' { $arguments = @('test','-c','-tags=integration','-mod=readonly','-o',(Join-Path $output "$OutputName.helpers.test.exe")) + $helpers }
    'FixtureCalibration' { $arguments = @('test','-json','-tags=integration,ai_assessments_fixture','-mod=readonly','-count=1','-timeout=120s','-run=^TestAIAssessmentOwnedIntakeNativeAndPublishedV7Fixture$') + $helpers }
    'UpgradePrerequisite' { $arguments = @('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=120s','-run=^TestAA8PublishedV7ToV8KeepsLegacyAPIDataAndReopens$/^published-v7-upgrade$') + $helpers }
    default { $arguments = @('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=240s','.\tests\ai_assessments') }
}
$start = [Diagnostics.ProcessStartInfo]::new()
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
foreach ($item in $mapping.GetEnumerator()) { $start.Environment[$item.Key]=$runtime[$item.Value] }
$start.Environment['AWS_EC2_METADATA_DISABLED']='true'
$start.Environment['ASPM_ASSESSMENT_ARTIFACT_DIR']=$output
$start.Environment['ASPM_ASSESSMENT_TESTDATA']=Join-Path $PSScriptRoot 'testdata'
$start.Environment['ASPM_ASSESSMENT_V7_SEED_EXE']=Join-Path $root ".cache\ai-assessments-v1\published-v7\$OutputName\owned-v7-seed.exe"
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
[ordered]@{
    schemaVersion=1; mode=$Mode; startedAt=$began.ToString('o'); finishedAt=[DateTimeOffset]::UtcNow.ToString('o')
    exitCode=$code; goExecutable=$go; arguments=$arguments; fullProductionBindingsIncluded=($Mode -eq 'Contract')
    helperOnlyIsNotAssessmentAcceptance=($Mode -ne 'Contract'); privateRuntimeValuesCopied=$false
    boundary='Owned random PG schemas, scoped local S3 for real intake, owned native HTTP/TLS adapter responses only; no live provider/model account or target.'
    publishedFixtureOrigin='98a41ced71c09e71c8cc2bd158678fd52268498c'; dependenciesInstalled=$false
} | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable | Write-Output
$process.Dispose()
exit $code
