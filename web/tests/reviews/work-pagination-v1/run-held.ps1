param(
    [Parameter(Mandatory=$true)][ValidateSet('typecheck','focused','compatibility')][string]$Stage,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z0-9]+(?:-[a-z0-9]+)*$')][string]$Label
)
$ErrorActionPreference='Stop'
$root=(Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..')).Path
Set-Location $root
$baselinePath=Join-Path $PSScriptRoot 'BEFORE.json'
if ((Get-FileHash $baselinePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne '92e0c19400a5d2f135e2c23da4c594ba0f40ed68ab245cef83a952af8a58bbb4') {
    throw 'Published frontend starting witnesses changed.'
}
$before=Get-Content -Raw $baselinePath | ConvertFrom-Json
$run=Join-Path '.artifacts\work-pagination-v1' $Label
if (Test-Path $run) { throw 'Use a fresh run label.' }
New-Item -ItemType Directory -Path $run | Out-Null
function Witness([string]$path) {
    [pscustomobject]@{path=$path;bytes=(Get-Item $path).Length;sha256=(Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant()}
}
$expected=@{}
function Pin($entry) {
    $full=if ([IO.Path]::IsPathRooted($entry.path)) {$entry.path} else {Join-Path $root $entry.path.Replace('/','\')}
    if ($expected.ContainsKey($full) -and $expected[$full].sha256 -ne $entry.sha256) { throw 'Conflicting fixed input.' }
    $expected[$full]=$entry
}
foreach ($entry in @($before.trackedFrontend)+@($before.historicalManifest,$before.historicalSignature,$before.executionClosure)) { Pin $entry }
if ((Witness $before.executionClosure.path).sha256 -ne $before.executionClosure.sha256) { throw 'Cached execution closure changed.' }
foreach ($entry in (Get-Content -Raw $before.executionClosure.path | ConvertFrom-Json)) { Pin $entry }
$new=@(Get-ChildItem 'web\tests\work-pagination-*' -File)+@(Get-ChildItem $PSScriptRoot -File |
    Where-Object { $_.Extension -in '.ts','.ps1','.txt' -or $_.Name -eq 'BEFORE.json' })
foreach ($file in $new) { Pin (Witness ([IO.Path]::GetRelativePath($root,$file.FullName))) }
$holds=[Collections.Generic.List[IO.FileStream]]::new()
$start=[DateTimeOffset]::UtcNow
$code=$null
$stable=$false
try {
    foreach ($full in ($expected.Keys | Sort-Object)) {
        $stream=[IO.File]::Open($full,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
        $holds.Add($stream)
        if ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant() -ne $expected[$full].sha256) {
            throw 'Pinned frontend/shared dependency changed before execution.'
        }
    }
    @($expected.Values | Sort-Object path) | ConvertTo-Json -Depth 8 |
        Set-Content (Join-Path $run 'held-inputs.json') -Encoding utf8NoBOM
    $env:ASPM_WEB_TEST_PORT='18824'; $env:ASPM_WORK_PAGINATION_RUN=$Label; $env:NODE_USE_SYSTEM_CA='1'
    Set-Location (Join-Path $root 'web')
    $arguments=if ($Stage -eq 'typecheck') {
        @('node_modules\typescript\bin\tsc','--noEmit','--pretty','false')
    } else {
        @('scripts\playwright.mjs','test','--config','tests\reviews\work-pagination-v1\playwright.config.ts')
    }
    if ($Stage -eq 'focused') { $arguments += 'work-pagination-ui.spec.ts' }
    if ($Stage -eq 'compatibility') { $arguments += @('--grep-invert','WP[1-6] ') }
    & node @arguments 2>&1 | Tee-Object -FilePath (Join-Path $root (Join-Path $run 'output.log'))
    $code=$LASTEXITCODE
    foreach ($stream in $holds) {
        $stream.Position=0
        if ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant() -ne $expected[$stream.Name].sha256) {
            throw 'Pinned frontend/shared dependency changed during execution.'
        }
    }
    $stable=$true
} finally {
    Set-Location $root
    foreach ($stream in $holds) { $stream.Dispose() }
    [ordered]@{
        schemaVersion=1;stage=$Stage;label=$Label;startedAt=$start.ToString('o');completedAt=[DateTimeOffset]::UtcNow.ToString('o')
        executable=(Get-Command node).Source;arguments=$arguments;cwd=(Join-Path $root 'web');exitCode=$code
        heldCount=$expected.Count;sourceHold='FileShare.Read';allHeldInputsStable=$stable;publishedHEAD=$before.publishedHEAD
        settings=@{caseTimeoutMs=20000;expectTimeoutMs=5000;workers=1;retries=0;combinedAPIRequests=60;port=18824}
        mutableBackendExcluded=$true;oldOracleRecapture=$false;authorityAccess=$false
    } | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $run 'execution.json') -Encoding utf8NoBOM
}
if ($null -eq $code) { exit 2 }
exit $code
