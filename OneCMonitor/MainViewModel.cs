using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Diagnostics;
using System.Globalization;
using System.Net.NetworkInformation;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using System.Windows.Input;
using Avalonia.Media;
using Avalonia.Threading;
using OneCMonitor.Core;

namespace OneCMonitor;

public sealed class MainViewModel : NotifyBase, IDisposable
{
    public ObservableCollection<ProcessRow> Processes { get; } = new();
    public ObservableCollection<SessionRow> Sessions { get; } = new();
    public ObservableCollection<AlertRow> Alerts { get; } = new();
    public MonitorSettings Settings { get; }
    public ICommand RefreshCommand { get; }
    public ICommand ExportCommand { get; }
    public ICommand SaveSettingsCommand { get; }
    public ICommand StartRasCommand { get; }
    public ICommand CheckUpdatesCommand { get; }
    public ICommand ApplyUpdateCommand { get; }
    public ICommand SetupWatchdogCommand { get; }
    public ICommand OpenWebsiteCommand { get; }
    public ICommand OpenEmailCommand { get; }
    public ICommand OpenPhoneCommand { get; }
    public ICommand GenerateDailyReportCommand { get; }
    public ICommand TerminateAllSessionsCommand { get; }
    public ICommand TerminateDuplicateSessionsCommand { get; }
    public ICommand TerminateSleepingSessionsCommand { get; }
    public ICommand TerminateExceptConfiguratorCommand { get; }
    public ICommand DisableBackgroundJobsCommand { get; }
    public ICommand EnableBackgroundJobsCommand { get; }
    public ICommand RestartOneCServicesCommand { get; }
    public ICommand KillAllRphostCommand { get; }
    public ICommand GracefulTurnOffCommand { get; }

    private readonly CancellationTokenSource _stop = new();
    private readonly RacClient _rac = new();
    private readonly Dictionary<int, CpuSample> _cpu = new();
    private readonly List<HistoryRow> _history = new();
    private readonly List<ServerHistoryRow> _serverHistory = new();
    private readonly Dictionary<string, DailyUserStat> _dailyUsers = new();
    private readonly Dictionary<int, DateTime> _memoryOverSince = new();
    private readonly Dictionary<int, DrainState> _drainingProcesses = new();
    private readonly Dictionary<string, DateTime> _lastAlertEmission = new(StringComparer.OrdinalIgnoreCase);
    private readonly List<string> _cachedTechLogFiles = new();
    private RacSnapshot? _cachedRac;
    private UpdateCheckResult? _pendingUpdate;
    private DateTime _lastAutomaticDrain = DateTime.MinValue, _lastUpdateCheckUtc = DateTime.MinValue;
    private DateTime _lastRacPoll = DateTime.MinValue, _lastTechLogScan = DateTime.MinValue, _techLogFileCacheAt = DateTime.MinValue, _lastHistorySample = DateTime.MinValue, _lastPersistentSample = DateTime.MinValue, _lastVolumeRefresh = DateTime.MinValue;
    private string _cachedVolumeSummary = "не определены", _cachedStaticSystem = "";
    private readonly StringBuilder _log = new();
    private readonly DiskPerformanceMonitor _disk = new();
    private readonly NetworkPerformanceMonitor _network = new();
    private string _physicalDiskInfo = "Накопители: определение типа SSD/HDD/NVMe…";
    private readonly string _virtualizationInfo = DetectVirtualization();
    private bool _busy, _updateBusy, _isUpdateAvailable;
    private bool _isRunning = true;
    private string _status = "Запуск…", _racStatus = "Не проверено", _footer = "Ожидание первого измерения";
    private string _updateStatus = $"Версия {GitHubAutoUpdater.GetCurrentVersion()} • проверка обновлений не выполнялась";
    private string _windowTitle = "1C Server Monitor", _serverHeaderTitle = $"{Environment.MachineName} — сервер 1С", _serverHeaderDetails = "Определение характеристик сервера…";
    private string _serverHardwareText = "Определение оборудования…", _networkLoadText = "Сеть: ожидание первого измерения…";
    private string _adminConfirmationText = "", _adminActionStatus = "Для опасных действий введите контрольную фразу";
    private string _autoDrainStatus = "Автовывод rphost выключен";
    private ProcessRow? _selectedProcess;
    private string? _lastRacPath, _lastClusterId;
    private readonly Dictionary<string, InfoBaseRef> _lastInfoBases = new(StringComparer.OrdinalIgnoreCase);
    private IBrush _statusColor = Brushes.Goldenrod;
    private int _processCount, _sessionCount, _activeSessionCount, _sleepingSessionCount, _alertCount;
    private string _totalMemoryText = "—", _totalCpuText = "—", _diskUsageText = "—", _diskIoText = "чтение — • запись —", _diskQueueText = "очередь —", _sessionBreakdownText = "активные — • спящие —", _logText = "";

    public MainViewModel()
    {
        Settings = MonitorSettings.Load();
        Settings.BackgroundJobsDisabledInfobases ??= new();
        RefreshCommand = new RelayCommand(async () => await RefreshAsync(true));
        ExportCommand = new RelayCommand(ExportHtmlReport);
        SaveSettingsCommand = new RelayCommand(async () => { Settings.Save(); _rac.InvalidateRacCache(); _lastRacPoll = DateTime.MinValue; await RefreshAsync(true); });
        StartRasCommand = new RelayCommand(async () => await StartRasAsync());
        CheckUpdatesCommand = new RelayCommand(async () => await CheckForUpdatesAsync(manual: true));
        ApplyUpdateCommand = new RelayCommand(async () => await ApplyAvailableUpdateAsync());
        SetupWatchdogCommand = new RelayCommand(LaunchWatchdogSetup);
        OpenWebsiteCommand = new RelayCommand(() => OpenUri("https://csl.by"));
        OpenEmailCommand = new RelayCommand(() => OpenUri("mailto:info@csl.by"));
        OpenPhoneCommand = new RelayCommand(() => OpenUri("tel:+375293371412"));
        GenerateDailyReportCommand = new RelayCommand(() => GenerateDailyReport(DateTime.Today, false));
        TerminateAllSessionsCommand = new RelayCommand(async () => await TerminateSessionsAsync(Sessions.ToList(), "всех сеансов"));
        TerminateDuplicateSessionsCommand = new RelayCommand(async () => await TerminateSessionsAsync(SessionAnalyzer.GetDuplicateSessions(Sessions), "дублирующихся ранних сеансов"));
        TerminateSleepingSessionsCommand = new RelayCommand(async () => await TerminateSessionsAsync(Sessions.Where(x => x.IsSleeping).ToList(), "спящих сеансов"));
        TerminateExceptConfiguratorCommand = new RelayCommand(async () => await TerminateSessionsAsync(Sessions.Where(x => !SessionAnalyzer.IsConfigurator(x)).ToList(), "всех сеансов, кроме Конфигуратора"));
        DisableBackgroundJobsCommand = new RelayCommand(async () => await SetBackgroundJobsAsync(false));
        EnableBackgroundJobsCommand = new RelayCommand(async () => await SetBackgroundJobsAsync(true));
        RestartOneCServicesCommand = new RelayCommand(async () => await RestartOneCServicesAsync());
        KillAllRphostCommand = new RelayCommand(async () => await KillAllRphostAsync());
        GracefulTurnOffCommand = new RelayCommand(async () => await GracefulTurnOffSelectedAsync());
        if (string.IsNullOrWhiteSpace(Settings.ReportFolder)) Settings.ReportFolder = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments), "1CServerMonitor", "Reports");
        UpdateServerHeader(null, default, default);
        _ = LoadPhysicalDiskInfoAsync();
        _ = LoopAsync();
        _ = AutoUpdateLoopAsync();
    }

    public bool IsRunning { get => _isRunning; set { Set(ref _isRunning, value); Status = value ? "Мониторинг включён" : "Мониторинг на паузе"; StatusColor = value ? Brushes.LightGreen : Brushes.Goldenrod; } }
    public string Status { get => _status; private set => Set(ref _status, value); }
    public IBrush StatusColor { get => _statusColor; private set => Set(ref _statusColor, value); }
    public string RacStatus { get => _racStatus; private set => Set(ref _racStatus, value); }
    public string UpdateStatus { get => _updateStatus; private set => Set(ref _updateStatus, value); }
    public bool IsUpdateAvailable { get => _isUpdateAvailable; private set => Set(ref _isUpdateAvailable, value); }
    public string Footer { get => _footer; private set => Set(ref _footer, value); }
    public string WindowTitle { get => _windowTitle; private set => Set(ref _windowTitle, value); }
    public string ServerHeaderTitle { get => _serverHeaderTitle; private set => Set(ref _serverHeaderTitle, value); }
    public string ServerHeaderDetails { get => _serverHeaderDetails; private set => Set(ref _serverHeaderDetails, value); }
    public string ServerHardwareText { get => _serverHardwareText; private set => Set(ref _serverHardwareText, value); }
    public string NetworkLoadText { get => _networkLoadText; private set => Set(ref _networkLoadText, value); }
    public string AdminConfirmationText { get => _adminConfirmationText; set => Set(ref _adminConfirmationText, value); }
    public string AdminActionStatus { get => _adminActionStatus; private set => Set(ref _adminActionStatus, value); }
    public string AutoDrainStatus { get => _autoDrainStatus; private set => Set(ref _autoDrainStatus, value); }
    public ProcessRow? SelectedProcess { get => _selectedProcess; set => Set(ref _selectedProcess, value); }
    public int ProcessCount { get => _processCount; private set => Set(ref _processCount, value); }
    public int SessionCount { get => _sessionCount; private set => Set(ref _sessionCount, value); }
    public int ActiveSessionCount { get => _activeSessionCount; private set => Set(ref _activeSessionCount, value); }
    public int SleepingSessionCount { get => _sleepingSessionCount; private set => Set(ref _sleepingSessionCount, value); }
    public int AlertCount { get => _alertCount; private set => Set(ref _alertCount, value); }
    public string TotalMemoryText { get => _totalMemoryText; private set => Set(ref _totalMemoryText, value); }
    public string TotalCpuText { get => _totalCpuText; private set => Set(ref _totalCpuText, value); }
    public string DiskUsageText { get => _diskUsageText; private set => Set(ref _diskUsageText, value); }
    public string DiskIoText { get => _diskIoText; private set => Set(ref _diskIoText, value); }
    public string DiskQueueText { get => _diskQueueText; private set => Set(ref _diskQueueText, value); }
    public string SessionBreakdownText { get => _sessionBreakdownText; private set => Set(ref _sessionBreakdownText, value); }
    public string LogText { get => _logText; private set => Set(ref _logText, value); }

    private void OpenUri(string uri)
    {
        try { Process.Start(new ProcessStartInfo(uri) { UseShellExecute = true }); }
        catch (Exception ex) { Log("Не удалось открыть ссылку: " + ex.Message); }
    }

    private void UpdateServerHeader(string? racPath, DiskSnapshot disk, NetworkSnapshot network)
    {
        var machine = Environment.MachineName;
        WindowTitle = $"{machine} — 1C Server Monitor";
        ServerHeaderTitle = $"{machine} — сервер 1С";
        if (_cachedStaticSystem.Length == 0)
            _cachedStaticSystem = $"{RuntimeInformation.OSDescription.Trim()} · {RuntimeInformation.OSArchitecture} · {GetCpuDescription()} · {Environment.ProcessorCount} лог. CPU";
        var memory = GetPhysicalMemory();
        var oneC = "1С/RAC: не подключён";
        if (!string.IsNullOrWhiteSpace(racPath) && File.Exists(racPath))
        {
            var version = FileVersionInfo.GetVersionInfo(racPath).FileVersion ?? "?";
            oneC = $"1С/RAC {version} ({ArchitectureLabel(racPath)})";
        }
        var diskText = disk.Available ? $"диск {disk.UsagePercent:0}% · R {FormatRate(disk.ReadBytesPerSec)} · W {FormatRate(disk.WriteBytesPerSec)} · ср. очередь {DiskQueueLabel(disk)}" : "диск: ожидание данных";
        NetworkLoadText = network.Available ? $"Сеть: ↓ {FormatRate(network.ReceiveBytesPerSec)} · ↑ {FormatRate(network.SendBytesPerSec)} · загрузка {network.UtilizationPercent:0.0}% · адаптеров {network.InterfaceCount}" : "Сеть: ожидание второго измерения";
        if (DateTime.UtcNow - _lastVolumeRefresh >= TimeSpan.FromMinutes(1)) { _cachedVolumeSummary = GetVolumeSummary(); _lastVolumeRefresh = DateTime.UtcNow; }
        ServerHardwareText = $"{_virtualizationInfo} · {_physicalDiskInfo} · Тома: {_cachedVolumeSummary}";
        ServerHeaderDetails = $"{_cachedStaticSystem} · RAM {memory}\n{ServerHardwareText}\n{oneC} · RAS {Settings.Endpoint} · rphost {ProcessCount} · {diskText} · {NetworkLoadText}";
    }

    private async Task LoadPhysicalDiskInfoAsync()
    {
        if (!OperatingSystem.IsWindows()) return;
        try
        {
            const string script = "[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new(); if(Get-Command Get-PhysicalDisk -ErrorAction SilentlyContinue){ Get-PhysicalDisk | ForEach-Object { $kind=if($_.BusType -eq 'NVMe'){'NVMe'}elseif($_.MediaType -eq 'SSD'){'SSD'}elseif($_.MediaType -eq 'HDD'){'HDD'}else{'тип не определён'}; '{0} [{1}, {2}, {3:N1} ГБ]' -f $_.FriendlyName,$kind,$_.BusType,($_.Size/1GB) } } else { Get-WmiObject Win32_DiskDrive | ForEach-Object { $kind=if($_.Model -match 'NVMe'){'NVMe'}elseif($_.Model -match 'SSD'){'SSD'}else{'HDD'}; '{0} [{1}, {2}, {3:N1} ГБ]' -f $_.Model,$kind,$_.InterfaceType,($_.Size/1GB) } }";
            var output = await RacClient.RunAsync("powershell.exe", new[] { "-NoProfile", "-NonInteractive", "-Command", script });
            var disks = output.Replace("\r", "").Split('\n', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
            _physicalDiskInfo = disks.Length == 0 ? "Накопители: тип не определён" : "Накопители: " + string.Join("; ", disks);
        }
        catch (Exception ex) { _physicalDiskInfo = "Накопители: тип не определён"; Log("Определение SSD/HDD/NVMe: " + ex.Message); }
    }

    private static string GetVolumeSummary()
    {
        try
        {
            return string.Join("; ", DriveInfo.GetDrives().Where(d => d.IsReady && d.DriveType == DriveType.Fixed).Select(d =>
            {
                var total = d.TotalSize / 1073741824d; var free = d.AvailableFreeSpace / 1073741824d; var used = total - free;
                return $"{d.Name.TrimEnd('\\')} занято {used:0.0}/{total:0.0} ГБ, свободно {free:0.0} ГБ";
            }));
        }
        catch { return "не определены"; }
    }

    private static string DetectVirtualization()
    {
        if (!OperatingSystem.IsWindows()) return "Тип сервера не определён";
        try
        {
            using var bios = Microsoft.Win32.Registry.LocalMachine.OpenSubKey(@"HARDWARE\DESCRIPTION\System\BIOS");
            var manufacturer = bios?.GetValue("SystemManufacturer")?.ToString()?.Trim() ?? "";
            var model = bios?.GetValue("SystemProductName")?.ToString()?.Trim() ?? "";
            var text = (manufacturer + " " + model).ToLowerInvariant();
            var platform = text.Contains("vmware") ? "VMware" : text.Contains("virtualbox") || text.Contains("oracle") ? "VirtualBox" :
                text.Contains("virtual machine") && text.Contains("microsoft") ? "Microsoft Hyper-V" : text.Contains("kvm") || text.Contains("qemu") ? "KVM/QEMU" :
                text.Contains("xen") ? "Xen" : text.Contains("amazon") || text.Contains("ec2") ? "Amazon EC2" : text.Contains("google") ? "Google Compute Engine" :
                text.Contains("parallels") ? "Parallels" : "";
            return platform.Length > 0 ? $"Виртуальный сервер · {platform} · {manufacturer} {model}" : $"Физический сервер · {manufacturer} {model}";
        }
        catch { return "Тип сервера не определён"; }
    }

    private static string GetCpuDescription()
    {
        var cpu = "процессор не определён";
        if (OperatingSystem.IsWindows())
            try { cpu = Microsoft.Win32.Registry.LocalMachine.OpenSubKey(@"HARDWARE\DESCRIPTION\System\CentralProcessor\0")?.GetValue("ProcessorNameString")?.ToString()?.Trim() ?? cpu; } catch { }
        return cpu;
    }

    private static string GetPhysicalMemory()
    {
        if (!OperatingSystem.IsWindows()) return "не определена";
        try
        {
            var status = new MemoryStatusEx { Length = (uint)Marshal.SizeOf<MemoryStatusEx>() };
            if (GlobalMemoryStatusEx(ref status)) return $"{status.TotalPhys / 1073741824d:0.0} ГБ, свободно {status.AvailPhys / 1073741824d:0.0} ГБ";
        }
        catch { }
        return "не определена";
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Auto)]
    private struct MemoryStatusEx
    {
        public uint Length; public uint MemoryLoad; public ulong TotalPhys; public ulong AvailPhys; public ulong TotalPageFile; public ulong AvailPageFile; public ulong TotalVirtual; public ulong AvailVirtual; public ulong AvailExtendedVirtual;
    }
    [DllImport("kernel32.dll", CharSet = CharSet.Auto, SetLastError = true)]
    private static extern bool GlobalMemoryStatusEx(ref MemoryStatusEx status);

    private async Task AutoUpdateLoopAsync()
    {
        await Task.Delay(TimeSpan.FromSeconds(10), _stop.Token).ContinueWith(_ => { });
        while (!_stop.IsCancellationRequested)
        {
            if (Settings.AutoUpdateEnabled)
            {
                var interval = TimeSpan.FromHours(Math.Max(1, (double)Settings.AutoUpdateCheckHours));
                if (_lastUpdateCheckUtc == DateTime.MinValue || DateTime.UtcNow - _lastUpdateCheckUtc >= interval)
                {
                    await CheckForUpdatesAsync(manual: false);
                }
            }
            try { await Task.Delay(TimeSpan.FromMinutes(15), _stop.Token); }
            catch (OperationCanceledException) { break; }
        }
    }

    private GitHubAutoUpdater CreateUpdater() => new(
        assetPrefix: "OneCServerMonitor",
        executableName: "1C-Server-Monitor.exe",
        owner: Settings.GitHubOwner,
        repository: Settings.GitHubRepository,
        gitHubToken: Settings.GitHubToken);

    private async Task CheckForUpdatesAsync(bool manual)
    {
        if (_updateBusy) return;
        _updateBusy = true;
        try
        {
            _lastUpdateCheckUtc = DateTime.UtcNow;
            UpdateStatus = "Проверка обновлений на GitHub…";
            var updater = CreateUpdater();
            var result = await updater.CheckForUpdateAsync(cancellationToken: _stop.Token);
            _pendingUpdate = result.IsUpdateAvailable ? result : null;
            IsUpdateAvailable = result.IsUpdateAvailable;
            UpdateStatus = result.Message;
            if (manual || result.IsUpdateAvailable) Log("Обновление: " + result.Message);

            if (result.IsUpdateAvailable && Settings.AutoUpdateEnabled && Settings.AutoUpdateInstallAutomatically)
            {
                await ApplyUpdateCoreAsync(updater, result);
            }
        }
        catch (OperationCanceledException) { }
        catch (Exception ex)
        {
            UpdateStatus = "Ошибка проверки обновлений: " + ex.Message;
            if (manual) Log(UpdateStatus);
        }
        finally { _updateBusy = false; }
    }

    private async Task ApplyAvailableUpdateAsync()
    {
        if (_updateBusy) return;
        _updateBusy = true;
        try
        {
            var updater = CreateUpdater();
            var target = _pendingUpdate;
            if (target == null || !target.IsUpdateAvailable)
            {
                UpdateStatus = "Проверка обновлений на GitHub…";
                target = await updater.CheckForUpdateAsync(cancellationToken: _stop.Token);
                _pendingUpdate = target.IsUpdateAvailable ? target : null;
                IsUpdateAvailable = target.IsUpdateAvailable;
                UpdateStatus = target.Message;
            }
            if (target.IsUpdateAvailable)
                await ApplyUpdateCoreAsync(updater, target);
        }
        catch (Exception ex)
        {
            UpdateStatus = "Ошибка установки обновления: " + ex.Message;
            Log(UpdateStatus);
        }
        finally { _updateBusy = false; }
    }

    private async Task ApplyUpdateCoreAsync(GitHubAutoUpdater updater, UpdateCheckResult update)
    {
        var exePath = Environment.ProcessPath;
        if (!OperatingSystem.IsWindows() || string.IsNullOrWhiteSpace(exePath))
        {
            UpdateStatus = $"Доступна версия {update.LatestVersion} (автозамена доступна только в Windows)";
            return;
        }

        UpdateStatus = $"Загрузка обновления {update.LatestVersion}…";
        var stagingRoot = Path.Combine(Path.GetTempPath(), "1CServerMonitorUpdate");
        var stagedDir = await updater.DownloadAndStageUpdateAsync(update, stagingRoot, msg => Log("Обновление: " + msg), _stop.Token);
        UpdateStatus = $"Установка версии {update.LatestVersion} и перезапуск…";
        Log(UpdateStatus);

        GitHubAutoUpdater.LaunchUpdaterAndExit(
            stagedSourceDir: stagedDir,
            targetExePath: exePath,
            executableName: "1C-Server-Monitor.exe",
            windowsServiceName: null,
            relaunchDesktopApp: true);

        await Task.Delay(300);
        Environment.Exit(0);
    }

    private void LaunchWatchdogSetup()
    {
        try
        {
            if (!OperatingSystem.IsWindows())
            {
                AdminActionStatus = "Служба Watchdog поддерживается только на Windows";
                return;
            }
            var dir = Path.GetDirectoryName(Environment.ProcessPath) ?? AppContext.BaseDirectory;
            var watchdogExe = Path.Combine(dir, "1C-Watchdog.exe");
            if (!File.Exists(watchdogExe))
            {
                AdminActionStatus = $"Файл 1C-Watchdog.exe не найден рядом с приложением ({dir})";
                Log(AdminActionStatus);
                return;
            }

            Process.Start(new ProcessStartInfo(watchdogExe)
            {
                Arguments = "--setup",
                UseShellExecute = true,
                Verb = "runas"
            });
            AdminActionStatus = "Запущен мастер установки службы 1C Watchdog";
            Log(AdminActionStatus);
        }
        catch (Exception ex)
        {
            AdminActionStatus = "Не удалось запустить настройку Watchdog: " + ex.Message;
            Log(AdminActionStatus);
        }
    }

    private async Task LoopAsync()
    {
        await Task.Delay(400);
        while (!_stop.IsCancellationRequested)
        {
            if (IsRunning) await RefreshAsync(false);
            try { await Task.Delay(TimeSpan.FromSeconds(Math.Max(2, (double)Settings.IntervalSeconds)), _stop.Token); }
            catch (OperationCanceledException) { break; }
        }
    }

    private async Task RefreshAsync(bool manual)
    {
        if (_busy) return;
        _busy = true;
        try
        {
            var local = SampleLocalProcesses();
            var disk = _disk.Sample();
            var network = _network.Sample();
            var now = DateTime.Now;
            RacSnapshot? rac = _cachedRac;
            string? selectedRac = _lastRacPath;
            var racDue = manual || rac == null || now - _lastRacPoll >= TimeSpan.FromSeconds(Math.Max(5, (double)Settings.RacPollSeconds));
            if (racDue)
            {
                _lastRacPoll = now; // при ошибке не запускаем тяжёлый повтор на каждом локальном такте
                RacSnapshot? freshRac = null; Exception? lastRacError = null;
                var racCandidates = _rac.FindRacCandidates(Settings.RacPath);
                var racOptions = Settings.ToRacOptions();
                foreach (var racPath in racCandidates)
                {
                    foreach (var endpoint in RacClient.EndpointCandidates(Settings.Endpoint))
                    {
                        try
                        {
                            freshRac = await _rac.ReadRacAsync(racPath, endpoint, racOptions, msg => Log(msg), _stop.Token);
                            var discoveredChanged = !string.Equals(Settings.RacPath, racPath, StringComparison.OrdinalIgnoreCase) || !string.Equals(Settings.Endpoint, endpoint, StringComparison.OrdinalIgnoreCase);
                            Settings.RacPath = racPath; Settings.Endpoint = endpoint;
                            if (discoveredChanged) { try { Settings.Save(); } catch (Exception saveEx) { Log("Не удалось сохранить найденный путь RAC: " + saveEx.Message); } }
                            selectedRac = racPath; _lastRacPath = racPath; _lastClusterId = freshRac.ClusterId; _cachedRac = freshRac; rac = freshRac;
                            _lastInfoBases.Clear(); foreach (var pair in freshRac.InfoBases) _lastInfoBases[pair.Key] = pair.Value;
                            WriteRacDiagnosticIfNeeded(freshRac);
                            var version = FileVersionInfo.GetVersionInfo(racPath).FileVersion ?? "?";
                            RacStatus = $"Подключено: {endpoint} • RAC {version} • {ArchitectureLabel(racPath)} • сеансов {freshRac.Sessions.Count} • опрос раз в {Settings.RacPollSeconds:0} с";
                            break;
                        }
                        catch (Exception ex) { lastRacError = ex; LogOnce($"rac:{racPath}:{endpoint}:{ex.Message}", $"RAC {racPath}, {endpoint}: {ex.Message}", TimeSpan.FromMinutes(5)); }
                    }
                    if (freshRac != null) break;
                }
                if (freshRac == null)
                {
                    var error = lastRacError?.Message ?? "нет данных";
                    var unavailable = racCandidates.Count == 0
                        ? "RAC/RAS не найдены. Запустите от администратора или укажите rac.exe вручную."
                        : error.Contains("не аутентифицирован", StringComparison.OrdinalIgnoreCase)
                            ? "Требуется имя администратора кластера. Если пароль не задан — заполните только имя."
                            : $"Проверено комплектов RAC/RAS: {racCandidates.Count}. Недоступны: {error}";
                    RacStatus = rac != null ? unavailable + " • показаны последние полученные данные" : unavailable;
                }
            }

            var sessions = rac?.Sessions ?? new List<SessionRow>();
            if (racDue && (manual || now - _lastTechLogScan >= TimeSpan.FromSeconds(Math.Max(15, (double)Settings.TechLogPollSeconds))))
            {
                ApplyTechLog(sessions); _lastTechLogScan = now;
            }
            var processMetrics = rac?.Processes ?? new Dictionary<int, RacProcess>();
            var sessionsByPid = sessions.GroupBy(x => x.Pid).ToDictionary(x => x.Key, x => x.ToList());
            var localPids = local.Select(x => x.Pid).ToHashSet();
            foreach (var p in local)
            {
                if (processMetrics.TryGetValue(p.Pid, out var rp))
                {
                    p.ProcessUuid = rp.ProcessUuid; p.AvgCallMs = rp.AvgCallMs; p.AvgDbMs = rp.AvgDbMs; p.RacMemoryMb = rp.MemoryMb;
                }
                var linked = sessionsByPid.TryGetValue(p.Pid, out var foundSessions) ? foundSessions : new List<SessionRow>();
                ApplyProcessLoadToSessions(p, linked);
                p.SessionCount = linked.Count;
                p.Users = string.Join(", ", linked.Select(x => string.IsNullOrWhiteSpace(x.UserName) ? "<не указан>" : x.UserName).Distinct().Take(8));
                Score(p, linked);
            }
            foreach (var rp in processMetrics.Values.Where(x => !localPids.Contains(x.Pid)))
            {
                var linked = sessionsByPid.TryGetValue(rp.Pid, out var foundSessions) ? foundSessions : new List<SessionRow>();
                var p = new ProcessRow { Pid = rp.Pid, ProcessUuid = rp.ProcessUuid, MemoryMb = rp.MemoryMb, RacMemoryMb = rp.MemoryMb, AvgCallMs = rp.AvgCallMs, AvgDbMs = rp.AvgDbMs, SessionCount = linked.Count, Users = string.Join(", ", linked.Select(x => x.UserName).Distinct().Take(8)) };
                ApplyProcessLoadToSessions(p, linked);
                Score(p, linked); local.Add(p);
            }
            local = local.OrderByDescending(x => x.Risk).ThenByDescending(x => x.MemoryMb).ToList();
            await EvaluateGracefulDrainAsync(local, sessions, now);
            if (now - _lastHistorySample >= TimeSpan.FromSeconds(10))
            {
                foreach (var p in local) _history.Add(new HistoryRow(now, p.Pid, p.Cpu, p.MemoryMb, p.SessionCount, p.Users, p.Risk, p.Diagnosis, disk.UsagePercent, disk.ReadBytesPerSec, disk.WriteBytesPerSec, disk.QueueLength, network.ReceiveBytesPerSec, network.SendBytesPerSec, network.UtilizationPercent));
                if (_history.Count > 12000) _history.RemoveRange(0, _history.Count - 12000);
                _lastHistorySample = now;
            }
            if (now - _lastPersistentSample >= TimeSpan.FromSeconds(Math.Max(30, (double)Settings.HistoryWriteSeconds)))
            {
                AppendDailyHistory(local, disk, network, now);
                AccumulateDailyStats(now, local, sessions, disk, network);
                _lastPersistentSample = now;
            }

            await Dispatcher.UIThread.InvokeAsync(() =>
            {
                var selectedPid = SelectedProcess?.Pid;
                Replace(Processes, local); SelectedProcess = selectedPid.HasValue ? Processes.FirstOrDefault(x => x.Pid == selectedPid.Value) : null;
                if (racDue || manual) Replace(Sessions, sessions.OrderByDescending(s => s.Blocked != "нет").ThenBy(s => s.IsSleeping).ThenBy(s => s.UserName));
                ProcessCount = local.Count; SessionCount = sessions.Count;
                ActiveSessionCount = sessions.Count(x => !x.IsSleeping); SleepingSessionCount = sessions.Count(x => x.IsSleeping);
                SessionBreakdownText = $"активные {ActiveSessionCount} • спящие {SleepingSessionCount}";
                var deviations = new List<string>();
                if (disk.Available && disk.UsagePercent >= (double)Settings.DiskAlert) deviations.Add($"диск {disk.UsagePercent:0}% ≥ порога {Settings.DiskAlert:0}% (ср. очередь {DiskQueueLabel(disk)})");
                foreach (var p in local.Where(x => x.Risk >= 50)) deviations.Add($"PID {p.Pid}: {p.Diagnosis}");
                foreach (var s in sessions.Where(x => x.Blocked != "нет")) deviations.Add($"блокировка: {s.UserName}, {s.InfoBase}, PID {s.Pid}");
                AlertCount = deviations.Count;
                TotalMemoryText = FormatMb(local.Sum(x => x.MemoryMb)); TotalCpuText = $"{local.Sum(x => x.Cpu):0.0} %";
                DiskUsageText = disk.Available ? $"{disk.UsagePercent:0.0} %" : "нет данных";
                DiskIoText = disk.Available ? $"чтение {FormatRate(disk.ReadBytesPerSec)} • запись {FormatRate(disk.WriteBytesPerSec)}" : "счётчики недоступны";
                DiskQueueText = disk.Available ? $"средняя очередь {DiskQueueLabel(disk)}" : "Windows PDH";
                Status = deviations.Count > 0 ? $"Отклонения: {deviations.Count} · {string.Join("; ", deviations.Take(3))}{(deviations.Count > 3 ? "; … подробнее на вкладке «Обзор»" : "")}" : "Нагрузка в норме";
                StatusColor = deviations.Count > 0 ? Brushes.LightCoral : Brushes.LightGreen;
                Footer = $"Последнее измерение: {now:dd.MM.yyyy HH:mm:ss} • логических CPU: {Environment.ProcessorCount} • история: {_history.Count} точек";
                UpdateServerHeader(selectedRac, disk, network);
            });
            AddAlerts(local, sessions, disk, now);
            CheckAutomaticDailyReport(now);
            if (manual) Log($"Ручное обновление: rphost={local.Count}, сеансы={sessions.Count}");
        }
        catch (Exception ex) { Status = "Ошибка мониторинга"; StatusColor = Brushes.LightCoral; Log(ex.ToString()); }
        finally { _busy = false; }
    }

    private async Task EvaluateGracefulDrainAsync(List<ProcessRow> processes, List<SessionRow> sessions, DateTime now)
    {
        foreach (var finished in _drainingProcesses.Keys.Where(pid => processes.All(p => p.Pid != pid)).ToList())
        {
            Log($"Плавный вывод PID {finished} завершён: процесс исчез из кластера"); _drainingProcesses.Remove(finished);
        }
        foreach (var p in processes)
            if (_drainingProcesses.TryGetValue(p.Pid, out var drain)) p.DrainStatus = $"Выводится с {drain.StartedAt:HH:mm:ss}";

        if (!Settings.AutoDrainEnabled) { AutoDrainStatus = _drainingProcesses.Count > 0 ? $"Выводятся процессов: {_drainingProcesses.Count}" : "Автовывод rphost выключен"; return; }
        if (_lastRacPath == null || _lastClusterId == null) { AutoDrainStatus = "Автовывод: нет подключения RAC"; return; }
        var threshold = (double)Settings.AutoDrainMemoryMb;
        foreach (var p in processes)
        {
            if (p.MemoryMb >= threshold) { if (!_memoryOverSince.ContainsKey(p.Pid)) _memoryOverSince[p.Pid] = now; }
            else _memoryOverSince.Remove(p.Pid);
        }
        foreach (var missing in _memoryOverSince.Keys.Where(pid => processes.All(p => p.Pid != pid)).ToList()) _memoryOverSince.Remove(missing);
        if (_drainingProcesses.Count > 0) { AutoDrainStatus = $"Плавно выводится PID {string.Join(", ", _drainingProcesses.Keys)}; принудительное завершение не применяется"; return; }
        if ((now - _lastAutomaticDrain).TotalMinutes < (double)Settings.AutoDrainCooldownMinutes) { AutoDrainStatus = $"Автовывод: пауза после предыдущей операции до {_lastAutomaticDrain.AddMinutes((double)Settings.AutoDrainCooldownMinutes):HH:mm}"; return; }
        if (processes.Count <= (int)Settings.AutoDrainMinProcesses) { AutoDrainStatus = $"Автовывод запрещён: процессов {processes.Count}, минимум должно остаться {Settings.AutoDrainMinProcesses}"; return; }
        var candidate = processes.Where(p => !string.IsNullOrWhiteSpace(p.ProcessUuid) && _memoryOverSince.TryGetValue(p.Pid, out var since) && (now - since).TotalMinutes >= (double)Settings.AutoDrainMinutes).OrderByDescending(p => p.MemoryMb).FirstOrDefault();
        if (candidate == null) { var max = processes.OrderByDescending(x => x.MemoryMb).FirstOrDefault(); AutoDrainStatus = max == null ? "Автовывод: процессы не найдены" : $"Автовывод активен · максимум PID {max.Pid}: {max.MemoryText} · порог {Settings.AutoDrainMemoryMb:0} МБ / {Settings.AutoDrainMinutes:0} мин"; return; }
        var active = sessions.Where(s => s.Pid == candidate.Pid && (s.DurationCurrentMs > 0 || s.Blocked != "нет")).ToList();
        if (active.Count > 0) { candidate.DrainStatus = $"Ожидание {active.Count} активных вызовов"; AutoDrainStatus = $"PID {candidate.Pid} превысил порог, ожидается завершение активных вызовов"; return; }
        if (await TurnOffProcessAsync(candidate, true)) _lastAutomaticDrain = now;
    }

    private async Task GracefulTurnOffSelectedAsync()
    {
        if (!CheckConfirmation()) return;
        var process = SelectedProcess;
        if (process == null) { AdminActionStatus = "Сначала выберите процесс rphost в таблице на вкладке «Обзор»"; return; }
        if (Processes.Count <= (int)Settings.AutoDrainMinProcesses) { AdminActionStatus = "Операция отменена: после вывода останется слишком мало процессов"; return; }
        await TurnOffProcessAsync(process, false); AdminConfirmationText = "";
    }

    private async Task<bool> TurnOffProcessAsync(ProcessRow process, bool automatic)
    {
        if (_lastRacPath == null || _lastClusterId == null || string.IsNullOrWhiteSpace(process.ProcessUuid)) { AdminActionStatus = "Невозможно вывести процесс: отсутствует UUID процесса или подключение RAC"; return false; }
        var auth = new List<string> { $"--cluster={_lastClusterId}" };
        if (!string.IsNullOrWhiteSpace(Settings.ClusterUser)) { auth.Add($"--cluster-user={Settings.ClusterUser}"); auth.Add($"--cluster-pwd={Settings.ClusterPassword}"); }
        var variants = new List<List<string>> { new() { "process" }, new() { "process", "turn-off" }, new() { Settings.Endpoint, "process" } };
        variants[0].AddRange(auth); variants[0].Add("turn-off"); variants[0].Add($"--process={process.ProcessUuid}"); variants[0].Add(Settings.Endpoint);
        variants[1].AddRange(auth); variants[1].Add($"--process={process.ProcessUuid}"); variants[1].Add(Settings.Endpoint);
        variants[2].AddRange(auth); variants[2].Add("turn-off"); variants[2].Add($"--process={process.ProcessUuid}");
        Exception? last = null;
        foreach (var args in variants)
        {
            try
            {
                await RacClient.RunAsync(_lastRacPath, args); var now = DateTime.Now; _drainingProcesses[process.Pid] = new DrainState(process.Pid, process.ProcessUuid, now, process.MemoryMb); process.DrainStatus = $"Выводится с {now:HH:mm:ss}";
                var message = $"{(automatic ? "Автоматически" : "Вручную")} запущен плавный вывод PID {process.Pid}, память {process.MemoryText}. Новая нагрузка будет направляться в другие rphost; принудительное завершение не выполняется.";
                AutoDrainStatus = message; AdminActionStatus = message; Log(message); return true;
            }
            catch (Exception ex) { last = ex; }
        }
        var error = $"Не удалось выполнить process turn-off для PID {process.Pid}: {last?.Message}"; AutoDrainStatus = error; AdminActionStatus = error; Log(error); return false;
    }

    private void AccumulateDailyStats(DateTime at, List<ProcessRow> processes, List<SessionRow> sessions, DiskSnapshot disk, NetworkSnapshot network)
    {
        _serverHistory.Add(new ServerHistoryRow(at, processes.Sum(x => x.Cpu), processes.Sum(x => x.MemoryMb), disk.UsagePercent, disk.ReadBytesPerSec, disk.WriteBytesPerSec, disk.QueueLength, network.ReceiveBytesPerSec, network.SendBytesPerSec, network.UtilizationPercent, sessions.Count, sessions.Count(x => !x.IsSleeping), sessions.Count(x => x.IsSleeping)));
        if (_serverHistory.Count > 30000) _serverHistory.RemoveRange(0, _serverHistory.Count - 30000);
        foreach (var s in sessions)
        {
            var key = $"{at:yyyyMMdd}|{s.InfoBase}|{s.UserName}";
            if (!_dailyUsers.TryGetValue(key, out var stat)) _dailyUsers[key] = stat = new DailyUserStat(at.Date, s.UserName, s.InfoBase);
            stat.Samples++; stat.Hosts.Add(s.Host); if (s.Pid > 0) stat.Pids.Add(s.Pid);
            stat.MaxProcessCpu = Math.Max(stat.MaxProcessCpu, s.ProcessCpu); stat.MaxProcessMemoryMb = Math.Max(stat.MaxProcessMemoryMb, s.ProcessMemoryMb);
            stat.MaxCpu5Min = Math.Max(stat.MaxCpu5Min, s.CpuTimeLast5Min); stat.MaxDuration5Min = Math.Max(stat.MaxDuration5Min, s.DurationLast5MinMs);
            stat.MaxDbDuration5Min = Math.Max(stat.MaxDbDuration5Min, s.DurationDbLast5MinMs); stat.MaxCalls5Min = Math.Max(stat.MaxCalls5Min, s.CallsLast5Min);
            stat.MaxMemory5MinMb = Math.Max(stat.MaxMemory5MinMb, Math.Abs(s.MemoryLast5MinMb));
            if (!string.IsNullOrWhiteSpace(s.TechContext)) stat.LastContext = s.TechContext;
            else if (!string.IsNullOrWhiteSpace(s.Operation)) stat.LastContext = s.Operation;
        }
        foreach (var old in _dailyUsers.Where(x => x.Value.Date < at.Date.AddDays(-2)).Select(x => x.Key).ToList()) _dailyUsers.Remove(old);
    }

    private void CheckAutomaticDailyReport(DateTime now)
    {
        if (!Settings.AutoDailyReport || now.TimeOfDay < new TimeSpan(23, 55, 0)) return;
        if (DateTime.TryParse(Settings.LastAutoReportDate, out var last) && last.Date == now.Date) return;
        GenerateDailyReport(now.Date, true);
        Settings.LastAutoReportDate = now.ToString("yyyy-MM-dd"); Settings.Save();
    }

    private void GenerateDailyReport(DateTime date, bool automatic)
    {
        try
        {
            var points = _serverHistory.Where(x => x.At.Date == date.Date).ToList();
            var users = _dailyUsers.Values.Where(x => x.Date == date.Date).OrderByDescending(x => x.MaxProcessCpu).ThenByDescending(x => x.MaxDuration5Min).ToList();
            if (points.Count == 0) { AdminActionStatus = "Нет накопленных данных для суточного отчёта"; return; }
            var folder = string.IsNullOrWhiteSpace(Settings.ReportFolder) ? Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments), "1CServerMonitor", "Reports") : Settings.ReportFolder;
            Directory.CreateDirectory(folder); var file = Path.Combine(folder, $"1C-daily-{Environment.MachineName}-{date:yyyy-MM-dd}.html");
            var hourly = points.GroupBy(x => x.At.Hour).Select(g => new { Hour = g.Key, AvgCpu = g.Average(x => x.Cpu), MaxCpu = g.Max(x => x.Cpu), AvgRam = g.Average(x => x.RamMb), MaxRam = g.Max(x => x.RamMb), AvgDisk = g.Average(x => x.DiskUsage), MaxDisk = g.Max(x => x.DiskUsage), MaxRead = g.Max(x => x.DiskRead), MaxWrite = g.Max(x => x.DiskWrite), MaxQueue = g.Max(x => x.DiskQueue), MaxNetIn = g.Max(x => x.NetIn), MaxNetOut = g.Max(x => x.NetOut), MaxSessions = g.Max(x => x.Sessions) }).ToList();
            var sb = new StringBuilder(200000);
            sb.Append("<!doctype html><html lang=\"ru\"><head><meta charset=\"utf-8\"><title>Суточный отчёт 1С</title><style>body{font:14px Segoe UI,Arial;background:#f4f7fb;color:#172033;margin:0;padding:24px}h1,h2{margin:0 0 12px}.sub{color:#64748b;margin-bottom:18px}.card,section{background:#fff;border:1px solid #dbe3ef;border-radius:10px;padding:16px;margin:12px 0;overflow-x:auto}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:10px}.cards .card{margin:0}.v{font-size:22px;font-weight:700}table{border-collapse:collapse;width:100%;table-layout:auto}th,td{padding:8px 10px;border-bottom:1px solid #dbe3ef;text-align:left;vertical-align:top;white-space:normal;overflow-wrap:anywhere}th{background:#eef3fa;white-space:nowrap}.bad{color:#b91c1c;font-weight:700}.wide{min-width:260px}@media print{@page{size:A4 landscape;margin:8mm}body{background:#fff;padding:0}}</style></head><body>");
            sb.Append($"<h1>{H(Environment.MachineName)} — нагрузка сервера 1С за {date:dd.MM.yyyy}</h1><div class=\"sub\">Покрытие {points.First().At:HH:mm:ss}–{points.Last().At:HH:mm:ss}, измерений: {points.Count} · Разработчик: Zakharevich Igor</div><div class=\"cards\">");
            DailyCard("Максимальный CPU", $"{points.Max(x => x.Cpu):0.0}%"); DailyCard("Максимальная RAM", FormatMb(points.Max(x => x.RamMb))); DailyCard("Максимум диска", $"{points.Max(x => x.DiskUsage):0.0}%"); DailyCard("Очередь диска", $"{points.Max(x => x.DiskQueue):0.0}"); DailyCard("Сеть приём", FormatRate(points.Max(x => x.NetIn))); DailyCard("Сеть отправка", FormatRate(points.Max(x => x.NetOut))); DailyCard("Максимум сеансов", points.Max(x => x.Sessions).ToString());
            var racDescription = !string.IsNullOrWhiteSpace(_lastRacPath) && File.Exists(_lastRacPath) ? $"{FileVersionInfo.GetVersionInfo(_lastRacPath).FileVersion} · {ArchitectureLabel(_lastRacPath)} · {_lastRacPath}" : "не подключён";
            sb.Append("</div><section><h2>Описание и параметры сервера</h2><table><tbody>");
            ServerParam("Имя сервера", Environment.MachineName); ServerParam("Операционная система", RuntimeInformation.OSDescription.Trim()); ServerParam("Архитектура ОС", RuntimeInformation.OSArchitecture.ToString());
            ServerParam("Процессор", GetCpuDescription()); ServerParam("Логические процессоры", Environment.ProcessorCount.ToString()); ServerParam("Оперативная память", GetPhysicalMemory());
            ServerParam("Тип сервера и виртуализация", _virtualizationInfo); ServerParam("Физические накопители", _physicalDiskInfo); ServerParam("Тома, занято и свободно", GetVolumeSummary());
            ServerParam("Платформа 1С / RAC", racDescription); ServerParam("Адрес RAS", Settings.Endpoint); ServerParam("Процессы rphost на момент отчёта", ProcessCount.ToString());
            ServerParam("Текущая сеть", NetworkLoadText); ServerParam("Текущий диск", $"{DiskUsageText} · {DiskIoText} · {DiskQueueText}"); ServerParam("Период сбора", $"{points.First().At:dd.MM.yyyy HH:mm:ss} — {points.Last().At:dd.MM.yyyy HH:mm:ss}");
            sb.Append("</tbody></table><p class=\"sub\">Отчёт описывает фактически наблюдавшийся период. Средние и максимальные значения рассчитаны по измерениям программы, а нагрузка пользователей сопоставлена с сеансами и процессами 1С через RAC.</p></section>");
            sb.Append("<section><h2>Рост нагрузки по часам</h2><table><thead><tr><th>Час</th><th>CPU сред.</th><th>CPU макс.</th><th>RAM сред.</th><th>RAM макс.</th><th>Диск сред.</th><th>Диск макс.</th><th>Чтение макс.</th><th>Запись макс.</th><th>Очередь</th><th>Сеть ↓</th><th>Сеть ↑</th><th>Сеансы</th></tr></thead><tbody>");
            foreach (var h in hourly) sb.Append($"<tr><td>{h.Hour:00}:00</td><td>{h.AvgCpu:0.0}%</td><td>{h.MaxCpu:0.0}%</td><td>{FormatMb(h.AvgRam)}</td><td>{FormatMb(h.MaxRam)}</td><td>{h.AvgDisk:0.0}%</td><td class=\"{(h.MaxDisk >= 90 ? "bad" : "")}\">{h.MaxDisk:0.0}%</td><td>{FormatRate(h.MaxRead)}</td><td>{FormatRate(h.MaxWrite)}</td><td>{h.MaxQueue:0.0}</td><td>{FormatRate(h.MaxNetIn)}</td><td>{FormatRate(h.MaxNetOut)}</td><td>{h.MaxSessions}</td></tr>");
            sb.Append("</tbody></table></section><section><h2>Кто генерировал нагрузку</h2><table><thead><tr><th>Пользователь</th><th>База</th><th>Компьютеры</th><th>PID</th><th>CPU rphost макс.</th><th>RAM rphost макс.</th><th>CPU-время 5 мин</th><th>Вызовы 5 мин</th><th>Время 5 мин</th><th>СУБД 5 мин</th><th>ΔRAM 5 мин</th><th class=\"wide\">Документ/контекст</th></tr></thead><tbody>");
            foreach (var u in users) sb.Append($"<tr><td>{H(u.UserName)}</td><td>{H(u.InfoBase)}</td><td>{H(string.Join(", ", u.Hosts))}</td><td>{H(string.Join(", ", u.Pids))}</td><td>{u.MaxProcessCpu:0.0}%</td><td>{FormatMb(u.MaxProcessMemoryMb)}</td><td>{u.MaxCpu5Min:0} мс</td><td>{u.MaxCalls5Min}</td><td>{u.MaxDuration5Min / 1000:0.0} с</td><td>{u.MaxDbDuration5Min / 1000:0.0} с</td><td>{u.MaxMemory5MinMb:0.0} МБ</td><td class=\"wide\">{H(u.LastContext)}</td></tr>");
            sb.Append("</tbody></table></section><section><b>Контакты:</b> Zakharevich Igor · <a href=\"https://csl.by\">csl.by</a> · <a href=\"mailto:info@csl.by\">info@csl.by</a> · +375 29 337 14 12</section></body></html>");
            File.WriteAllText(file, sb.ToString(), new UTF8Encoding(true)); AdminActionStatus = (automatic ? "Автоотчёт создан: " : "Отчёт создан: ") + file; Log(AdminActionStatus);
            if (!automatic && OperatingSystem.IsWindows()) Process.Start(new ProcessStartInfo(file) { UseShellExecute = true });
            void DailyCard(string label, string value) => sb.Append($"<div class=\"card\"><div>{H(label)}</div><div class=\"v\">{H(value)}</div></div>");
            void ServerParam(string name, string value) => sb.Append($"<tr><th>{H(name)}</th><td>{H(value)}</td></tr>");
        }
        catch (Exception ex) { AdminActionStatus = "Ошибка суточного отчёта: " + ex.Message; Log(AdminActionStatus); }
    }

    private void WriteRacDiagnosticIfNeeded(RacSnapshot result)
    {
        if (result.Sessions.Count != 0 && !result.Sessions.Any(x => x.Pid <= 0)) return;
        try
        {
            var diagnosticDir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "1CServerMonitor");
            Directory.CreateDirectory(diagnosticDir);
            var diagFile = Path.Combine(diagnosticDir, "last-rac-diagnostic.txt");
            File.WriteAllText(diagFile, $"Generated: {DateTime.Now:O}\r\nRAC: {result.RacPath}\r\nEndpoint: {result.Endpoint}\r\nClusters found: {string.Join(", ", result.ClusterIds)}\r\nSelected cluster: {result.ClusterId}\r\nParsed sessions: {result.Sessions.Count}\r\n\r\n=== CLUSTER LIST ===\r\n{result.ClusterOutput}\r\n=== INFOBASE LIST ===\r\n{result.InfoBaseOutput}\r\n=== PROCESS LIST ===\r\n{result.ProcessOutput}\r\n=== CONNECTION LIST ===\r\n{result.ConnectionOutput}\r\n=== SESSION LIST ===\r\n{result.SessionOutput}", new UTF8Encoding(true));
            if (result.Sessions.Count == 0) Log("RAC подключён, но session list пуст. Диагностика: " + diagFile);
        }
        catch { }
    }

    private bool CheckConfirmation(bool killRphost = false)
    {
        var expected = killRphost ? "УБИТЬ RPHOST" : "ПОДТВЕРЖДАЮ";
        if (!string.Equals(AdminConfirmationText.Trim(), expected, StringComparison.OrdinalIgnoreCase)) { AdminActionStatus = $"Введите контрольную фразу: {expected}"; return false; }
        return true;
    }

    private async Task SetBackgroundJobsAsync(bool enable)
    {
        if (!CheckConfirmation()) return;
        if (_lastRacPath == null || _lastClusterId == null) { AdminActionStatus = "Нет подключения RAC"; return; }
        if (_lastInfoBases.Count == 0) { AdminActionStatus = "Информационные базы не найдены через RAC"; return; }

        var tracked = Settings.BackgroundJobsDisabledInfobases;
        var targets = enable
            ? _lastInfoBases.Values.Where(x => tracked.Contains(x.Uuid, StringComparer.OrdinalIgnoreCase)).ToList()
            : _lastInfoBases.Values.ToList();
        if (targets.Count == 0)
        {
            AdminActionStatus = enable ? "Нет баз, фоновые задания которых выключались этим монитором" : "Во всех найденных базах фоновые задания уже запрещены";
            return;
        }

        var ok = 0; var errors = 0; var skipped = 0; var state = enable ? "off" : "on";
        foreach (var infoBase in targets)
        {
            if (!enable && await IsScheduledJobsAlreadyDeniedAsync(infoBase))
            {
                skipped++; Log($"Фоновые задания уже были запрещены до операции, база не будет добавлена в список обратного включения: {infoBase.Name}"); continue;
            }
            AdminActionStatus = $"{(enable ? "Включение" : "Выключение")} фоновых заданий: {infoBase.Name} ({ok + errors + 1}/{targets.Count})…";
            var auth = new List<string> { $"--cluster={_lastClusterId}", $"--infobase={infoBase.Uuid}", $"--scheduled-jobs-deny={state}" };
            if (!string.IsNullOrWhiteSpace(Settings.ClusterUser)) { auth.Add($"--cluster-user={Settings.ClusterUser}"); auth.Add($"--cluster-pwd={Settings.ClusterPassword}"); }
            var variants = new List<List<string>>
            {
                new[] { "infobase", "update" }.Concat(auth).Append(Settings.Endpoint).ToList(),
                new[] { Settings.Endpoint, "infobase", "update" }.Concat(auth).ToList(),
                new[] { "infobase" }.Concat(auth).Append("update").Append(Settings.Endpoint).ToList()
            };
            Exception? last = null; var changed = false;
            foreach (var args in variants)
            {
                try { await RacClient.RunAsync(_lastRacPath, args, Math.Max(15, (int)Settings.RacTimeoutSeconds)); changed = true; break; }
                catch (Exception ex) { last = ex; }
            }
            if (changed)
            {
                ok++;
                if (enable) tracked.RemoveAll(x => x.Equals(infoBase.Uuid, StringComparison.OrdinalIgnoreCase));
                else if (!tracked.Contains(infoBase.Uuid, StringComparer.OrdinalIgnoreCase)) tracked.Add(infoBase.Uuid);
                Log($"Фоновые задания {(enable ? "разрешены" : "запрещены")}: {infoBase.Name} ({infoBase.Uuid})");
            }
            else { errors++; Log($"Не изменён режим фоновых заданий {infoBase.Name}: {last?.Message}"); }
        }
        Settings.Save();

        if (!enable && ok > 0)
        {
            var running = Sessions.Where(SessionAnalyzer.IsBackgroundSession).ToList();
            if (running.Count > 0)
            {
                AdminActionStatus = $"Новые фоновые задания запрещены в {ok} базах. Завершение {running.Count} уже работающих фоновых сеансов…";
                await TerminateSessionsAsync(running, "уже работающих фоновых заданий");
                return;
            }
        }
        AdminConfirmationText = "";
        AdminActionStatus = $"Фоновые задания {(enable ? "включены" : "выключены")}: баз успешно {ok}, уже были запрещены {skipped}, ошибок {errors}.";
        Log(AdminActionStatus); await RefreshAsync(true);

        async Task<bool> IsScheduledJobsAlreadyDeniedAsync(InfoBaseRef infoBase)
        {
            var options = new List<string> { $"--cluster={_lastClusterId}", $"--infobase={infoBase.Uuid}" };
            if (!string.IsNullOrWhiteSpace(Settings.ClusterUser)) { options.Add($"--cluster-user={Settings.ClusterUser}"); options.Add($"--cluster-pwd={Settings.ClusterPassword}"); }
            var variants = new List<List<string>>
            {
                new[] { "infobase", "info" }.Concat(options).Append(Settings.Endpoint).ToList(),
                new[] { Settings.Endpoint, "infobase", "info" }.Concat(options).ToList(),
                new[] { "infobase" }.Concat(options).Append("info").Append(Settings.Endpoint).ToList()
            };
            foreach (var args in variants)
            {
                try
                {
                    var record = RacClient.ParseRecords(await RacClient.RunAsync(_lastRacPath!, args, Math.Max(15, (int)Settings.RacTimeoutSeconds))).FirstOrDefault();
                    if (record != null) return RacHelpers.IsTrue(record.Get("scheduled-jobs-deny"));
                }
                catch { }
            }
            return infoBase.ScheduledJobsDenied;
        }
    }

    private async Task TerminateSessionsAsync(List<SessionRow> targets, string description)
    {
        if (!CheckConfirmation()) return;
        if (_lastRacPath == null || _lastClusterId == null) { AdminActionStatus = "Нет подключения RAC"; return; }
        if (targets.Count == 0) { AdminActionStatus = "Подходящие сеансы не найдены"; return; }
        var ok = 0; var errors = 0; AdminActionStatus = $"Завершение: {targets.Count}…";
        foreach (var s in targets.Where(x => !string.IsNullOrWhiteSpace(x.SessionUuid)))
        {
            AdminActionStatus = $"Завершается сеанс {s.UserName} ({ok + errors + 1}/{targets.Count})…";
            try
            {
                await RacClient.TerminateSessionAsync(_lastRacPath, Settings.Endpoint, _lastClusterId, s.SessionUuid, Settings.ClusterUser, Settings.ClusterPassword, Math.Max(15, (int)Settings.RacTimeoutSeconds));
                ok++; Log($"Сеанс завершён: {s.UserName}, UUID {s.SessionUuid}");
            }
            catch (Exception ex) { errors++; Log($"Не завершён сеанс {s.UserName}: {ex.Message}"); }
        }
        await Task.Delay(1200);
        AdminConfirmationText = ""; AdminActionStatus = $"Завершение {description}: успешно {ok}, ошибок {errors}. Подробности во вкладке «Журнал»."; Log(AdminActionStatus); await RefreshAsync(true);
    }

    private async Task RestartOneCServicesAsync()
    {
        if (!CheckConfirmation()) return;
        const string script = "$s=Get-CimInstance Win32_Service | Where-Object {$_.PathName -match 'ragent.exe'}; if(-not $s){throw 'Службы ragent.exe не найдены'}; $s | ForEach-Object { Restart-Service -Name $_.Name -Force -ErrorAction Stop; $_.Name }";
        try { AdminActionStatus = "Перезапуск всех служб сервера 1С…"; var output = await RacClient.RunAsync("powershell.exe", new[] { "-NoProfile", "-NonInteractive", "-Command", script }, 180); AdminActionStatus = "Перезапущены службы: " + output.Replace("\r", " ").Replace("\n", ", ").Trim(' ', ','); Log(AdminActionStatus); }
        catch (Exception ex) { AdminActionStatus = "Ошибка перезапуска служб: " + ex.Message; Log(AdminActionStatus); }
        finally { AdminConfirmationText = ""; }
    }

    private async Task KillAllRphostAsync()
    {
        if (!CheckConfirmation(true)) return;
        try { AdminActionStatus = "Принудительное завершение всех rphost…"; await RacClient.RunAsync("taskkill.exe", new[] { "/F", "/IM", "rphost.exe" }); AdminActionStatus = "Все процессы rphost принудительно завершены"; Log(AdminActionStatus); }
        catch (Exception ex) { AdminActionStatus = "Ошибка завершения rphost: " + ex.Message; Log(AdminActionStatus); }
        finally { AdminConfirmationText = ""; }
    }

    private void ApplyTechLog(List<SessionRow> sessions)
    {
        var path = Settings.TechLogPath;
        if (string.IsNullOrWhiteSpace(path) || !Directory.Exists(path) || sessions.Count == 0) return;
        try
        {
            if (DateTime.UtcNow - _techLogFileCacheAt >= TimeSpan.FromMinutes(10) || _cachedTechLogFiles.Count == 0)
            {
                _cachedTechLogFiles.Clear();
                _cachedTechLogFiles.AddRange(Directory.EnumerateFiles(path, "*.log", SearchOption.AllDirectories)
                    .Select(x => new FileInfo(x)).OrderByDescending(x => x.LastWriteTimeUtc).Take(8).Select(x => x.FullName));
                _techLogFileCacheAt = DateTime.UtcNow;
            }
            var lines = new List<string>();
            foreach (var file in _cachedTechLogFiles.Where(File.Exists))
            {
                using var fs = new FileStream(file, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
                if (fs.Length > 131072) fs.Seek(-131072, SeekOrigin.End);
                using var reader = new StreamReader(fs, Encoding.UTF8, true);
                if (fs.Position > 0) reader.ReadLine();
                while (reader.ReadLine() is { } line) if (line.Contains("Context=", StringComparison.OrdinalIgnoreCase)) lines.Add(line);
            }
            foreach (var session in sessions)
            {
                var line = lines.LastOrDefault(x =>
                    (!string.IsNullOrWhiteSpace(session.SessionNumber) && Regex.IsMatch(x, $@"SessionID\s*=\s*{Regex.Escape(session.SessionNumber)}(?:\D|$)", RegexOptions.IgnoreCase)) ||
                    (!string.IsNullOrWhiteSpace(session.UserName) && x.Contains($"Usr={session.UserName}", StringComparison.OrdinalIgnoreCase)));
                if (line == null) continue;
                var context = Regex.Match(line, "Context\\s*=\\s*(?:'([^']*)'|\\\"([^\\\"]*)\\\"|([^,\\r\\n]+))", RegexOptions.IgnoreCase);
                if (context.Success) session.TechContext = context.Groups.Cast<Group>().Skip(1).FirstOrDefault(g => g.Success)?.Value.Trim() ?? "";
                var duration = Regex.Match(line, @"^\d{2}:\d{2}\.\d+-(\d+)");
                if (duration.Success && double.TryParse(duration.Groups[1].Value, out var microseconds)) session.LastCallMs = microseconds / 1000d;
            }
        }
        catch (Exception ex) { Log("Технологический журнал: " + ex.Message); }
    }

    private List<ProcessRow> SampleLocalProcesses()
    {
        var rows = new List<ProcessRow>(); var now = DateTime.UtcNow;
        foreach (var proc in Process.GetProcessesByName("rphost"))
        {
            try
            {
                var total = proc.TotalProcessorTime;
                double cpu = 0;
                if (_cpu.TryGetValue(proc.Id, out var old))
                {
                    var sec = (now - old.At).TotalSeconds;
                    if (sec > 0) cpu = Math.Max(0, Math.Min(100, (total - old.Total).TotalSeconds / sec / Environment.ProcessorCount * 100));
                }
                _cpu[proc.Id] = new CpuSample(now, total);
                rows.Add(new ProcessRow { Pid = proc.Id, Cpu = cpu, MemoryMb = proc.WorkingSet64 / 1048576d });
            }
            catch { }
            finally { proc.Dispose(); }
        }
        var alive = rows.Select(x => x.Pid).ToHashSet();
        foreach (var stale in _cpu.Keys.Where(x => !alive.Contains(x)).ToList()) _cpu.Remove(stale);
        return rows;
    }

    private static void ApplyProcessLoadToSessions(ProcessRow process, List<SessionRow> sessions)
    {
        // ОС учитывает CPU/RAM на уровне rphost, а не отдельного сеанса. Поэтому показываем
        // точную нагрузку процесса и отдельно — оценочную долю CPU с большим весом активных сеансов.
        var totalWeight = sessions.Sum(s => s.IsSleeping ? 0.05 : 1.0);
        foreach (var s in sessions)
        {
            s.ProcessCpu = process.Cpu; s.ProcessMemoryMb = process.MemoryMb;
            s.EstimatedCpu = totalWeight > 0 ? process.Cpu * (s.IsSleeping ? 0.05 : 1.0) / totalWeight : 0;
        }
    }

    private void Score(ProcessRow p, List<SessionRow> linked)
    {
        var reasons = new List<string>(); int risk = 0;
        var cpuLimit = (double)Settings.CpuAlert; var memoryLimit = (double)Settings.MemoryAlertMb; var callLimit = (double)Settings.CallAlertMs;
        if (p.Cpu >= cpuLimit) { risk += 45; reasons.Add($"высокий CPU {p.Cpu:0}%"); }
        else if (p.Cpu >= cpuLimit * .7) { risk += 20; reasons.Add("повышенный CPU"); }
        if (p.MemoryMb >= memoryLimit) { risk += 40; reasons.Add($"RAM {FormatMb(p.MemoryMb)}"); }
        else if (p.MemoryMb >= memoryLimit * .75) { risk += 15; reasons.Add("RAM близка к порогу"); }
        if (p.AvgCallMs >= callLimit) { risk += 35; reasons.Add($"долгие вызовы {p.AvgCallMs:0} мс"); }
        if (linked.Any(x => x.Blocked != "нет")) { risk += 45; reasons.Add("есть блокировки"); }
        if (p.SessionCount > 100) { risk += 15; reasons.Add($"много сеансов: {p.SessionCount}"); }
        p.Risk = Math.Min(100, risk); p.Diagnosis = reasons.Count == 0 ? "Норма" : string.Join("; ", reasons);
    }

    private void AddAlerts(List<ProcessRow> proc, List<SessionRow> sessions, DiskSnapshot disk, DateTime now)
    {
        var candidates = new List<(string Key, AlertRow Row)>();
        if (disk.Available && disk.UsagePercent >= (double)Settings.DiskAlert)
            candidates.Add(("disk", new(now, disk.UsagePercent >= 98 ? "КРИТИЧНО" : "ВНИМАНИЕ", "Диск", $"Загрузка {disk.UsagePercent:0.0}% за интервал, чтение {FormatRate(disk.ReadBytesPerSec)}, запись {FormatRate(disk.WriteBytesPerSec)}, средняя очередь {DiskQueueLabel(disk)}")));
        foreach (var p in proc.Where(x => x.Risk >= 50)) candidates.Add(($"pid:{p.Pid}", new(now, p.Risk >= 80 ? "КРИТИЧНО" : "ВНИМАНИЕ", $"PID {p.Pid}", p.Diagnosis + (string.IsNullOrEmpty(p.Users) ? "" : $"; пользователи: {p.Users}"))));
        foreach (var s in sessions.Where(x => x.Blocked != "нет")) candidates.Add(($"block:{s.SessionUuid}:{s.Pid}", new(now, "БЛОКИРОВКА", s.UserName, $"База {s.InfoBase}, PID {s.Pid}, {s.Blocked}")));

        var repeatAfter = TimeSpan.FromSeconds(Math.Max(60, (double)Settings.AlertRepeatSeconds));
        var add = new List<AlertRow>();
        foreach (var item in candidates)
        {
            if (_lastAlertEmission.TryGetValue(item.Key, out var last) && now - last < repeatAfter) continue;
            _lastAlertEmission[item.Key] = now; add.Add(item.Row);
        }
        foreach (var expired in _lastAlertEmission.Where(x => now - x.Value > TimeSpan.FromDays(1)).Select(x => x.Key).ToList()) _lastAlertEmission.Remove(expired);
        if (add.Count > 0) Dispatcher.UIThread.Post(() => { foreach (var a in add.Take(20).Reverse()) Alerts.Insert(0, a); while (Alerts.Count > 100) Alerts.RemoveAt(Alerts.Count - 1); });
    }

    private async Task StartRasAsync()
    {
        try
        {
            var rac = _rac.FindRac(Settings.RacPath) ?? throw new Exception("Сначала укажите путь к rac.exe");
            RacStatus = $"Запуск RAS для {Settings.AgentEndpoint}…";
            var readyEndpoint = await RacClient.EnsureRasRunningAsync(rac, Settings.ToRacOptions(), msg => Log(msg), _stop.Token);
            Settings.Endpoint = readyEndpoint;
            RacStatus = $"RAS готов: {readyEndpoint} · агент {Settings.AgentEndpoint}";
            Log(RacStatus);
            await RefreshAsync(true);
        }
        catch (Exception ex) { RacStatus = "Ошибка запуска RAS: " + ex.Message; Log(RacStatus); }
    }

    private static string ArchitectureLabel(string executable)
    {
        var x86Folder = Environment.GetFolderPath(Environment.SpecialFolder.ProgramFilesX86);
        return !string.IsNullOrEmpty(x86Folder) && executable.StartsWith(x86Folder, StringComparison.OrdinalIgnoreCase) ? "x86" : Environment.Is64BitOperatingSystem ? "x64/не определено" : "x86 OS";
    }

    private static void AppendDailyHistory(IEnumerable<ProcessRow> rows, DiskSnapshot disk, NetworkSnapshot network, DateTime at)
    {
        try
        {
            var dir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "1CServerMonitor", "history");
            Directory.CreateDirectory(dir); var file = Path.Combine(dir, $"history-{at:yyyy-MM-dd}.csv"); var exists = File.Exists(file);
            using var w = new StreamWriter(file, true, new UTF8Encoding(!exists));
            if (!exists) w.WriteLine("Время;PID;CPU %;RAM МБ;Сеансы;Пользователи;Риск;Диагноз;Диск %;Чтение байт/с;Запись байт/с;Очередь диска;Сеть приём байт/с;Сеть отправка байт/с;Сеть %");
            foreach (var p in rows) w.WriteLine($"{at:dd.MM.yyyy HH:mm:ss};{p.Pid};{p.Cpu.ToString("0.0", CultureInfo.InvariantCulture)};{p.MemoryMb.ToString("0.0", CultureInfo.InvariantCulture)};{p.SessionCount};{Csv(p.Users)};{p.Risk};{Csv(p.Diagnosis)};{F(disk.UsagePercent)};{F(disk.ReadBytesPerSec)};{F(disk.WriteBytesPerSec)};{F(disk.QueueLength)};{F(network.ReceiveBytesPerSec)};{F(network.SendBytesPerSec)};{F(network.UtilizationPercent)}");
        }
        catch { }
    }

    private void ExportHtmlReport()
    {
        try
        {
            var dir = Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory); if (string.IsNullOrEmpty(dir)) dir = AppContext.BaseDirectory;
            var file = Path.Combine(dir, $"1C-monitor-{DateTime.Now:yyyyMMdd-HHmmss}.html");
            var sb = new StringBuilder(256000);
            sb.Append("""
<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Отчёт мониторинга сервера 1С</title><style>
:root{color-scheme:light;--bg:#f4f7fb;--card:#fff;--line:#d9e2ef;--text:#172033;--muted:#607089;--blue:#2563eb;--red:#b91c1c;--orange:#c2410c;--green:#047857}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 "Segoe UI",Arial,sans-serif}.page{max-width:1800px;margin:auto;padding:24px}h1{font-size:25px;margin:0 0 4px}h2{font-size:18px;margin:0 0 12px}.sub{color:var(--muted);margin-bottom:20px}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:12px;margin-bottom:18px}.card,.section{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px}.value{font-size:24px;font-weight:700;margin-top:5px}.label{font-size:12px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}.section{margin:14px 0;overflow-x:auto}table{border-collapse:collapse;width:100%;table-layout:auto}th,td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:left;vertical-align:top;white-space:normal;overflow:visible;text-overflow:clip;overflow-wrap:anywhere;word-break:normal;min-width:80px}th{background:#eef3fa;font-size:12px;position:sticky;top:0}td.nowrap,th.nowrap{white-space:nowrap;overflow-wrap:normal;min-width:max-content}.wide{min-width:240px}.medium{min-width:150px}.ok{color:var(--green);font-weight:600}.warn{color:var(--orange);font-weight:700}.critical{color:var(--red);font-weight:700}.note{padding:10px 12px;background:#eff6ff;border-left:4px solid var(--blue);margin:12px 0;color:#334155}details summary{cursor:pointer;font-weight:600;padding:5px}@media print{body{background:#fff}.page{max-width:none;padding:8px}.section,.card{break-inside:avoid;box-shadow:none}th{position:static}@page{size:A4 landscape;margin:8mm}}
</style></head><body><main class="page">
""");
            sb.Append($"<h1>Отчёт мониторинга сервера 1С</h1><div class=\"sub\">Сформирован {DateTime.Now:dd.MM.yyyy HH:mm:ss} · {H(Status)} · Разработчик: Zakharevich Igor</div>");
            sb.Append("<div class=\"cards\">");
            Card("Процессы rphost", ProcessCount.ToString()); Card("CPU rphost", TotalCpuText); Card("Память rphost", TotalMemoryText);
            Card("Активные сеансы", ActiveSessionCount.ToString()); Card("Спящие сеансы", SleepingSessionCount.ToString()); Card("Тревоги", AlertCount.ToString());
            Card("Загрузка диска", DiskUsageText); Card("Ввод-вывод", DiskIoText); Card("Очередь диска", DiskQueueText); Card("Сеть", NetworkLoadText);
            sb.Append("</div>");
            sb.Append($"<section class=\"section\"><h2>Сервер и накопители</h2><p>{H(ServerHeaderDetails).Replace("\n", "<br>")}</p><p><strong>Оборудование:</strong> {H(ServerHardwareText)}</p><p><strong>Сеть:</strong> {H(NetworkLoadText)}</p></section>");
            sb.Append("<section class=\"section\"><h2>Рабочие процессы</h2><table><thead><tr><th>Риск</th><th>PID</th><th>Состояние процесса</th><th>CPU</th><th>RAM</th><th>Сеансы</th><th>Средний вызов</th><th>Средний запрос БД</th><th class=\"wide\">Пользователи</th><th class=\"wide\">Диагноз</th></tr></thead><tbody>");
            foreach (var p in Processes) sb.Append($"<tr><td class=\"{RiskCss(p.Risk)} nowrap\">{p.Risk}</td><td class=\"nowrap\">{p.Pid}</td><td>{H(p.DrainStatus)}</td><td class=\"nowrap\">{H(p.CpuText)}</td><td class=\"nowrap\">{H(p.MemoryText)}</td><td>{p.SessionCount}</td><td class=\"nowrap\">{H(p.AvgCallText)}</td><td class=\"nowrap\">{H(p.AvgDbText)}</td><td class=\"wide\">{H(p.Users)}</td><td class=\"wide\">{H(p.Diagnosis)}</td></tr>");
            sb.Append("</tbody></table></section>");
            sb.Append("<section class=\"section\"><h2>Пользователи и сеансы</h2><div class=\"note\">CPU и RAM измеряются на уровне rphost. CPU пользователя — оценочная доля. Контекст документа берётся из технологического журнала.</div><table><thead><tr><th>Состояние</th><th class=\"medium\">Пользователь</th><th>База</th><th>Приложение</th><th class=\"medium\">Компьютер</th><th>PID</th><th>Память вызова RAC</th><th>CPU-время RAC</th><th>Текущий вызов</th><th>Вызов СУБД</th><th>I/O сеанса</th><th class=\"wide\">Нагрузка за 5 минут</th><th class=\"wide\">I/O за 5 минут</th><th>CPU rphost</th><th>RAM rphost</th><th>CPU пользователя*</th><th class=\"nowrap\">Начало</th><th class=\"nowrap\">Последняя активность</th><th class=\"wide\">Метод/операция RAC</th><th class=\"wide\">Документ/контекст ТЖ</th><th>Время вызова ТЖ</th><th class=\"wide\">Запрос СУБД</th><th>Блокировка</th></tr></thead><tbody>");
            foreach (var s in Sessions) sb.Append($"<tr><td class=\"{(s.IsSleeping ? "" : "ok")} nowrap\">{H(s.SessionState)}</td><td class=\"medium\">{H(s.UserName)}</td><td>{H(s.InfoBase)}</td><td>{H(s.AppId)}</td><td class=\"medium\">{H(s.Host)}</td><td class=\"nowrap\">{(s.Pid > 0 ? s.Pid.ToString() : "—")}</td><td class=\"nowrap\">{H(s.SessionMemoryText)}</td><td class=\"nowrap\">{H(s.SessionCpuTimeText)}</td><td class=\"nowrap\">{H(s.CurrentCallText)}</td><td class=\"nowrap\">{H(s.CurrentDbText)}</td><td class=\"nowrap\">{H(s.IoCurrentText)}</td><td class=\"wide\">{H(s.Load5MinText)}</td><td class=\"wide\">{H(s.Io5MinText)}</td><td class=\"nowrap\">{H(s.ProcessCpuText)}</td><td class=\"nowrap\">{H(s.ProcessMemoryText)}</td><td class=\"nowrap\">{H(s.EstimatedCpuText)}</td><td class=\"nowrap\">{H(s.StartedAt)}</td><td class=\"nowrap\">{H(s.LastActiveAt)}</td><td class=\"wide\">{H(s.Operation)}</td><td class=\"wide\">{H(s.TechContext)}</td><td class=\"nowrap\">{H(s.LastCallText)}</td><td class=\"wide\">{H(s.DbRequest)}</td><td class=\"{(s.Blocked == "нет" ? "" : "critical")}\">{H(s.Blocked)}</td></tr>");
            sb.Append("</tbody></table></section>");
            sb.Append("<section class=\"section\"><h2>Тревоги</h2><table><thead><tr><th class=\"nowrap\">Время</th><th>Уровень</th><th>Источник</th><th class=\"wide\">Сообщение</th></tr></thead><tbody>");
            foreach (var a in Alerts) sb.Append($"<tr><td class=\"nowrap\">{H(a.TimeText)}</td><td class=\"critical\">{H(a.Severity)}</td><td>{H(a.Source)}</td><td class=\"wide\">{H(a.Message)}</td></tr>");
            sb.Append("</tbody></table></section>");
            sb.Append("<section class=\"section\"><details><summary>История измерений (последние 5000 точек)</summary><table><thead><tr><th>Время</th><th>PID</th><th>CPU %</th><th>RAM МБ</th><th>Сеансы</th><th class=\"wide\">Пользователи</th><th>Риск</th><th>Диск %</th><th>Чтение</th><th>Запись</th><th>Очередь</th><th>Сеть ↓</th><th>Сеть ↑</th><th>Сеть %</th><th class=\"wide\">Диагноз</th></tr></thead><tbody>");
            foreach (var h in _history.TakeLast(5000)) sb.Append($"<tr><td class=\"nowrap\">{h.At:dd.MM.yyyy HH:mm:ss}</td><td>{h.Pid}</td><td>{h.Cpu:0.0}</td><td>{h.MemoryMb:0.0}</td><td>{h.Sessions}</td><td class=\"wide\">{H(h.Users)}</td><td class=\"{RiskCss(h.Risk)}\">{h.Risk}</td><td>{h.DiskUsage:0.0}</td><td class=\"nowrap\">{H(FormatRate(h.DiskRead))}</td><td class=\"nowrap\">{H(FormatRate(h.DiskWrite))}</td><td>{h.DiskQueue:0.00}</td><td class=\"nowrap\">{H(FormatRate(h.NetworkReceive))}</td><td class=\"nowrap\">{H(FormatRate(h.NetworkSend))}</td><td>{h.NetworkUsage:0.0}</td><td class=\"wide\">{H(h.Diagnosis)}</td></tr>");
            sb.Append("</tbody></table></details></section><section class=\"section\"><h2>Контакты разработчика</h2><table><tbody><tr><th>Разработчик</th><td>Zakharevich Igor</td></tr><tr><th>Организация</th><td>ООО «Компьютерная безопасность»</td></tr><tr><th>Сайт</th><td><a href=\"https://csl.by\">https://csl.by</a></td></tr><tr><th>Email</th><td><a href=\"mailto:info@csl.by\">info@csl.by</a></td></tr><tr><th>Телефон</th><td><a href=\"tel:+375293371412\">+375 29 337 14 12</a></td></tr><tr><th>Адрес</th><td>Республика Беларусь, г. Минск, ул. Фабрициуса, 9</td></tr><tr><th>Время работы</th><td>Понедельник–пятница, 9:00–17:00</td></tr></tbody></table></section><div class=\"sub\">1C Server Monitor · автономный HTML-отчёт · Zakharevich Igor · csl.by</div></main></body></html>");
            File.WriteAllText(file, sb.ToString(), new UTF8Encoding(true));
            Log("HTML-отчёт сохранён: " + file); Status = "HTML-отчёт сохранён на рабочем столе";
            if (OperatingSystem.IsWindows()) Process.Start(new ProcessStartInfo(file) { UseShellExecute = true });

            void Card(string label, string value) => sb.Append($"<div class=\"card\"><div class=\"label\">{H(label)}</div><div class=\"value\">{H(value)}</div></div>");
        }
        catch (Exception ex) { Log("Ошибка создания HTML-отчёта: " + ex.Message); }
    }

    private static string H(string? value) => System.Net.WebUtility.HtmlEncode(value ?? "");
    private static string RiskCss(int risk) => risk >= 80 ? "critical" : risk >= 50 ? "warn" : risk >= 20 ? "warn" : "ok";

    private void LogOnce(string key, string text, TimeSpan repeatAfter)
    {
        var now = DateTime.Now;
        if (_lastAlertEmission.TryGetValue("log:" + key, out var last) && now - last < repeatAfter) return;
        _lastAlertEmission["log:" + key] = now; Log(text);
    }

    private void Log(string text)
    {
        Dispatcher.UIThread.Post(() => { _log.AppendLine($"[{DateTime.Now:dd.MM.yyyy HH:mm:ss}] {text}"); if (_log.Length > 100000) _log.Remove(0, 20000); LogText = _log.ToString(); });
    }
    private static string FormatMb(double mb) => mb >= 1024 ? $"{mb / 1024:0.00} ГБ" : $"{mb:0} МБ";
    private static string FormatRate(double bytes) => bytes >= 1073741824 ? $"{bytes / 1073741824:0.00} ГБ/с" : bytes >= 1048576 ? $"{bytes / 1048576:0.0} МБ/с" : $"{bytes / 1024:0} КБ/с";
    private static string DiskQueueLabel(DiskSnapshot disk)
    {
        // Некоторые SAN/NETAPP/RAID/виртуальные драйверы не экспортируют очередь в Windows PDH
        // и всегда возвращают ноль даже при интенсивном I/O. Не выдаём такой ноль за достоверный.
        if (disk.QueueLength < 0.005 && disk.UsagePercent >= 90 && disk.ReadBytesPerSec + disk.WriteBytesPerSec >= 1048576)
            return "0,00 (возможно, драйвер SAN/RAID не сообщает)";
        return disk.QueueLength.ToString("0.00", CultureInfo.CurrentCulture);
    }
    private static string F(double value) => value.ToString("0.00", CultureInfo.InvariantCulture);
    private static string Csv(string s) => "\"" + (s ?? "").Replace("\"", "\"\"") + "\"";
    private static void Replace<T>(ObservableCollection<T> target, IEnumerable<T> src) { target.Clear(); foreach (var x in src) target.Add(x); }
    public void Dispose() { _stop.Cancel(); _disk.Dispose(); _network.Dispose(); }
    private record CpuSample(DateTime At, TimeSpan Total);
    private record HistoryRow(DateTime At, int Pid, double Cpu, double MemoryMb, int Sessions, string Users, int Risk, string Diagnosis, double DiskUsage, double DiskRead, double DiskWrite, double DiskQueue, double NetworkReceive, double NetworkSend, double NetworkUsage);
    private record ServerHistoryRow(DateTime At, double Cpu, double RamMb, double DiskUsage, double DiskRead, double DiskWrite, double DiskQueue, double NetIn, double NetOut, double NetUsage, int Sessions, int ActiveSessions, int SleepingSessions);
    private record DrainState(int Pid, string ProcessUuid, DateTime StartedAt, double MemoryMb);
    private sealed class DailyUserStat
    {
        public DailyUserStat(DateTime date, string userName, string infoBase) { Date = date; UserName = userName; InfoBase = infoBase; }
        public DateTime Date { get; } public string UserName { get; } public string InfoBase { get; } public int Samples { get; set; }
        public HashSet<string> Hosts { get; } = new(StringComparer.OrdinalIgnoreCase); public HashSet<int> Pids { get; } = new();
        public double MaxProcessCpu { get; set; } public double MaxProcessMemoryMb { get; set; } public double MaxCpu5Min { get; set; }
        public double MaxDuration5Min { get; set; } public double MaxDbDuration5Min { get; set; } public int MaxCalls5Min { get; set; } public double MaxMemory5MinMb { get; set; }
        public string LastContext { get; set; } = "";
    }
}

public sealed class MonitorSettings : NotifyBase
{
    private string _racPath = "", _endpoint = "127.0.0.1:1545", _agentEndpoint = "127.0.0.1:1540", _clusterUser = "", _clusterPassword = "", _techLogPath = "", _reportFolder = "", _lastAutoReportDate = "";
    private string _gitHubOwner = "zigorminsk-debug", _gitHubRepository = "1C-Monitor", _encryptedGitHubToken = "";
    private bool _autoDailyReport = true, _autoDrainEnabled = false, _autoUpdateEnabled = true, _autoUpdateInstallAutomatically = true;
    private decimal _intervalSeconds = 5, _racPollSeconds = 15, _techLogPollSeconds = 60, _historyWriteSeconds = 60, _alertRepeatSeconds = 300, _racTimeoutSeconds = 60, _cpuAlert = 80, _memoryAlertMb = 4096, _callAlertMs = 5000, _diskAlert = 90;
    private decimal _autoDrainMemoryMb = 6144, _autoDrainMinutes = 10, _autoDrainMinProcesses = 2, _autoDrainCooldownMinutes = 30, _autoUpdateCheckHours = 6;
    public string RacPath { get => _racPath; set => Set(ref _racPath, value); }
    public string Endpoint { get => _endpoint; set => Set(ref _endpoint, value); }
    public string AgentEndpoint { get => _agentEndpoint; set => Set(ref _agentEndpoint, value); }
    public string ClusterUser { get => _clusterUser; set => Set(ref _clusterUser, value); }
    public string ClusterPassword { get => _clusterPassword; set => Set(ref _clusterPassword, value); }
    public string TechLogPath { get => _techLogPath; set => Set(ref _techLogPath, value); }
    public string ReportFolder { get => _reportFolder; set => Set(ref _reportFolder, value); }
    public string LastAutoReportDate { get => _lastAutoReportDate; set => Set(ref _lastAutoReportDate, value); }
    public bool AutoDailyReport { get => _autoDailyReport; set => Set(ref _autoDailyReport, value); }
    public List<string> BackgroundJobsDisabledInfobases { get; set; } = new();
    public bool AutoDrainEnabled { get => _autoDrainEnabled; set => Set(ref _autoDrainEnabled, value); }
    public decimal AutoDrainMemoryMb { get => _autoDrainMemoryMb; set => Set(ref _autoDrainMemoryMb, value); }
    public decimal AutoDrainMinutes { get => _autoDrainMinutes; set => Set(ref _autoDrainMinutes, value); }
    public decimal AutoDrainMinProcesses { get => _autoDrainMinProcesses; set => Set(ref _autoDrainMinProcesses, value); }
    public decimal AutoDrainCooldownMinutes { get => _autoDrainCooldownMinutes; set => Set(ref _autoDrainCooldownMinutes, value); }
    public decimal IntervalSeconds { get => _intervalSeconds; set => Set(ref _intervalSeconds, value); }
    public decimal RacPollSeconds { get => _racPollSeconds; set => Set(ref _racPollSeconds, value); }
    public decimal TechLogPollSeconds { get => _techLogPollSeconds; set => Set(ref _techLogPollSeconds, value); }
    public decimal HistoryWriteSeconds { get => _historyWriteSeconds; set => Set(ref _historyWriteSeconds, value); }
    public decimal AlertRepeatSeconds { get => _alertRepeatSeconds; set => Set(ref _alertRepeatSeconds, value); }
    public decimal RacTimeoutSeconds { get => _racTimeoutSeconds; set => Set(ref _racTimeoutSeconds, value); }
    public decimal CpuAlert { get => _cpuAlert; set => Set(ref _cpuAlert, value); }
    public decimal MemoryAlertMb { get => _memoryAlertMb; set => Set(ref _memoryAlertMb, value); }
    public decimal CallAlertMs { get => _callAlertMs; set => Set(ref _callAlertMs, value); }
    public decimal DiskAlert { get => _diskAlert; set => Set(ref _diskAlert, value); }
    public bool AutoUpdateEnabled { get => _autoUpdateEnabled; set => Set(ref _autoUpdateEnabled, value); }
    public bool AutoUpdateInstallAutomatically { get => _autoUpdateInstallAutomatically; set => Set(ref _autoUpdateInstallAutomatically, value); }
    public decimal AutoUpdateCheckHours { get => _autoUpdateCheckHours; set => Set(ref _autoUpdateCheckHours, value); }
    public string GitHubOwner { get => _gitHubOwner; set => Set(ref _gitHubOwner, value); }
    public string GitHubRepository { get => _gitHubRepository; set => Set(ref _gitHubRepository, value); }
    public string EncryptedGitHubToken { get => _encryptedGitHubToken; set => Set(ref _encryptedGitHubToken, value); }
    [JsonIgnore]
    public string GitHubToken
    {
        get => SecretProtector.Unprotect(EncryptedGitHubToken);
        set { EncryptedGitHubToken = SecretProtector.Protect(value); PropertyChanged?.Invoke(this, new(nameof(GitHubToken))); }
    }
    public RacConnectionOptions ToRacOptions() => new()
    {
        RacPath = RacPath,
        Endpoint = Endpoint,
        AgentEndpoint = AgentEndpoint,
        ClusterUser = ClusterUser,
        ClusterPassword = ClusterPassword,
        TimeoutSeconds = Math.Max(15, (int)RacTimeoutSeconds)
    };
    private static string FileName => Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "1CServerMonitor", "settings.json");
    public static MonitorSettings Load() { try { if (File.Exists(FileName)) return JsonSerializer.Deserialize<MonitorSettings>(File.ReadAllText(FileName)) ?? new(); } catch { } return new(); }
    public void Save() { Directory.CreateDirectory(Path.GetDirectoryName(FileName)!); var pwd = ClusterPassword; ClusterPassword = ""; try { File.WriteAllText(FileName, JsonSerializer.Serialize(this, new JsonSerializerOptions { WriteIndented = true })); } finally { ClusterPassword = pwd; } }
}

public sealed record AlertRow(DateTime At, string Severity, string Source, string Message) { public string TimeText => At.ToString("dd.MM.yyyy HH:mm:ss"); }
internal readonly record struct DiskSnapshot(bool Available, double UsagePercent, double ReadBytesPerSec, double WriteBytesPerSec, double QueueLength);
internal readonly record struct NetworkSnapshot(bool Available, double ReceiveBytesPerSec, double SendBytesPerSec, double UtilizationPercent, int InterfaceCount);

internal sealed class NetworkPerformanceMonitor : IDisposable
{
    private readonly Dictionary<string, (long Received, long Sent)> _previous = new();
    private DateTime _lastSample;

    public NetworkSnapshot Sample()
    {
        try
        {
            var now = DateTime.UtcNow; double received = 0, sent = 0, speed = 0; var count = 0;
            foreach (var nic in NetworkInterface.GetAllNetworkInterfaces().Where(n => n.OperationalStatus == OperationalStatus.Up && n.NetworkInterfaceType is not NetworkInterfaceType.Loopback and not NetworkInterfaceType.Tunnel))
            {
                try
                {
                    var stats = nic.GetIPv4Statistics(); count++; speed += Math.Max(0, nic.Speed);
                    if (_previous.TryGetValue(nic.Id, out var old))
                    {
                        if (stats.BytesReceived >= old.Received) received += stats.BytesReceived - old.Received;
                        if (stats.BytesSent >= old.Sent) sent += stats.BytesSent - old.Sent;
                    }
                    _previous[nic.Id] = (stats.BytesReceived, stats.BytesSent);
                }
                catch { }
            }
            var seconds = _lastSample == default ? 0 : (now - _lastSample).TotalSeconds; _lastSample = now;
            if (seconds <= 0) return default;
            received /= seconds; sent /= seconds;
            var utilization = speed > 0 ? Math.Clamp((received + sent) * 8d / speed * 100d, 0, 100) : 0;
            return new(true, received, sent, utilization, count);
        }
        catch { return default; }
    }
    public void Dispose() { _previous.Clear(); }
}

internal sealed class DiskPerformanceMonitor : IDisposable
{
    private const uint PdhFmtDouble = 0x00000200;
    private IntPtr _query, _usage, _read, _write, _queue;
    private bool _ready, _disposed;

    public DiskPerformanceMonitor()
    {
        if (!OperatingSystem.IsWindows()) return;
        try
        {
            if (PdhOpenQuery(null, IntPtr.Zero, out _query) != 0) return;
            if (PdhAddEnglishCounter(_query, @"\PhysicalDisk(_Total)\% Disk Time", IntPtr.Zero, out _usage) != 0 ||
                PdhAddEnglishCounter(_query, @"\PhysicalDisk(_Total)\Disk Read Bytes/sec", IntPtr.Zero, out _read) != 0 ||
                PdhAddEnglishCounter(_query, @"\PhysicalDisk(_Total)\Disk Write Bytes/sec", IntPtr.Zero, out _write) != 0 ||
                PdhAddEnglishCounter(_query, @"\PhysicalDisk(_Total)\Avg. Disk Queue Length", IntPtr.Zero, out _queue) != 0) return;
            // Первое измерение создаёт базовую точку для скоростных счётчиков.
            _ready = PdhCollectQueryData(_query) == 0;
        }
        catch { _ready = false; }
    }

    public DiskSnapshot Sample()
    {
        if (!_ready || _disposed) return default;
        try
        {
            if (PdhCollectQueryData(_query) != 0) return default;
            if (!TryValue(_usage, out var usage)) return default;
            TryValue(_read, out var read); TryValue(_write, out var write); TryValue(_queue, out var queue);
            return new(true, Math.Clamp(usage, 0, 100), Math.Max(0, read), Math.Max(0, write), Math.Max(0, queue));
        }
        catch { return default; }
    }

    private static bool TryValue(IntPtr counter, out double value)
    {
        value = 0; if (counter == IntPtr.Zero) return false;
        var result = PdhGetFormattedCounterValue(counter, PdhFmtDouble, out _, out var formatted);
        if (result != 0 || formatted.CStatus != 0 || double.IsNaN(formatted.DoubleValue) || double.IsInfinity(formatted.DoubleValue)) return false;
        value = formatted.DoubleValue; return true;
    }

    public void Dispose()
    {
        if (_disposed) return; _disposed = true;
        if (_query != IntPtr.Zero) PdhCloseQuery(_query);
    }

    [StructLayout(LayoutKind.Explicit, Size = 16)]
    private struct PdhFmtCounterValue
    {
        [FieldOffset(0)] public uint CStatus;
        [FieldOffset(8)] public double DoubleValue;
    }
    [DllImport("pdh.dll", CharSet = CharSet.Unicode)] private static extern uint PdhOpenQuery(string? dataSource, IntPtr userData, out IntPtr query);
    [DllImport("pdh.dll", CharSet = CharSet.Unicode, EntryPoint = "PdhAddEnglishCounterW")] private static extern uint PdhAddEnglishCounter(IntPtr query, string path, IntPtr userData, out IntPtr counter);
    [DllImport("pdh.dll")] private static extern uint PdhCollectQueryData(IntPtr query);
    [DllImport("pdh.dll")] private static extern uint PdhGetFormattedCounterValue(IntPtr counter, uint format, out uint type, out PdhFmtCounterValue value);
    [DllImport("pdh.dll")] private static extern uint PdhCloseQuery(IntPtr query);
}

public abstract class NotifyBase : INotifyPropertyChanged
{
    public event PropertyChangedEventHandler? PropertyChanged;
    protected bool Set<T>(ref T field, T value, [CallerMemberName] string? name = null) { if (EqualityComparer<T>.Default.Equals(field, value)) return false; field = value; PropertyChanged?.Invoke(this, new(name)); return true; }
}
public sealed class RelayCommand : ICommand
{
    private readonly Action _action; public RelayCommand(Action action) => _action = action; public event EventHandler? CanExecuteChanged { add { } remove { } } public bool CanExecute(object? p) => true; public void Execute(object? p) => _action();
}
