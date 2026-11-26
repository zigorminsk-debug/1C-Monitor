<#
.SYNOPSIS
    Устанавливает и регистрирует системную службу Windows «1C Server Watchdog» (LocalSystem) с запросом UAC.
#>
[CmdletBinding()]
param(
    [string]$Endpoint = "127.0.0.1:1545",
    [string]$AgentEndpoint = "127.0.0.1:1540",
    [string]$ClusterUser = "",
    [string]$ClusterPassword = "",
    [string]$GitHubToken = "",
    [switch]$DryRun,
    [switch]$Silent
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$scriptDir = $PSScriptRoot
$repoRoot = Split-Path -Parent $scriptDir

$candidates = @(
    (Join-Path $scriptDir "1C-Watchdog.exe"),
    (Join-Path $repoRoot "artifacts\OneCWatchdog-win-x64\1C-Watchdog.exe"),
    (Join-Path $repoRoot "artifacts\OneCServerMonitor-win-x64\1C-Watchdog.exe")
)

$exePath = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $exePath) {
    throw "Не найден файл 1C-Watchdog.exe. Поместите скрипт рядом с 1C-Watchdog.exe или выполните .\scripts\publish.ps1."
}

$argsList = @("--setup")
if ($Silent) {
    $argsList += "--non-interactive"
}
if ($Endpoint) { $argsList += @("--endpoint", $Endpoint) }
if ($AgentEndpoint) { $argsList += @("--agent-endpoint", $AgentEndpoint) }
if ($ClusterUser) { $argsList += @("--cluster-user", $ClusterUser) }
if ($ClusterPassword) { $argsList += @("--cluster-pwd", $ClusterPassword) }
if ($GitHubToken) { $argsList += @("--github-token", $GitHubToken) }
if ($DryRun) { $argsList += "--dry-run" }

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "Запрос повышения прав администратора (UAC) для установки службы 1C Watchdog..." -ForegroundColor Yellow
    $p = Start-Process -FilePath $exePath -ArgumentList $argsList -Verb RunAs -PassThru -Wait
    exit $p.ExitCode
}

& $exePath @argsList
exit $LASTEXITCODE
