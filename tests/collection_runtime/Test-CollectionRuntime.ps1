param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [Parameter(Mandatory = $true)] [string] $StorageConfig,
    [ValidateSet('Contract','ServiceContract','CommandBuild','HarnessCompile','RightsPreflight')] [string] $Mode='Contract',
    [string] $OutputName='runtime-red-01'
)

$ErrorActionPreference='Stop'
if($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$'){throw 'Use a plain unique output label.'}
$root=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go=Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if(-not(Test-Path -LiteralPath $go -PathType Leaf)){throw 'Existing cached Go 1.27.1 is required; no installation authorized.'}
try{$runtime=Get-Content -LiteralPath $RuntimeConfig -Raw|ConvertFrom-Json -AsHashtable;$storage=Get-Content -LiteralPath $StorageConfig -Raw|ConvertFrom-Json -AsHashtable}
catch{throw 'Cannot read explicitly selected private runtime/storage metadata; contents withheld.'}
$values=[ordered]@{
    ASPM_COLLECTION_RUNTIME_DATABASE_URL=$runtime.databaseUrl
    ASPM_COLLECTION_RUNTIME_RAW_ENDPOINT=$runtime.s3Endpoint
    ASPM_COLLECTION_RUNTIME_RAW_BUCKET=$runtime.s3Bucket
    ASPM_COLLECTION_RUNTIME_RAW_ACCESS_KEY=$runtime.s3AccessKey
    ASPM_COLLECTION_RUNTIME_RAW_SECRET_KEY=$runtime.s3SecretKey
    ASPM_COLLECTION_RUNTIME_EVIDENCE_ENDPOINT=$storage.endpoint
    ASPM_COLLECTION_RUNTIME_EVIDENCE_BUCKET=$storage.bucket
    ASPM_COLLECTION_RUNTIME_EVIDENCE_REGION=$storage.region
    ASPM_COLLECTION_RUNTIME_READER_ACCESS_KEY=$storage.roles.ai.accessKey
    ASPM_COLLECTION_RUNTIME_READER_SECRET_KEY=$storage.roles.ai.secretKey
    ASPM_COLLECTION_RUNTIME_PUBLISHER_ACCESS_KEY=$storage.roles.core.accessKey
    ASPM_COLLECTION_RUNTIME_PUBLISHER_SECRET_KEY=$storage.roles.core.secretKey
    ASPM_COLLECTION_RUNTIME_ADMIN_ACCESS_KEY=$storage.roles.admin.accessKey
    ASPM_COLLECTION_RUNTIME_ADMIN_SECRET_KEY=$storage.roles.admin.secretKey
}
foreach($name in $values.Keys){if(-not($values[$name] -is [string])-or[string]::IsNullOrWhiteSpace($values[$name])){throw "Required private fixture field $name missing; no fallback."}}
try{
    $db=[Uri]$runtime.databaseUrl;$raw=[Uri]$runtime.s3Endpoint;$selected=[Uri]$storage.endpoint
    if($db.Scheme -notin @('postgres','postgresql')-or$db.Host -ne '127.0.0.1'-or-not$db.UserInfo.Contains(':')-or
       $raw.Host -ne '127.0.0.1'-or$selected.Host -ne '127.0.0.1'-or$selected.Port -ne 18335-or$storage.bucket -ne 'aspm-isolation'){
        throw 'Invalid local boundary'
    }
}catch{throw 'Only selected authenticated loopback PG/raw S3 and the existing local scoped-policy store are permitted.'}
$redactions=@($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$db.UserInfo,[Uri]::UnescapeDataString($db.UserInfo.Split(':',2)[1]))
foreach($role in @('ai','core','admin')){$redactions+=$storage.roles[$role].accessKey;$redactions+=$storage.roles[$role].secretKey}
foreach($value in @($redactions)){if($value){$redactions+=[Uri]::EscapeDataString($value);$j=ConvertTo-Json -InputObject $value -Compress;$redactions+=$j.Substring(1,$j.Length-2)}}
$redactions=@($redactions|Where-Object {$_}|Sort-Object -CaseSensitive -Unique|Sort-Object Length -Descending)
$output=Join-Path $root '.artifacts\collection-runtime-v1';$work=Join-Path $root '.cache\collection-runtime-v1\work'
New-Item -ItemType Directory -Path $output,$work -Force|Out-Null
if(Test-Path (Join-Path $output "$OutputName.receipt.json")){throw 'Refusing to overwrite prior evidence.'}
$binary=Join-Path $output "$OutputName.collection-worker.test-owned.exe"
$stages=[System.Collections.Generic.List[object]]::new()
function Invoke-OwnedGo([string[]]$Arguments,[string]$Stage){
    $events=Join-Path $output "$OutputName-$Stage.jsonl";$log=Join-Path $output "$OutputName-$Stage.log"
    if((Test-Path $events)-or(Test-Path $log)){throw 'Prior stage evidence exists.'}
    $start=[System.Diagnostics.ProcessStartInfo]::new();$start.FileName=$go;$start.WorkingDirectory=$root
    $start.UseShellExecute=$false;$start.RedirectStandardOutput=$true;$start.RedirectStandardError=$true
    foreach($argument in $Arguments){$start.ArgumentList.Add($argument)}
    foreach($name in @($start.Environment.Keys)){if($name -match '^(ASPM_|AWS_|GH_|GITHUB_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$'){$start.Environment.Remove($name)|Out-Null}}
    $start.Environment['GOTOOLCHAIN']='local';$start.Environment['GOWORK']='off';$start.Environment['GOENV']='off';$start.Environment['GOFLAGS']=''
    $start.Environment['GOPROXY']='off';$start.Environment['GOSUMDB']='off';$start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules'
    $start.Environment['GOCACHE']=Join-Path $root '.cache\build';$start.Environment['GOPATH']=Join-Path $root '.cache\collection-runtime-v1\gopath'
    foreach($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')){$start.Environment[$name]=$work}
    $start.Environment['AWS_EC2_METADATA_DISABLED']='true'
    $start.Environment['ASPM_COLLECTION_RUNTIME_ARTIFACT_DIR']=$output;$start.Environment['ASPM_COLLECTION_RUNTIME_BINARY']=$binary
    foreach($name in $values.Keys){$start.Environment[$name]=$values[$name]}
    $process=[System.Diagnostics.Process]::new();$process.StartInfo=$start;$began=[DateTimeOffset]::UtcNow
    if(-not$process.Start()){throw 'Go process did not start.'}
    $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync();$process.WaitForExit();$exit=$process.ExitCode
    $text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult();foreach($value in $redactions){$text=$text.Replace($value,'[REDACTED]')}
    $text|Set-Content -LiteralPath $events -Encoding utf8NoBOM
    $readable=foreach($line in ($text -split "`r?`n")){if($line.StartsWith('{')){$event=$line|ConvertFrom-Json -AsHashtable;if($event.Output){$event.Output.TrimEnd()}}elseif($line){$line}}
    $readable|Set-Content -LiteralPath $log -Encoding utf8NoBOM;$readable|Write-Host
    $stages.Add([ordered]@{stage=$Stage;startedAt=$began.ToString('o');finishedAt=[DateTimeOffset]::UtcNow.ToString('o');arguments=$Arguments;exitCode=$exit;events=$events;log=$log})
    $process.Dispose();return $exit
}
$code=0
$helperFiles=@(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File|Where-Object {$_.Name -ne 'production_bindings_test.go'}|Sort-Object Name|Select-Object -ExpandProperty FullName)
if($Mode -eq 'HarnessCompile'){$code=Invoke-OwnedGo (@('test','-c','-tags=integration','-mod=readonly','-o',(Join-Path $output "$OutputName.helpers.test.exe"))+$helperFiles) 'helper-compile'}
elseif($Mode -eq 'RightsPreflight'){$code=Invoke-OwnedGo (@('test','-json','-tags=integration,collection_runtime_rights','-mod=readonly','-count=1','-timeout=60s','-run=^TestCollectionRuntimeOwnedStorageRightsCalibration$')+$helperFiles) 'rights-preflight'}
else{
    if($Mode -in @('Contract','CommandBuild')){$code=Invoke-OwnedGo @('build','-mod=readonly','-o',$binary,'.\cmd\collection-worker') 'command-build'}
    if($code -eq 0 -and $Mode -in @('Contract','ServiceContract')){$code=Invoke-OwnedGo @('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=180s','.\tests\collection_runtime') 'service-contract'}
}
[ordered]@{
    schemaVersion=1;mode=$Mode;exitCode=$code;goExecutable=$go;stages=@($stages)
    helperOrRightsProofIsNotRuntimeAcceptance=($Mode -in @('HarnessCompile','RightsPreflight'))
    explicitStorageIdentitySelection='Existing local ai key for core RO; existing local core key for publisher; admin key only for owned fixture cleanup'
    policyNotChanged=$true;valuesWithheld=$true;liveVendorActivity=$false;commandBinary=$binary
}|ConvertTo-Json -Depth 6|Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
exit $code
