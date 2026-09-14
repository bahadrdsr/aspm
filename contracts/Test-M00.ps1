param([string] $Run = '.')

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$names = @('GOTOOLCHAIN', 'GOWORK', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR', 'TEMP', 'TMP')
$previous = @{}
foreach ($name in $names) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$exitCode = 1
Push-Location $root
try {
    $cache = Join-Path $root '.cache'
    foreach ($directory in @('gopath', 'modules', 'build', 'work')) {
        New-Item -ItemType Directory -Path (Join-Path $cache $directory) -Force | Out-Null
    }
    $env:GOTOOLCHAIN = 'auto'
    $env:GOWORK = 'off'
    $env:GOPATH = Join-Path $cache 'gopath'
    $env:GOMODCACHE = Join-Path $cache 'modules'
    $env:GOCACHE = Join-Path $cache 'build'
    $env:GOTMPDIR = Join-Path $cache 'work'
    $env:TEMP = $env:GOTMPDIR
    $env:TMP = $env:GOTMPDIR
    & go test -count=1 -run $Run .\contracts
    $exitCode = $LASTEXITCODE
}
finally {
    Pop-Location
    foreach ($name in $names) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
}
exit $exitCode
