param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [ValidateSet('Contract','HarnessCompile','FixtureCalibration','UpgradePrerequisite')] [string] $Mode='Contract',
    [string] $OutputName='configuration-red-01'
)

$ErrorActionPreference='Stop'
if($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$'){throw 'Use a plain unique artifact label.'}
$root=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go=Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
if(-not(Test-Path -LiteralPath $go -PathType Leaf)){throw 'Existing cached Go 1.27.1 is required; no installs/downloads authorized.'}
try{$runtime=Get-Content -LiteralPath $RuntimeConfig -Raw|ConvertFrom-Json -AsHashtable;$database=[string]$runtime.databaseUrl;$uri=[Uri]$database
    if($uri.Scheme -notin @('postgres','postgresql')-or$uri.Host -ne '127.0.0.1'-or$uri.Port -lt 1-or-not$uri.UserInfo.Contains(':')){throw 'Invalid PG boundary'}
}catch{throw 'Explicit private loopback PG runtime is required; contents withheld.'}
$redactions=@($database,$uri.UserInfo,[Uri]::UnescapeDataString($uri.UserInfo.Split(':',2)[1]))
foreach($value in @($redactions)){if($value){$redactions+=[Uri]::EscapeDataString($value);$json=ConvertTo-Json -InputObject $value -Compress;$redactions+=$json.Substring(1,$json.Length-2)}}
$redactions=@($redactions|Where-Object {$_}|Sort-Object -CaseSensitive -Unique|Sort-Object Length -Descending)
$out=Join-Path $root '.artifacts\ai-configuration-v1';$work=Join-Path $root '.cache\ai-configuration-v1\work';New-Item -ItemType Directory -Path $out,$work -Force|Out-Null
foreach($ext in @('jsonl','log','receipt.json')){if(Test-Path (Join-Path $out "$OutputName.$ext")){throw 'Refusing to overwrite prior evidence.'}}
$helpers=@(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go' -File|Where-Object {$_.Name -ne 'production_bindings_test.go'}|Sort-Object Name|Select-Object -ExpandProperty FullName)
switch($Mode){
    'HarnessCompile'{$arguments=@('test','-c','-tags=integration','-mod=readonly','-o',(Join-Path $out "$OutputName.helpers.test.exe"))+$helpers}
    'FixtureCalibration'{$arguments=@('test','-json','-tags=integration,ai_configuration_fixture','-mod=readonly','-count=1','-timeout=60s','-run=^TestAIConfigurationOwnedPGAndNetworkNoneFixture$')+$helpers}
    'UpgradePrerequisite'{$arguments=@('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=60s','-run=^TestAIConfigurationPersistenceConcurrentUpdatesAndPublishedV6Upgrade$/^published-v6-upgrade$')+$helpers}
    default{$arguments=@('test','-json','-tags=integration','-mod=readonly','-count=1','-timeout=180s','.\tests\ai_configuration')}
}
$start=[System.Diagnostics.ProcessStartInfo]::new();$start.FileName=$go;$start.WorkingDirectory=$root;$start.UseShellExecute=$false;$start.RedirectStandardOutput=$true;$start.RedirectStandardError=$true
foreach($argument in $arguments){$start.ArgumentList.Add($argument)}
foreach($name in @($start.Environment.Keys)){if($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$'){$start.Environment.Remove($name)|Out-Null}}
$start.Environment['GOTOOLCHAIN']='local';$start.Environment['GOENV']='off';$start.Environment['GOWORK']='off';$start.Environment['GOFLAGS']='';$start.Environment['GOPROXY']='off';$start.Environment['GOSUMDB']='off'
$start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules';$start.Environment['GOCACHE']=Join-Path $root '.cache\build';$start.Environment['GOPATH']=Join-Path $root '.cache\ai-configuration-v1\gopath'
foreach($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')){$start.Environment[$name]=$work}
$start.Environment['ASPM_AI_CONFIG_DATABASE_URL']=$database
$start.Environment['ASPM_AI_CONFIG_ARTIFACT_DIR']=$out
$start.Environment['ASPM_AI_CONFIG_FIXTURE_PATH']=Join-Path $PSScriptRoot 'testdata\published-24556-schema-v6.json'
$start.Environment['AWS_EC2_METADATA_DISABLED']='true'
$process=[System.Diagnostics.Process]::new();$process.StartInfo=$start;$began=[DateTimeOffset]::UtcNow
if(-not$process.Start()){throw 'Go process failed to start'}
$stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync();$process.WaitForExit();$code=$process.ExitCode;$text=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
foreach($value in $redactions){$text=$text.Replace($value,'[REDACTED]')}
$text|Set-Content (Join-Path $out "$OutputName.jsonl") -Encoding utf8NoBOM
$readable=foreach($line in ($text -split "`r?`n")){if($line.StartsWith('{')){$event=$line|ConvertFrom-Json -AsHashtable;if($event.Output){$event.Output.TrimEnd()}}elseif($line){$line}}
$readable|Set-Content (Join-Path $out "$OutputName.log") -Encoding utf8NoBOM
[ordered]@{schemaVersion=1;mode=$Mode;startedAt=$began.ToString('o');finishedAt=[DateTimeOffset]::UtcNow.ToString('o');exitCode=$code;goExecutable=$go;arguments=$arguments
    fullProductionResolverBindingIncluded=($Mode -eq 'Contract');helperOnlyIsNotFullAcceptance=($Mode -ne 'Contract')
    capabilities='Owned random PG schemas and real API sessions; owned HTTP tripwires with no provider requests. No S3, prompt, inference, source/scan or verification execution.'
    privateRuntimeValuesCopied=$false;providerAccountsOrKeysDiscovered=$false
}|ConvertTo-Json -Depth 5|Set-Content (Join-Path $out "$OutputName.receipt.json") -Encoding utf8NoBOM
$readable|Write-Output;$process.Dispose();exit $code
