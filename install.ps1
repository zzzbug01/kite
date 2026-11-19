# Install kite and kite-agent on Windows (PowerShell 5.1 or newer).
#
#   irm https://raw.githubusercontent.com/zzzbug01/kite/main/install.ps1 | iex
#
# Agent install (the script relaunches elevated when it is not already):
#
#   $env:KITE_AGENT='1'; $env:KITE_RELAY='tunnel.example.com'; $env:KITE_CODE='ABCD-EFGH'; irm https://raw.githubusercontent.com/zzzbug01/kite/main/install.ps1 | iex
#
# KITE_VERSION pins a tag such as v0.1.0-rc.3.
# Saved-file arguments also work: .\install.ps1 --agent --relay HOST --code ABCD-EFGH
# KITE_GITHUB_API and KITE_RELEASE_BASE override the download locations.
# Do not call exit: this file is often invoked with iex in the caller's session.
$ErrorActionPreference = 'Stop'
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

if ($env:OS -ne 'Windows_NT') {
  throw 'install.ps1 is for Windows'
}

function Show-KiteInstallHelp {
  Write-Host @'
Usage: install.ps1 [--version vX] [--agent --relay HOST --code ABCD-EFGH]
Installs kite.exe and kite-agent.exe.
An elevated session installs to %ProgramFiles%\kite. Otherwise the
directory is %LOCALAPPDATA%\kite\bin. Both are added to the user PATH.
--agent relaunches elevated when required, then runs kite-agent install.
'@
}

$Version = [string]$env:KITE_VERSION
$Relay = [string]$env:KITE_RELAY
$Code = [string]$env:KITE_CODE
$Agent = ($env:KITE_AGENT -eq '1' -or $env:KITE_AGENT -eq 'true')
$RelayServer = ($env:KITE_RELAY_SERVER -eq '1')

$i = 0
while ($i -lt $args.Count) {
  $a = [string]$args[$i]
  switch -Regex ($a) {
    '^(-Agent|--agent)$' { $Agent = $true }
    '^(-RelayServer|--relay-server)$' { $RelayServer = $true }
    '^(-Version|--version)$' {
      $i++
      if ($i -ge $args.Count) { throw '--version needs a value' }
      $Version = [string]$args[$i]
    }
    '^(-Relay|--relay)$' {
      $i++
      if ($i -ge $args.Count) { throw '--relay needs a value' }
      $Relay = [string]$args[$i]
    }
    '^(-Code|--code)$' {
      $i++
      if ($i -ge $args.Count) { throw '--code needs a value' }
      $Code = [string]$args[$i]
    }
    '^(-h|--help|-Help)$' { Show-KiteInstallHelp; return }
    default { throw "unknown argument $a" }
  }
  $i++
}

if ($Version -match '^[0-9]') { $Version = "v$Version" }
if ($RelayServer) {
  throw 'kite-relay is installed on Linux. This script does not install it on Windows.'
}
if ($Agent -and ([string]::IsNullOrWhiteSpace($Relay) -or [string]::IsNullOrWhiteSpace($Code))) {
  throw '--agent requires --relay and --code (or KITE_RELAY and KITE_CODE)'
}
if ($Relay -match "[\r\n'`"]" -or $Code -match "[\r\n'`"]" -or $Version -match "[\r\n'`"]") {
  throw 'version, relay, and code cannot contain quotes or newlines'
}

function Test-KiteAdmin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p = New-Object Security.Principal.WindowsPrincipal($id)
  return $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if ($Agent -and -not (Test-KiteAdmin)) {
  Write-Host 'kite-agent install needs an elevated PowerShell. Approve the prompt to continue.'
  $url = 'https://raw.githubusercontent.com/zzzbug01/kite/main/install.ps1'
  if ($env:KITE_INSTALL_URL) { $url = $env:KITE_INSTALL_URL }
  $tmp = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n') + '.ps1')
  $relayEsc = $Relay.Replace("'", "''")
  $codeEsc = $Code.Replace("'", "''")
  $verEsc = $Version.Replace("'", "''")
  $urlEsc = $url.Replace("'", "''")
  @"
`$ErrorActionPreference = 'Stop'
`$env:KITE_AGENT = '1'
`$env:KITE_RELAY = '$relayEsc'
`$env:KITE_CODE = '$codeEsc'
`$env:KITE_VERSION = '$verEsc'
irm '$urlEsc' | iex
"@ | Set-Content -Path $tmp -Encoding UTF8
  try {
    $argLine = '-NoProfile -ExecutionPolicy Bypass -File "' + $tmp + '"'
    $p = Start-Process -FilePath 'powershell.exe' -Verb RunAs -Wait -PassThru -ArgumentList $argLine
  } catch {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    throw 'elevation is required to install the kite agent'
  }
  Remove-Item -Force $tmp -ErrorAction SilentlyContinue
  if (-not $p -or $p.ExitCode -ne 0) {
    throw 'elevated install failed'
  }
  return
}

$apiRoot = 'https://api.github.com'
if ($env:KITE_GITHUB_API) { $apiRoot = $env:KITE_GITHUB_API.TrimEnd('/') }
$releaseBase = 'https://github.com/zzzbug01/kite/releases/download'
if ($env:KITE_RELEASE_BASE) { $releaseBase = $env:KITE_RELEASE_BASE.TrimEnd('/') }

function Invoke-KiteRest($Url) {
  $params = @{
    Uri = $Url
    Headers = @{ Accept = 'application/vnd.github+json'; 'User-Agent' = 'kite-install' }
  }
  if ($env:HTTPS_PROXY) { $params.Proxy = $env:HTTPS_PROXY }
  elseif ($env:https_proxy) { $params.Proxy = $env:https_proxy }
  return Invoke-RestMethod @params
}

function Save-KiteFile($Url, $Dest) {
  $params = @{
    Uri = $Url
    OutFile = $Dest
    UseBasicParsing = $true
    Headers = @{ 'User-Agent' = 'kite-install' }
  }
  if ($env:HTTPS_PROXY) { $params.Proxy = $env:HTTPS_PROXY }
  elseif ($env:https_proxy) { $params.Proxy = $env:https_proxy }
  Invoke-WebRequest @params
}

if ([string]::IsNullOrWhiteSpace($Version)) {
  $rels = @(Invoke-KiteRest ($apiRoot + '/repos/zzzbug01/kite/releases?per_page=30'))
  $stable = $null
  $pre = $null
  foreach ($r in $rels) {
    if ($r.draft) { continue }
    if (-not $r.prerelease -and -not $stable) { $stable = [string]$r.tag_name }
    if ($r.prerelease -and -not $pre) { $pre = [string]$r.tag_name }
  }
  if ($stable) { $Version = $stable }
  elseif ($pre) { $Version = $pre }
  else { throw 'no kite release was published' }
}

$pa = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $pa = $env:PROCESSOR_ARCHITEW6432 }
switch ($pa) {
  'AMD64' { $goarch = 'amd64' }
  'ARM64' { $goarch = 'arm64' }
  default { throw "unsupported architecture $pa" }
}

$archive = "kite_${Version}_windows_${goarch}.zip"
$base = "$releaseBase/$Version"
$work = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
  Write-Host "Downloading $archive"
  $zipPath = Join-Path $work $archive
  $sumPath = Join-Path $work 'SHA256SUMS'
  Save-KiteFile "$base/$archive" $zipPath
  Save-KiteFile "$base/SHA256SUMS" $sumPath
  $want = $null
  foreach ($line in (Get-Content -Path $sumPath)) {
    if ($line -match '^([0-9A-Fa-f]{64})\s+\*?(\S+)\s*$' -and $Matches[2] -eq $archive) {
      $want = $Matches[1].ToLowerInvariant()
      break
    }
  }
  if (-not $want) { throw "SHA256SUMS has no entry for $archive" }
  $got = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLowerInvariant()
  if ($got -ne $want) { throw "checksum mismatch for $archive" }
  Expand-Archive -Path $zipPath -DestinationPath (Join-Path $work 'out') -Force
  $out = Join-Path $work 'out'

  $dest = $null
  if (Test-KiteAdmin) {
    $root = $env:ProgramFiles
    if ([string]::IsNullOrWhiteSpace($root)) { $root = 'C:\Program Files' }
    $dest = Join-Path $root 'kite'
  } else {
    $root = $env:LOCALAPPDATA
    if ([string]::IsNullOrWhiteSpace($root)) { throw 'LOCALAPPDATA is not set' }
    $dest = Join-Path $root 'kite\bin'
  }
  New-Item -ItemType Directory -Force -Path $dest | Out-Null
  foreach ($name in @('kite.exe', 'kite-agent.exe')) {
    $src = Join-Path $out $name
    if (-not (Test-Path $src)) { throw "archive does not contain $name" }
    Unblock-File -Path $src
    Copy-Item -Force -Path $src -Destination (Join-Path $dest $name)
    Unblock-File -Path (Join-Path $dest $name)
  }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not $userPath) { $userPath = '' }
  $found = $false
  foreach ($part in ($userPath -split ';')) {
    if ($part.TrimEnd('\') -ieq $dest.TrimEnd('\')) { $found = $true }
  }
  if (-not $found) {
    if ($userPath -and -not $userPath.EndsWith(';')) { $userPath = $userPath + ';' }
    [Environment]::SetEnvironmentVariable('Path', ($userPath + $dest), 'User')
  }
  if (($env:Path -split ';') -notcontains $dest) {
    $env:Path = $dest + ';' + $env:Path
  }

  Write-Host "Installed kite $Version to $dest"
  if ($Agent) {
    $exe = Join-Path $dest 'kite-agent.exe'
    & $exe install --relay $Relay --code $Code
    if ($LASTEXITCODE -ne 0) { throw "kite-agent install failed ($LASTEXITCODE)" }
    Write-Host 'Next, on this machine: kite-agent pair'
  }
} finally {
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
