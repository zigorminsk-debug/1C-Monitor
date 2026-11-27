using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.CompilerServices;
using System.Text;
using System.Windows.Input;
using Avalonia.Media;
using Avalonia.Threading;
using OneCMonitor.Core;

namespace OneCWatchdog;

public sealed class WatchdogGuiViewModel : INotifyPropertyChanged, IDisposable
{
    public event PropertyChangedEventHandler? PropertyChanged;

    private readonly CancellationTokenSource _stop = new();
    private readonly RacClient _rac = new();
    private readonly NotificationService _notifications = new();
    private readonly StringBuilder _operationLog = new();
    private UpdateCheckResult? _pendingUpdate;
    private bool _busy, _updateBusy;

    public Func<string, Task<bool>>? ConfirmActionAsync { get; set; }

    public ObservableCollection<SessionTerminationCandidate> PreviewTargets { get; } = new();

    public IReadOnlyList<string> GroupingModes { get; } = new[]
    {
        DuplicateGroupingModes.Full,
        DuplicateGroupingModes.UserAndBase,
        DuplicateGroupingModes.UserBaseApp
    };

    public IReadOnlyList<string> LogLevels { get; } = new[] { "Все", "WARN", "ERROR" };

    public ICommand ElevateCommand { get; }
    public ICommand RefreshServiceStatusCommand { get; }
    public ICommand InstallServiceCommand { get; }
    public ICommand StartServiceCommand { get; }
    public ICommand StopServiceCommand { get; }
    public ICommand RestartServiceCommand { get; }
    public ICommand UninstallServiceCommand { get; }
    public ICommand SaveConfigCommand { get; }
    public ICommand SaveAndRestartServiceCommand { get; }
    public ICommand ReloadConfigCommand { get; }
    public ICommand FindRacCommand { get; }
    public ICommand StartRasCommand { get; }
    public ICommand TestConnectionCommand { get; }
    public ICommand DryRunEvaluateCommand { get; }
    public ICommand SendTestNotificationCommand { get; }
    public ICommand CheckUpdatesCommand { get; }
    public ICommand ApplyUpdateCommand { get; }
    public ICommand RefreshLogsCommand { get; }
    public ICommand OpenLogsFolderCommand { get; }
    public ICommand OpenCurrentLogFileCommand { get; }

    private bool _isAdministrator, _serviceExists, _serviceIsRunning, _installInPlace;
    private string _serviceStatusText = "Проверка состояния службы…";
    private IBrush _serviceStatusColor = Brushes.Goldenrod;
    private string _statusMessage = "Готов к настройке";
    private string _operationLogText = "";

    private string _racPath = "", _endpoint = "127.0.0.1:1545", _agentEndpoint = "127.0.0.1:1540", _clusterUser = "", _clusterPassword = "";
    private decimal _racTimeoutSeconds = 60;
    private bool _autoStartRas = true, _restartHungRas = true, _monitorAllClusters;
    private string _targetClustersText = "", _connectionTestResult = "Проверка подключения не выполнялась";

    private decimal _checkIntervalSeconds = 30;
    private bool _dryRun;
    private bool _terminateDuplicates = true, _terminateHung = true, _terminateHungOnlyWhenBlockingOthers;
    private string _duplicateGroupingMode = DuplicateGroupingModes.Full;
    private decimal _duplicateKeepNewestCount = 1, _duplicateMinAgeSeconds = 30;
    private decimal _hungMaxCurrentCallMinutes = 60, _hungMaxDbCallMinutes = 45, _hungMaxBlockedMinutes = 15, _hungMaxSleepingMinutes = 180;
    private decimal _consecutiveChecksRequired = 1;
    private bool _ignoreConfigurator = true, _ignoreBackgroundJobs = true;
    private string _excludedUsersText = "", _excludedInfoBasesText = "", _targetInfoBasesText = "";
    private string _previewSessionsSummary = "Нажмите «Проверить правила (Dry-Run)», чтобы увидеть, какие сеансы попали бы под завершение.";

    private bool _notificationsEnabled, _notifyOnWatchdogTermination = true, _notifyOnUpdates = true;
    private decimal _notificationCooldownSeconds = 60;
    private bool _telegramEnabled, _webhookEnabled, _emailEnabled, _smtpUseSsl = true;
    private string _telegramChatId = "", _telegramBotToken = "", _webhookUrl = "", _smtpHost = "", _smtpUser = "", _smtpPassword = "", _emailFrom = "", _emailTo = "";
    private decimal _smtpPort = 587;
    private string _notificationStatus = "";

    private bool _autoUpdateEnabled = true, _includePrereleases, _isUpdateAvailable, _hasReleaseNotes;
    private decimal _autoUpdateCheckHours = 6;
    private string _gitHubOwner = "zigorminsk-debug", _gitHubRepository = "1C-Monitor", _gitHubToken = "";
    private string _updateStatus = $"Версия {GitHubAutoUpdater.GetCurrentVersion()} • проверка обновлений не выполнялась";
    private string _availableReleaseNotes = "";

    private string _logDirectory = WatchdogConfig.DefaultLogDirectory;
    private decimal _logRetentionDays = 30;
    private string _rawLogText = "", _filteredLogText = "", _logFilterText = "", _selectedLogLevel = "Все";
    private bool _autoRefreshLogs = true;

    public WatchdogGuiViewModel()
    {
        _isAdministrator = WatchdogSetup.IsAdministrator();

        ElevateCommand = new WatchdogRelayCommand(() => WatchdogSetup.RestartElevatedForGui("--gui"));
        RefreshServiceStatusCommand = new WatchdogRelayCommand(async () => await RefreshServiceStateAsync());
        InstallServiceCommand = new WatchdogRelayCommand(async () => await InstallOrUpdateServiceAsync());
        StartServiceCommand = new WatchdogRelayCommand(async () => await ControlServiceAsync("start"));
        StopServiceCommand = new WatchdogRelayCommand(async () => await ControlServiceAsync("stop"));
        RestartServiceCommand = new WatchdogRelayCommand(async () => await ControlServiceAsync("restart"));
        UninstallServiceCommand = new WatchdogRelayCommand(async () => await UninstallServiceAsync());
        SaveConfigCommand = new WatchdogRelayCommand(() => SaveConfigToDisk());
        SaveAndRestartServiceCommand = new WatchdogRelayCommand(async () => await SaveAndRestartAsync());
        ReloadConfigCommand = new WatchdogRelayCommand(() => { LoadFromConfig(WatchdogConfig.Load()); StatusMessage = "Конфигурация перечитана с диска"; });
        FindRacCommand = new WatchdogRelayCommand(FindRac);
        StartRasCommand = new WatchdogRelayCommand(async () => await StartRasAsync());
        TestConnectionCommand = new WatchdogRelayCommand(async () => await TestClusterConnectionAsync(evaluateDryRun: false));
        DryRunEvaluateCommand = new WatchdogRelayCommand(async () => await TestClusterConnectionAsync(evaluateDryRun: true));
        SendTestNotificationCommand = new WatchdogRelayCommand(async () => await SendTestNotificationAsync());
        CheckUpdatesCommand = new WatchdogRelayCommand(async () => await CheckForUpdatesAsync());
        ApplyUpdateCommand = new WatchdogRelayCommand(async () => await ApplyAvailableUpdateAsync());
        RefreshLogsCommand = new WatchdogRelayCommand(RefreshLogs);
        OpenLogsFolderCommand = new WatchdogRelayCommand(() => OpenPath(LogDirectory));
        OpenCurrentLogFileCommand = new WatchdogRelayCommand(OpenLatestLogFile);

        LoadFromConfig(WatchdogConfig.Load());
        RefreshLogs();
        _ = RefreshServiceStateAsync();
        _ = LogRefreshLoopAsync();
    }

    public string WindowTitle => $"{Environment.MachineName} — 1C Watchdog v{GitHubAutoUpdater.GetCurrentVersion()} (Настройка службы)";
    public string HeaderTitle => $"1C Server Watchdog v{GitHubAutoUpdater.GetCurrentVersion()}";
    public string ConfigFilePath => WatchdogConfig.DefaultConfigPath;
    public string InstallDirectoryPath => WatchdogSetup.DefaultInstallDir;
    public bool IsAdministrator { get => _isAdministrator; private set { if (Set(ref _isAdministrator, value)) OnPropertyChanged(nameof(NeedsElevation)); } }
    public bool NeedsElevation => !IsAdministrator && OperatingSystem.IsWindows();
    public string PrivilegeStatusText => IsAdministrator ? "Права: Администратор (UAC подтверждён)" : "Права: Ограниченные (для установки/управления службой нужен UAC)";
    public bool ServiceExists { get => _serviceExists; private set => Set(ref _serviceExists, value); }
    public bool ServiceIsRunning { get => _serviceIsRunning; private set => Set(ref _serviceIsRunning, value); }
    public string ServiceStatusText { get => _serviceStatusText; private set => Set(ref _serviceStatusText, value); }
    public IBrush ServiceStatusColor { get => _serviceStatusColor; private set => Set(ref _serviceStatusColor, value); }
    public bool InstallInPlace { get => _installInPlace; set => Set(ref _installInPlace, value); }
    public string StatusMessage { get => _statusMessage; private set => Set(ref _statusMessage, value); }
    public string OperationLogText { get => _operationLogText; private set => Set(ref _operationLogText, value); }

    public string RacPath { get => _racPath; set => Set(ref _racPath, value); }
    public string Endpoint { get => _endpoint; set => Set(ref _endpoint, value); }
    public string AgentEndpoint { get => _agentEndpoint; set => Set(ref _agentEndpoint, value); }
    public string ClusterUser { get => _clusterUser; set => Set(ref _clusterUser, value); }
    public string ClusterPassword { get => _clusterPassword; set => Set(ref _clusterPassword, value); }
    public decimal RacTimeoutSeconds { get => _racTimeoutSeconds; set => Set(ref _racTimeoutSeconds, value); }
    public bool AutoStartRas { get => _autoStartRas; set => Set(ref _autoStartRas, value); }
    public bool RestartHungRas { get => _restartHungRas; set => Set(ref _restartHungRas, value); }
    public bool MonitorAllClusters { get => _monitorAllClusters; set => Set(ref _monitorAllClusters, value); }
    public string TargetClustersText { get => _targetClustersText; set => Set(ref _targetClustersText, value); }
    public string ConnectionTestResult { get => _connectionTestResult; private set => Set(ref _connectionTestResult, value); }

    public decimal CheckIntervalSeconds { get => _checkIntervalSeconds; set => Set(ref _checkIntervalSeconds, value); }
    public bool DryRun { get => _dryRun; set => Set(ref _dryRun, value); }
    public bool TerminateDuplicates { get => _terminateDuplicates; set => Set(ref _terminateDuplicates, value); }
    public string DuplicateGroupingMode { get => _duplicateGroupingMode; set => Set(ref _duplicateGroupingMode, value); }
    public decimal DuplicateKeepNewestCount { get => _duplicateKeepNewestCount; set => Set(ref _duplicateKeepNewestCount, value); }
    public decimal DuplicateMinAgeSeconds { get => _duplicateMinAgeSeconds; set => Set(ref _duplicateMinAgeSeconds, value); }
    public bool TerminateHung { get => _terminateHung; set => Set(ref _terminateHung, value); }
    public decimal HungMaxCurrentCallMinutes { get => _hungMaxCurrentCallMinutes; set => Set(ref _hungMaxCurrentCallMinutes, value); }
    public decimal HungMaxDbCallMinutes { get => _hungMaxDbCallMinutes; set => Set(ref _hungMaxDbCallMinutes, value); }
    public decimal HungMaxBlockedMinutes { get => _hungMaxBlockedMinutes; set => Set(ref _hungMaxBlockedMinutes, value); }
    public decimal HungMaxSleepingMinutes { get => _hungMaxSleepingMinutes; set => Set(ref _hungMaxSleepingMinutes, value); }
    public bool TerminateHungOnlyWhenBlockingOthers { get => _terminateHungOnlyWhenBlockingOthers; set => Set(ref _terminateHungOnlyWhenBlockingOthers, value); }
    public decimal ConsecutiveChecksRequired { get => _consecutiveChecksRequired; set => Set(ref _consecutiveChecksRequired, value); }
    public bool IgnoreConfigurator { get => _ignoreConfigurator; set => Set(ref _ignoreConfigurator, value); }
    public bool IgnoreBackgroundJobs { get => _ignoreBackgroundJobs; set => Set(ref _ignoreBackgroundJobs, value); }
    public string ExcludedUsersText { get => _excludedUsersText; set => Set(ref _excludedUsersText, value); }
    public string ExcludedInfoBasesText { get => _excludedInfoBasesText; set => Set(ref _excludedInfoBasesText, value); }
    public string TargetInfoBasesText { get => _targetInfoBasesText; set => Set(ref _targetInfoBasesText, value); }
    public string PreviewSessionsSummary { get => _previewSessionsSummary; private set => Set(ref _previewSessionsSummary, value); }

    public bool NotificationsEnabled { get => _notificationsEnabled; set => Set(ref _notificationsEnabled, value); }
    public bool NotifyOnWatchdogTermination { get => _notifyOnWatchdogTermination; set => Set(ref _notifyOnWatchdogTermination, value); }
    public bool NotifyOnUpdates { get => _notifyOnUpdates; set => Set(ref _notifyOnUpdates, value); }
    public decimal NotificationCooldownSeconds { get => _notificationCooldownSeconds; set => Set(ref _notificationCooldownSeconds, value); }
    public bool TelegramEnabled { get => _telegramEnabled; set => Set(ref _telegramEnabled, value); }
    public string TelegramChatId { get => _telegramChatId; set => Set(ref _telegramChatId, value); }
    public string TelegramBotToken { get => _telegramBotToken; set => Set(ref _telegramBotToken, value); }
    public bool WebhookEnabled { get => _webhookEnabled; set => Set(ref _webhookEnabled, value); }
    public string WebhookUrl { get => _webhookUrl; set => Set(ref _webhookUrl, value); }
    public bool EmailEnabled { get => _emailEnabled; set => Set(ref _emailEnabled, value); }
    public string SmtpHost { get => _smtpHost; set => Set(ref _smtpHost, value); }
    public decimal SmtpPort { get => _smtpPort; set => Set(ref _smtpPort, value); }
    public bool SmtpUseSsl { get => _smtpUseSsl; set => Set(ref _smtpUseSsl, value); }
    public string SmtpUser { get => _smtpUser; set => Set(ref _smtpUser, value); }
    public string SmtpPassword { get => _smtpPassword; set => Set(ref _smtpPassword, value); }
    public string EmailFrom { get => _emailFrom; set => Set(ref _emailFrom, value); }
    public string EmailTo { get => _emailTo; set => Set(ref _emailTo, value); }
    public string NotificationStatus { get => _notificationStatus; private set => Set(ref _notificationStatus, value); }

    public bool AutoUpdateEnabled { get => _autoUpdateEnabled; set => Set(ref _autoUpdateEnabled, value); }
    public bool IncludePrereleases { get => _includePrereleases; set => Set(ref _includePrereleases, value); }
    public decimal AutoUpdateCheckHours { get => _autoUpdateCheckHours; set => Set(ref _autoUpdateCheckHours, value); }
    public string GitHubOwner { get => _gitHubOwner; set => Set(ref _gitHubOwner, value); }
    public string GitHubRepository { get => _gitHubRepository; set => Set(ref _gitHubRepository, value); }
    public string GitHubToken { get => _gitHubToken; set => Set(ref _gitHubToken, value); }
    public string UpdateStatus { get => _updateStatus; private set => Set(ref _updateStatus, value); }
    public bool IsUpdateAvailable { get => _isUpdateAvailable; private set => Set(ref _isUpdateAvailable, value); }
    public string AvailableReleaseNotes { get => _availableReleaseNotes; private set => Set(ref _availableReleaseNotes, value); }
    public bool HasReleaseNotes { get => _hasReleaseNotes; private set => Set(ref _hasReleaseNotes, value); }

    public string LogDirectory { get => _logDirectory; set => Set(ref _logDirectory, value); }
    public decimal LogRetentionDays { get => _logRetentionDays; set => Set(ref _logRetentionDays, value); }
    public string FilteredLogText { get => _filteredLogText; private set => Set(ref _filteredLogText, value); }
    public string LogFilterText { get => _logFilterText; set { if (Set(ref _logFilterText, value)) ApplyLogFilter(); } }
    public string SelectedLogLevel { get => _selectedLogLevel; set { if (Set(ref _selectedLogLevel, value)) ApplyLogFilter(); } }
    public bool AutoRefreshLogs { get => _autoRefreshLogs; set => Set(ref _autoRefreshLogs, value); }

    private void LoadFromConfig(WatchdogConfig cfg)
    {
        RacPath = cfg.RacPath;
        Endpoint = cfg.Endpoint;
        AgentEndpoint = cfg.AgentEndpoint;
        ClusterUser = cfg.ClusterUser;
        ClusterPassword = cfg.ClusterPassword;
        RacTimeoutSeconds = Math.Clamp(cfg.RacTimeoutSeconds, 15, 300);
        AutoStartRas = cfg.AutoStartRas;
        RestartHungRas = cfg.RestartHungRas;
        MonitorAllClusters = cfg.MonitorAllClusters;
        TargetClustersText = string.Join(", ", cfg.TargetClusters ?? new());

        CheckIntervalSeconds = Math.Clamp(cfg.CheckIntervalSeconds, 5, 3600);
        DryRun = cfg.DryRun;

        var p = cfg.Policy ?? new SessionPolicyOptions();
        TerminateDuplicates = p.TerminateDuplicates;
        DuplicateGroupingMode = string.IsNullOrWhiteSpace(p.DuplicateGroupingMode) ? DuplicateGroupingModes.Full : p.DuplicateGroupingMode;
        DuplicateKeepNewestCount = Math.Max(1, p.DuplicateKeepNewestCount);
        DuplicateMinAgeSeconds = Math.Max(0, p.DuplicateMinAgeSeconds);
        TerminateHung = p.TerminateHung;
        HungMaxCurrentCallMinutes = Math.Max(0, p.HungMaxCurrentCallMinutes);
        HungMaxDbCallMinutes = Math.Max(0, p.HungMaxDbCallMinutes);
        HungMaxBlockedMinutes = Math.Max(0, p.HungMaxBlockedMinutes);
        HungMaxSleepingMinutes = Math.Max(0, p.HungMaxSleepingMinutes);
        TerminateHungOnlyWhenBlockingOthers = p.TerminateHungOnlyWhenBlockingOthers;
        ConsecutiveChecksRequired = Math.Clamp(p.ConsecutiveChecksRequired, 1, 20);
        IgnoreConfigurator = p.IgnoreConfigurator;
        IgnoreBackgroundJobs = p.IgnoreBackgroundJobs;
        ExcludedUsersText = string.Join(", ", p.ExcludedUsers ?? new());
        ExcludedInfoBasesText = string.Join(", ", p.ExcludedInfoBases ?? new());
        TargetInfoBasesText = string.Join(", ", p.TargetInfoBases ?? new());

        var n = cfg.Notifications ?? new NotificationOptions();
        NotificationsEnabled = n.Enabled;
        NotifyOnWatchdogTermination = n.NotifyOnWatchdogTermination;
        NotifyOnUpdates = n.NotifyOnUpdates;
        NotificationCooldownSeconds = Math.Clamp(n.CooldownSeconds, 5, 3600);
        TelegramEnabled = n.TelegramEnabled;
        TelegramChatId = n.TelegramChatId;
        TelegramBotToken = n.TelegramBotToken;
        WebhookEnabled = n.WebhookEnabled;
        WebhookUrl = n.WebhookUrl;
        EmailEnabled = n.EmailEnabled;
        SmtpHost = n.SmtpHost;
        SmtpPort = Math.Clamp(n.SmtpPort <= 0 ? 587 : n.SmtpPort, 1, 65535);
        SmtpUseSsl = n.SmtpUseSsl;
        SmtpUser = n.SmtpUser;
        SmtpPassword = n.SmtpPassword;
        EmailFrom = n.EmailFrom;
        EmailTo = n.EmailTo;

        AutoUpdateEnabled = cfg.AutoUpdateEnabled;
        IncludePrereleases = cfg.IncludePrereleases;
        AutoUpdateCheckHours = Math.Clamp(cfg.AutoUpdateCheckHours, 1, 168);
        GitHubOwner = cfg.GitHubOwner;
        GitHubRepository = cfg.GitHubRepository;
        GitHubToken = cfg.GitHubToken;

        LogDirectory = string.IsNullOrWhiteSpace(cfg.LogDirectory) ? WatchdogConfig.DefaultLogDirectory : cfg.LogDirectory;
        LogRetentionDays = Math.Clamp(cfg.LogRetentionDays, 1, 365);

        if (string.IsNullOrWhiteSpace(RacPath) && OperatingSystem.IsWindows())
        {
            var detected = _rac.FindRac();
            if (!string.IsNullOrWhiteSpace(detected)) RacPath = detected;
        }
    }

    private WatchdogConfig BuildConfig()
    {
        var cfg = new WatchdogConfig
        {
            RacPath = RacPath.Trim(),
            Endpoint = string.IsNullOrWhiteSpace(Endpoint) ? "127.0.0.1:1545" : Endpoint.Trim(),
            AgentEndpoint = string.IsNullOrWhiteSpace(AgentEndpoint) ? "127.0.0.1:1540" : AgentEndpoint.Trim(),
            ClusterUser = ClusterUser.Trim(),
            RacTimeoutSeconds = (int)RacTimeoutSeconds,
            AutoStartRas = AutoStartRas,
            RestartHungRas = RestartHungRas,
            MonitorAllClusters = MonitorAllClusters,
            TargetClusters = SplitCsv(TargetClustersText),
            CheckIntervalSeconds = (int)CheckIntervalSeconds,
            DryRun = DryRun,
            LogDirectory = string.IsNullOrWhiteSpace(LogDirectory) ? WatchdogConfig.DefaultLogDirectory : LogDirectory.Trim(),
            LogRetentionDays = (int)LogRetentionDays,
            AutoUpdateEnabled = AutoUpdateEnabled,
            IncludePrereleases = IncludePrereleases,
            AutoUpdateCheckHours = (int)AutoUpdateCheckHours,
            GitHubOwner = string.IsNullOrWhiteSpace(GitHubOwner) ? "zigorminsk-debug" : GitHubOwner.Trim(),
            GitHubRepository = string.IsNullOrWhiteSpace(GitHubRepository) ? "1C-Monitor" : GitHubRepository.Trim(),
            Policy = new SessionPolicyOptions
            {
                TerminateDuplicates = TerminateDuplicates,
                DuplicateGroupingMode = DuplicateGroupingMode,
                DuplicateKeepNewestCount = (int)DuplicateKeepNewestCount,
                DuplicateMinAgeSeconds = (int)DuplicateMinAgeSeconds,
                TerminateHung = TerminateHung,
                HungMaxCurrentCallMinutes = (int)HungMaxCurrentCallMinutes,
                HungMaxDbCallMinutes = (int)HungMaxDbCallMinutes,
                HungMaxBlockedMinutes = (int)HungMaxBlockedMinutes,
                HungMaxSleepingMinutes = (int)HungMaxSleepingMinutes,
                TerminateHungOnlyWhenBlockingOthers = TerminateHungOnlyWhenBlockingOthers,
                ConsecutiveChecksRequired = (int)ConsecutiveChecksRequired,
                IgnoreConfigurator = IgnoreConfigurator,
                IgnoreBackgroundJobs = IgnoreBackgroundJobs,
                ExcludedUsers = SplitCsv(ExcludedUsersText),
                ExcludedInfoBases = SplitCsv(ExcludedInfoBasesText),
                TargetInfoBases = SplitCsv(TargetInfoBasesText)
            },
            Notifications = new NotificationOptions
            {
                Enabled = NotificationsEnabled,
                NotifyOnWatchdogTermination = NotifyOnWatchdogTermination,
                NotifyOnUpdates = NotifyOnUpdates,
                CooldownSeconds = (int)NotificationCooldownSeconds,
                TelegramEnabled = TelegramEnabled,
                TelegramChatId = TelegramChatId.Trim(),
                WebhookEnabled = WebhookEnabled,
                WebhookUrl = WebhookUrl.Trim(),
                EmailEnabled = EmailEnabled,
                SmtpHost = SmtpHost.Trim(),
                SmtpPort = (int)SmtpPort,
                SmtpUseSsl = SmtpUseSsl,
                SmtpUser = SmtpUser.Trim(),
                EmailFrom = EmailFrom.Trim(),
                EmailTo = EmailTo.Trim()
            }
        };

        cfg.ClusterPassword = ClusterPassword;
        cfg.GitHubToken = GitHubToken;
        cfg.Notifications.TelegramBotToken = TelegramBotToken;
        cfg.Notifications.SmtpPassword = SmtpPassword;
        return cfg;
    }

    private bool SaveConfigToDisk()
    {
        try
        {
            var cfg = BuildConfig();
            cfg.Save();
            AppendOpLog($"Конфигурация сохранена в {WatchdogConfig.DefaultConfigPath} (секреты зашифрованы DPAPI).");
            StatusMessage = "Конфигурация сохранена (применится службой на следующем такте)";
            return true;
        }
        catch (UnauthorizedAccessException)
        {
            const string msg = "Нет прав записи в %ProgramData%\\1CServerMonitor (защищено NTFS ACL). Нажмите «Перезапустить от администратора (UAC)».";
            AppendOpLog(msg);
            StatusMessage = msg;
            return false;
        }
        catch (Exception ex)
        {
            AppendOpLog("Ошибка сохранения конфигурации: " + ex.Message);
            StatusMessage = "Ошибка сохранения: " + ex.Message;
            return false;
        }
    }

    private async Task RefreshServiceStateAsync()
    {
        var (exists, running, text, _) = await WatchdogSetup.QueryServiceStateAsync();
        Dispatcher.UIThread.Post(() =>
        {
            ServiceExists = exists;
            ServiceIsRunning = running;
            ServiceStatusText = text;
            ServiceStatusColor = running ? Brushes.LightGreen : exists ? Brushes.Goldenrod : Brushes.LightCoral;
        });
    }

    private async Task InstallOrUpdateServiceAsync()
    {
        if (_busy) return;
        if (!WatchdogSetup.IsAdministrator() && OperatingSystem.IsWindows())
        {
            WatchdogSetup.RestartElevatedForGui("--gui");
            return;
        }

        _busy = true;
        try
        {
            StatusMessage = "Установка / обновление службы 1CServerWatchdog…";
            var cfg = BuildConfig();
            await Task.Run(() => WatchdogSetup.InstallOrReconfigureFromGuiAsync(cfg, InstallInPlace, AppendOpLog));
            StatusMessage = "Служба 1CServerWatchdog установлена и работает (LocalSystem)";
            await RefreshServiceStateAsync();
        }
        catch (Exception ex)
        {
            AppendOpLog("Ошибка установки службы: " + ex.Message);
            StatusMessage = "Ошибка установки: " + ex.Message;
        }
        finally { _busy = false; }
    }

    private async Task ControlServiceAsync(string action)
    {
        if (_busy) return;
        if (!WatchdogSetup.IsAdministrator() && OperatingSystem.IsWindows())
        {
            WatchdogSetup.RestartElevatedForGui("--gui");
            return;
        }

        _busy = true;
        try
        {
            StatusMessage = $"Управление службой ({action})…";
            await Task.Run(() => WatchdogSetup.ControlServiceAsync(action, AppendOpLog));
            StatusMessage = $"Команда '{action}' выполнена успешно";
            await RefreshServiceStateAsync();
        }
        catch (Exception ex)
        {
            AppendOpLog($"Ошибка команды '{action}': {ex.Message}");
            StatusMessage = "Ошибка: " + ex.Message;
        }
        finally { _busy = false; }
    }

    private async Task SaveAndRestartAsync()
    {
        if (!SaveConfigToDisk()) return;
        if (ServiceExists)
            await ControlServiceAsync("restart");
    }

    private async Task UninstallServiceAsync()
    {
        if (_busy) return;
        if (!WatchdogSetup.IsAdministrator() && OperatingSystem.IsWindows())
        {
            WatchdogSetup.RestartElevatedForGui("--gui");
            return;
        }

        if (ConfirmActionAsync != null && !await ConfirmActionAsync("Остановить и удалить системную службу Windows «1CServerWatchdog»?"))
            return;

        _busy = true;
        try
        {
            StatusMessage = "Удаление службы 1CServerWatchdog…";
            await Task.Run(() => WatchdogSetup.UninstallFromGuiAsync(AppendOpLog));
            StatusMessage = "Служба удалена";
            await RefreshServiceStateAsync();
        }
        catch (Exception ex)
        {
            AppendOpLog("Ошибка удаления службы: " + ex.Message);
            StatusMessage = "Ошибка удаления: " + ex.Message;
        }
        finally { _busy = false; }
    }

    private void FindRac()
    {
        _rac.InvalidateRacCache();
        var list = _rac.FindRacCandidates(RacPath);
        if (list.Count > 0)
        {
            RacPath = list[0];
            ConnectionTestResult = $"Найден rac.exe: {RacPath} (всего вариантов: {list.Count})";
            AppendOpLog(ConnectionTestResult);
        }
        else
        {
            ConnectionTestResult = "rac.exe/ras.exe не найдены. Укажите путь вручную или запустите от администратора.";
            AppendOpLog(ConnectionTestResult);
        }
    }

    private async Task StartRasAsync()
    {
        try
        {
            var rac = _rac.FindRac(RacPath) ?? throw new Exception("Сначала укажите путь к rac.exe");
            RacPath = rac;
            ConnectionTestResult = $"Запуск RAS для {AgentEndpoint}…";
            var cfg = BuildConfig();
            var ready = await RacClient.EnsureRasRunningAsync(rac, cfg.ToRacOptions(), AppendOpLog, _stop.Token);
            Endpoint = ready;
            ConnectionTestResult = $"RAS работает: {ready}";
            StatusMessage = ConnectionTestResult;
        }
        catch (Exception ex)
        {
            ConnectionTestResult = "Ошибка запуска RAS: " + ex.Message;
            AppendOpLog(ConnectionTestResult);
        }
    }

    private async Task TestClusterConnectionAsync(bool evaluateDryRun)
    {
        try
        {
            var cfg = BuildConfig();
            var rac = _rac.FindRac(cfg.RacPath) ?? throw new Exception("rac.exe не найден");
            RacPath = rac;
            ConnectionTestResult = "Подключение к кластеру 1С…";
            var snap = await _rac.ReadRacAsync(rac, cfg.Endpoint, cfg.ToRacOptions(), AppendOpLog, _stop.Token);
            ConnectionTestResult = $"Успешно: кластер {snap.ClusterId} (всего {snap.ClusterIds.Count}), инфобаз {snap.InfoBases.Count}, сеансов {snap.Sessions.Count}, rphost {snap.Processes.Count}";

            if (evaluateDryRun)
            {
                var targets = SessionAnalyzer.EvaluateWatchdogTargets(
                    snap.Sessions,
                    cfg.Policy,
                    new Dictionary<string, DateTime>(StringComparer.OrdinalIgnoreCase),
                    DateTime.Now);
                Dispatcher.UIThread.Post(() =>
                {
                    PreviewTargets.Clear();
                    foreach (var t in targets) PreviewTargets.Add(t);
                    PreviewSessionsSummary = $"Пробный анализ (Dry-Run): найдено сеансов {snap.Sessions.Count}, попали бы под завершение: {targets.Count}";
                });
            }
            StatusMessage = ConnectionTestResult;
        }
        catch (Exception ex)
        {
            ConnectionTestResult = "Ошибка подключения: " + ex.Message;
            StatusMessage = ConnectionTestResult;
            AppendOpLog(ConnectionTestResult);
        }
    }

    private async Task SendTestNotificationAsync()
    {
        try
        {
            NotificationStatus = "Отправка тестового уведомления…";
            var cfg = BuildConfig();
            if (!cfg.Notifications.Enabled)
            {
                NotificationStatus = "Включите переключатель «Оповещения включены» перед проверкой.";
                return;
            }
            var errors = await _notifications.SendAsync(
                cfg.Notifications,
                "Тестовое уведомление 1C Watchdog",
                $"Проверка каналов оповещения 1C Watchdog с сервера {Environment.MachineName} ({DateTime.Now:dd.MM.yyyy HH:mm:ss}).",
                bypassCooldown: true,
                cancellationToken: _stop.Token);

            NotificationStatus = errors.Count == 0
                ? "Тестовое уведомление отправлено успешно."
                : "Ошибки отправки: " + string.Join("; ", errors);
        }
        catch (Exception ex)
        {
            NotificationStatus = "Ошибка отправки: " + ex.Message;
        }
    }

    private async Task CheckForUpdatesAsync()
    {
        if (_updateBusy) return;
        _updateBusy = true;
        try
        {
            UpdateStatus = "Проверка обновлений на GitHub…";
            var cfg = BuildConfig();
            var updater = new GitHubAutoUpdater("OneCWatchdog", "1C-Watchdog.exe", cfg.GitHubOwner, cfg.GitHubRepository, cfg.GitHubToken);
            var res = await updater.CheckForUpdateAsync(includePrereleases: cfg.IncludePrereleases, cancellationToken: _stop.Token);
            _pendingUpdate = res.IsUpdateAvailable ? res : null;
            IsUpdateAvailable = res.IsUpdateAvailable;
            UpdateStatus = res.Message;
            AvailableReleaseNotes = res.IsUpdateAvailable ? (res.ReleaseNotes ?? "").Trim() : "";
            HasReleaseNotes = res.IsUpdateAvailable && !string.IsNullOrWhiteSpace(AvailableReleaseNotes);
            AppendOpLog("Обновление: " + res.Message);
        }
        catch (Exception ex)
        {
            UpdateStatus = "Ошибка проверки: " + ex.Message;
        }
        finally { _updateBusy = false; }
    }

    private async Task ApplyAvailableUpdateAsync()
    {
        if (_updateBusy) return;
        if (!OperatingSystem.IsWindows())
        {
            UpdateStatus = "Автообновление exe доступно только в ОС Windows.";
            return;
        }
        if (!WatchdogSetup.IsAdministrator())
        {
            WatchdogSetup.RestartElevatedForGui("--gui");
            return;
        }

        _updateBusy = true;
        try
        {
            var cfg = BuildConfig();
            var updater = new GitHubAutoUpdater("OneCWatchdog", "1C-Watchdog.exe", cfg.GitHubOwner, cfg.GitHubRepository, cfg.GitHubToken);
            var target = _pendingUpdate;
            if (target == null || !target.IsUpdateAvailable)
                target = await updater.CheckForUpdateAsync(includePrereleases: cfg.IncludePrereleases, cancellationToken: _stop.Token);

            if (!target.IsUpdateAvailable)
            {
                UpdateStatus = target.Message;
                return;
            }

            UpdateStatus = $"Загрузка и проверка SHA-256 ({target.LatestVersion})…";
            var stagedDir = await updater.DownloadAndStageUpdateAsync(target, WatchdogConfig.DefaultUpdateStagingDirectory, AppendOpLog, _stop.Token);

            var installedExe = Path.Combine(WatchdogSetup.DefaultInstallDir, "1C-Watchdog.exe");
            var targetExe = ServiceExists && File.Exists(installedExe)
                ? installedExe
                : (Environment.ProcessPath ?? installedExe);

            UpdateStatus = $"Применение обновления {target.LatestVersion}…";
            GitHubAutoUpdater.LaunchUpdaterAndExit(
                stagedSourceDir: stagedDir,
                targetExePath: targetExe,
                executableName: "1C-Watchdog.exe",
                windowsServiceName: ServiceExists ? WatchdogConfig.ServiceName : null,
                relaunchDesktopApp: true,
                relaunchArgs: new[] { "--gui" });

            await Task.Delay(300);
            Environment.Exit(0);
        }
        catch (Exception ex)
        {
            UpdateStatus = "Ошибка установки обновления: " + ex.Message;
            AppendOpLog(UpdateStatus);
        }
        finally { _updateBusy = false; }
    }

    private async Task LogRefreshLoopAsync()
    {
        while (!_stop.IsCancellationRequested)
        {
            try { await Task.Delay(TimeSpan.FromSeconds(5), _stop.Token); }
            catch (OperationCanceledException) { break; }
            if (AutoRefreshLogs)
                Dispatcher.UIThread.Post(RefreshLogs);
        }
    }

    private void RefreshLogs()
    {
        try
        {
            var dir = string.IsNullOrWhiteSpace(LogDirectory) ? WatchdogConfig.DefaultLogDirectory : LogDirectory;
            if (!Directory.Exists(dir))
            {
                _rawLogText = $"Каталог логов ещё не создан: {dir}";
                ApplyLogFilter();
                return;
            }
            var latest = Directory.EnumerateFiles(dir, "watchdog-*.log")
                .Select(f => new FileInfo(f))
                .OrderByDescending(f => f.LastWriteTimeUtc)
                .FirstOrDefault();
            if (latest == null)
            {
                _rawLogText = "Файлы логов watchdog-*.log пока отсутствуют.";
                ApplyLogFilter();
                return;
            }
            using var fs = new FileStream(latest.FullName, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
            if (fs.Length > 131072) fs.Seek(-131072, SeekOrigin.End);
            using var reader = new StreamReader(fs, Encoding.UTF8, true);
            if (fs.Position > 0) reader.ReadLine();
            _rawLogText = $"Файл: {latest.FullName}\r\n\r\n" + reader.ReadToEnd();
            ApplyLogFilter();
        }
        catch (Exception ex)
        {
            _rawLogText = "Не удалось прочитать лог: " + ex.Message;
            ApplyLogFilter();
        }
    }

    private void ApplyLogFilter()
    {
        var lines = _rawLogText.Replace("\r", "").Split('\n');
        var level = SelectedLogLevel;
        var q = (LogFilterText ?? "").Trim();

        var filtered = lines.Where(l =>
        {
            if (level == "WARN" && !l.Contains("[WARN]") && !l.Contains("[ERROR]")) return false;
            if (level == "ERROR" && !l.Contains("[ERROR]")) return false;
            if (q.Length > 0 && !l.Contains(q, StringComparison.OrdinalIgnoreCase)) return false;
            return true;
        });

        FilteredLogText = string.Join(Environment.NewLine, filtered);
    }

    private void OpenLatestLogFile()
    {
        try
        {
            var dir = string.IsNullOrWhiteSpace(LogDirectory) ? WatchdogConfig.DefaultLogDirectory : LogDirectory;
            if (!Directory.Exists(dir)) return;
            var latest = Directory.EnumerateFiles(dir, "*.log").Select(f => new FileInfo(f)).OrderByDescending(f => f.LastWriteTimeUtc).FirstOrDefault();
            if (latest != null) OpenPath(latest.FullName);
        }
        catch { }
    }

    private void OpenPath(string path)
    {
        try
        {
            if (Directory.Exists(path) || File.Exists(path))
                Process.Start(new ProcessStartInfo(path) { UseShellExecute = true });
        }
        catch (Exception ex) { AppendOpLog("Не удалось открыть путь: " + ex.Message); }
    }

    private void AppendOpLog(string message)
    {
        Dispatcher.UIThread.Post(() =>
        {
            _operationLog.AppendLine($"[{DateTime.Now:HH:mm:ss}] {message}");
            if (_operationLog.Length > 32000) _operationLog.Remove(0, 8000);
            OperationLogText = _operationLog.ToString();
        });
    }

    private static List<string> SplitCsv(string text) =>
        (text ?? "").Split(new[] { ',', ';', '\n', '\r' }, StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            .Distinct(StringComparer.OrdinalIgnoreCase)
            .ToList();

    private void OnPropertyChanged([CallerMemberName] string? name = null) =>
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));

    private bool Set<T>(ref T field, T value, [CallerMemberName] string? name = null)
    {
        if (EqualityComparer<T>.Default.Equals(field, value)) return false;
        field = value;
        OnPropertyChanged(name);
        return true;
    }

    public void Dispose() => _stop.Cancel();
}

internal sealed class WatchdogRelayCommand : ICommand
{
    private readonly Action _action;
    public WatchdogRelayCommand(Action action) => _action = action;
    public event EventHandler? CanExecuteChanged { add { } remove { } }
    public bool CanExecute(object? parameter) => true;
    public void Execute(object? parameter) => _action();
}
