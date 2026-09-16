$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$manifest = Join-Path $root 'FROZEN.A5.sha256'
$canonical = Join-Path $root 'FROZEN.sha256'
$expectedManifestHash = (Get-FileHash -LiteralPath $manifest -Algorithm SHA256).Hash
if ((Get-FileHash -LiteralPath $canonical -Algorithm SHA256).Hash -cne $expectedManifestHash) {
    throw 'Canonical manifest differs from A5.'
}
$closure = Get-Content -LiteralPath (Join-Path $root 'COMPILATION.A5.json') -Raw | ConvertFrom-Json
$actualFiles = @(Get-ChildItem -LiteralPath $root -Recurse -File -Force | ForEach-Object {
    [System.IO.Path]::GetRelativePath($root, $_.FullName).Replace('/', '\')
} | Where-Object { $_ -notmatch '^\.run[\\/]' } | Sort-Object -CaseSensitive)
$expectedFiles = @($closure.packageTreeFiles | Sort-Object -CaseSensitive)
if (@(Compare-Object -ReferenceObject $expectedFiles -DifferenceObject $actualFiles -CaseSensitive).Count -ne 0) {
    throw 'The full package file set changed; unselected Go/init/binding/embed inputs may not be excluded.'
}
$seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
foreach ($line in Get-Content -LiteralPath $manifest) {
    if ($line.StartsWith('#')) { continue }
    if ($line -notmatch '^([0-9a-f]{64})  ([A-Za-z0-9._\\@-]+)$') { throw 'Malformed A5 hash entry.' }
    $expected = $Matches[1]
    $relative = $Matches[2]
    $native = $relative.Replace('\', [System.IO.Path]::DirectorySeparatorChar)
    $path = [System.IO.Path]::GetFullPath((Join-Path $root $native))
    if (-not $path.StartsWith($root + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase) -or -not $seen.Add($relative)) {
        throw 'A5 path escaped the package or was duplicated.'
    }
    if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expected) {
        throw "Frozen A5 input changed: $relative"
    }
}
foreach ($file in $expectedFiles) {
    if ($file -notin @('FROZEN.sha256', 'FROZEN.A5.sha256') -and -not $seen.Contains($file)) {
        throw "Package input is not protected by A5: $file"
    }
}
Write-Output ("Verified full A5 package: {0} closed-tree files, {1} hashes, {2} same-package Go sources." -f $expectedFiles.Count, $seen.Count, @($closure.samePackageGoFiles).Count)
Write-Output ('A5 manifest SHA256: ' + $expectedManifestHash.ToLowerInvariant())
