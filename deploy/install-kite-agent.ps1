#Requires -Version 5.1
<#
.SYNOPSIS
    Install kite-agent and register a Task Scheduler job that starts it at logon.

.DESCRIPTION
    Writes a local wrapper script containing the registration key and points a
    scheduled task at that wrapper. The wrapper is created under the install
    directory, not in this git repository. Do not commit it.

    This script refuses the placeholder key CHANGE_ME.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install-kite-agent.ps1 `
        -RelayAddr "wss://tunnel.example.com/agent" `
        -RegistrationKey "CHANGE_ME" `
        -AgentName "my-pc"
#>
param(
    [string]$InstallDir = "$env:ProgramFiles\kite",
    [string]$BinaryPath = "",
    [string]$RelayAddr = "wss://tunnel.example.com/agent",
    [string]$RegistrationKey = "CHANGE_ME",
    [string]$AgentName = "my-pc",
    [string]$LocalSSHAddr = "127.0.0.1:22",
    [string]$TaskName = "kite-agent"
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RegistrationKey) -or $RegistrationKey -eq "CHANGE_ME") {
    Write-Error "Pass -RegistrationKey with a real secret. The example placeholder CHANGE_ME is refused, and the key must not be committed."
}
if ([string]::IsNullOrWhiteSpace($RelayAddr) -or $RelayAddr -like "*tunnel.example.com*") {
    Write-Error "Pass -RelayAddr with your relay URL (wss://...). tunnel.example.com is only a placeholder."
}
if ([string]::IsNullOrWhiteSpace($AgentName)) {
    Write-Error "Pass -AgentName. It must match the Host alias in your SSH config."
}

if (-not $BinaryPath) {
    $cmd = Get-Command kite-agent.exe -ErrorAction SilentlyContinue
    if ($cmd) {
        $BinaryPath = $cmd.Source
    }
}
if (-not $BinaryPath -or -not (Test-Path -LiteralPath $BinaryPath)) {
    Write-Error "kite-agent.exe was not found. Pass -BinaryPath."
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$destExe = Join-Path $InstallDir "kite-agent.exe"
Copy-Item -LiteralPath $BinaryPath -Destination $destExe -Force

$wrapper = Join-Path $InstallDir "start-kite-agent.cmd"
# Quote for cmd.exe. The key is written only to this local file.
$lines = @(
    "@echo off",
    "setlocal",
    "set `"RELAY_ADDR=$RelayAddr`"",
    "set `"REGISTRATION_KEY=$RegistrationKey`"",
    "set `"AGENT_NAME=$AgentName`"",
    "set `"LOCAL_SSH_ADDR=$LocalSSHAddr`"",
    "`"$destExe`""
)
Set-Content -LiteralPath $wrapper -Value $lines -Encoding ASCII

$action = New-ScheduledTaskAction -Execute $wrapper
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null

Write-Host "Installed $destExe"
Write-Host "Scheduled task '$TaskName' runs $wrapper at logon."
Write-Host "The wrapper holds the registration key. Leave it out of git."
