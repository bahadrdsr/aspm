param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Contract','HarnessCompile','Preflight','ExistingAdapters')] [string] $Mode = 'Contract',
    [string] $OutputName = 'source-red-01'
)

$ErrorActionPreference = 'Stop'
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'Use a plain unique output label.' }
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) { throw 'BLOCKED: cached Go 1.27.1 is required; no downloads authorized.' }
$output = Join-Path $root '.artifacts\source-collection-v1'
$work = Join-Path $root '.cache\source-collection-v1\work'
New-Item -ItemType Directory -Path $output,$work -Force | Out-Null
foreach ($ext in @('jsonl','log','receipt.json')) {
    if (Test-Path (Join-Path $output "$OutputName.$ext")) { throw 'Refusing to overwrite existing evidence.' }
}
try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
catch { throw 'Cannot read authorized private runtime; contents withheld.' }
$mapping = [ordered]@{
    ASPM_COLLECTION_DATABASE_URL='databaseUrl'
    ASPM_COLLECTION_S3_ENDPOINT='s3Endpoint'
    ASPM_COLLECTION_S3_ACCESS_KEY='s3AccessKey'
    ASPM_COLLECTION_S3_SECRET_KEY='s3SecretKey'
    ASPM_COLLECTION_S3_BUCKET='s3Bucket'
}
foreach ($entry in $mapping.GetEnumerator()) {
    if (-not ($runtime[$entry.Value] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$entry.Value])) {
        throw "Required private runtime field $($entry.Value) is missing; values withheld."
    }
}
try {
    $db=[Uri]$runtime.databaseUrl
    $store=[Uri]$runtime.s3Endpoint
    if ($db.Scheme -notin @('postgres','postgresql') -or $db.Host -ne '127.0.0.1' -or $db.Port -lt 1 -or
        -not $db.UserInfo.Contains(':') -or $store.Host -ne '127.0.0.1' -or $store.Port -lt 1 -or
        $store.Scheme -notin @('http','https') -or $store.UserInfo -ne '' -or $store.Query -ne '' -or $store.Fragment -ne '') {
        throw 'Invalid local boundary'
    }
}
catch { throw 'Only the explicit authenticated loopback PG/local-S3 runtime is allowed.' }
$redactions=@($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$db.UserInfo,
    [Uri]::UnescapeDataString($db.UserInfo.Split(':',2)[1]))
if ($runtime.postgresPassword -is [string]) { $redactions+=$runtime.postgresPassword }
foreach($value in @($redactions)) {
    if($value) {
        $redactions += [Uri]::EscapeDataString($value)
        $json=ConvertTo-Json -InputObject $value -Compress
        $redactions += $json.Substring(1,$json.Length-2)
    }
}
$redactions=@($redactions | Where-Object {$_} | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
$arguments=@('test','-mod=readonly','-tags=integration')
$helpers=@(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File |
    Where-Object {$_.Name -ne 'production_bindings_test.go'} | Sort-Object Name | Select-Object -ExpandProperty FullName)
switch($Mode) {
    'HarnessCompile' { $arguments+=@('-c','-o',(Join-Path $output "$OutputName.helpers.test.exe"))+$helpers }
    'Preflight' { $arguments+=@('-json','-count=1','-timeout=60s','-run=^TestSourceCollectionOwnedIOPreflight$')+$helpers }
    'ExistingAdapters' { $arguments=@('test','-json','-mod=readonly','-count=1','-timeout=60s','.\tests\connectors','.\internal\connectors') }
    default { $arguments+=@('-json','-count=1','-timeout=240s','.\tests\source_collection') }
}
$start=[System.Diagnostics.ProcessStartInfo]::new()
$start.FileName=$go; $start.WorkingDirectory=$root; $start.UseShellExecute=$false
$start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
foreach($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach($name in @($start.Environment.Keys)) {
    if($name -match '^(ASPM_|AWS_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$') { $start.Environment.Remove($name)|Out-Null }
}
$start.Environment['GOTOOLCHAIN']='local'; $start.Environment['GOENV']='off'; $start.Environment['GOWORK']='off'
$start.Environment['GOFLAGS']=''; $start.Environment['GOPROXY']='off'; $start.Environment['GOSUMDB']='off'
$start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules'
$start.Environment['GOCACHE']=Join-Path $root '.cache\build'
$start.Environment['GOPATH']=Join-Path $root '.cache\source-collection-v1\gopath'
foreach($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')) { $start.Environment[$name]=$work }
$start.Environment['AWS_EC2_METADATA_DISABLED']='true'
$start.Environment['ASPM_COLLECTION_ARTIFACT_DIR']=$output
foreach($entry in $mapping.GetEnumerator()) { $start.Environment[$entry.Key]=$runtime[$entry.Value] }
$process=[System.Diagnostics.Process]::new(); $process.StartInfo=$start
$began=[DateTimeOffset]::UtcNow
if(-not $process.Start()) { throw 'Go process failed to start.' }
$stdout=$process.StandardOutput.ReadToEndAsync(); $stderr=$process.StandardError.ReadToEndAsync()
$process.WaitForExit(); $code=$process.ExitCode
$text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
foreach($value in $redactions) { $text=$text.Replace($value,'[REDACTED]') }
$text|Set-Content (Join-Path $output "$OutputName.jsonl") -Encoding utf8NoBOM
$readable=foreach($line in ($text -split "`r?`n")) {
    if($line.StartsWith('{')) { $event=$line|ConvertFrom-Json -AsHashtable; if($event.Output){$event.Output.TrimEnd()} }
    elseif($line){$line}
}
$readable|Set-Content (Join-Path $output "$OutputName.log") -Encoding utf8NoBOM
[ordered]@{
    schemaVersion=1; mode=$Mode; startedAt=$began.ToString('o'); finishedAt=[DateTimeOffset]::UtcNow.ToString('o')
    exitCode=$code; goExecutable=$go; arguments=$arguments
    fullProductionBindingIncluded=($Mode -eq 'Contract')
    helperOnlyIsNotAcceptance=($Mode -in @('HarnessCompile','Preflight'))
    boundary='Explicit loopback PG/local S3 and exact owned TLS GitHub protocol fixture only; values withheld'
    liveGitHubCloudOrCredentialDiscoveryAuthorized=$false
} | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable|Write-Output
$process.Dispose()
exit $code
