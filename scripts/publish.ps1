<#
.SYNOPSIS
    Собирает автономные single-file релизные сборки 1C Server Monitor для Windows x64 и x86.
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
$projectPath = Join-Path $repoRoot "OneCMonitor\OneCMonitor.csproj"
$artifactsPath = Join-Path $repoRoot $OutputRoot

if (Test-Path $artifactsPath) {
    Remove-Item -Path $artifactsPath -Recurse -Force
}
New-Item -ItemType Directory -Path $artifactsPath | Out-Null

$checksums = @()

foreach ($rid in $Runtimes) {
    $outDir = Join-Path $artifactsPath "OneCServerMonitor-$rid"
    Write-Host "==> Publishing $rid ($Configuration) -> $outDir" -ForegroundColor Cyan

    dotnet publish $projectPath `
        -c $Configuration `
        -r $rid `
        --self-contained true `
        -p:PublishSingleFile=true `
        -p:IncludeNativeLibrariesForSelfExtract=true `
        -p:DebugType=none `
        -p:DebugSymbols=false `
        -o $outDir

    if ($LASTEXITCODE -ne 0) {
        throw "dotnet publish failed for $rid with exit code $LASTEXITCODE"
    }

    $exePath = Join-Path $outDir "1C-Server-Monitor.exe"
    if (-not (Test-Path $exePath)) {
        throw "Expected executable not found: $exePath"
    }

    $hash = (Get-FileHash -Path $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$hash  OneCServerMonitor-$rid/1C-Server-Monitor.exe"

    $zipPath = Join-Path $artifactsPath "OneCServerMonitor-$rid.zip"
    Compress-Archive -Path "$outDir\*" -DestinationPath $zipPath -Force
    $zipHash = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$zipHash  OneCServerMonitor-$rid.zip"
}

$sumsFile = Join-Path $artifactsPath "SHA256SUMS.txt"
$checksums | Set-Content -Path $sumsFile -Encoding UTF8

Write-Host "`nDone. Checksums saved to $sumsFile" -ForegroundColor Green
Get-Content $sumsFile
