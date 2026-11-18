#Requires -Version 5.1
#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Install kite-agent as a Windows service.

.DESCRIPTION
    Calls `kite-agent install`. The service starts at boot, including when
    nobody is logged on, and it does not open a console window.

    Settings are passed to the installer through environment variables. The
    installer writes them to %ProgramData%\kite\config.yaml (or -ConfigPath)
    and does not put the registration key on the service command line.
    This script does not write a wrapper and does not register a scheduled task.

    The placeholder key CHANGE_ME and the placeholder host tunnel.example.com
    are refused. Do not commit the generated config file.

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
    [string]$ConfigPath = ""
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
    $beside = Join-Path $PSScriptRoot "kite-agent.exe"
    if (Test-Path -LiteralPath $beside) {
        $BinaryPath = $beside
    }
}
if (-not $BinaryPath) {
    $cmd = Get-Command kite-agent.exe -ErrorAction SilentlyContinue
    if ($cmd) {
        $BinaryPath = $cmd.Source
    }
}
if (-not $BinaryPath) {
    $installed = Join-Path $InstallDir "kite-agent.exe"
    if (Test-Path -LiteralPath $installed) {
        $BinaryPath = $installed
    }
}
if (-not $BinaryPath -or -not (Test-Path -LiteralPath $BinaryPath)) {
    Write-Error "kite-agent.exe was not found. Pass -BinaryPath."
}

$previous = @{
    RELAY_ADDR       = [Environment]::GetEnvironmentVariable("RELAY_ADDR", "Process")
    REGISTRATION_KEY = [Environment]::GetEnvironmentVariable("REGISTRATION_KEY", "Process")
    AGENT_NAME       = [Environment]::GetEnvironmentVariable("AGENT_NAME", "Process")
    LOCAL_SSH_ADDR   = [Environment]::GetEnvironmentVariable("LOCAL_SSH_ADDR", "Process")
}

$env:RELAY_ADDR = $RelayAddr
$env:REGISTRATION_KEY = $RegistrationKey
$env:AGENT_NAME = $AgentName
$env:LOCAL_SSH_ADDR = $LocalSSHAddr

$installArgs = @("install", "--install-dir", $InstallDir)
if (-not [string]::IsNullOrWhiteSpace($ConfigPath)) {
    $installArgs += @("--config", $ConfigPath)
}

try {
    & $BinaryPath @installArgs
    $code = $LASTEXITCODE
    if ($null -eq $code) {
        $code = 0
    }
} finally {
    foreach ($name in @("RELAY_ADDR", "REGISTRATION_KEY", "AGENT_NAME", "LOCAL_SSH_ADDR")) {
        $prior = $previous[$name]
        if ($null -eq $prior) {
            Remove-Item "Env:$name" -ErrorAction SilentlyContinue
        } else {
            Set-Item "Env:$name" $prior
        }
    }
}

exit $code
