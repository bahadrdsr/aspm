param(
    [Parameter(Mandatory=$true)] [string] $RuntimeConfig,
    [ValidateSet('Contract','ServiceContract','CommandBuild','HarnessCompile','FixtureCalibration','EnvironmentProbe')] [string] $Mode='Contract',
    [Parameter(Mandatory=$true)] [string] $OutputName
)
$ErrorActionPreference='Stop'
if($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79){throw 'Use a fresh bounded label.'}
$root=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go=Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if(-not(Test-Path -LiteralPath $go -PathType Leaf)){throw 'Cached Go1.27.1 required; no installation authorized.'}
$output=Join-Path $root '.artifacts\assessment-runtime-v1'
$work=Join-Path $root '.cache\assessment-runtime-v1\work'
$binary=Join-Path $output "$OutputName.assessment-worker.owned.exe"
New-Item -ItemType Directory -Path $output,$work -Force|Out-Null
if(Test-Path (Join-Path $output "$OutputName.receipt.json")){throw 'Refusing to overwrite prior evidence.'}
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
    if($db.Scheme -notin @('postgres','postgresql')-or$db.Host -ne '127.0.0.1'-or$db.Port -lt 1-or-not$db.UserInfo.Contains(':')-or
        $store.Scheme -notin @('http','https')-or$store.Host -ne '127.0.0.1'-or$store.Port -lt 1-or$store.UserInfo -ne ''-or$store.Query -ne ''-or$store.Fragment -ne ''){throw 'Invalid owned boundary'}
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
        if($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$'){$start.Environment.Remove($name)|Out-Null}
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
    $process=[Diagnostics.Process]::new();$process.StartInfo=$start;$began=[DateTimeOffset]::UtcNow
    if(-not$process.Start()){throw 'Go process did not start.'}
    $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync()
    $process.WaitForExit();$code=$process.ExitCode
    $text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
    foreach($value in $redactions){$text=$text.Replace($value,'[REDACTED]')}
    $text|Set-Content -LiteralPath $events -Encoding utf8NoBOM
    $readable=foreach($line in ($text -split "`r?`n")){
        if($line.StartsWith('{')){$event=$line|ConvertFrom-Json -AsHashtable;if($event.Output){$event.Output.TrimEnd()}}
        elseif($line){$line}
    }
    [IO.File]::WriteAllText($log,($readable -join [Environment]::NewLine),[Text.UTF8Encoding]::new($false))
    $readable|Write-Host
    $stages.Add([ordered]@{stage=$Stage;arguments=$Arguments;startedAt=$began.ToString('o');finishedAt=[DateTimeOffset]::UtcNow.ToString('o');exitCode=$code;events=$events;log=$log})
    $process.Dispose();return $code
}
$helpers=@(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File|Where-Object {$_.Name -ne 'production_bindings_test.go'}|Sort-Object Name|Select-Object -ExpandProperty FullName)
$code=0
switch($Mode){
    'HarnessCompile'{$code=Invoke-OwnedGo (@('test','-c','-tags=integration','-mod=readonly','-o',(Join-Path $output "$OutputName.helpers.test.exe"))+$helpers) 'helper-compile'}
    'FixtureCalibration'{$code=Invoke-OwnedGo (@('test','-json','-tags=integration,assessment_runtime_fixture','-mod=readonly','-count=1','-timeout=120s','-run=^TestAssessmentRuntimeOwnedServiceIntakeAndNativeFixtureCalibration$')+$helpers) 'fixture-calibration'}
    'EnvironmentProbe'{$code=Invoke-OwnedGo (@('test','-json','-tags=integration,assessment_runtime_probe','-mod=readonly','-count=1','-timeout=120s','-run=^TestAssessmentRuntimeExistingEnvironmentRoleProbe$')+$helpers) 'environment-probe'}
    default {
        if($Mode -in @('Contract','CommandBuild')){$code=Invoke-OwnedGo @('build','-mod=readonly','-o',$binary,'.\cmd\assessment-worker') 'command-build'}
        if($code -eq 0 -and $Mode -in @('Contract','ServiceContract')){$code=Invoke-OwnedGo @('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=240s','.\tests\assessment_runtime') 'service-contract'}
    }
}
[ordered]@{
    schemaVersion=1;mode=$Mode;exitCode=$code;goExecutable=$go;stages=@($stages);commandBinary=$binary
    helperOrProbeIsNotRuntimeAcceptance=($Mode -in @('HarnessCompile','FixtureCalibration','EnvironmentProbe'))
    realCommandBuildRequired=($Mode -in @('Contract','CommandBuild'));privateRuntimeValuesCopied=$false
    boundary='Owned random PG schema, scoped local S3 only for legitimate intake, normal TLS/native synthetic model fixture; no provider account or live model'
    dependenciesInstalled=$false
}|ConvertTo-Json -Depth 7|Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
exit $code
