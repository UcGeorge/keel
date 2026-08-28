# Installs the keel CLI on Windows from the latest GitHub release.
#
#   irm https://ucgeorge.github.io/keel/install.ps1 | iex
#
# Environment:
#   KEEL_VERSION      release tag to install (default: the latest release)
#   KEEL_INSTALL_DIR  where to put keel.exe (default: %LOCALAPPDATA%\Programs\keel)
#
# The directory is added to the user PATH when missing. The archive's
# SHA-256 is verified against the release's checksums.txt.
$ErrorActionPreference = 'Stop'
$repo = 'UcGeorge/keel'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "Unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:KEEL_VERSION
if (-not $version) {
  $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ 'User-Agent' = 'keel-install' }
  $version = $latest.tag_name
}
if (-not $version.StartsWith('v')) { $version = "v$version" }
$bare = $version.TrimStart('v')

$archive = "keel_${bare}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$version"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("keel-" + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading keel $version for windows/$arch"
  Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($archive))$" }
  if (-not $line) { throw "No checksum for $archive in checksums.txt" }
  $expected = ($line -split '\s+')[0]
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLower()
  if ($actual -ne $expected) { throw "Checksum mismatch for $archive (expected $expected, got $actual)" }

  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force

  $dir = $env:KEEL_INSTALL_DIR
  if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\keel' }
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item -Path (Join-Path $tmp 'keel.exe') -Destination (Join-Path $dir 'keel.exe') -Force

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    $env:Path = "$env:Path;$dir"
    Write-Host "Added $dir to your user PATH (open a new terminal to pick it up)."
  }

  Write-Host "Installed $(& (Join-Path $dir 'keel.exe') --version) to $dir\keel.exe"
  Write-Host ''
  Write-Host "Next: cd into a repository and run 'keel dev' - the docs are at https://ucgeorge.github.io/keel/"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
