param(
    [Parameter(Mandatory=$true)] [string] $RuntimeConfig,
    [ValidateSet('Contract','AR4','RunContext','Diagnostic')] [string] $Mode='Contract',
    [Parameter(Mandatory=$true)] [string] $OutputName,
    [ValidateRange(1,3)] [int] $Count=1,
    [ValidateSet('All','Run-context','actual-command-crash')] [string] $DiagnosticCase='All'
)
$ErrorActionPreference='Stop'
if($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79){throw 'Use a fresh bounded label.'}
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
$source=Join-Path $root 'tests\assessment_runtime'
$baselinePath=Join-Path $PSScriptRoot 'BEFORE-A1.json'
if((Get-FileHash -LiteralPath $baselinePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne '0dfc6df61bea16d63f256b67629e33b951c38341776f022d9259b5b998d81552'){throw 'Successor baseline changed'}
$baseline=Get-Content -Raw -LiteralPath $baselinePath|ConvertFrom-Json
function Witness([string]$path){
    $full=Join-Path $root $path
    [ordered]@{path=$path;bytes=(Get-Item -LiteralPath $full).Length;sha256=(Get-FileHash -LiteralPath $full -Algorithm SHA256).Hash.ToLowerInvariant()}
}
function Assert-Inputs {
    foreach($entry in $baseline.existingNonWebInputs){
        $current=Witness $entry.path
        if($current.sha256 -eq $entry.sha256){continue}
        if($entry.path -ne 'tests\assessment_runtime\fixtures_test.go'){throw 'Held source/frozen control changed'}
        & node (Join-Path $PSScriptRoot 'prepare-A1.mjs') verify-cadence
        if($LASTEXITCODE -ne 0){throw 'Unapproved fixture change'}
    }
    $actual=@(Get-ChildItem -LiteralPath (Join-Path $root 'internal'),(Join-Path $root 'cmd') -Recurse -File -Filter '*.go'|ForEach-Object {[IO.Path]::GetRelativePath($root,$_.FullName)}|Sort-Object)
    if(Compare-Object $baseline.productionGoFileSet $actual){throw 'Production Go source set changed or shadow file added'}
}
Assert-Inputs
$inputPaths=@($baseline.existingNonWebInputs.path)+@(
    'tests\assessment_runtime\reviews\v1\Test-Runtime-A1.ps1',
    'tests\assessment_runtime\reviews\v1\prepare-A1.mjs',
    'tests\assessment_runtime\reviews\v1\observe-A1.go.txt',
    'tests\assessment_runtime\reviews\v1\BEFORE-A1.json'
)
$inputs=@(foreach($path in $inputPaths){Witness $path})
$go=Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if(-not(Test-Path -LiteralPath $go -PathType Leaf)){throw 'Cached Go1.27.1 required; no installation authorized.'}
$output=Join-Path $root '.artifacts\assessment-runtime-v1'
$work=Join-Path $root '.cache\assessment-runtime-v1\work'
$binary=Join-Path $output "$OutputName.assessment-worker.owned.exe"
New-Item -ItemType Directory -Path $output,$work -Force|Out-Null
foreach($extension in @('receipt.json','inputs.json','inputs-before.json','assessment-worker.owned.exe')){if(Test-Path (Join-Path $output "$OutputName.$extension")){throw 'Refusing to overwrite prior evidence.'}}
[ordered]@{schemaVersion=1;run=$OutputName;files=$inputs;phase='before-execution';guardExclusions=@('web\**','README.md')}|ConvertTo-Json -Depth 6|Set-Content -LiteralPath (Join-Path $output "$OutputName.inputs-before.json") -Encoding utf8NoBOM
try{$runtime=Get-Content -LiteralPath $RuntimeConfig -Raw|ConvertFrom-Json -AsHashtable}
catch{throw 'Cannot read selected owned private runtime; values withheld.'}
$mapping=[ordered]@{
    ASPM_ASSESSMENT_RUNTIME_DATABASE_URL='databaseUrl'
    ASPM_ASSESSMENT_RUNTIME_S3_ENDPOINT='s3Endpoint'
    ASPM_ASSESSMENT_RUNTIME_S3_ACCESS_KEY='s3AccessKey'
    ASPM_ASSESSMENT_RUNTIME_S3_SECRET_KEY='s3SecretKey'
    ASPM_ASSESSMENT_RUNTIME_S3_BUCKET='s3Bucket'
}
foreach($entry in $mapping.GetEnumerator()){
    if(-not($runtime[$entry.Value] -is [string])-or[string]::IsNullOrWhiteSpace($runtime[$entry.Value])){throw 'Required owned runtime input missing; values withheld.'}
}
try{
    $db=[Uri]$runtime.databaseUrl;$store=[Uri]$runtime.s3Endpoint
    if($db.Scheme -notin @('postgres','postgresql')-or$db.Host -ne '127.0.0.1'-or$db.Port -ne 15432-or-not$db.UserInfo.Contains(':')-or
        $store.Scheme -notin @('http','https')-or$store.Host -ne '127.0.0.1'-or$store.Port -ne 18333-or$store.UserInfo -ne ''-or$store.Query -ne ''-or$store.Fragment -ne ''){throw 'Invalid owned boundary'}
}catch{throw 'Only explicit authenticated owned loopback PG/S3 fixtures are permitted.'}
$redactions=@($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$db.UserInfo,[Uri]::UnescapeDataString($db.UserInfo.Split(':',2)[1]))
if($runtime.postgresPassword -is [string]){$redactions+=$runtime.postgresPassword}
foreach($value in @($redactions)){
    if($value){$redactions+=[Uri]::EscapeDataString($value);$json=ConvertTo-Json -InputObject $value -Compress;$redactions+=$json.Substring(1,$json.Length-2)}
}
$redactions=@($redactions|Where-Object {$_}|Sort-Object -CaseSensitive -Unique|Sort-Object Length -Descending)
$stages=[Collections.Generic.List[object]]::new()
function Invoke-OwnedGo([string[]]$Arguments,[string]$Stage){
    $events=Join-Path $output "$OutputName-$Stage.jsonl";$log=Join-Path $output "$OutputName-$Stage.log"
    if((Test-Path $events)-or(Test-Path $log)){throw 'Stage label already exists.'}
    $start=[Diagnostics.ProcessStartInfo]::new();$start.FileName=$go;$start.WorkingDirectory=$root;$start.UseShellExecute=$false
    $start.RedirectStandardOutput=$true;$start.RedirectStandardError=$true
    foreach($argument in $Arguments){$start.ArgumentList.Add($argument)}
    foreach($name in @($start.Environment.Keys)){
        if($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY|GODEBUG)$'){$start.Environment.Remove($name)|Out-Null}
    }
    $start.Environment['GOTOOLCHAIN']='local';$start.Environment['GOENV']='off';$start.Environment['GOWORK']='off'
    $start.Environment['GOFLAGS']='';$start.Environment['GOPROXY']='off';$start.Environment['GOSUMDB']='off'
    $start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules';$start.Environment['GOCACHE']=Join-Path $root '.cache\build'
    $start.Environment['GOPATH']=Join-Path $root '.cache\assessment-runtime-v1\gopath'
    foreach($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')){$start.Environment[$name]=$work}
    foreach($entry in $mapping.GetEnumerator()){$start.Environment[$entry.Key]=$runtime[$entry.Value]}
    $start.Environment['AWS_EC2_METADATA_DISABLED']='true'
    $start.Environment['ASPM_ASSESSMENT_RUNTIME_ARTIFACT_DIR']=$output
    $start.Environment['ASPM_ASSESSMENT_RUNTIME_BINARY']=$binary
    $start.Environment['ASPM_ASSESSMENT_RUNTIME_A1_LABEL']=$OutputName
    $process=[Diagnostics.Process]::new();$process.StartInfo=$start;$began=[DateTimeOffset]::UtcNow
    if(-not$process.Start()){throw 'Go process did not start.'}
    $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync()
    $timedOut=-not$process.WaitForExit(270000)
    if($timedOut){$process.Kill($true);$process.WaitForExit()}
    $code=$process.ExitCode
    $text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
    foreach($value in $redactions){$text=$text.Replace($value,'[REDACTED]')}
    $outputExceeded=$text.Length -gt 2097152
    if($outputExceeded){$text=$text.Substring(0,2097152)}
    if($timedOut -or $outputExceeded){$code=124;$text+="`nBLOCKED runner time/output bound exceeded; no acceptance claimed.`n"}
    $text|Set-Content -LiteralPath $events -Encoding utf8NoBOM
    $readable=foreach($line in ($text -split "`r?`n")){
        if($line.StartsWith('{')){try{$event=$line|ConvertFrom-Json -AsHashtable;if($event.Output){$event.Output.TrimEnd()}}catch{'[Incomplete bounded JSON event]'}}
        elseif($line){$line}
    }
    [IO.File]::WriteAllText($log,($readable -join [Environment]::NewLine),[Text.UTF8Encoding]::new($false))
    $readable|Select-Object -First 100|Write-Host
    $stages.Add([ordered]@{stage=$Stage;arguments=$Arguments;startedAt=$began.ToString('o');finishedAt=[DateTimeOffset]::UtcNow.ToString('o');exitCode=$code;events=$events;log=$log;hardTimeout=$timedOut;outputExceeded=$outputExceeded})
    $process.Dispose();return $code
}
$target='.\tests\assessment_runtime'
$selector='^TestAR[1-4]'
switch($Mode){
    'AR4'{$selector='^TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota$'}
    'RunContext'{$selector='^TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota$/^Run-context$'}
    'Diagnostic'{
        & node (Join-Path $PSScriptRoot 'prepare-A1.mjs') prepare $OutputName
        if($LASTEXITCODE -ne 0){throw 'Readonly diagnostic preparation failed'}
        $target=".\.cache\assessment-runtime-a1\$OutputName"
        $selector='^TestA1AR4ReadonlyDiagnostic$'
        if($DiagnosticCase -ne 'All'){$selector+='/^'+$DiagnosticCase+'$'}
    }
}
$code=Invoke-OwnedGo @('build','-mod=readonly','-o',$binary,'.\cmd\assessment-worker') 'command-build'
if($code -eq 0){$code=Invoke-OwnedGo @('test','-json','-tags=integration','-mod=readonly',"-count=$Count",'-timeout=240s',"-run=$selector",$target) 'tests'}
if($Mode -eq 'Diagnostic'){
    & node (Join-Path $PSScriptRoot 'prepare-A1.mjs') cleanup $OutputName
    if($LASTEXITCODE -ne 0){throw 'Selected diagnostic working copies not removed'}
}
Assert-Inputs
foreach($entry in $inputs){if((Witness $entry.path).sha256 -ne $entry.sha256){throw 'Run input drifted'}}
$inputPath=Join-Path $output "$OutputName.inputs.json"
[ordered]@{schemaVersion=1;run=$OutputName;files=$inputs;allStable=$true;guardExclusions=@('web\**','README.md')}|ConvertTo-Json -Depth 6|Set-Content -LiteralPath $inputPath -Encoding utf8NoBOM
[ordered]@{
    schemaVersion=1;mode=$Mode;exitCode=$code;goExecutable=$go;stages=@($stages);commandBinary=$binary
    author='independent-runtime-test-successor-20260921';repeatCount=$Count;diagnosticCase=$DiagnosticCase;diagnosticIsNotNormalAcceptance=($Mode -eq 'Diagnostic')
    realCommandBuildRequired=$true;actualCommand=(Witness ([IO.Path]::GetRelativePath($root,$binary)));privateRuntimeValuesCopied=$false
    boundary='Owned random PG schema, scoped local S3 only for legitimate intake, normal TLS/native synthetic model fixture; no provider account or live model'
    dependenciesInstalled=$false
    inputs=(Witness ([IO.Path]::GetRelativePath($root,$inputPath)));inputCount=$inputs.Count;allInputsStable=$true;heldRuntimePaths=7
}|ConvertTo-Json -Depth 7|Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
exit $code
