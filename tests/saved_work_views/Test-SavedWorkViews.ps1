param(
    [ValidateSet('Compile','Run','Calibration')] [string] $Mode = 'Compile',
    [Parameter(Mandatory = $true)] [string] $OutputName,
    [string] $RuntimeConfig,
    [string] $PublishedV8Receipt,
    [switch] $FixturesReady
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'Use a fresh plain artifact label.' }
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'BLOCKED: existing offline Go 1.27.1 is required; no install.' }
$out = Join-Path $root '.artifacts\saved-work-views-v1'
$work = Join-Path $root '.cache\saved-work-views-v1\work'
New-Item -ItemType Directory -Path $out,$work -Force | Out-Null
foreach ($extension in @('log','receipt.json','test.exe')) {
    if (Test-Path (Join-Path $out "$OutputName.$extension")) { throw 'Refusing to overwrite earlier evidence.' }
}
$redactions = @()
if ($Mode -ne 'Compile') {
    if (-not $FixturesReady) { throw 'BLOCKED: parent must relay readiness; this runner never restores PG/S3/Docker.' }
    if (-not $RuntimeConfig -or (Split-Path -Leaf $RuntimeConfig) -ne 'runtime.json' -or ($Mode -eq 'Run' -and -not $PublishedV8Receipt)) {
        throw 'BLOCKED: explicit private runtime.json and exact published-V8 build receipt are required.'
    }
    try {
        $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable
        $uri = [Uri]$runtime.databaseUrl
        if ($uri.Host -ne '127.0.0.1' -or $uri.Port -ne 15432 -or -not $uri.UserInfo.Contains(':')) { throw 'Invalid private fixture boundary' }
        $redactions = @($runtime.Values | Where-Object { $_ -is [string] -and $_ }) +
            @($uri.UserInfo,[Uri]::UnescapeDataString($uri.UserInfo.Split(':',2)[1]))
        if ($Mode -eq 'Run') { $null = Get-Content -LiteralPath $PublishedV8Receipt -Raw | ConvertFrom-Json }
    } catch { throw 'BLOCKED: private fixture config/build receipt unavailable or invalid; details withheld.' }
    foreach ($value in @($redactions)) {
        $redactions += [Uri]::EscapeDataString($value)
        $json = ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1,$json.Length-2)
    }
    $redactions = @($redactions | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    if ($Mode -eq 'Calibration') {
        $arguments = @('test','-json','-tags=integration,saved_work_views_calibration','-mod=readonly','-count=1','-timeout=240s','-run=^TestSavedWorkViewsOperatorRevocationCalibration$','.\tests\saved_work_views')
    } else {
        $arguments = @('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=240s','-run=^TestSavedWorkViews','.\tests\saved_work_views')
    }
} else {
    $arguments = @('test','-c','-tags=integration','-mod=readonly','-o',(Join-Path $out "$OutputName.test.exe"),'.\tests\saved_work_views')
}
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go
$start.WorkingDirectory = $root
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_|PG)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') {
        $start.Environment.Remove($name) | Out-Null
    }
}
foreach ($entry in @{
    GOTOOLCHAIN='local'; GOENV='off'; GOWORK='off'; GOFLAGS=''; GOPROXY='off'; GOSUMDB='off';
    GOMODCACHE=(Join-Path $root '.cache\modules'); GOCACHE=(Join-Path $root '.cache\build');
    GOPATH=(Join-Path $root '.cache\saved-work-views-v1\gopath');
    GOTMPDIR=$work; TEMP=$work; TMP=$work; TMPDIR=$work; AWS_EC2_METADATA_DISABLED='true'
}.GetEnumerator()) { $start.Environment[$entry.Key] = $entry.Value }
if ($Mode -ne 'Compile') {
    $start.Environment['ASPM_SAVED_VIEWS_RUNTIME'] = (Resolve-Path -LiteralPath $RuntimeConfig).Path
    if ($Mode -eq 'Run') { $start.Environment['ASPM_SAVED_VIEWS_V8_RECEIPT'] = (Resolve-Path -LiteralPath $PublishedV8Receipt).Path }
}
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$began = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Go process failed to start.' }
$stdout = $process.StandardOutput.ReadToEndAsync()
$stderr = $process.StandardError.ReadToEndAsync()
$process.WaitForExit()
$code = $process.ExitCode
$text = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
foreach ($value in $redactions) { $text = $text.Replace($value,'[REDACTED]') }
$text | Set-Content (Join-Path $out "$OutputName.log") -Encoding utf8NoBOM
$inputs = @(Get-ChildItem -LiteralPath $PSScriptRoot -Recurse -File |
    Where-Object { $_.Extension -in @('.go','.txt','.mjs','.ps1') } | Sort-Object FullName | ForEach-Object {
        [ordered]@{path=[IO.Path]::GetRelativePath($root,$_.FullName);sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
    })
[ordered]@{
    mode=$Mode; startedAt=$began.ToString('o'); finishedAt=[DateTimeOffset]::UtcNow.ToString('o');
    exitCode=$code; arguments=$arguments; inputHashes=$inputs;
    dataExecutionAttempted=($Mode -ne 'Compile'); fixturesReadinessRelayed=[bool]$FixturesReady;
    productionConstructor='app.Open'; mockedHandlers=$false; businessSQLSeeds=$false;
    providerActions=$false; installedDependencies=$false; signed=$false;
    operatorFixtureException='Approved single non-admin membership DELETE with both keys, owned schema/name verification and exactly one RETURNING row. No other business SQL writes or public removal API.'
    qualification=$(if($Mode -eq 'Calibration'){'Ordinary Work/session operator calibration only, not saved-view acceptance'}else{'Four feature narratives; consult actual reached/blocked evidence'})
} | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $out "$OutputName.receipt.json") -Encoding utf8NoBOM
if ($text) { $text | Write-Output }
Write-Output "$Mode exited $code. Evidence: .artifacts\saved-work-views-v1\$OutputName.log"
$process.Dispose()
exit $code
