<#
.SYNOPSIS
    Собирает автономные single-file релизные сборки 1C Server Monitor и 1C Watchdog для Windows x64 и x86.
#>
[CmdletBinding()]
param(
    [string]$Configuration = "Release",
    [string[]]$Runtimes = @("win-x64", "win-x86"),
    [string]$OutputRoot = "artifacts"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$monitorProject = Join-Path $repoRoot "OneCMonitor\OneCMonitor.csproj"
$watchdogProject = Join-Path $repoRoot "OneCWatchdog\OneCWatchdog.csproj"
$artifactsPath = Join-Path $repoRoot $OutputRoot

if (Test-Path $artifactsPath) {
    Remove-Item -Path $artifactsPath -Recurse -Force
}
New-Item -ItemType Directory -Path $artifactsPath | Out-Null

$checksums = @()

foreach ($rid in $Runtimes) {
    $monitorOutDir = Join-Path $artifactsPath "OneCServerMonitor-$rid"
    $watchdogOutDir = Join-Path $artifactsPath "OneCWatchdog-$rid"

    Write-Host "==> Publishing 1C Watchdog $rid ($Configuration) -> $watchdogOutDir" -ForegroundColor Cyan
    dotnet publish $watchdogProject `
        -c $Configuration `
        -r $rid `
        --self-contained true `
        -p:PublishSingleFile=true `
        -p:IncludeNativeLibrariesForSelfExtract=true `
        -p:DebugType=none `
        -p:DebugSymbols=false `
        -o $watchdogOutDir

    if ($LASTEXITCODE -ne 0) {
        throw "dotnet publish failed for OneCWatchdog ($rid) with exit code $LASTEXITCODE"
    }

    $watchdogExe = Join-Path $watchdogOutDir "1C-Watchdog.exe"
    if (-not (Test-Path $watchdogExe)) {
        throw "Expected executable not found: $watchdogExe"
    }

    Write-Host "==> Publishing 1C Server Monitor $rid ($Configuration) -> $monitorOutDir" -ForegroundColor Cyan
    dotnet publish $monitorProject `
        -c $Configuration `
        -r $rid `
        --self-contained true `
        -p:PublishSingleFile=true `
        -p:IncludeNativeLibrariesForSelfExtract=true `
        -p:DebugType=none `
        -p:DebugSymbols=false `
        -o $monitorOutDir

    if ($LASTEXITCODE -ne 0) {
        throw "dotnet publish failed for OneCMonitor ($rid) with exit code $LASTEXITCODE"
    }

    $monitorExe = Join-Path $monitorOutDir "1C-Server-Monitor.exe"
    if (-not (Test-Path $monitorExe)) {
        throw "Expected executable not found: $monitorExe"
    }

    # Включаем 1C-Watchdog.exe в комплект монитора для запуска мастера установки службы из UI
    Copy-Item -Path $watchdogExe -Destination (Join-Path $monitorOutDir "1C-Watchdog.exe") -Force

    $monitorHash = (Get-FileHash -Path $monitorExe -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$monitorHash  OneCServerMonitor-$rid/1C-Server-Monitor.exe"

    $watchdogHash = (Get-FileHash -Path $watchdogExe -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$watchdogHash  OneCWatchdog-$rid/1C-Watchdog.exe"

    $monitorZip = Join-Path $artifactsPath "OneCServerMonitor-$rid.zip"
    Compress-Archive -Path "$monitorOutDir\*" -DestinationPath $monitorZip -Force
    $monitorZipHash = (Get-FileHash -Path $monitorZip -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$monitorZipHash  OneCServerMonitor-$rid.zip"

    $watchdogZip = Join-Path $artifactsPath "OneCWatchdog-$rid.zip"
    Compress-Archive -Path "$watchdogOutDir\*" -DestinationPath $watchdogZip -Force
    $watchdogZipHash = (Get-FileHash -Path $watchdogZip -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$watchdogZipHash  OneCWatchdog-$rid.zip"
}

$sumsFile = Join-Path $artifactsPath "SHA256SUMS.txt"
$checksums | Set-Content -Path $sumsFile -Encoding UTF8

Write-Host "`nDone. Checksums saved to $sumsFile" -ForegroundColor Green
Get-Content $sumsFile
