param(
    [Parameter(Mandatory = $true)] [string] $Helm,
    [string] $OutputName = 'artifact-red-01',
    [string] $Distribution = 'Ubuntu'
)

$ErrorActionPreference = 'Stop'
if (-not [IO.Path]::IsPathFullyQualified($Helm) -or -not (Test-Path -LiteralPath $Helm -PathType Leaf)) {
    throw 'An explicit existing Helm executable is required; no installation or simulated renderer.'
}
if ($OutputName -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$' -or $Distribution -notmatch '^[a-zA-Z0-9_.-]+$') {
    throw 'Output label/distribution must be explicit bounded names.'
}
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go = Join-Path $root '.cache\modules\golang.org\toolchain@v0.0.1-go1.27.1.windows-amd64\bin\go.exe'
$generator = Join-Path $root '.cache\podman-validation\extracted\usr\libexec\podman\quadlet'
$wsl = Join-Path $env:WINDIR 'System32\wsl.exe'
foreach ($tool in @($go, $generator, $wsl)) {
    if (-not (Test-Path -LiteralPath $tool -PathType Leaf)) { throw 'Required existing Go/WSL/Podman generator is unavailable; no installation authorized.' }
}
$output = Join-Path $root ".artifacts\delivery-deployment-v1\$OutputName"
if (Test-Path $output) { throw 'Refusing to overwrite earlier artifact evidence.' }
$work = Join-Path $root '.cache\delivery-deployment-v1\work'
New-Item -ItemType Directory -Path $output, $work -Force | Out-Null
$start = [System.Diagnostics.ProcessStartInfo]::new()
$start.FileName = $go
$start.WorkingDirectory = $PSScriptRoot
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
$arguments = @('test', '-json', '-mod=readonly', '-count=1', '-timeout=120s', '.')
foreach ($argument in $arguments) { $start.ArgumentList.Add($argument) }
foreach ($name in @($start.Environment.Keys)) {
    if ($name -match '^(ASPM_|AWS_|SLACK_|KUBE|HELM_)') { $start.Environment.Remove($name) | Out-Null }
}
$start.Environment['GOTOOLCHAIN'] = 'local'
$start.Environment['GOENV'] = 'off'
$start.Environment['GOWORK'] = 'off'
$start.Environment['GOFLAGS'] = ''
$start.Environment['GOPROXY'] = 'off'
$start.Environment['GOSUMDB'] = 'off'
$start.Environment['GOMODCACHE'] = Join-Path $root '.cache\modules'
$start.Environment['GOCACHE'] = Join-Path $root '.cache\build'
$start.Environment['GOPATH'] = Join-Path $root '.cache\delivery-deployment-v1\gopath'
foreach ($name in @('GOTMPDIR', 'TEMP', 'TMP', 'TMPDIR')) { $start.Environment[$name] = $work }
$start.Environment['ASPM_DELIVERY_DEPLOY_ARTIFACTS'] = $output
$start.Environment['ASPM_DELIVERY_DEPLOY_HELM'] = $Helm
$start.Environment['ASPM_DELIVERY_DEPLOY_WSL'] = $wsl
$start.Environment['ASPM_DELIVERY_DEPLOY_DISTRO'] = $Distribution
$start.Environment['ASPM_DELIVERY_DEPLOY_GENERATOR'] = $generator
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $start
$began = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Go artifact test process could not start.' }
$stdout = $process.StandardOutput.ReadToEndAsync()
$stderr = $process.StandardError.ReadToEndAsync()
$process.WaitForExit()
$code = $process.ExitCode
$text = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
$text | Set-Content (Join-Path $output 'events.jsonl') -Encoding utf8NoBOM
$readable = foreach ($line in ($text -split "`r?`n")) {
    if ($line.StartsWith('{')) {
        $event = $line | ConvertFrom-Json -AsHashtable
        if ($event.Output) { $event.Output.TrimEnd() }
    } elseif ($line) { $line }
}
$readable | Set-Content (Join-Path $output 'run.log') -Encoding utf8NoBOM
[ordered]@{
    schemaVersion = 1
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
    rendering = 'Helm client-only empty kubeconfig plus rootless Podman 4.9 -dryrun -no-kmsg-log; no activation'
    deploymentOrPrivilegeActions = $false
    imageBuildExecuted = $false
    vendorOrClusterCredentialsLoaded = $false
} | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $output 'receipt.json') -Encoding utf8NoBOM
$readable | Write-Output
$process.Dispose()
exit $code
