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

if ($env:OS -ne 'Windows_NT' -and $env:KITE_INSTALL_LIB_ONLY -ne '1') {
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
$ServiceName = [string]$env:KITE_SERVICE_NAME
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
    '^(-ServiceName|--service-name)$' {
      $i++
      if ($i -ge $args.Count) { throw '--service-name needs a value' }
      $ServiceName = [string]$args[$i]
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
  # Relaunch this same script, not a fresh download of main. iex has no path,
  # so the script text is saved and executed as a file.
  $body = $MyInvocation.MyCommand.Definition
  if ([string]::IsNullOrWhiteSpace($body) -and $PSCommandPath) {
    $body = [IO.File]::ReadAllText($PSCommandPath)
  }
  if ([string]::IsNullOrWhiteSpace($body)) {
    throw 'could not capture this installer to relaunch it elevated'
  }
  $tmp = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n') + '.ps1')
  Set-Content -Path $tmp -Value $body -Encoding UTF8
  $envFile = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n') + '.env.ps1')
  $svcEsc = $ServiceName.Replace("'", "''")
  @"
`$env:KITE_AGENT = '1'
`$env:KITE_RELAY = '$($Relay.Replace("'","''"))'
`$env:KITE_CODE = '$($Code.Replace("'","''"))'
`$env:KITE_VERSION = '$($Version.Replace("'","''"))'
`$env:KITE_SERVICE_NAME = '$svcEsc'
"@ | Set-Content -Path $envFile -Encoding UTF8
  $argLine = '-NoProfile -ExecutionPolicy Bypass -Command "& { . ''' + $envFile + '''; & ''' + $tmp + ''' }"'
  try {
    $p = Start-Process -FilePath 'powershell.exe' -Verb RunAs -Wait -PassThru -ArgumentList $argLine
  } catch {
    Remove-Item -Force $tmp, $envFile -ErrorAction SilentlyContinue
    throw 'elevation is required to install the kite agent'
  }
  Remove-Item -Force $tmp, $envFile -ErrorAction SilentlyContinue
  if (-not $p -or $p.ExitCode -ne 0) {
    throw 'elevated install failed'
  }
  return
}

# PowerShell 5.1 can hand back a JSON array as one object. @(that) stays one
# element, and $r.draft is then the array of every draft flag, which is
# truthy, so every release is skipped. Unwrap until each item is one release.
function Expand-KiteList($raw) {
  $items = New-Object System.Collections.Generic.List[object]
  if ($null -eq $raw) { return ,@() }
  $queue = New-Object System.Collections.Queue
  $queue.Enqueue($raw)
  while ($queue.Count -gt 0) {
    $cur = $queue.Dequeue()
    if ($null -eq $cur) { continue }
    if ($cur.PSObject -and $null -ne $cur.PSObject.Properties['tag_name']) {
      $items.Add($cur) | Out-Null
      continue
    }
    foreach ($inner in $cur) { $queue.Enqueue($inner) }
  }
  return ,$items.ToArray()
}

if ($env:KITE_INSTALL_LIB_ONLY -eq '1') { return }

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
  # The comma keeps a JSON array as one value so Expand-KiteList can unwrap it.
  return , (Invoke-RestMethod @params)
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
  $rels = Expand-KiteList (Invoke-KiteRest ($apiRoot + '/repos/zzzbug01/kite/releases?per_page=30'))
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
  try {
    Save-KiteFile "$base/$archive" $zipPath
    Save-KiteFile "$base/SHA256SUMS" $sumPath
  } catch {
    throw "release $Version was not found"
  }
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
    if (-not (Test-Path (Join-Path $out $name))) { throw "archive does not contain $name" }
    Unblock-File -Path (Join-Path $out $name)
  }
  $svc = 'kite-agent'
  if (-not [string]::IsNullOrWhiteSpace($ServiceName)) { $svc = $ServiceName }
  $running = $false
  try {
    $info = Get-Service -Name $svc -ErrorAction Stop
    if ($info.Status -eq 'Running') { $running = $true }
  } catch {}
  if ($running) {
    # The service binary is locked. Stage both files outside the directory this
    # script deletes, then upgrade --from stops, replaces both, and starts.
    # The task removes the stage after the copy. A failure does not leave
    # kite.exe new and kite-agent.exe old.
    $stage = Join-Path $env:TEMP ("kite-upgrade-" + [guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Path $stage | Out-Null
    Copy-Item -LiteralPath (Join-Path $out 'kite.exe') -Destination (Join-Path $stage 'kite.exe')
    Copy-Item -LiteralPath (Join-Path $out 'kite-agent.exe') -Destination (Join-Path $stage 'kite-agent.exe')
    $staged = Join-Path $stage 'kite-agent.exe'
    & $staged upgrade --from $staged --service-name $svc
    if ($LASTEXITCODE -ne 0) {
      Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
      throw "kite-agent upgrade failed ($LASTEXITCODE)"
    }
  } else {
    # Copy both, or restore both. A locked kite-agent.exe must not leave a new kite.exe behind.
    $backs = @()
    try {
      foreach ($name in @('kite.exe', 'kite-agent.exe')) {
        $target = Join-Path $dest $name
        $src = Join-Path $out $name
        $bak = $target + '.prev'
        if (Test-Path -LiteralPath $target) {
          if (Test-Path -LiteralPath $bak) { Remove-Item -LiteralPath $bak -Force }
          Move-Item -LiteralPath $target -Destination $bak
          $backs += ,@($target, $bak)
        }
        Copy-Item -LiteralPath $src -Destination $target
        Unblock-File -LiteralPath $target
      }
      foreach ($pair in $backs) {
        Remove-Item -LiteralPath $pair[1] -Force -ErrorAction SilentlyContinue
      }
    } catch {
      for ($i = $backs.Count - 1; $i -ge 0; $i--) {
        $target = $backs[$i][0]
        $bak = $backs[$i][1]
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue }
        if (Test-Path -LiteralPath $bak) { Move-Item -LiteralPath $bak -Destination $target }
      }
      throw "could not replace the binaries (is $svc running?). The previous files were put back. $_"
    }
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
    if (-not (Test-Path $exe)) { $exe = Join-Path $out 'kite-agent.exe' }
    $installArgs = @('install', '--relay', $Relay, '--code', $Code)
    if (-not [string]::IsNullOrWhiteSpace($ServiceName)) {
      $installArgs += @('--service-name', $ServiceName)
    }
    & $exe @installArgs
    if ($LASTEXITCODE -ne 0) { throw "kite-agent install failed ($LASTEXITCODE)" }
    Write-Host 'Next, on this machine: kite-agent pair'
  }
} finally {
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
