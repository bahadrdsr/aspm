param(
    [Parameter(Mandatory = $true)] [string] $Helm,
    [string] $OutputName = 'artifact-red-01',
    [string] $Distribution = 'Ubuntu'
)

$ErrorActionPreference = 'Stop'
if (-not [IO.Path]::IsPathFullyQualified($Helm) -or -not (Test-Path -LiteralPath $Helm -PathType Leaf)) {
    throw 'An explicit existing Helm executable is required; no renderer or installation fallback.'
}
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$' -or $Distribution -notmatch '^[a-zA-Z0-9_.-]+$') {
    throw 'Output/distribution selections must be explicit bounded names.'
}
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
$generator = Join-Path $root '.cache\podman-validation\extracted\usr\libexec\podman\quadlet'
$wsl = Join-Path $env:WINDIR 'System32\wsl.exe'
$yaml = Join-Path $root '.cache\modules\gopkg.in\yaml.v3@v3.0.1\yaml.go'
foreach ($tool in @($go, $generator, $wsl, $yaml)) {
    if (-not (Test-Path -LiteralPath $tool -PathType Leaf)) {
        throw 'Required existing Go, cached YAML, WSL or Podman 4.9 generator is missing; no downloads authorized.'
    }
}
$output = Join-Path $root ".artifacts\assessment-deployment-v1\$OutputName"
if (Test-Path $output) { throw 'Refusing to overwrite prior run evidence; use a fresh OutputName.' }
$work = Join-Path $root '.cache\assessment-deployment-v1\work'
New-Item -ItemType Directory -Path $output, $work -Force | Out-Null
& node (Join-Path $PSScriptRoot 'reviews\v1\guard.mjs') before $output
if ($LASTEXITCODE -ne 0) { throw 'Current source/control guard failed before tests.' }
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go
$start.WorkingDirectory = $PSScriptRoot
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
$arguments = @('test', '-json', '-mod=readonly', '-count=1', '-timeout=120s', '.')
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -notmatch '^(PATH|PATHEXT|SystemRoot|WINDIR|ComSpec|OS|SystemDrive|PROCESSOR_ARCHITECTURE|NUMBER_OF_PROCESSORS|ProgramFiles|ProgramFiles\(x86\)|ProgramData|USERPROFILE|HOMEDRIVE|HOMEPATH|LOCALAPPDATA|APPDATA)$') {
        $start.Environment.Remove($name) | Out-Null
    }
}
$start.Environment['GOTOOLCHAIN'] = 'local'
$start.Environment['GOWORK'] = 'off'
$start.Environment['GOENV'] = 'off'
$start.Environment['GOFLAGS'] = ''
$start.Environment['GOPROXY'] = 'off'
$start.Environment['GOSUMDB'] = 'off'
$start.Environment['GOMODCACHE'] = Join-Path $root '.cache\modules'
$start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
$start.Environment['GOPATH'] = Join-Path $root '.cache\assessment-deployment-v1\gopath'
foreach ($name in @('GOTMPDIR', 'TEMP', 'TMP', 'TMPDIR')) { $start.Environment[$name] = $work }
$start.Environment['ASPM_ASSESSMENT_DEPLOY_ARTIFACTS'] = $output
$start.Environment['ASPM_ASSESSMENT_DEPLOY_HELM'] = $Helm
$start.Environment['ASPM_ASSESSMENT_DEPLOY_WSL'] = $wsl
$start.Environment['ASPM_ASSESSMENT_DEPLOY_DISTRO'] = $Distribution
$start.Environment['ASPM_ASSESSMENT_DEPLOY_GENERATOR'] = $generator
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$began = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Go artifact test process failed to start.' }
$stdout = $process.StandardOutput.ReadToEndAsync()
$stderr = $process.StandardError.ReadToEndAsync()
$hardTimeout = -not $process.WaitForExit(160000)
if ($hardTimeout) { $process.Kill($true); $process.WaitForExit() }
$code = $process.ExitCode
$text = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
$outputExceeded = $text.Length -gt 2097152
if ($outputExceeded) { $text = $text.Substring(0,2097152) }
if ($hardTimeout -or $outputExceeded) { $code = 124; $text += "`nBLOCKED: runner time/output bound exceeded; no behavioral artifact result claimed.`n" }
$text | Set-Content -LiteralPath (Join-Path $output 'events.jsonl') -Encoding utf8NoBOM
$readable = foreach ($line in ($text -split "`r?`n")) {
    if ($line.StartsWith('{')) {
        try { $event = $line | ConvertFrom-Json -AsHashtable; if ($event.Output) { $event.Output.TrimEnd() } } catch { '[Incomplete bounded event]' }
    } elseif ($line) { $line }
}
$readable | Set-Content -LiteralPath (Join-Path $output 'run.log') -Encoding utf8NoBOM
[ordered]@{
    schemaVersion = 1
    scope = 'assessment-deployment-v1'
    startedAt = $began.ToString('o')
    finishedAt = [DateTimeOffset]::UtcNow.ToString('o')
    exitCode = $code
    goExecutable = $go
    arguments = $arguments
    nestedModule = $PSScriptRoot
    helmExecutable = $Helm
    wslExecutable = $wsl
    wslDistribution = $Distribution
    nativeGenerator = $generator
    tools = @(@($go, $Helm, $generator) | ForEach-Object {
        [ordered]@{path=$_;sha256=(Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant()}
    })
    rendering = 'Actual Helm --dry-run=client with empty kubeconfig; native Podman 4.9 unprivileged -user -dryrun -no-kmsg-log.'
    deploymentOrPrivilegeActions = $false
    imageBuildOrPackagerExecution = $false
    privateRuntimeOrVendorCredentialsLoaded = $false
    dependencyInstallsOrDownloads = $false
    hardTimeout = $hardTimeout
    outputExceeded = $outputExceeded
} | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $output 'receipt.json') -Encoding utf8NoBOM
& node (Join-Path $PSScriptRoot 'reviews\v1\guard.mjs') after $output
if ($LASTEXITCODE -ne 0) { throw 'Current source/control guard failed after actual tests.' }
$readable | Select-Object -First 150 | Write-Output
$process.Dispose()
exit $code
