param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [string] $PgDump,
    [string] $PgRestore,
    [string] $Psql,
    [string] $Go = 'go',
    [string] $ModuleCache
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$runtimePath = (Resolve-Path -LiteralPath $RuntimeConfig).Path
$run = Join-Path $PSScriptRoot '.run\m13-backup-restore'
$work = Join-Path $run 'work'
$binary = Join-Path $run 'aspmctl.owned.exe'
$names = @(
    'ASPM_RECOVERY_BINARY',
    'ASPM_RECOVERY_PG_DUMP',
    'ASPM_RECOVERY_PG_RESTORE',
    'ASPM_RECOVERY_PSQL',
    'ASPM_RECOVERY_ACCEPTANCE_ROOT'
)
$previous = @{}
foreach ($name in $names) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$exitCode = 1

try {
    try {
        $runtime = Get-Content -LiteralPath $runtimePath -Raw | ConvertFrom-Json -AsHashtable
    }
    catch {
        throw 'BLOCKED: cannot read the authorized runtime configuration; contents withheld.'
    }
    if (-not $PgDump) { $PgDump = $runtime['pgDump'] }
    if (-not $PgRestore) { $PgRestore = $runtime['pgRestore'] }
    if (-not $Psql) { $Psql = $runtime['psql'] }
    foreach ($entry in @(
        @{ Name = 'pgDump'; Value = $PgDump },
        @{ Name = 'pgRestore'; Value = $PgRestore },
        @{ Name = 'psql'; Value = $Psql }
    )) {
        if (-not ($entry.Value -is [string]) -or [string]::IsNullOrWhiteSpace($entry.Value)) {
            throw "BLOCKED: explicit PostgreSQL 18.6 tool path $($entry.Name) is missing."
        }
        $entry.Value = (Resolve-Path -LiteralPath $entry.Value).Path
        if (-not (Test-Path -LiteralPath $entry.Value -PathType Leaf)) {
            throw "BLOCKED: explicit PostgreSQL 18.6 tool $($entry.Name) is unavailable."
        }
        switch ($entry.Name) {
            'pgDump' { $PgDump = $entry.Value }
            'pgRestore' { $PgRestore = $entry.Value }
            'psql' { $Psql = $entry.Value }
        }
    }

    New-Item -ItemType Directory -Path $work -Force | Out-Null
    Push-Location $root
    try {
        & $Go build -mod=readonly -trimpath -buildvcs=false -o $binary .\cmd\aspmctl
        $exitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    if ($exitCode -ne 0) {
        exit $exitCode
    }

    $env:ASPM_RECOVERY_BINARY = $binary
    $env:ASPM_RECOVERY_PG_DUMP = $PgDump
    $env:ASPM_RECOVERY_PG_RESTORE = $PgRestore
    $env:ASPM_RECOVERY_PSQL = $Psql
    $env:ASPM_RECOVERY_ACCEPTANCE_ROOT = $work
    & (Join-Path $PSScriptRoot 'Test-Acceptance.ps1') `
        -RuntimeConfig $runtimePath `
        -Run '^TestM13RecoveryQuiescedV27BackupRestoreAndPostRestoreWorkflow$' `
        -Go $Go `
        -ModuleCache $ModuleCache
    $exitCode = $LASTEXITCODE
}
finally {
    foreach ($name in $names) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
    Remove-Item -LiteralPath $run -Recurse -Force -ErrorAction SilentlyContinue
}

exit $exitCode
