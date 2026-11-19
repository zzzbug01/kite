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
# The whole installer is one scriptblock. Under iex, $MyInvocation.MyCommand.Definition
# is the caller's command, not this file. $KiteInstall.ToString() is this body.
$KiteInstall = {
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
# --code is required only when the service is not installed yet. An existing
# instance can be upgraded with --agent and no code.
if ($Relay -match "[\r\n'`"]" -or $Code -match "[\r\n'`"]" -or $Version -match "[\r\n'`"]") {
  throw 'version, relay, and code cannot contain quotes or newlines'
}

function Test-KiteAdmin {
  if ($env:KITE_TEST_ADMIN -eq '0') { return $false }
  if ($env:KITE_TEST_ADMIN -eq '1') { return $true }
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p = New-Object Security.Principal.WindowsPrincipal($id)
  return $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if ($Agent -and -not (Test-KiteAdmin)) {
  if ($env:KITE_ELEVATED -eq '1') {
    throw 'elevation is required to install the kite agent, and the elevated process is still not Administrator. The installer did not relaunch again.'
  }
  Write-Host 'kite-agent install needs an elevated PowerShell. Approve the prompt to continue.'
  if (-not $KiteInstall) {
    throw 'could not capture this installer to relaunch it elevated'
  }
  $body = $KiteInstall.ToString()
  if ($body -notmatch 'Expand-KiteList') {
    throw 'could not capture this installer to relaunch it elevated'
  }
  if ($env:KITE_TEST_NO_ELEVATE -eq '1') {
    if ($env:KITE_TEST_BODY_OUT) {
      Set-Content -Path $env:KITE_TEST_BODY_OUT -Value $body -Encoding UTF8
    }
    throw 'KITE_TEST_NO_ELEVATE'
  }
  $tmp = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n') + '.ps1')
  $envFile = Join-Path $env:TEMP ("kite-install-" + [guid]::NewGuid().ToString('n') + '.env.ps1')
  try {
    Set-Content -Path $tmp -Value $body -Encoding UTF8
    $svcEsc = $ServiceName.Replace("'", "''")
    @"
`$env:KITE_AGENT = '1'
`$env:KITE_ELEVATED = '1'
`$env:KITE_RELAY = '$($Relay.Replace("'","''"))'
`$env:KITE_CODE = '$($Code.Replace("'","''"))'
`$env:KITE_VERSION = '$($Version.Replace("'","''"))'
`$env:KITE_SERVICE_NAME = '$svcEsc'
`$env:KITE_GITHUB_API = '$($env:KITE_GITHUB_API)'
`$env:KITE_RELEASE_BASE = '$($env:KITE_RELEASE_BASE)'
"@ | Set-Content -Path $envFile -Encoding UTF8
    $argLine = '-NoProfile -ExecutionPolicy Bypass -Command "& { . ''' + $envFile + '''; & ''' + $tmp + ''' }"'
    $p = Start-Process -FilePath 'powershell.exe' -Verb RunAs -Wait -PassThru -ArgumentList $argLine
    if (-not $p -or $p.ExitCode -ne 0) {
      throw 'elevated install failed'
    }
  } catch {
    throw $_
  } finally {
    Remove-Item -Force $tmp, $envFile -ErrorAction SilentlyContinue
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

function Get-KiteProgramFiles {
  if (-not [string]::IsNullOrWhiteSpace($env:ProgramW6432)) { return $env:ProgramW6432 }
  if (-not [string]::IsNullOrWhiteSpace($env:ProgramFiles)) { return $env:ProgramFiles }
  $known = Get-KiteKnownFolder '6D809377-6AF0-444b-8957-A3773F02200E'
  if (-not [string]::IsNullOrWhiteSpace($known)) { return $known }
  return 'C:\Program Files'
}

function Get-KiteKnownFolder([string]$Guid) {
  try {
    if (-not ('Kite.KnownFolder' -as [type])) {
      Add-Type -Namespace Kite -Name KnownFolder -MemberDefinition @'
[DllImport("shell32.dll")]
public static extern int SHGetKnownFolderPath([MarshalAs(UnmanagedType.LPStruct)] Guid rfid, uint dwFlags, IntPtr hToken, out IntPtr pszPath);
'@
    }
    $ptr = [IntPtr]::Zero
    $hr = [Kite.KnownFolder]::SHGetKnownFolderPath([Guid]$Guid, [uint32]0, [IntPtr]::Zero, [ref]$ptr)
    if ($hr -ne 0 -or $ptr -eq [IntPtr]::Zero) { return $null }
    $path = [Runtime.InteropServices.Marshal]::PtrToStringUni($ptr)
    [Runtime.InteropServices.Marshal]::FreeCoTaskMem($ptr)
    return $path
  } catch {
    return $null
  }
}

# CopyDefaultAgent is true only for the default service. A named install
# stages kite-agent.exe and lets `install --service-name` copy it into that
# instance's directory. The client kite.exe is the only PATH binary.
function Get-KiteInstallTargets($Dest, $ServiceName, $Agent) {
  $named = -not [string]::IsNullOrWhiteSpace($ServiceName) -and ($ServiceName -ne 'kite-agent')
  $svc = 'kite-agent'
  if ($named) { $svc = $ServiceName }
  $serviceDir = $Dest
  if ($named) { $serviceDir = Join-Path $Dest $svc }
  $copyDefaultAgent = $false
  if ($Agent -and -not $named) { $copyDefaultAgent = $true }
  return [pscustomobject]@{
    Service = $svc
    Named = [bool]$named
    ClientExe = (Join-Path $Dest 'kite.exe')
    DefaultAgent = (Join-Path $Dest 'kite-agent.exe')
    ServiceDir = $serviceDir
    CopyDefaultAgent = [bool]$copyDefaultAgent
  }
}

function Get-KiteInstanceHint($Command, $ServiceName) {
  if ($ServiceName -and $ServiceName -ne 'kite-agent') {
    return "$Command --service-name $ServiceName"
  }
  return $Command
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
    $dest = Join-Path (Get-KiteProgramFiles) 'kite'
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
  $plan = Get-KiteInstallTargets $dest $ServiceName $Agent
  $svc = $plan.Service
  $svcExists = $false
  try {
    Get-Service -Name $svc -ErrorAction Stop | Out-Null
    $svcExists = $true
  } catch {}
  if ($Agent -and -not $svcExists -and ([string]::IsNullOrWhiteSpace($Relay) -or [string]::IsNullOrWhiteSpace($Code))) {
    throw "--agent requires --relay and --code when $svc is not installed yet"
  }
  $pairHint = Get-KiteInstanceHint 'kite-agent pair' $svc
  $upgradeHint = Get-KiteInstanceHint 'kite-agent upgrade' $svc
  $agentExe = $null
  $didUpgrade = $false
  # Client kite.exe is the only binary copied onto the PATH directory.
  Copy-Item -LiteralPath (Join-Path $out 'kite.exe') -Destination $plan.ClientExe
  Unblock-File -LiteralPath $plan.ClientExe
  if (-not $Agent) {
    Write-Host "Installed kite $Version"
    Write-Host "  kite.exe -> $($plan.ClientExe)"
    if ($svcExists) {
      Write-Host "kite-agent service $svc was not updated or restarted."
      Write-Host "To update it: $upgradeHint"
    }
  } else {
    $stage = Join-Path $env:TEMP ("kite-upgrade-" + [guid]::NewGuid().ToString('n'))
    New-Item -ItemType Directory -Path $stage | Out-Null
    $cleanupStage = $true
    try {
      Copy-Item -LiteralPath (Join-Path $out 'kite-agent.exe') -Destination (Join-Path $stage 'kite-agent.exe')
      if (Test-Path (Join-Path $out 'kite.exe')) {
        Copy-Item -LiteralPath (Join-Path $out 'kite.exe') -Destination (Join-Path $stage 'kite.exe')
      }
      $staged = Join-Path $stage 'kite-agent.exe'
      $instanceExe = Join-Path $plan.ServiceDir 'kite-agent.exe'
      # A named install must not treat the default service binary as its own.
      $runner = $staged
      if ($svcExists -and (Test-Path -LiteralPath $instanceExe)) { $runner = $instanceExe }
      elseif ($svcExists -and $plan.CopyDefaultAgent -and (Test-Path -LiteralPath $plan.DefaultAgent)) { $runner = $plan.DefaultAgent }
      elseif ($svcExists -and -not $plan.Named -and (Test-Path -LiteralPath $plan.DefaultAgent)) { $runner = $plan.DefaultAgent }
      if ($plan.CopyDefaultAgent -and -not $svcExists) {
        Copy-Item -LiteralPath $staged -Destination $plan.DefaultAgent
        Unblock-File -LiteralPath $plan.DefaultAgent
        $runner = $plan.DefaultAgent
      }
      if ($svcExists) {
        & $runner upgrade --from $staged --version $Version --service-name $svc
        if ($LASTEXITCODE -ne 0) {
          throw "kite-agent upgrade failed ($LASTEXITCODE). The agent service was not left half-updated."
        }
        $didUpgrade = $true
        $agentExe = $instanceExe
        if (-not (Test-Path -LiteralPath $agentExe)) { $agentExe = $runner }
        Write-Host "Installed kite $Version"
        Write-Host "  kite.exe -> $($plan.ClientExe)"
        Write-Host "  service $svc -> $($plan.ServiceDir)"
        if ([string]::IsNullOrWhiteSpace($Code)) {
          Write-Host "No install code was given, so the existing token was kept."
          Write-Host "Next, on this machine: $pairHint"
          Write-Host "To update it later: $upgradeHint"
        }
      } else {
        $agentExe = $runner
        Write-Host "Installed kite $Version"
        Write-Host "  kite.exe -> $($plan.ClientExe)"
        if ($plan.Named) {
          Write-Host "  service $svc -> $($plan.ServiceDir)"
        } else {
          Write-Host "  kite-agent.exe -> $($plan.DefaultAgent)"
        }
      }
      if (-not [string]::IsNullOrWhiteSpace($Code)) {
        if (-not $agentExe) { $agentExe = $staged }
        & $agentExe install --relay $Relay --code $Code --service-name $svc
        if ($LASTEXITCODE -ne 0) { throw "kite-agent install failed ($LASTEXITCODE)" }
        Write-Host "Next, on this machine: $pairHint"
        Write-Host "If that cannot read the control file, from an elevated PowerShell: $pairHint"
      }
    } finally {
      Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
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

} finally {
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
}

if ($env:KITE_INSTALL_LIB_ONLY -eq '1') {
  . $KiteInstall
  return
}
& $KiteInstall @args
