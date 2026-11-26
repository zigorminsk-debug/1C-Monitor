using System.Diagnostics;
using System.IO.Compression;
using System.Net.Http.Headers;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace OneCMonitor.Core;

public sealed record UpdateCheckResult(
    bool IsUpdateAvailable,
    string CurrentVersion,
    string LatestVersion,
    string ReleaseNotes,
    string HtmlUrl,
    string? AssetName,
    string? AssetDownloadUrl,
    string? AssetApiUrl,
    string? ChecksumsApiUrl,
    string? ChecksumsDownloadUrl,
    string Message);

public sealed class GitHubAutoUpdater
{
    private static readonly HttpClient Http = CreateHttpClient();

    public string Owner { get; }
    public string Repository { get; }
    public string AssetPrefix { get; }
    public string ExecutableName { get; }
    public string? GitHubToken { get; }

    public GitHubAutoUpdater(
        string assetPrefix,
        string executableName,
        string owner = "zigorminsk-debug",
        string repository = "1C-Monitor",
        string? gitHubToken = null)
    {
        AssetPrefix = assetPrefix;
        ExecutableName = executableName;
        Owner = string.IsNullOrWhiteSpace(owner) ? "zigorminsk-debug" : owner.Trim();
        Repository = string.IsNullOrWhiteSpace(repository) ? "1C-Monitor" : repository.Trim();
        GitHubToken = string.IsNullOrWhiteSpace(gitHubToken) ? null : gitHubToken.Trim();
    }

    public static string GetCurrentVersion(Assembly? assembly = null)
    {
        var asm = assembly ?? Assembly.GetEntryAssembly() ?? Assembly.GetExecutingAssembly();
        var info = asm.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion;
        if (!string.IsNullOrWhiteSpace(info))
        {
            var clean = info.Split('+')[0].Trim();
            if (clean.Length > 0) return clean;
        }
        var ver = asm.GetName().Version;
        return ver == null ? "0.0.0" : $"{ver.Major}.{ver.Minor}.{Math.Max(0, ver.Build)}";
    }

    public static Version ParseComparableVersion(string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw)) return new Version(0, 0, 0);
        var clean = raw.Trim().TrimStart('v', 'V').Split('+', '-')[0];
        if (Version.TryParse(clean, out var parsed))
            return new Version(parsed.Major, Math.Max(0, parsed.Minor), Math.Max(0, parsed.Build));
        return new Version(0, 0, 0);
    }

    public static string CurrentRuntimeRid() =>
        RuntimeInformation.ProcessArchitecture == Architecture.X86 ? "win-x86" : "win-x64";

    public async Task<UpdateCheckResult> CheckForUpdateAsync(string? currentVersion = null, CancellationToken cancellationToken = default)
    {
        var current = string.IsNullOrWhiteSpace(currentVersion) ? GetCurrentVersion() : currentVersion;
        var apiUrl = $"https://api.github.com/repos/{Owner}/{Repository}/releases/latest";

        using var req = new HttpRequestMessage(HttpMethod.Get, apiUrl);
        ConfigureGitHubHeaders(req, "application/vnd.github+json", current);

        using var resp = await Http.SendAsync(req, cancellationToken);
        if (resp.StatusCode == System.Net.HttpStatusCode.NotFound)
        {
            return new UpdateCheckResult(false, current, current, "", "", null, null, null, null, null,
                "На GitHub пока нет опубликованных релизов (или репозиторий приватный без токена доступа).");
        }
        if ((int)resp.StatusCode == 403 || (int)resp.StatusCode == 429)
        {
            return new UpdateCheckResult(false, current, current, "", "", null, null, null, null, null,
                "Превышен лимит запросов GitHub API или требуется токен доступа.");
        }
        resp.EnsureSuccessStatusCode();

        var json = await resp.Content.ReadAsStringAsync(cancellationToken);
        var release = JsonSerializer.Deserialize<GitHubReleaseDto>(json)
            ?? throw new Exception("Пустой ответ GitHub Releases API");

        var latest = (release.TagName ?? release.Name ?? "").Trim().TrimStart('v', 'V');
        if (string.IsNullOrWhiteSpace(latest)) latest = current;

        var currentVer = ParseComparableVersion(current);
        var latestVer = ParseComparableVersion(latest);
        var isNewer = latestVer > currentVer;

        var rid = CurrentRuntimeRid();
        var assets = release.Assets ?? new List<GitHubAssetDto>();

        var targetAsset =
            assets.FirstOrDefault(a => string.Equals(a.Name, $"{AssetPrefix}-{rid}.zip", StringComparison.OrdinalIgnoreCase)) ??
            assets.FirstOrDefault(a => a.Name.Contains(AssetPrefix, StringComparison.OrdinalIgnoreCase) && a.Name.Contains(rid, StringComparison.OrdinalIgnoreCase) && a.Name.EndsWith(".zip", StringComparison.OrdinalIgnoreCase)) ??
            assets.FirstOrDefault(a => a.Name.Contains(ExecutableName, StringComparison.OrdinalIgnoreCase) && a.Name.Contains(rid, StringComparison.OrdinalIgnoreCase)) ??
            assets.FirstOrDefault(a => a.Name.Contains(AssetPrefix, StringComparison.OrdinalIgnoreCase) && a.Name.EndsWith(".zip", StringComparison.OrdinalIgnoreCase));

        var checksumAsset = assets.FirstOrDefault(a =>
            string.Equals(a.Name, "SHA256SUMS.txt", StringComparison.OrdinalIgnoreCase) ||
            string.Equals(a.Name, "SHA256.txt", StringComparison.OrdinalIgnoreCase));

        var msg = isNewer
            ? (targetAsset != null
                ? $"Доступна новая версия {latest} (текущая {current}, пакет {targetAsset.Name})"
                : $"Доступна версия {latest}, но архив для {rid} не найден в релизе")
            : $"Установлена актуальная версия {current} (последняя на GitHub: {latest})";

        return new UpdateCheckResult(
            isNewer && targetAsset != null,
            current,
            latest,
            release.Body ?? "",
            release.HtmlUrl ?? "",
            targetAsset?.Name,
            targetAsset?.BrowserDownloadUrl,
            targetAsset?.Url,
            checksumAsset?.Url,
            checksumAsset?.BrowserDownloadUrl,
            msg);
    }

    public async Task<string> DownloadAndStageUpdateAsync(UpdateCheckResult update, string stagingRoot, Action<string>? log = null, CancellationToken cancellationToken = default)
    {
        if (!update.IsUpdateAvailable || string.IsNullOrWhiteSpace(update.AssetName))
            throw new InvalidOperationException("Нет доступного обновления для загрузки");

        var versionDir = Path.Combine(stagingRoot, $"v{update.LatestVersion}_{Guid.NewGuid():N}");
        var extractDir = Path.Combine(versionDir, "extracted");
        Directory.CreateDirectory(extractDir);

        var downloadedFile = Path.Combine(versionDir, update.AssetName);
        log?.Invoke($"Загрузка обновления {update.AssetName}…");
        await DownloadAssetAsync(update.AssetApiUrl, update.AssetDownloadUrl, downloadedFile, update.CurrentVersion, cancellationToken);

        if (!string.IsNullOrWhiteSpace(update.ChecksumsApiUrl) || !string.IsNullOrWhiteSpace(update.ChecksumsDownloadUrl))
        {
            var sumsPath = Path.Combine(versionDir, "SHA256SUMS.txt");
            try
            {
                await DownloadAssetAsync(update.ChecksumsApiUrl, update.ChecksumsDownloadUrl, sumsPath, update.CurrentVersion, cancellationToken);
                VerifyChecksumIfListed(downloadedFile, update.AssetName, sumsPath, log);
            }
            catch (Exception ex) when (ex is not CryptographicException)
            {
                log?.Invoke("Не удалось проверить SHA256SUMS.txt: " + ex.Message);
            }
        }

        if (downloadedFile.EndsWith(".zip", StringComparison.OrdinalIgnoreCase))
        {
            ZipFile.ExtractToDirectory(downloadedFile, extractDir, true);
        }
        else
        {
            File.Copy(downloadedFile, Path.Combine(extractDir, ExecutableName), true);
        }

        var candidateExe = Directory.EnumerateFiles(extractDir, ExecutableName, SearchOption.AllDirectories).FirstOrDefault()
            ?? throw new Exception($"В архиве {update.AssetName} не найден исполняемый файл {ExecutableName}");

        var sourceDir = Path.GetDirectoryName(candidateExe)!;
        log?.Invoke($"Обновление загружено и проверено: {candidateExe}");
        return sourceDir;
    }

    public static void LaunchUpdaterAndExit(
        string stagedSourceDir,
        string targetExePath,
        string executableName,
        string? windowsServiceName = null,
        bool relaunchDesktopApp = true,
        string[]? relaunchArgs = null)
    {
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Автозамена исполняемого файла поддерживается только в Windows.");

        var targetDir = Path.GetDirectoryName(targetExePath)!;
        var currentPid = Environment.ProcessId;
        var scriptPath = Path.Combine(Path.GetDirectoryName(stagedSourceDir)!, "apply-update.ps1");
        var logPath = Path.Combine(Path.GetDirectoryName(stagedSourceDir)!, "apply-update.log");
        var relaunchArgString = relaunchArgs is { Length: > 0 }
            ? string.Join(" ", relaunchArgs.Select(a => $"\"{a.Replace("\"", "`\"")}\""))
            : "";

        var script = $$"""
$ErrorActionPreference = 'Stop'
$targetExe = '{{EscapePs(targetExePath)}}'
$targetDir = '{{EscapePs(targetDir)}}'
$sourceDir = '{{EscapePs(stagedSourceDir)}}'
$exeName = '{{EscapePs(executableName)}}'
$serviceName = '{{EscapePs(windowsServiceName ?? "")}}'
$waitPid = {{currentPid}}
$relaunch = ${{relaunchDesktopApp.ToString().ToLowerInvariant()}}
$relaunchArgs = '{{EscapePs(relaunchArgString)}}'
$logFile = '{{EscapePs(logPath)}}'

function Write-UpdateLog([string]$msg) {
    Add-Content -Path $logFile -Value ("[{0:yyyy-MM-dd HH:mm:ss}] {1}" -f (Get-Date), $msg) -Encoding UTF8
}

Write-UpdateLog "Starting update for $targetExe (PID=$waitPid, Service=$serviceName)"

if ($serviceName) {
    try {
        Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
    } catch {
        Write-UpdateLog "Stop-Service warning: $_"
    }
}

$deadline = (Get-Date).AddSeconds(45)
while ((Get-Date) -lt $deadline) {
    $proc = Get-Process -Id $waitPid -ErrorAction SilentlyContinue
    if (-not $proc) { break }
    Start-Sleep -Milliseconds 500
}

if (Get-Process -Id $waitPid -ErrorAction SilentlyContinue) {
    Write-UpdateLog "Process $waitPid still alive, forcing stop"
    Stop-Process -Id $waitPid -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
}

$backupExe = "$targetExe.bak"
if (Test-Path $targetExe) {
    Copy-Item -Path $targetExe -Destination $backupExe -Force
}

$newExe = Join-Path $sourceDir $exeName
$copied = $false
for ($i = 1; $i -le 15; $i++) {
    try {
        Copy-Item -Path $newExe -Destination $targetExe -Force
        $copied = $true
        break
    } catch {
        Write-UpdateLog "Copy attempt $i failed: $_"
        Start-Sleep -Milliseconds 800
    }
}

if (-not $copied) {
    Write-UpdateLog "Failed to replace $targetExe, restoring backup"
    if (Test-Path $backupExe) { Copy-Item -Path $backupExe -Destination $targetExe -Force }
    if ($serviceName) { Start-Service -Name $serviceName -ErrorAction SilentlyContinue }
    exit 1
}

Write-UpdateLog "Replaced $targetExe successfully"

if ($serviceName) {
    try {
        Start-Service -Name $serviceName -ErrorAction Stop
        Write-UpdateLog "Service $serviceName started"
    } catch {
        Write-UpdateLog "Start-Service failed ($_); rolling back"
        if (Test-Path $backupExe) {
            Copy-Item -Path $backupExe -Destination $targetExe -Force
            Start-Service -Name $serviceName -ErrorAction SilentlyContinue
        }
        exit 1
    }
} elseif ($relaunch) {
    if ($relaunchArgs) {
        Start-Process -FilePath $targetExe -ArgumentList $relaunchArgs
    } else {
        Start-Process -FilePath $targetExe
    }
    Write-UpdateLog "Relaunched $targetExe"
}
""";

        File.WriteAllText(scriptPath, script, new UTF8Encoding(true));

        var psi = new ProcessStartInfo("powershell.exe")
        {
            UseShellExecute = false,
            CreateNoWindow = true
        };
        psi.ArgumentList.Add("-NoProfile");
        psi.ArgumentList.Add("-NonInteractive");
        psi.ArgumentList.Add("-ExecutionPolicy");
        psi.ArgumentList.Add("Bypass");
        psi.ArgumentList.Add("-File");
        psi.ArgumentList.Add(scriptPath);
        Process.Start(psi);
    }

    private async Task DownloadAssetAsync(string? apiUrl, string? browserUrl, string destinationPath, string currentVersion, CancellationToken cancellationToken)
    {
        var url = !string.IsNullOrWhiteSpace(GitHubToken) && !string.IsNullOrWhiteSpace(apiUrl)
            ? apiUrl
            : browserUrl ?? apiUrl ?? throw new Exception("Не указан URL для скачивания файла");

        using var req = new HttpRequestMessage(HttpMethod.Get, url);
        var accept = string.Equals(url, apiUrl, StringComparison.OrdinalIgnoreCase)
            ? "application/octet-stream"
            : "*/*";
        ConfigureGitHubHeaders(req, accept, currentVersion);

        using var resp = await Http.SendAsync(req, HttpCompletionOption.ResponseHeadersRead, cancellationToken);
        resp.EnsureSuccessStatusCode();
        await using var stream = await resp.Content.ReadAsStreamAsync(cancellationToken);
        await using var fs = new FileStream(destinationPath, FileMode.Create, FileAccess.Write, FileShare.None);
        await stream.CopyToAsync(fs, cancellationToken);
    }

    private void ConfigureGitHubHeaders(HttpRequestMessage req, string accept, string currentVersion)
    {
        req.Headers.UserAgent.ParseAdd($"1C-Server-Monitor/{currentVersion}");
        req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue(accept));
        req.Headers.Add("X-GitHub-Api-Version", "2022-11-28");
        if (!string.IsNullOrWhiteSpace(GitHubToken))
            req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", GitHubToken);
    }

    private static void VerifyChecksumIfListed(string filePath, string assetName, string sumsPath, Action<string>? log)
    {
        var lines = File.ReadAllLines(sumsPath);
        var expected = lines
            .Select(l => l.Trim())
            .Where(l => !string.IsNullOrWhiteSpace(l))
            .Select(l => l.Split(new[] { ' ', '\t' }, StringSplitOptions.RemoveEmptyEntries))
            .FirstOrDefault(parts => parts.Length >= 2 && Path.GetFileName(parts[1]).Equals(assetName, StringComparison.OrdinalIgnoreCase))
            ?[0];

        if (string.IsNullOrWhiteSpace(expected)) return;

        using var sha = SHA256.Create();
        using var fs = File.OpenRead(filePath);
        var actual = Convert.ToHexString(sha.ComputeHash(fs)).ToLowerInvariant();
        if (!string.Equals(actual, expected.Trim().ToLowerInvariant(), StringComparison.OrdinalIgnoreCase))
            throw new CryptographicException($"Контрольная сумма SHA-256 файла {assetName} не совпадает: ожидалось {expected}, получено {actual}");

        log?.Invoke($"SHA-256 файла {assetName} подтверждён: {actual}");
    }

    private static string EscapePs(string value) => value.Replace("'", "''");

    private static HttpClient CreateHttpClient()
    {
        var client = new HttpClient { Timeout = TimeSpan.FromMinutes(3) };
        return client;
    }

    private sealed class GitHubReleaseDto
    {
        [JsonPropertyName("tag_name")] public string? TagName { get; set; }
        [JsonPropertyName("name")] public string? Name { get; set; }
        [JsonPropertyName("body")] public string? Body { get; set; }
        [JsonPropertyName("html_url")] public string? HtmlUrl { get; set; }
        [JsonPropertyName("assets")] public List<GitHubAssetDto>? Assets { get; set; }
    }

    private sealed class GitHubAssetDto
    {
        [JsonPropertyName("name")] public string Name { get; set; } = "";
        [JsonPropertyName("url")] public string Url { get; set; } = "";
        [JsonPropertyName("browser_download_url")] public string BrowserDownloadUrl { get; set; } = "";
    }
}
