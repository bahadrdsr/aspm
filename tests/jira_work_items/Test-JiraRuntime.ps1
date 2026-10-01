param(
    [Parameter(Mandatory = $true)] [string] $RuntimeConfig,
    [Parameter(Mandatory = $true)] [string] $OutputName,
    [ValidateSet('Compile', 'MainBuild', 'Contract')] [string] $Mode = 'Contract'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not [IO.Path]::IsPathFullyQualified($RuntimeConfig)) { throw 'RuntimeConfig must be an absolute private input path.' }
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$') { throw 'A unique plain output label is required.' }
$output = Join-Path $root ".artifacts\jira-runtime\$OutputName"
$cache = Join-Path $root ".cache\jira-runtime\$OutputName"
if ((Test-Path -LiteralPath $output) -or (Test-Path -LiteralPath $cache)) { throw 'Refusing to overwrite prior evidence or work.' }
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
$modules = Join-Path $root '.cache\modules'
if (-not (Test-Path -LiteralPath $go -PathType Leaf) -or -not (Test-Path -LiteralPath $modules -PathType Container)) {
    throw 'BLOCKED: cached Go1.27.1/modules required; no download or install is authorized.'
}
$null = New-Item -ItemType Directory -Path $output
foreach ($part in @('work', 'gopath')) { $null = New-Item -ItemType Directory -Path (Join-Path $cache $part) -Force }
$binary = Join-Path $output 'delivery-worker.exe'
$compiled = Join-Path $output 'jira-runtime.test.exe'
$receiptPath = Join-Path $output 'receipt.json'
$phases = @(
    [ordered]@{ name = 'compile'; status = 'NOT_EXECUTED'; exitCode = $null },
    [ordered]@{ name = 'mainbuild'; status = 'NOT_EXECUTED'; exitCode = $null },
    [ordered]@{ name = 'actualrun'; status = 'NOT_EXECUTED'; exitCode = $null }
)
$receipt = [ordered]@{
    schemaVersion = 1; mode = $Mode; startedAt = [DateTimeOffset]::UtcNow.ToString('o')
    baselineCommit = '88aa7f4d702486c9895fdfcffdcad6d09a28dc8c'
    phases = $phases; exitCode = $null; runtimeConfig = $RuntimeConfig
    fixtureBoundary = 'explicit authenticated PG15432/S318333, read-only runtime input; no readiness repair'
    selector = '^TestJiraRuntime'; buildTags = @('integration', 'jira_runtime')
    commandBinary = $binary; commandSHA256 = $null; testBinarySHA256 = $null
    vendorAccessAuthorized = $false; uiExecutionAuthorized = $false
    sourceInputs = (Join-Path $output 'source-inputs.json')
    sourceFinal = (Join-Path $output 'source-final.json')
    sourceUnchangedDuringInvocation = $null; orphanCleanup = @()
}
$secrets = @()

function Protect-Text([string] $Text) {
    foreach ($secret in $script:secrets) { $Text = $Text.Replace($secret, '[REDACTED]') }
    return $Text
}

function Save-Receipt {
    $receipt | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $receiptPath -Encoding utf8NoBOM
}

function Get-SourceInputs {
    $files = @(
        Get-ChildItem -LiteralPath (Join-Path $root 'internal'), (Join-Path $root 'cmd') -Filter '*.go' -Recurse -File
        Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.go' -File
        Get-Item -LiteralPath (Join-Path $root 'go.mod'), (Join-Path $root 'go.sum'),
            (Join-Path $PSScriptRoot 'Test-JiraRuntime.ps1'), (Join-Path $PSScriptRoot 'runtime\CONTRACT.txt'),
            (Join-Path $PSScriptRoot 'runtime\HANDOFF.json')
    )
    return @($files | Sort-Object FullName | ForEach-Object {
        [ordered]@{
            path = [IO.Path]::GetRelativePath($root, $_.FullName)
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    })
}

function Stop-OwnedMainProcesses {
    $records = [Collections.Generic.List[object]]::new()
    foreach ($file in @(Get-ChildItem -LiteralPath $output -Filter '*-process-*.json' -File)) {
        $record = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json -AsHashtable
        $ownedPID = [int]$record.pid
        $process = Get-Process -Id $ownedPID -ErrorAction SilentlyContinue
        $result = [ordered]@{ pid = $ownedPID; status = 'ALREADY_EXITED'; receipt = $file.Name }
        if ($null -ne $process) {
            $earliest = [DateTimeOffset]::FromUnixTimeMilliseconds([long]$record.startedNotBeforeUnixMs).AddSeconds(-1)
            $latest = [DateTimeOffset]::FromUnixTimeMilliseconds([long]$record.startedNotAfterUnixMs).AddSeconds(1)
            $started = [DateTimeOffset]$process.StartTime.ToUniversalTime()
            if ($process.Path -ine $binary -or $record.binaryPath -ine $binary -or
                $record.binarySHA256 -ne $receipt.commandSHA256 -or $started -lt $earliest -or $started -gt $latest) {
                $result.status = 'REFUSED_IDENTITY_MISMATCH'
            } else {
                Stop-Process -Id $ownedPID -Force -ErrorAction SilentlyContinue
                $result.status = if ($process.WaitForExit(5000)) { 'OWNED_PID_STOPPED' } else { 'EXIT_TIMEOUT' }
            }
            $process.Dispose()
        }
        $records.Add($result)
    }
    $receipt.orphanCleanup = @($records.ToArray())
}

function Invoke-Phase([string] $Name, [string[]] $Arguments, [int] $Seconds, [string] $Executable = $go) {
    $phase = $phases | Where-Object { $_.name -eq $Name } | Select-Object -First 1
    $phase.status = 'RUNNING'
    $phase.startedAt = [DateTimeOffset]::UtcNow.ToString('o')
    $phase.arguments = $Arguments
    $phase.executable = $Executable
    $phase.events = Join-Path $output "$Name.capture.txt"
    $phase.log = Join-Path $output "$Name.log"
    Save-Receipt
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Executable
    $start.WorkingDirectory = if ($Name -eq 'actualrun') { $PSScriptRoot } else { $root }
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $start.Environment.Clear()
    foreach ($osName in @('SystemRoot', 'WINDIR')) {
        $value = [Environment]::GetEnvironmentVariable($osName)
        if ($value) { $start.Environment[$osName] = $value }
    }
    $settings = @{
        GOTOOLCHAIN = 'local'; GOENV = 'off'; GOWORK = 'off'; GOFLAGS = ''; GOPROXY = 'off'; GOSUMDB = 'off'
        GOOS = 'windows'; GOARCH = 'amd64'; CGO_ENABLED = '0'; GOMODCACHE = $modules
        GOCACHE = (Join-Path $root '.cache\build'); GOPATH = (Join-Path $cache 'gopath')
        GOTMPDIR = (Join-Path $cache 'work'); TEMP = (Join-Path $cache 'work')
        TMP = (Join-Path $cache 'work'); TMPDIR = (Join-Path $cache 'work')
        ASPM_JIRA_RUNTIME = $RuntimeConfig; ASPM_JIRA_COMMAND_BINARY = $binary
        ASPM_JIRA_COMMAND_ARTIFACT_DIR = $output; ASPM_JIRA_COMMAND_SHA256 = [string]$receipt.commandSHA256
    }
    foreach ($entry in $settings.GetEnumerator()) { $start.Environment[$entry.Key] = $entry.Value }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $started = $false
    try {
        if (-not $process.Start()) { throw 'Selected owned phase process did not start.' }
        $started = $true
        $phase.pid = $process.Id
        Save-Receipt
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($Seconds * 1000)) {
            Stop-OwnedMainProcesses
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            $null = $process.WaitForExit(5000)
            $phase.status = 'TIMEOUT'
            $code = 124
        } else {
            $code = $process.ExitCode
            $phase.status = if ($code -eq 0) { 'PASS' } else { 'FAIL' }
        }
        if ($stdout.Wait(5000) -and $stderr.Wait(5000)) {
            $raw = Protect-Text ($stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult())
        } else {
            $raw = '[owned process output did not close within its bound]'
            $phase.status = 'OUTPUT_TIMEOUT'
            $code = 124
        }
        $raw | Set-Content -LiteralPath $phase.events -Encoding utf8NoBOM
        $readable = foreach ($line in ($raw -split "`r?`n")) {
            if ($line.StartsWith('{')) {
                try {
                    $event = $line | ConvertFrom-Json -AsHashtable
                    if ($event.Output) { $event.Output.TrimEnd() }
                } catch { '[unparseable redacted process event]' }
            } elseif ($line) { $line }
        }
        $readable | Set-Content -LiteralPath $phase.log -Encoding utf8NoBOM
        $readable | Write-Host
        $phase.exitCode = $code
        return $code
    } catch {
        $phase.status = 'BLOCKED'
        $phase.exitCode = 2
        throw
    } finally {
        if ($started -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            $null = $process.WaitForExit(5000)
        }
        $phase.finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
        $process.Dispose()
        Save-Receipt
    }
}

$code = 2
$activity = 'private-runtime-input'
Save-Receipt
try {
    try { $runtime = Get-Content -LiteralPath $RuntimeConfig -Raw | ConvertFrom-Json -AsHashtable }
    catch { throw 'BLOCKED: private runtime input unavailable; values withheld.' }
    foreach ($field in @('databaseUrl', 's3Endpoint', 's3AccessKey', 's3SecretKey', 's3Bucket')) {
        if (-not ($runtime[$field] -is [string]) -or [string]::IsNullOrWhiteSpace($runtime[$field])) {
            throw 'BLOCKED: private runtime input has a missing required field; values withheld.'
        }
    }
    try {
        $db, $s3 = [Uri]$runtime.databaseUrl, [Uri]$runtime.s3Endpoint
        if ($db.Scheme -notin @('postgres', 'postgresql') -or $db.Host -ne '127.0.0.1' -or $db.Port -ne 15432 -or
            -not $db.UserInfo.Contains(':') -or $db.Fragment -ne '' -or $db.Query -ne '?sslmode=disable' -or
            $s3.Scheme -ne 'http' -or $s3.Host -ne '127.0.0.1' -or $s3.Port -ne 18333 -or
            $s3.UserInfo -ne '' -or $s3.Query -ne '' -or $s3.Fragment -ne '') { throw 'Wrong fixture boundary' }
        $password = [Uri]::UnescapeDataString($db.UserInfo.Split(':', 2)[1])
        if (-not $password) { throw 'Missing explicit fixture credential' }
    } catch { throw 'BLOCKED: exact authenticated PG15432/S318333 with explicit local sslmode=disable required; no repair or downgrade.' }
    $secrets = @($runtime.databaseUrl, $runtime.s3AccessKey, $runtime.s3SecretKey, $db.UserInfo, $password)
    foreach ($entry in $runtime.GetEnumerator()) {
        if ($entry.Value -is [string] -and $entry.Key -match '(?i)password|secret|token|key') { $secrets += $entry.Value }
    }
    foreach ($value in @($secrets)) {
        if (-not $value) { continue }
        $secrets += [Uri]::EscapeDataString($value)
        $secrets += [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($value))
        $json = ConvertTo-Json -InputObject $value -Compress
        $secrets += $json.Substring(1, $json.Length - 2)
    }
    $secrets = @($secrets | Where-Object { $_ } | Sort-Object -CaseSensitive -Unique | Sort-Object Length -Descending)
    $activity = 'authored-and-frozen-input-manifest'
    $handoff = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'runtime\HANDOFF.json') -Raw | ConvertFrom-Json -AsHashtable
    foreach ($manifestItem in @($handoff.files) + @($handoff.frozenInputs)) {
        $path = [IO.Path]::GetFullPath((Join-Path $root $manifestItem.path))
        if (-not $path.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $manifestItem.sha256) {
            throw 'BLOCKED: authored input manifest or a frozen prior input changed; obtain an explicitly justified author correction.'
        }
    }
    $before = @(Get-SourceInputs)
    $before | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receipt.sourceInputs -Encoding utf8NoBOM
    $head = & git -C $root --no-pager rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot identify the actual source checkout.' }
    $receipt.sourceHead = [string]$head
    $receipt.productionPathsChangedFromBaseline = @(& git -C $root --no-pager diff --name-only $receipt.baselineCommit -- internal cmd go.mod go.sum)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot record source changes against the published baseline.' }
    $receipt.productionWorkingTree = @(& git -C $root --no-pager status --short --untracked-files=all -- internal cmd go.mod go.sum)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot record production working-tree inputs.' }
    if ($Mode -ne 'MainBuild') {
        $activity = 'compile'
        $code = Invoke-Phase 'compile' @('test', '-c', '-mod=readonly', '-buildvcs=false',
            '-tags=integration,jira_runtime', '-o', $compiled, '.\tests\jira_work_items') 180
        if ($code -eq 0) { $receipt.testBinarySHA256 = (Get-FileHash -LiteralPath $compiled -Algorithm SHA256).Hash.ToLowerInvariant() }
    } else { $code = 0 }
    if ($code -eq 0 -and $Mode -ne 'Compile') {
        $activity = 'mainbuild'
        $code = Invoke-Phase 'mainbuild' @('build', '-mod=readonly', '-buildvcs=false', '-trimpath', '-o', $binary, '.\cmd\delivery-worker') 120
        if ($code -eq 0) { $receipt.commandSHA256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    if ($code -eq 0 -and $Mode -eq 'Contract') {
        $activity = 'actualrun-inputs'
        $current = @(Get-SourceInputs)
        if (($before | ConvertTo-Json -Depth 5 -Compress) -cne ($current | ConvertTo-Json -Depth 5 -Compress)) {
            throw 'Source inputs changed after compilation; actualrun is blocked.'
        }
        if ((Get-FileHash -LiteralPath $compiled -Algorithm SHA256).Hash.ToLowerInvariant() -ne $receipt.testBinarySHA256) {
            throw 'The freshly compiled tagged harness changed before execution.'
        }
        $activity = 'actualrun'
        $code = Invoke-Phase 'actualrun' @('-test.v', '-test.count=1', '-test.parallel=1', '-test.timeout=90s',
            '-test.run=^TestJiraRuntime') 120 $compiled
    }
} catch {
    $code = 2
    $receipt.runnerFailure = 'BLOCKED or runner failure; no private exception text emitted'
    $receipt.runnerFailureCategory = $activity
    Write-Host $receipt.runnerFailure
} finally {
    try { Stop-OwnedMainProcesses } catch { $receipt.cleanupFailure = 'Owned PID cleanup could not be completely observed'; $code = 2 }
    if ($before) {
        $after = @(Get-SourceInputs)
        $after | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receipt.sourceFinal -Encoding utf8NoBOM
        $receipt.sourceUnchangedDuringInvocation = (($before | ConvertTo-Json -Depth 5 -Compress) -ceq ($after | ConvertTo-Json -Depth 5 -Compress))
        if (-not $receipt.sourceUnchangedDuringInvocation) { $code = 2 }
    }
    if (@($receipt.orphanCleanup | Where-Object { $_.status -ne 'ALREADY_EXITED' }).Count -gt 0) { $code = 2 }
    $receipt.finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    $receipt.exitCode = $code
    $receipt.evidence = @(Get-ChildItem -LiteralPath $output -File | Where-Object { $_.FullName -ne $receiptPath } |
        Sort-Object Name | ForEach-Object {
            [ordered]@{ path = $_.Name; sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
        })
    Save-Receipt
    Write-Host "Runtime receipt: $receiptPath"
}
exit $code
