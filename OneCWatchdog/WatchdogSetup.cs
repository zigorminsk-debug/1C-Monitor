using System.Diagnostics;
using System.Runtime.Versioning;
using System.Security.AccessControl;
using System.Security.Principal;
using OneCMonitor.Core;

namespace OneCWatchdog;

public static class WatchdogSetup
{
    public static string DefaultInstallDir =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "1CServerMonitor", "Watchdog");

    public static bool IsAdministrator()
    {
        if (!OperatingSystem.IsWindows()) return true;
        try
        {
            using var identity = WindowsIdentity.GetCurrent();
            var principal = new WindowsPrincipal(identity);
            return principal.IsInRole(WindowsBuiltInRole.Administrator);
        }
        catch { return false; }
    }

    public static bool EnsureElevated(string[] args)
    {
        if (!OperatingSystem.IsWindows() || IsAdministrator()) return true;

        var exePath = Environment.ProcessPath;
        if (string.IsNullOrWhiteSpace(exePath))
            throw new Exception("Не удалось определить путь к исполняемому файлу для повышения прав (UAC).");

        Console.WriteLine("Запрос прав администратора (UAC) для настройки службы и привилегий…");
        var psi = new ProcessStartInfo(exePath)
        {
            UseShellExecute = true,
            Verb = "runas"
        };
        foreach (var arg in args) psi.ArgumentList.Add(arg);

        var proc = Process.Start(psi);
        proc?.WaitForExit();
        Environment.Exit(proc?.ExitCode ?? 0);
        return false;
    }

    public static void RestartElevatedForGui(string extraArgs = "--gui")
    {
        if (!OperatingSystem.IsWindows() || IsAdministrator()) return;
        var exePath = Environment.ProcessPath ?? throw new Exception("Не удалось определить путь к 1C-Watchdog.exe");
        var psi = new ProcessStartInfo(exePath)
        {
            UseShellExecute = true,
            Verb = "runas",
            Arguments = extraArgs
        };
        Process.Start(psi);
        Environment.Exit(0);
    }

    public static async Task<(bool Exists, bool IsRunning, string StatusText, string RawOutput)> QueryServiceStateAsync()
    {
        if (!OperatingSystem.IsWindows())
            return (false, false, "Служба Windows доступна только на ОС Windows", "");

        var (code, output) = await RunProcessCaptureAsync("sc.exe", new[] { "query", WatchdogConfig.ServiceName });
        if (code != 0)
            return (false, false, "Не установлена (служба 1CServerWatchdog не зарегистрирована)", output);

        if (output.Contains("RUNNING", StringComparison.OrdinalIgnoreCase))
            return (true, true, "Работает (RUNNING · LocalSystem · delayed-auto)", output);
        if (output.Contains("STOPPED", StringComparison.OrdinalIgnoreCase))
            return (true, false, "Остановлена (STOPPED · LocalSystem)", output);
        if (output.Contains("START_PENDING", StringComparison.OrdinalIgnoreCase))
            return (true, false, "Запускается (START_PENDING)…", output);
        if (output.Contains("STOP_PENDING", StringComparison.OrdinalIgnoreCase))
            return (true, false, "Останавливается (STOP_PENDING)…", output);
        if (output.Contains("PAUSED", StringComparison.OrdinalIgnoreCase))
            return (true, false, "Приостановлена (PAUSED)", output);

        return (true, false, "Установлена (статус неизвестен)", output);
    }

    public static async Task InstallOrReconfigureFromGuiAsync(WatchdogConfig config, bool inPlace, Action<string> log)
    {
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Установка службы Windows поддерживается только в ОС Windows.");
        if (!IsAdministrator())
            throw new UnauthorizedAccessException("Требуются права администратора (UAC). Нажмите «Перезапустить от администратора».");

        Directory.CreateDirectory(WatchdogConfig.BaseDataDirectory);
        Directory.CreateDirectory(WatchdogConfig.DefaultLogDirectory);
        Directory.CreateDirectory(WatchdogConfig.DefaultUpdateStagingDirectory);

        var fileLogger = new WatchdogLogger(WatchdogConfig.DefaultLogDirectory, filePrefix: "setup", writeToConsole: false);
        void Emit(string msg) { fileLogger.Info(msg); log(msg); }

        Emit("=== Установка / обновление службы 1C Watchdog ===");
        Emit($"Учётная запись: {GetIdentityName()}");

        var currentExe = Environment.ProcessPath ?? throw new Exception("Не определён путь к 1C-Watchdog.exe");
        var targetExe = inPlace ? currentExe : Path.Combine(DefaultInstallDir, "1C-Watchdog.exe");

        Emit("1. Настройка защищённых каталогов и NTFS ACL (SYSTEM + Administrators)…");
        Directory.CreateDirectory(Path.GetDirectoryName(targetExe)!);
        ApplyPrivilegedDirectoryAcl(WatchdogConfig.BaseDataDirectory, fileLogger);
        ApplyPrivilegedDirectoryAcl(Path.GetDirectoryName(targetExe)!, fileLogger);

        Emit("2. Остановка текущей службы (если запущена)…");
        await TryScAsync("stop", WatchdogConfig.ServiceName);
        await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));

        if (!string.Equals(currentExe, targetExe, StringComparison.OrdinalIgnoreCase))
        {
            Emit($"3. Копирование 1C-Watchdog.exe в {targetExe}…");
            File.Copy(currentExe, targetExe, true);
        }
        else
        {
            Emit($"3. Используется текущий исполняемый файл: {targetExe}");
        }

        Emit("4. Сохранение конфигурации (секреты шифруются DPAPI LocalMachine)…");
        config.Save();

        Emit("5. Регистрация источника журнала событий Windows (EventLog)…");
        EnsureEventLogSource(fileLogger);

        Emit("6. Регистрация службы Windows (LocalSystem, delayed-auto, recovery)…");
        var binPath = $"\"{targetExe}\" --service";
        var exists = await ServiceExistsAsync(WatchdogConfig.ServiceName);
        if (exists)
            await RunScStrictAsync("config", WatchdogConfig.ServiceName, $"binPath= {binPath}", "start= delayed-auto", "depend= Tcpip", "obj= LocalSystem", $"DisplayName= {WatchdogConfig.ServiceDisplayName}");
        else
            await RunScStrictAsync("create", WatchdogConfig.ServiceName, $"binPath= {binPath}", "start= delayed-auto", "depend= Tcpip", "obj= LocalSystem", $"DisplayName= {WatchdogConfig.ServiceDisplayName}");

        await TryScAsync("description", WatchdogConfig.ServiceName, "Автоматическое отслеживание и завершение дублирующихся и зависших сеансов пользователей 1С:Предприятие 8.");
        await TryScAsync("failure", WatchdogConfig.ServiceName, "reset= 86400", "actions= restart/5000/restart/15000/restart/60000");
        await TryScAsync("failureflag", WatchdogConfig.ServiceName, "1");

        Emit("7. Запуск службы 1CServerWatchdog…");
        await RunScStrictAsync("start", WatchdogConfig.ServiceName);
        await WaitForServiceRunningAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));

        fileLogger.Info($"Служба '{WatchdogConfig.ServiceName}' настроена через GUI и запущена от LocalSystem.", eventLog: true);
        Emit("Готово! Служба 1CServerWatchdog успешно установлена и работает.");
    }

    public static async Task ControlServiceAsync(string action, Action<string> log)
    {
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Доступно только на Windows.");
        if (!IsAdministrator())
            throw new UnauthorizedAccessException("Требуются права администратора (UAC).");

        switch (action.ToLowerInvariant())
        {
            case "start":
                log("Запуск службы 1CServerWatchdog…");
                await RunScStrictAsync("start", WatchdogConfig.ServiceName);
                await WaitForServiceRunningAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));
                log("Служба запущена.");
                break;
            case "stop":
                log("Остановка службы 1CServerWatchdog…");
                await RunScStrictAsync("stop", WatchdogConfig.ServiceName);
                await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));
                log("Служба остановлена.");
                break;
            case "restart":
                log("Перезапуск службы 1CServerWatchdog…");
                await TryScAsync("stop", WatchdogConfig.ServiceName);
                await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));
                await RunScStrictAsync("start", WatchdogConfig.ServiceName);
                await WaitForServiceRunningAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));
                log("Служба перезапущена.");
                break;
            default:
                throw new ArgumentOutOfRangeException(nameof(action));
        }
    }

    public static async Task UninstallFromGuiAsync(Action<string> log)
    {
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Доступно только на Windows.");
        if (!IsAdministrator())
            throw new UnauthorizedAccessException("Требуются права администратора (UAC).");

        var fileLogger = new WatchdogLogger(WatchdogConfig.DefaultLogDirectory, filePrefix: "setup", writeToConsole: false);
        log("Остановка и удаление службы 1CServerWatchdog…");
        await TryScAsync("stop", WatchdogConfig.ServiceName);
        await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(15));
        await RunScStrictAsync("delete", WatchdogConfig.ServiceName);
        fileLogger.Info("Служба 1CServerWatchdog удалена через GUI.", eventLog: true);
        log("Служба 1CServerWatchdog удалена.");
    }

    public static async Task<int> RunSetupAsync(string[] args)
    {
        if (!OperatingSystem.IsWindows())
        {
            Console.Error.WriteLine("Установка службы Windows поддерживается только в ОС Windows.");
            return 1;
        }

        if (!EnsureElevated(args)) return 0;

        Directory.CreateDirectory(WatchdogConfig.BaseDataDirectory);
        Directory.CreateDirectory(WatchdogConfig.DefaultLogDirectory);
        Directory.CreateDirectory(WatchdogConfig.DefaultUpdateStagingDirectory);

        var logger = new WatchdogLogger(WatchdogConfig.DefaultLogDirectory, filePrefix: "setup", writeToConsole: true);
        logger.Info("=== Настройка 1C Server Monitor Watchdog ===");
        logger.Info($"Учётная запись установки: {GetIdentityName()}");

        var nonInteractive = HasFlag(args, "--non-interactive") || HasFlag(args, "-y");
        var inPlace = HasFlag(args, "--in-place");

        try
        {
            var currentExe = Environment.ProcessPath ?? throw new Exception("Не определён путь к 1C-Watchdog.exe");
            var targetExe = inPlace ? currentExe : Path.Combine(DefaultInstallDir, "1C-Watchdog.exe");

            logger.Info("1. Настройка каталогов и прав доступа (NTFS ACL: SYSTEM + Administrators)…");
            Directory.CreateDirectory(Path.GetDirectoryName(targetExe)!);
            ApplyPrivilegedDirectoryAcl(WatchdogConfig.BaseDataDirectory, logger);
            ApplyPrivilegedDirectoryAcl(Path.GetDirectoryName(targetExe)!, logger);

            logger.Info("2. Остановка существующей службы (если установлена)…");
            await TryScAsync("stop", WatchdogConfig.ServiceName);
            await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));

            if (!string.Equals(currentExe, targetExe, StringComparison.OrdinalIgnoreCase))
            {
                logger.Info($"3. Копирование службы в системный каталог: {targetExe}");
                File.Copy(currentExe, targetExe, true);
            }
            else
            {
                logger.Info($"3. Использование текущего расположения: {targetExe}");
            }

            logger.Info("4. Конфигурация параметров подключения и политик…");
            var config = WatchdogConfig.Load();
            ApplyCliOverrides(config, args);

            var rac = new RacClient();
            var detectedRac = rac.FindRac(config.RacPath);
            if (!string.IsNullOrWhiteSpace(detectedRac))
            {
                config.RacPath = detectedRac;
                logger.Info($"Автоопределён rac.exe: {detectedRac}");
            }

            if (!nonInteractive)
            {
                PromptInteractiveConfig(config);
            }

            config.Save();
            logger.Info($"Конфигурация сохранена (пароль/токен зашифрованы DPAPI LocalMachine): {WatchdogConfig.DefaultConfigPath}");

            logger.Info("5. Регистрация источника журнала событий Windows (EventLog)…");
            EnsureEventLogSource(logger);

            logger.Info("6. Регистрация службы Windows с правами LocalSystem и отложенным автозапуском…");
            var binPath = $"\"{targetExe}\" --service";
            var exists = await ServiceExistsAsync(WatchdogConfig.ServiceName);
            if (exists)
            {
                await RunScStrictAsync("config", WatchdogConfig.ServiceName, $"binPath= {binPath}", "start= delayed-auto", "depend= Tcpip", "obj= LocalSystem", $"DisplayName= {WatchdogConfig.ServiceDisplayName}");
            }
            else
            {
                await RunScStrictAsync("create", WatchdogConfig.ServiceName, $"binPath= {binPath}", "start= delayed-auto", "depend= Tcpip", "obj= LocalSystem", $"DisplayName= {WatchdogConfig.ServiceDisplayName}");
            }

            await TryScAsync("description", WatchdogConfig.ServiceName, "Автоматическое отслеживание и завершение дублирующихся и зависших сеансов пользователей 1С:Предприятие 8.");
            await TryScAsync("failure", WatchdogConfig.ServiceName, "reset= 86400", "actions= restart/5000/restart/15000/restart/60000");
            await TryScAsync("failureflag", WatchdogConfig.ServiceName, "1");

            logger.Info("7. Проверка подключения к кластеру 1С (RAC/RAS)…");
            if (!string.IsNullOrWhiteSpace(config.RacPath))
            {
                try
                {
                    var snapshot = await rac.ReadRacAsync(config.RacPath, config.Endpoint, config.ToRacOptions(), m => logger.Info(m));
                    logger.Info($"Подключение к кластеру {snapshot.ClusterId} успешно: активных сеансов={snapshot.Sessions.Count}");
                }
                catch (Exception ex)
                {
                    logger.Warn($"Проверка RAC завершилась предупреждением: {ex.Message}. Служба при запуске попробует запустить ras.exe автоматически.");
                }
            }
            else
            {
                logger.Warn("rac.exe не найден автоматически. Укажите RacPath в watchdog.json после настройки.");
            }

            logger.Info("8. Запуск службы Windows 1CServerWatchdog…");
            await RunScStrictAsync("start", WatchdogConfig.ServiceName);
            await WaitForServiceRunningAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(20));

            logger.Info($"Служба '{WatchdogConfig.ServiceName}' успешно настроена, получила системные привилегии LocalSystem и запущена!", eventLog: true);
            logger.Info($"Логи работы службы: {WatchdogConfig.DefaultLogDirectory}");
            return 0;
        }
        catch (Exception ex)
        {
            logger.Error($"Ошибка настройки Watchdog: {ex}");
            return 1;
        }
    }

    public static async Task<int> RunUninstallAsync(string[] args)
    {
        if (!OperatingSystem.IsWindows())
        {
            Console.Error.WriteLine("Удаление службы поддерживается только в ОС Windows.");
            return 1;
        }

        if (!EnsureElevated(args)) return 0;

        var logger = new WatchdogLogger(WatchdogConfig.DefaultLogDirectory, filePrefix: "setup", writeToConsole: true);
        logger.Info("Остановка и удаление службы 1CServerWatchdog…");
        await TryScAsync("stop", WatchdogConfig.ServiceName);
        await WaitForServiceStoppedAsync(WatchdogConfig.ServiceName, TimeSpan.FromSeconds(15));
        await RunScStrictAsync("delete", WatchdogConfig.ServiceName);
        logger.Info("Служба 1CServerWatchdog удалена.", eventLog: true);
        return 0;
    }

    public static async Task<int> PrintStatusAsync()
    {
        var config = WatchdogConfig.Load();
        Console.WriteLine($"Версия 1C Watchdog: {GitHubAutoUpdater.GetCurrentVersion()}");
        Console.WriteLine($"Конфигурация: {WatchdogConfig.DefaultConfigPath} ({(File.Exists(WatchdogConfig.DefaultConfigPath) ? "найдена" : "по умолчанию")})");
        Console.WriteLine($"Каталог логов: {config.LogDirectory}");
        Console.WriteLine($"RAS: {config.Endpoint} | Агент: {config.AgentEndpoint} | Интервал: {config.CheckIntervalSeconds} с | DryRun: {config.DryRun}");
        Console.WriteLine($"Политика: дубли={config.Policy.TerminateDuplicates}, зависшие={config.Policy.TerminateHung} (вызов>={config.Policy.HungMaxCurrentCallMinutes}м, СУБД>={config.Policy.HungMaxDbCallMinutes}м, блок>={config.Policy.HungMaxBlockedMinutes}м, сон>={config.Policy.HungMaxSleepingMinutes}м)");

        if (OperatingSystem.IsWindows())
        {
            var (_, output) = await RunProcessCaptureAsync("sc.exe", new[] { "query", WatchdogConfig.ServiceName });
            Console.WriteLine("--- Состояние службы Windows ---");
            Console.WriteLine(output.Trim());
        }

        return 0;
    }

    private static void PromptInteractiveConfig(WatchdogConfig config)
    {
        Console.WriteLine();
        Console.WriteLine("Настройка параметров (Enter — оставить текущее значение):");
        config.RacPath = Prompt("Путь к rac.exe", config.RacPath);
        config.Endpoint = Prompt("Адрес RAS", config.Endpoint);
        config.AgentEndpoint = Prompt("Адрес агента 1С (ragent)", config.AgentEndpoint);
        config.ClusterUser = Prompt("Администратор кластера (если задан)", config.ClusterUser);
        var pwd = PromptSecret("Пароль администратора кластера (оставьте пустым, чтобы сохранить текущий/пустой)");
        if (pwd != null) config.ClusterPassword = pwd;

        config.CheckIntervalSeconds = PromptInt("Интервал проверки, сек.", config.CheckIntervalSeconds, 5, 3600);
        config.Policy.DuplicateMinAgeSeconds = PromptInt("Мин. возраст дубликата перед завершением, сек.", config.Policy.DuplicateMinAgeSeconds, 0, 3600);
        config.Policy.HungMaxCurrentCallMinutes = PromptInt("Порог зависшего вызова сервера, мин. (0=выкл)", config.Policy.HungMaxCurrentCallMinutes, 0, 1440);
        config.Policy.HungMaxBlockedMinutes = PromptInt("Порог зависшей блокировки, мин. (0=выкл)", config.Policy.HungMaxBlockedMinutes, 0, 1440);
        config.Policy.HungMaxSleepingMinutes = PromptInt("Порог зависшего спящего сеанса, мин. (0=выкл)", config.Policy.HungMaxSleepingMinutes, 0, 10080);
        config.AutoUpdateEnabled = PromptBool("Включить автообновление с GitHub", config.AutoUpdateEnabled);
        if (config.AutoUpdateEnabled)
        {
            var token = PromptSecret("GitHub Token (только для приватного репозитория; Enter — пропустить)");
            if (token != null) config.GitHubToken = token;
        }
        Console.WriteLine();
    }

    private static string Prompt(string label, string current)
    {
        Console.Write($"{label} [{current}]: ");
        var line = Console.ReadLine();
        return string.IsNullOrWhiteSpace(line) ? current : line.Trim();
    }

    private static string? PromptSecret(string label)
    {
        Console.Write($"{label}: ");
        var line = Console.ReadLine();
        return string.IsNullOrEmpty(line) ? null : line;
    }

    private static int PromptInt(string label, int current, int min, int max)
    {
        Console.Write($"{label} [{current}]: ");
        var line = Console.ReadLine();
        return int.TryParse(line?.Trim(), out var parsed) ? Math.Clamp(parsed, min, max) : current;
    }

    private static bool PromptBool(string label, bool current)
    {
        Console.Write($"{label} [{(current ? "y" : "n")}]: ");
        var line = Console.ReadLine()?.Trim().ToLowerInvariant();
        if (string.IsNullOrEmpty(line)) return current;
        return line is "y" or "yes" or "д" or "да" or "1" or "true";
    }

    private static void ApplyCliOverrides(WatchdogConfig config, string[] args)
    {
        if (GetArgValue(args, "--rac-path") is { } rac) config.RacPath = rac;
        if (GetArgValue(args, "--endpoint") is { } ep) config.Endpoint = ep;
        if (GetArgValue(args, "--agent-endpoint") is { } agent) config.AgentEndpoint = agent;
        if (GetArgValue(args, "--cluster-user") is { } cu) config.ClusterUser = cu;
        if (GetArgValue(args, "--cluster-pwd") is { } cp) config.ClusterPassword = cp;
        if (GetArgValue(args, "--github-token") is { } gt) config.GitHubToken = gt;
        if (int.TryParse(GetArgValue(args, "--interval"), out var interval)) config.CheckIntervalSeconds = Math.Max(5, interval);
        if (int.TryParse(GetArgValue(args, "--dup-min-age"), out var dupAge)) config.Policy.DuplicateMinAgeSeconds = Math.Max(0, dupAge);
        if (int.TryParse(GetArgValue(args, "--hung-call-min"), out var callMin)) config.Policy.HungMaxCurrentCallMinutes = Math.Max(0, callMin);
        if (int.TryParse(GetArgValue(args, "--hung-block-min"), out var blkMin)) config.Policy.HungMaxBlockedMinutes = Math.Max(0, blkMin);
        if (int.TryParse(GetArgValue(args, "--hung-sleep-min"), out var slpMin)) config.Policy.HungMaxSleepingMinutes = Math.Max(0, slpMin);
        if (HasFlag(args, "--dry-run")) config.DryRun = true;
        if (HasFlag(args, "--no-auto-update")) config.AutoUpdateEnabled = false;
    }

    private static bool HasFlag(string[] args, string flag) =>
        args.Any(a => a.Equals(flag, StringComparison.OrdinalIgnoreCase));

    private static string? GetArgValue(string[] args, string key)
    {
        for (var i = 0; i < args.Length; i++)
        {
            if (args[i].Equals(key, StringComparison.OrdinalIgnoreCase) && i + 1 < args.Length)
                return args[i + 1];
            if (args[i].StartsWith(key + "=", StringComparison.OrdinalIgnoreCase))
                return args[i][(key.Length + 1)..];
        }
        return null;
    }

    [SupportedOSPlatform("windows")]
    private static void ApplyPrivilegedDirectoryAcl(string path, WatchdogLogger logger)
    {
        try
        {
            var dirInfo = new DirectoryInfo(path);
            var security = dirInfo.GetAccessControl();
            security.SetAccessRuleProtection(isProtected: true, preserveInheritance: false);
            var systemSid = new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null);
            var adminsSid = new SecurityIdentifier(WellKnownSidType.BuiltinAdministratorsSid, null);

            security.ResetAccessRule(new FileSystemAccessRule(systemSid, FileSystemRights.FullControl,
                InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit, PropagationFlags.None, AccessControlType.Allow));
            security.AddAccessRule(new FileSystemAccessRule(adminsSid, FileSystemRights.FullControl,
                InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit, PropagationFlags.None, AccessControlType.Allow));

            dirInfo.SetAccessControl(security);
        }
        catch (Exception ex)
        {
            logger.Warn($"Не удалось обновить ACL для {path}: {ex.Message}", eventLog: false);
        }
    }

    [SupportedOSPlatform("windows")]
    private static void EnsureEventLogSource(WatchdogLogger logger)
    {
        try
        {
            if (!EventLog.SourceExists(WatchdogConfig.ServiceName))
                EventLog.CreateEventSource(WatchdogConfig.ServiceName, "Application");
        }
        catch (Exception ex)
        {
            logger.Warn($"Не удалось зарегистрировать источник EventLog: {ex.Message}", eventLog: false);
        }
    }

    [SupportedOSPlatform("windows")]
    private static string GetIdentityName()
    {
        try { return WindowsIdentity.GetCurrent().Name; }
        catch { return Environment.UserName; }
    }

    private static async Task<bool> ServiceExistsAsync(string serviceName)
    {
        var (code, _) = await RunProcessCaptureAsync("sc.exe", new[] { "query", serviceName });
        return code == 0;
    }

    private static async Task WaitForServiceStoppedAsync(string serviceName, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            var (code, output) = await RunProcessCaptureAsync("sc.exe", new[] { "query", serviceName });
            if (code != 0 || output.Contains("STOPPED", StringComparison.OrdinalIgnoreCase)) return;
            await Task.Delay(500);
        }
    }

    private static async Task WaitForServiceRunningAsync(string serviceName, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            var (_, output) = await RunProcessCaptureAsync("sc.exe", new[] { "query", serviceName });
            if (output.Contains("RUNNING", StringComparison.OrdinalIgnoreCase)) return;
            await Task.Delay(500);
        }
    }

    private static async Task TryScAsync(params string[] args) => await RunProcessCaptureAsync("sc.exe", args);

    private static async Task RunScStrictAsync(params string[] args)
    {
        var (code, output) = await RunProcessCaptureAsync("sc.exe", args);
        if (code != 0) throw new Exception($"sc.exe {string.Join(' ', args)} завершилась с кодом {code}: {output.Trim()}");
    }

    private static async Task<(int ExitCode, string Output)> RunProcessCaptureAsync(string fileName, IEnumerable<string> args)
    {
        var psi = new ProcessStartInfo(fileName)
        {
            UseShellExecute = false,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            CreateNoWindow = true
        };
        foreach (var a in args) psi.ArgumentList.Add(a);
        using var proc = Process.Start(psi) ?? throw new Exception($"Не удалось запустить {fileName}");
        var stdout = await proc.StandardOutput.ReadToEndAsync();
        var stderr = await proc.StandardError.ReadToEndAsync();
        await proc.WaitForExitAsync();
        return (proc.ExitCode, stdout + Environment.NewLine + stderr);
    }
}
