param([Parameter(Mandatory = $true)] [string] $Helm)

$ErrorActionPreference = 'Stop'
if (-not [System.IO.Path]::IsPathFullyQualified($Helm) -or -not (Test-Path -LiteralPath $Helm -PathType Leaf)) {
    throw 'An explicit existing Helm executable path is required; rendering is never simulated.'
}
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$names = @('GOWORK','GOTOOLCHAIN','GOPATH','GOMODCACHE','GOCACHE','GOTMPDIR','TEMP','TMP','ASPM_TEST_HELM','ASPM_TEST_RENDER_DIR')
$previous = @{}
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$exitCode = 1
Push-Location $PSScriptRoot
try {
    foreach ($directory in @('.run', '.run\work', '.run\renders')) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
    $env:GOWORK = 'off'; $env:GOTOOLCHAIN = 'auto'
    $env:GOPATH = Join-Path $root '.cache\gopath'; $env:GOMODCACHE = Join-Path $root '.cache\modules'
    $env:GOCACHE = Join-Path $root '.cache\build'; $env:GOTMPDIR = Join-Path $PSScriptRoot '.run\work'
    $env:TEMP = $env:GOTMPDIR; $env:TMP = $env:GOTMPDIR
    $env:ASPM_TEST_HELM = $Helm
    $env:ASPM_TEST_RENDER_DIR = Join-Path $PSScriptRoot '.run\renders'
    $lines = @(go test -json -mod=readonly -count=1 -timeout=120s . 2>&1)
    $exitCode = $LASTEXITCODE
    $lines | Set-Content -LiteralPath '.run\last-run.jsonl' -Encoding utf8
    foreach ($line in $lines) {
        if ($line.ToString().StartsWith('{')) {
            $event = $line.ToString() | ConvertFrom-Json -AsHashtable
            if ($event.Output) { Write-Output $event.Output.TrimEnd() }
        } else { Write-Output $line }
    }
}
finally {
    Pop-Location
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
}
exit $exitCode
