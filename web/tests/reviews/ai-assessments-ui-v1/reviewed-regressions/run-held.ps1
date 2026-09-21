param(
  [Parameter(Mandatory = $true)][ValidateSet("typecheck", "test", "list")][string]$Stage,
  [Parameter(Mandatory = $true)][ValidatePattern('^[a-z0-9]+(?:-[a-z0-9]+)*$')][string]$Label,
  [string[]]$Selectors = @(),
  [int]$Port = 18798
)
$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..\..")).Path
Set-Location $root
$run = Join-Path ".artifacts\ai-assessment-review-author" $Label
if (Test-Path $run) { throw "Choose a fresh evidence label: $Label" }
New-Item -ItemType Directory -Path $run | Out-Null
$before = Get-Content -Raw (Join-Path $PSScriptRoot "BEFORE.json") | ConvertFrom-Json
$fixture = "web\tests\ai-assessments-fixture.ts"
$holds = [System.Collections.Generic.List[System.IO.FileStream]]::new()
$inputs = [System.Collections.Generic.List[object]]::new()
$expected = @{}
function Add-Input($entry) {
  $path = $entry.path.Replace("/", "\")
  $full = if ([System.IO.Path]::IsPathRooted($path)) { $path } else { Join-Path $root $path }
  if ($expected.ContainsKey($full)) {
    if ($expected[$full].sha256 -ne $entry.sha256) { throw "Conflicting pinned input: $path" }
    return
  }
  $expected[$full] = $entry
}
foreach ($entry in $before.controls) { if ($entry.path -ne $fixture) { Add-Input $entry } }
foreach ($entry in @($before.productionCandidate) + @($before.allFrontendSources) + @($before.fixed) +
  @($before.publicManifest, $before.publicSignature, $before.coderHandoff, $before.archive)) { Add-Input $entry }
$closure = Get-Content -Raw ".cache\ai-assessments-ui-coder\execution-files.json" | ConvertFrom-Json
foreach ($entry in $closure) { Add-Input $entry }
$new = @(Get-Item $fixture) + @(Get-ChildItem "web\tests\ai-assessment-review-*") +
  @(Get-ChildItem "web\tests\harness\ai-assessment-review" -Recurse -File) +
  @(Get-ChildItem $PSScriptRoot -File | Where-Object { $_.Extension -in ".ps1", ".txt" -or $_.Name -eq "BEFORE.json" })
foreach ($file in $new) {
  $relative = [System.IO.Path]::GetRelativePath($root, $file.FullName)
  Add-Input ([pscustomobject]@{ path = $relative; bytes = $file.Length; sha256 = (Get-FileHash $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() })
}
$started = [DateTimeOffset]::UtcNow.ToString("o")
$exitCode = $null
$verification = "not-completed"
try {
  foreach ($full in ($expected.Keys | Sort-Object)) {
    $entry = $expected[$full]
    $stream = [System.IO.File]::Open($full, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    $holds.Add($stream)
    $hash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant()
    if ($hash -ne $entry.sha256) { throw "Pinned bytes changed before execution: $($entry.path)" }
    $inputs.Add([pscustomobject]@{ path = $entry.path; bytes = $stream.Length; sha256 = $hash })
  }
  $inputs | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $run "held-inputs.json") -Encoding utf8
  $env:ASPM_AI_ASSESS_UI_RUN = $Label
  $env:ASPM_WEB_TEST_PORT = [string]$Port
  $env:NODE_USE_SYSTEM_CA = "1"
  Set-Location (Join-Path $root "web")
  $arguments = if ($Stage -eq "typecheck") {
    @("node_modules\typescript\bin\tsc", "--noEmit", "--pretty", "false")
  } else {
    @("scripts\playwright.mjs", "test", "--config", "tests\reviews\ai-assessments-ui-v1\playwright.config.ts") + $Selectors
  }
  if ($Stage -eq "list") { $arguments += "--list" }
  & node @arguments 2>&1 | Tee-Object -FilePath (Join-Path $root (Join-Path $run "output.log"))
  $exitCode = $LASTEXITCODE
  foreach ($stream in $holds) {
    $stream.Position = 0
    $entry = $expected[$stream.Name]
    $hash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($stream)).ToLowerInvariant()
    if ($hash -ne $entry.sha256) { throw "Pinned bytes changed during execution: $($entry.path)" }
  }
  $verification = "all-held-bytes-match-before-and-after"
} finally {
  Set-Location $root
  foreach ($stream in $holds) { $stream.Dispose() }
  [pscustomobject]@{
    schemaVersion = 1; stage = $Stage; label = $Label; startedAt = $started; completedAt = [DateTimeOffset]::UtcNow.ToString("o")
    executable = (Get-Command node).Source; arguments = $arguments; cwd = (Join-Path $root "web")
    exitCode = $exitCode; sourceHold = $verification; shareMode = "FileShare.Read"; heldCount = $inputs.Count
    originalControls = 803; authorizedException = $fixture
    configSHA256 = "7a837a72b72cf8dfee4d967357e872326a287eb6578ebf6d083ba1c5666f34cd"
    settings = @{ caseTimeoutMs = 20000; expectTimeoutMs = 5000; workers = 1; retries = 0; combinedAPIRequests = 60 }
    environment = @{ ASPM_AI_ASSESS_UI_RUN = $Label; ASPM_WEB_TEST_PORT = $Port }
    backendRuntimeExcluded = $true; oracleRecapture = $false; signingAuthorityAccess = $false
  } | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $run "execution.json") -Encoding utf8
}
if ($null -eq $exitCode) { exit 2 }
exit $exitCode
