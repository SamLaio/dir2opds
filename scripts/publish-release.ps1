param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._+-]*$')]
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$releaseDir = Join-Path $repoRoot "release/$Version"
$notes = [IO.Path]::GetFullPath((Join-Path $releaseDir 'RELEASE_NOTES.md'))
if (!(Test-Path -LiteralPath $notes -PathType Leaf) -or
    [string]::IsNullOrWhiteSpace((Get-Content -LiteralPath $notes -Raw))) {
    throw "Missing or empty release notes: $notes"
}
$assets = @(Get-ChildItem -LiteralPath $releaseDir -File -Recurse)
if (@($assets | Where-Object { $_.FullName -ne $notes }).Count -eq 0) {
    throw "No release attachments in $releaseDir"
}
if (@($assets | Group-Object Name | Where-Object Count -gt 1).Count -gt 0) {
    throw 'Release attachments must have unique filenames.'
}

Push-Location -LiteralPath $repoRoot
try {
    $ghArgs = @('release', 'create', $Version, '--verify-tag', '--title', $Version, '--notes-file', $notes) + $assets.FullName
    & gh @ghArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'GitHub Release publication failed. Check the release state before retrying.'
    }
} finally {
    Pop-Location
}
