<#
.SYNOPSIS
  Fills in the winget manifest templates and the Scoop manifest for a published Syncer release.

.DESCRIPTION
  Reads the release's assets from the GitHub API (public, no token needed), takes the
  SHA-256 digests GitHub publishes for Syncer-amd64-installer.exe and Syncer.exe, and writes:
    <OutDir>\manifests\a\ApolloF\Syncer\<version>\*.yaml   (ready for microsoft/winget-pkgs)
    packaging\scoop\syncer.json                             (updated in place)
  Nothing is submitted or pushed.

.EXAMPLE
  ./packaging/render-manifests.ps1 -Version 1.0.0
  winget validate --manifest packaging/out/manifests/a/ApolloF/Syncer/1.0.0
#>
param(
    [Parameter(Mandatory)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version,
    [string]$OutDir = (Join-Path $PSScriptRoot 'out')
)
$ErrorActionPreference = 'Stop'

$release = Invoke-RestMethod "https://api.github.com/repos/ApolloF/syncer/releases/tags/v$Version" -Headers @{ 'User-Agent' = 'syncer-packaging' }

function Get-AssetSha256([string]$name) {
    $asset = $release.assets | Where-Object name -eq $name
    if (-not $asset) { throw "Release v$Version has no asset '$name'." }
    if ($asset.digest -notmatch '^sha256:([0-9a-f]{64})$') { throw "Asset '$name' has no SHA-256 digest on GitHub." }
    $Matches[1].ToUpperInvariant()
}

$values = @{
    '{{VERSION}}'          = $Version
    '{{INSTALLER_SHA256}}' = Get-AssetSha256 'Syncer-amd64-installer.exe'
    '{{RELEASE_DATE}}'     = ([datetime]$release.published_at).ToString('yyyy-MM-dd')
}

$target = Join-Path $OutDir "manifests\a\ApolloF\Syncer\$Version"
New-Item -ItemType Directory -Force $target | Out-Null
foreach ($template in Get-ChildItem (Join-Path $PSScriptRoot 'winget') -Filter '*.yaml') {
    $text = (Get-Content $template.FullName -Raw) -replace '(?m)^# Template:.*\r?\n', ''
    foreach ($key in $values.Keys) { $text = $text.Replace($key, $values[$key]) }
    if ($text -match '\{\{\w+\}\}') { throw "Unfilled placeholder in $($template.Name)." }
    [IO.File]::WriteAllText((Join-Path $target $template.Name), $text, [Text.UTF8Encoding]::new($false))
}
Write-Host "winget manifests: $target"

$scoopPath = Join-Path $PSScriptRoot 'scoop\syncer.json'
$scoop = Get-Content $scoopPath -Raw | ConvertFrom-Json
$scoop.version = $Version
$scoop.architecture.'64bit'.url = "https://github.com/ApolloF/syncer/releases/download/v$Version/Syncer.exe"
$scoop.architecture.'64bit'.hash = (Get-AssetSha256 'Syncer.exe').ToLowerInvariant()
[IO.File]::WriteAllText($scoopPath, ($scoop | ConvertTo-Json -Depth 10) + "`n", [Text.UTF8Encoding]::new($false))
Write-Host "Scoop manifest: $scoopPath"
