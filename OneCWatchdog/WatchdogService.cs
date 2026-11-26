using System.Security.Principal;
using Microsoft.Extensions.Hosting;
using OneCMonitor.Core;

namespace OneCWatchdog;

public sealed class WatchdogService : BackgroundService
{
    private readonly IHostApplicationLifetime _lifetime;
    private readonly WatchdogLogger _logger;
    private readonly RacClient _rac = new();
    private readonly Dictionary<string, DateTime> _blockedSince = new(StringComparer.OrdinalIgnoreCase);
    private readonly bool _isServiceMode;
    private DateTime _lastUpdateCheckUtc = DateTime.MinValue;
    private DateTime _lastStatusLogUtc = DateTime.MinValue;

    public WatchdogService(IHostApplicationLifetime lifetime, WatchdogLogger logger, bool isServiceMode)
    {
        _lifetime = lifetime;
        _logger = logger;
        _isServiceMode = isServiceMode;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var identity = OperatingSystem.IsWindows() ? GetCurrentIdentityName() : Environment.UserName;
        var version = GitHubAutoUpdater.GetCurrentVersion();
        var config = WatchdogConfig.Load();
        if (!File.Exists(WatchdogConfig.DefaultConfigPath))
            config.Save();

        _logger.Info(
            $"Запуск 1C Watchdog v{version} (режим={(_isServiceMode ? "Windows Service" : "Console")}, учётная запись={identity}, конфиг={WatchdogConfig.DefaultConfigPath})",
            eventLog: true);

        _logger.Info(
            $"Политика: интервал={config.CheckIntervalSeconds}с, DryRun={config.DryRun}, " +
            $"дубли={config.Policy.TerminateDuplicates} (мин.возраст={config.Policy.DuplicateMinAgeSeconds}с, хранить={config.Policy.DuplicateKeepNewestCount}), " +
            $"зависшие={config.Policy.TerminateHung} (вызов>={config.Policy.HungMaxCurrentCallMinutes}м, СУБД>={config.Policy.HungMaxDbCallMinutes}м, блок>={config.Policy.HungMaxBlockedMinutes}м, сон>={config.Policy.HungMaxSleepingMinutes}м), " +
            $"игнор Конфигуратора={config.Policy.IgnoreConfigurator}, игнор фоновых={config.Policy.IgnoreBackgroundJobs}, автообновление={config.AutoUpdateEnabled}");

        await Task.Delay(TimeSpan.FromSeconds(3), stoppingToken);

        while (!stoppingToken.IsCancellationRequested)
        {
            config = WatchdogConfig.Load();
            try
            {
                await RunWatchdogCycleAsync(config, stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex)
            {
                _logger.Error($"Ошибка цикла мониторинга: {ex.Message}");
            }

            try
            {
                if (await CheckAndApplyAutoUpdateIfDueAsync(config, stoppingToken))
                {
                    _lifetime.StopApplication();
                    break;
                }
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex)
            {
                _logger.Warn($"Ошибка проверки автообновления: {ex.Message}", eventLog: false);
            }

            try
            {
                await Task.Delay(TimeSpan.FromSeconds(Math.Max(5, config.CheckIntervalSeconds)), stoppingToken);
            }
            catch (OperationCanceledException)
            {
                break;
            }
        }

        _logger.Info("1C Watchdog остановлен.", eventLog: true);
    }

    private async Task RunWatchdogCycleAsync(WatchdogConfig config, CancellationToken stoppingToken)
    {
        var racOptions = config.ToRacOptions();
        var candidates = _rac.FindRacCandidates(config.RacPath);
        if (candidates.Count == 0)
        {
            _logger.Warn("rac.exe/ras.exe не найдены. Укажите путь в watchdog.json или запустите --setup.", eventLog: false);
            return;
        }

        RacSnapshot? snapshot = null;
        string? connectedRac = null;
        string? connectedEndpoint = null;
        Exception? lastError = null;

        foreach (var racPath in candidates)
        {
            foreach (var endpoint in RacClient.EndpointCandidates(config.Endpoint))
            {
                try
                {
                    snapshot = await _rac.ReadRacAsync(racPath, endpoint, racOptions, msg => _logger.Info(msg), stoppingToken);
                    connectedRac = racPath;
                    connectedEndpoint = endpoint;
                    break;
                }
                catch (Exception ex)
                {
                    lastError = ex;
                }
            }
            if (snapshot != null) break;
        }

        if (snapshot == null && config.AutoStartRas)
        {
            var racPath = candidates[0];
            try
            {
                _logger.Warn($"RAS не отвечает ({lastError?.Message}). Попытка запуска ras.exe…", eventLog: false);
                var readyEndpoint = await RacClient.EnsureRasRunningAsync(racPath, racOptions, msg => _logger.Info(msg), stoppingToken);
                snapshot = await _rac.ReadRacAsync(racPath, readyEndpoint, racOptions, msg => _logger.Info(msg), stoppingToken);
                connectedRac = racPath;
                connectedEndpoint = readyEndpoint;
            }
            catch (Exception rasEx)
            {
                _logger.Error($"Не удалось подключиться к RAC/RAS после попытки запуска: {rasEx.Message}", eventLog: false);
                return;
            }
        }

        if (snapshot == null || connectedRac == null || connectedEndpoint == null)
        {
            _logger.Warn($"RAC недоступен: {lastError?.Message}", eventLog: false);
            return;
        }

        if (!string.Equals(config.RacPath, connectedRac, StringComparison.OrdinalIgnoreCase) ||
            !string.Equals(config.Endpoint, connectedEndpoint, StringComparison.OrdinalIgnoreCase))
        {
            config.RacPath = connectedRac;
            config.Endpoint = connectedEndpoint;
            config.Save();
            _logger.Info($"Обновлён активный RAC/RAS: {connectedRac} @ {connectedEndpoint}");
        }

        var now = DateTime.Now;
        var targets = SessionAnalyzer.EvaluateWatchdogTargets(snapshot.Sessions, config.Policy, _blockedSince, now);

        if (DateTime.UtcNow - _lastStatusLogUtc >= TimeSpan.FromMinutes(15) || targets.Count > 0)
        {
            _lastStatusLogUtc = DateTime.UtcNow;
            _logger.Info($"Проверка кластера {snapshot.ClusterId}: всего сеансов={snapshot.Sessions.Count}, к завершению={targets.Count}");
        }

        foreach (var item in targets)
        {
            var s = item.Session;
            var summary = $"пользователь='{s.UserName}', база='{s.InfoBase}', хост='{s.Host}', приложение='{s.AppId}', PID={s.Pid}, начало='{s.StartedAt}', UUID={s.SessionUuid}. Причина: {item.Reason}";

            if (config.DryRun)
            {
                _logger.Warn($"[DRY-RUN] Завершение пропущено: {summary}", eventLog: false);
                continue;
            }

            try
            {
                await RacClient.TerminateSessionAsync(
                    connectedRac,
                    connectedEndpoint,
                    snapshot.ClusterId,
                    s.SessionUuid,
                    config.ClusterUser,
                    config.ClusterPassword,
                    Math.Max(15, config.RacTimeoutSeconds),
                    stoppingToken);

                _logger.Warn($"Сеанс завершён: {summary}", eventLog: true);
            }
            catch (Exception ex)
            {
                _logger.Error($"Ошибка завершения сеанса ({summary}): {ex.Message}", eventLog: true);
            }
        }
    }

    private async Task<bool> CheckAndApplyAutoUpdateIfDueAsync(WatchdogConfig config, CancellationToken stoppingToken)
    {
        if (!config.AutoUpdateEnabled) return false;

        var interval = TimeSpan.FromHours(Math.Max(1, config.AutoUpdateCheckHours));
        if (_lastUpdateCheckUtc != DateTime.MinValue && DateTime.UtcNow - _lastUpdateCheckUtc < interval)
            return false;

        _lastUpdateCheckUtc = DateTime.UtcNow;

        var updater = new GitHubAutoUpdater(
            assetPrefix: "OneCWatchdog",
            executableName: "1C-Watchdog.exe",
            owner: config.GitHubOwner,
            repository: config.GitHubRepository,
            gitHubToken: config.GitHubToken);

        var result = await updater.CheckForUpdateAsync(cancellationToken: stoppingToken);
        _logger.Info($"Автообновление: {result.Message}");

        if (!result.IsUpdateAvailable) return false;

        var exePath = Environment.ProcessPath;
        if (string.IsNullOrWhiteSpace(exePath) || !OperatingSystem.IsWindows())
        {
            _logger.Warn("Автозамена бинарного файла доступна только при запуске в Windows.");
            return false;
        }

        var stagedDir = await updater.DownloadAndStageUpdateAsync(
            result,
            WatchdogConfig.DefaultUpdateStagingDirectory,
            msg => _logger.Info(msg),
            stoppingToken);

        _logger.Warn(
            $"Запуск установки обновления 1C Watchdog до версии {result.LatestVersion}…",
            eventLog: true);

        GitHubAutoUpdater.LaunchUpdaterAndExit(
            stagedSourceDir: stagedDir,
            targetExePath: exePath,
            executableName: "1C-Watchdog.exe",
            windowsServiceName: _isServiceMode ? WatchdogConfig.ServiceName : null,
            relaunchDesktopApp: !_isServiceMode,
            relaunchArgs: _isServiceMode ? null : new[] { "--console" });

        return true;
    }

    private static string GetCurrentIdentityName()
    {
        if (!OperatingSystem.IsWindows()) return Environment.UserName;
        try { return WindowsIdentity.GetCurrent().Name; }
        catch { return Environment.UserName; }
    }
}
