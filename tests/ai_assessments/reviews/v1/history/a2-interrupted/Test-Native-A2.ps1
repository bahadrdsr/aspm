param(
    [Parameter(Mandatory=$true)] [string] $RuntimeConfig,
    [Parameter(Mandatory=$true)] [ValidateSet('H2Calibration','H2Regression','Concurrency','OriginalEight','Combined')] [string] $Mode,
    [Parameter(Mandatory=$true)] [string] $OutputName
)
$ErrorActionPreference='Stop'
if($OutputName -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $OutputName.Length -gt 79){throw 'Use a fresh bounded label.'}
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
$go=Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if(-not(Test-Path -LiteralPath $go -PathType Leaf)){throw 'Pinned existing Go1.27.1 required; no installation authorized.'}
$output=Join-Path $root '.artifacts\ai-assessments-v1'
$work=Join-Path $root '.cache\ai-assessments-v1\work'
$copies=Join-Path $root ".cache\ai-assessments-a2\$OutputName"
New-Item -ItemType Directory -Path $output,$work -Force|Out-Null
foreach($ext in @('jsonl','log','receipt.json')){if(Test-Path (Join-Path $output "$OutputName.$ext")){throw 'Refusing to replace earlier evidence.'}}
try{$runtime=Get-Content -LiteralPath $RuntimeConfig -Raw|ConvertFrom-Json -AsHashtable}catch{throw 'Cannot read selected private runtime; values withheld.'}
$mapping=[ordered]@{ASPM_ASSESSMENT_DATABASE_URL='databaseUrl';ASPM_ASSESSMENT_S3_ENDPOINT='s3Endpoint';ASPM_ASSESSMENT_S3_ACCESS_KEY='s3AccessKey';ASPM_ASSESSMENT_S3_SECRET_KEY='s3SecretKey';ASPM_ASSESSMENT_S3_BUCKET='s3Bucket'}
foreach($e in $mapping.GetEnumerator()){if(-not($runtime[$e.Value] -is [string])-or[string]::IsNullOrWhiteSpace($runtime[$e.Value])){throw 'Missing owned fixture input; values withheld.'}}
try{
    $db=[Uri]$runtime.databaseUrl;$store=[Uri]$runtime.s3Endpoint
    if($db.Host -ne '127.0.0.1'-or$db.Port -lt 1-or$db.Scheme -notin @('postgres','postgresql')-or-not$db.UserInfo.Contains(':')-or
        $store.Host -ne '127.0.0.1'-or$store.Port -lt 1-or$store.Scheme -notin @('http','https')-or$store.UserInfo -ne ''-or$store.Query -ne ''-or$store.Fragment -ne ''){throw 'Invalid boundary'}
}catch{throw 'Only explicit authenticated owned loopback PG/S3 is allowed.'}
$redactions=@($runtime.databaseUrl,$runtime.s3AccessKey,$runtime.s3SecretKey,$db.UserInfo,[Uri]::UnescapeDataString($db.UserInfo.Split(':',2)[1]))
if($runtime.postgresPassword -is [string]){$redactions+=$runtime.postgresPassword}
foreach($value in @($redactions)){if($value){$redactions+=[Uri]::EscapeDataString($value);$j=ConvertTo-Json -InputObject $value -Compress;$redactions+=$j.Substring(1,$j.Length-2)}}
$redactions=@($redactions|Where-Object {$_}|Sort-Object -CaseSensitive -Unique|Sort-Object Length -Descending)
$sourceRoot=Join-Path $root 'tests\ai_assessments'
$target='.\tests\ai_assessments'
$copied=@()
switch($Mode){
    'H2Calibration'{
        if(Test-Path $copies){throw 'Working calibration label exists.'}
        New-Item -ItemType Directory -Path $copies -Force|Out-Null
        foreach($name in @('contract_test.go','fixtures_test.go','native_test.go','production_bindings_test.go','transport_replay_test.go')){
            $source=Join-Path $sourceRoot $name;$copy=Join-Path $copies $name
            Copy-Item -LiteralPath $source -Destination $copy
            $hash=(Get-FileHash $source -Algorithm SHA256).Hash.ToLowerInvariant()
            if($hash -ne (Get-FileHash $copy -Algorithm SHA256).Hash.ToLowerInvariant()){throw 'Calibration input copy drifted.'}
            $copied += [ordered]@{source=[IO.Path]::GetRelativePath($root,$source);copy=[IO.Path]::GetRelativePath($root,$copy);sha256=$hash}
        }
        $source=Join-Path $PSScriptRoot 'calibrate-http2-A2.go.txt';$copy=Join-Path $copies 'calibration_test.go'
        Copy-Item -LiteralPath $source -Destination $copy
        $copied += [ordered]@{source=[IO.Path]::GetRelativePath($root,$source);copy=[IO.Path]::GetRelativePath($root,$copy);sha256=(Get-FileHash $source -Algorithm SHA256).Hash.ToLowerInvariant()}
        $target=".\.cache\ai-assessments-a2\$OutputName";$selector='^TestA2PinnedHTTP2RefusedStreamCalibration$'
    }
    'H2Regression'{$selector='^TestAA9NativeHTTP2RefusedStreamCannotReplayAssessmentPOST$'}
    'Concurrency'{$selector='^TestAA10LocalNativeLifetimeRetainsSharedConcurrency$'}
    'OriginalEight'{$selector='^TestAA[1-8][^0-9]'}
    'Combined'{$selector='^TestAA'}
}
if($Mode -in @('OriginalEight','Combined')){
    & node (Join-Path $sourceRoot 'prepare-published-v7.mjs') $OutputName
    if($LASTEXITCODE -ne 0){throw 'Pinned published fixture build failed.'}
}
$arguments=@('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=240s',"-run=$selector",$target)
$start=[Diagnostics.ProcessStartInfo]::new();$start.FileName=$go;$start.WorkingDirectory=$root;$start.UseShellExecute=$false
$start.RedirectStandardOutput=$true;$start.RedirectStandardError=$true
foreach($a in $arguments){$start.ArgumentList.Add($a)}
foreach($name in @($start.Environment.Keys)){if($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$'){$start.Environment.Remove($name)|Out-Null}}
$start.Environment['GOTOOLCHAIN']='local';$start.Environment['GOENV']='off';$start.Environment['GOWORK']='off';$start.Environment['GOFLAGS']=''
$start.Environment['GOPROXY']='off';$start.Environment['GOSUMDB']='off';$start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules'
$start.Environment['GOCACHE']=Join-Path $root '.cache\build';$start.Environment['GOPATH']=Join-Path $root '.cache\ai-assessments-v1\gopath'
foreach($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')){$start.Environment[$name]=$work}
foreach($e in $mapping.GetEnumerator()){$start.Environment[$e.Key]=$runtime[$e.Value]}
$start.Environment['AWS_EC2_METADATA_DISABLED']='true';$start.Environment['ASPM_ASSESSMENT_A2_REVIEW']=$PSScriptRoot
$start.Environment['ASPM_ASSESSMENT_ARTIFACT_DIR']=$output;$start.Environment['ASPM_ASSESSMENT_TESTDATA']=Join-Path $sourceRoot 'testdata'
$start.Environment['ASPM_ASSESSMENT_V7_SEED_EXE']=Join-Path $root ".cache\ai-assessments-v1\published-v7\$OutputName\owned-v7-seed.exe"
$process=[Diagnostics.Process]::new();$process.StartInfo=$start;$began=[DateTimeOffset]::UtcNow
if(-not$process.Start()){throw 'Go did not start.'}
$stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync();$process.WaitForExit();$code=$process.ExitCode
$text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
foreach($value in $redactions){$text=$text.Replace($value,'[REDACTED]')}
[IO.File]::WriteAllText((Join-Path $output "$OutputName.jsonl"),$text,[Text.UTF8Encoding]::new($false))
$readable=foreach($line in ($text -split "`r?`n")){if($line.StartsWith('{')){$e=$line|ConvertFrom-Json -AsHashtable;if($e.Output){$e.Output.TrimEnd()}}elseif($line){$line}}
[IO.File]::WriteAllText((Join-Path $output "$OutputName.log"),($readable -join [Environment]::NewLine),[Text.UTF8Encoding]::new($false))
$process.Dispose()
if($Mode -eq 'H2Calibration'){foreach($f in $copied){Remove-Item -LiteralPath (Join-Path $root $f.copy)};Remove-Item -LiteralPath $copies}
[ordered]@{
    schemaVersion=1;mode=$Mode;run=$OutputName;exitCode=$code;arguments=$arguments;startedAt=$began.ToString('o');finishedAt=[DateTimeOffset]::UtcNow.ToString('o')
    calibrationCopies=$copied;workingCopiesRemoved=($Mode -ne 'H2Calibration'-or-not(Test-Path $copies));privateRuntimeValuesCopied=$false
    newDependenciesInstalled=$false;realLLMAccountsCalled=$false;originalControlsChanged=$false;calibrationIsNotProductAcceptance=($Mode -eq 'H2Calibration')
}|ConvertTo-Json -Depth 6|Set-Content (Join-Path $output "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable|Write-Output
exit $code
