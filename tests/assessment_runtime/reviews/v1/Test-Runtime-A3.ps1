param(
    [Parameter(Mandatory=$true)][string]$RuntimeConfig,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z0-9]+(?:-[a-z0-9]+)*$')][string]$OutputName
)
$ErrorActionPreference = 'Stop'
if ($OutputName.Length -gt 79) { throw 'Use a fresh bounded label.' }
$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
Set-Location $root
$baselinePath = Join-Path $PSScriptRoot 'BEFORE-A3.json'
if ((Get-FileHash $baselinePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne 'ae173015737a3b17784bc0ce56ccca408ee051c01668d64fae5f82ac1ba5d56c') {
    throw 'A3 starting witnesses changed.'
}
$baseline = Get-Content -Raw $baselinePath | ConvertFrom-Json
$output = '.artifacts\assessment-runtime-v1'
$work = '.cache\assessment-runtime-v1\work'
New-Item -ItemType Directory -Force -Path $output,$work | Out-Null
$prefix = Join-Path $output $OutputName
if ((Test-Path "$prefix.hold.json") -or (Test-Path "$prefix.receipt.json") -or (Test-Path "$prefix.inputs-before.json")) {
    throw 'Refusing to overwrite prior evidence.'
}
$expected = @{}
function Add-Input($entry) {
    $full = if ([IO.Path]::IsPathRooted($entry.path)) { $entry.path } else { Join-Path $root $entry.path.Replace('/', '\') }
    if ($expected.ContainsKey($full) -and $expected[$full].sha256 -ne $entry.sha256) { throw 'Conflicting pinned input.' }
    $expected[$full] = $entry
}
function Witness([string]$path) {
    [pscustomobject]@{ path=$path; bytes=(Get-Item $path).Length; sha256=(Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant() }
}
foreach ($set in $baseline.controls) {
    foreach ($entry in @($set.manifest,$set.signature)) {
        if ((Witness $entry.path).sha256 -ne $entry.sha256) { throw 'Fixed control identity changed.' }
        Add-Input $entry
    }
    $manifest = Get-Content -Raw $set.manifest.path | ConvertFrom-Json
    if ($manifest.files.Count -ne $set.count) { throw 'Fixed control count changed.' }
    foreach ($entry in $manifest.files) { Add-Input $entry }
}
foreach ($entry in @($baseline.heldRuntimeSource7) + @($baseline.oldRuntimeFiles) + @($baseline.oldReviewHistory) +
    @($baseline.toolchain) + @($baseline.fixedRuntimeConfig,$baseline.runtimeHandoff,$baseline.originalRunner,$baseline.contractBeforeTest)) {
    Add-Input $entry
}
$newInputs = @('tests\assessment_runtime\header_timeout_test.go',
    'tests\assessment_runtime\reviews\v1\Test-Runtime-A3.ps1',
    'tests\assessment_runtime\reviews\v1\BEFORE-A3.json',
    'tests\assessment_runtime\reviews\v1\CONTRACT-A3.txt')
foreach ($path in $newInputs) { Add-Input (Witness $path) }
$holds = [Collections.Generic.List[IO.FileStream]]::new()
$started = [DateTimeOffset]::UtcNow
$code = $null
$stable = $false
$compilerInputs = @()
try {
    foreach ($full in @($expected.Keys)) {
        $stream = [IO.File]::Open($full,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
        $holds.Add($stream)
        if ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant() -ne $expected[$full].sha256) {
            throw 'Frozen control/source changed before execution.'
        }
    }
    $go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName=$go; $start.WorkingDirectory=$root; $start.UseShellExecute=$false
    $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
    foreach ($name in @($start.Environment.Keys)) {
        if ($name -match '^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY|GODEBUG)$') {
            $start.Environment.Remove($name) | Out-Null
        }
    }
    foreach ($entry in @{GOTOOLCHAIN='local';GOENV='off';GOWORK='off';GOFLAGS='';GOPROXY='off';GOSUMDB='off'}.GetEnumerator()) {
        $start.Environment[$entry.Key]=$entry.Value
    }
    $start.Environment['GOMODCACHE']=Join-Path $root '.cache\modules'
    $start.Environment['GOCACHE']=Join-Path $root '.cache\build'
    $start.Environment['GOPATH']=Join-Path $root '.cache\assessment-runtime-v1\gopath'
    foreach ($name in @('GOTMPDIR','TEMP','TMP','TMPDIR')) { $start.Environment[$name]=Join-Path $root $work }
    $template = '{{ $d := .Dir }}{{ range .GoFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .CgoFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .CFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .HFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .SFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .SysoFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}{{ range .EmbedFiles }}{{ printf "%s|%s\n" $d . }}{{ end }}'
    $arguments = @('list','-deps','-test','-tags=integration','-mod=readonly','-f',$template,'.\tests\assessment_runtime','.\cmd\assessment-worker')
    foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new(); $process.StartInfo=$start
    if (-not $process.Start()) { throw 'Offline dependency enumeration did not start.' }
    $stdout=$process.StandardOutput.ReadToEndAsync(); $stderr=$process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(120000)) { $process.Kill($true); $process.WaitForExit(); throw 'Offline dependency enumeration exceeded its bound.' }
    $text=$stdout.GetAwaiter().GetResult(); $errors=$stderr.GetAwaiter().GetResult(); $listCode=$process.ExitCode
    $process.Dispose()
    if ($listCode -ne 0 -or $errors.Trim()) { throw 'Offline dependency enumeration failed; no runtime result claimed.' }
    $text | Set-Content "$prefix.compiler-paths.txt" -Encoding utf8NoBOM
    $compilerInputs = @($text -split "`r?`n" | Where-Object { $_ } | ForEach-Object {
        $parts=$_.Split('|',2)
        if ($parts.Count -ne 2) { throw 'Unexpected compiler path record.' }
        if ([IO.Path]::IsPathRooted($parts[1])) { $parts[1] } else { Join-Path $parts[0] $parts[1] }
    } | Sort-Object -Unique)
    foreach ($full in $compilerInputs) {
        if ($expected.ContainsKey($full)) { continue }
        $entry=Witness $full
        $stream=[IO.File]::Open($full,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
        $holds.Add($stream)
        if ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant() -ne $entry.sha256) {
            throw 'Compiler input changed while acquiring its hold.'
        }
        Add-Input $entry
    }
    @($expected.Values | Sort-Object path) | ConvertTo-Json -Depth 7 |
        Set-Content "$prefix.inputs-before.json" -Encoding utf8NoBOM
    Remove-Item Env:GODEBUG -ErrorAction SilentlyContinue
    & (Join-Path $root 'tests\assessment_runtime\Test-AssessmentRuntime.ps1') -RuntimeConfig $RuntimeConfig -Mode Contract -OutputName $OutputName
    $code=$LASTEXITCODE
    foreach ($stream in $holds) {
        $stream.Position=0
        if ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant() -ne $expected[$stream.Name].sha256) {
            throw 'Held input changed during original-runner execution.'
        }
    }
    $stable=$true
} finally {
    foreach ($stream in $holds) { $stream.Dispose() }
    [ordered]@{
        schemaVersion=1; label=$OutputName; startedAt=$started.ToString('o'); completedAt=[DateTimeOffset]::UtcNow.ToString('o')
        originalRunner=$baseline.originalRunner; mode='Contract'; exitCode=$code; allHeldInputsStable=$stable
        heldInputCount=$expected.Count; compilerInputCount=$compilerInputs.Count; heldRuntimeSourcePaths=7
        unchangedControls=@{runtime=100;backend=245;ui=838}; holdMode='FileShare.Read'
        sourceScope='Only actual assessment_runtime/assessment-worker dependency inputs plus fixed controls; unrelated future packaging-author files excluded.'
        privateRuntimeValuesCopied=$false; authorityCaptured=$false; originalRunnerEdited=$false; dependenciesInstalled=$false
    } | ConvertTo-Json -Depth 7 | Set-Content "$prefix.hold.json" -Encoding utf8NoBOM
}
if ($null -eq $code) { exit 2 }
exit $code
