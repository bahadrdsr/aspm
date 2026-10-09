param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [string] $Go = 'go'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$run = Join-Path $PSScriptRoot '.run'
$binary = Join-Path $run 'verification-worker.owned.exe'
$previous = [Environment]::GetEnvironmentVariable('ASPM_VERIFICATION_RUNTIME_BINARY', 'Process')
$exitCode = 1

New-Item -ItemType Directory -Path $run -Force | Out-Null
Push-Location $root
try {
    & $Go build -mod=readonly -trimpath -buildvcs=false -o $binary .\cmd\verification-worker
    $exitCode = $LASTEXITCODE
    if ($exitCode -eq 0) {
        $env:ASPM_VERIFICATION_RUNTIME_BINARY = $binary
        & (Join-Path $PSScriptRoot 'Test-Acceptance.ps1') `
            -RuntimeConfig $RuntimeConfig `
            -Run '^TestM13_VerificationCommandCrashRestartSettlesOnceAndPreservesTerminalState$' `
            -Go $Go `
            -ModuleCache (Join-Path $root '.cache\modules')
        $exitCode = $LASTEXITCODE
    }
}
finally {
    Pop-Location
    [Environment]::SetEnvironmentVariable('ASPM_VERIFICATION_RUNTIME_BINARY', $previous, 'Process')
    Remove-Item -LiteralPath $binary -Force -ErrorAction SilentlyContinue
}

exit $exitCode
