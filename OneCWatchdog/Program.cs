using System.Runtime.InteropServices;
using System.Text;
using Avalonia;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using OneCMonitor.Core;

namespace OneCWatchdog;

internal static class Program
{
    private const uint AttachParentProcess = 0xFFFFFFFF;

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AttachConsole(uint dwProcessId);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AllocConsole();

    [DllImport("kernel32.dll")]
    private static extern IntPtr GetConsoleWindow();

    [STAThread]
    public static int Main(string[] args)
    {
        Encoding.RegisterProvider(CodePagesEncodingProvider.Instance);

        var isServiceFlag = args.Any(a => a.Equals("--service", StringComparison.OrdinalIgnoreCase));
        if (isServiceFlag || !Environment.UserInteractive)
            return RunHostAsync(isServiceMode: true, args).GetAwaiter().GetResult();

        var isHelp = args.Any(a =>
            a.Equals("--help", StringComparison.OrdinalIgnoreCase) ||
            a.Equals("-h", StringComparison.OrdinalIgnoreCase) ||
            a.Equals("/?", StringComparison.OrdinalIgnoreCase));
        var isSetup = args.Any(a => a.Equals("--setup", StringComparison.OrdinalIgnoreCase) || a.Equals("--install", StringComparison.OrdinalIgnoreCase));
        var isUninstall = args.Any(a => a.Equals("--uninstall", StringComparison.OrdinalIgnoreCase));
        var isStatus = args.Any(a => a.Equals("--status", StringComparison.OrdinalIgnoreCase));
        var isCheckUpdate = args.Any(a => a.Equals("--check-update", StringComparison.OrdinalIgnoreCase));
        var isConsoleFlag = args.Any(a => a.Equals("--console", StringComparison.OrdinalIgnoreCase));
        var isCliMode = isHelp || isSetup || isUninstall || isStatus || isCheckUpdate || isConsoleFlag;

        if (isCliMode)
        {
            EnsureConsoleAttached();
            if (isHelp) { PrintHelp(); return 0; }
            if (isSetup) return WatchdogSetup.RunSetupAsync(args).GetAwaiter().GetResult();
            if (isUninstall) return WatchdogSetup.RunUninstallAsync(args).GetAwaiter().GetResult();
            if (isStatus) return WatchdogSetup.PrintStatusAsync().GetAwaiter().GetResult();
            if (isCheckUpdate) return CheckUpdateOnceAsync().GetAwaiter().GetResult();
            if (isConsoleFlag) return RunHostAsync(isServiceMode: false, args).GetAwaiter().GetResult();
        }

        // Запуск по двойному клику или с флагом --gui -> полноценный Avalonia GUI
        return BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }

    public static AppBuilder BuildAvaloniaApp()
        => AppBuilder.Configure<App>()
            .UsePlatformDetect()
            .LogToTrace();

    private static void EnsureConsoleAttached()
    {
        if (!OperatingSystem.IsWindows()) return;
        if (GetConsoleWindow() != IntPtr.Zero) return;

        var attached = AttachConsole(AttachParentProcess);
        if (!attached && !Console.IsOutputRedirected)
            attached = AllocConsole();

        if (attached)
        {
            try
            {
                Console.OutputEncoding = Encoding.UTF8;
                Console.InputEncoding = Encoding.UTF8;
                if (!Console.IsOutputRedirected)
                    Console.SetOut(new StreamWriter(Console.OpenStandardOutput(), new UTF8Encoding(false)) { AutoFlush = true });
                if (!Console.IsErrorRedirected)
                    Console.SetError(new StreamWriter(Console.OpenStandardError(), new UTF8Encoding(false)) { AutoFlush = true });
                if (!Console.IsInputRedirected)
                    Console.SetIn(new StreamReader(Console.OpenStandardInput(), Encoding.UTF8));
            }
            catch
            {
            }
        }
    }

    private static async Task<int> RunHostAsync(bool isServiceMode, string[] args)
    {
        var config = WatchdogConfig.Load();
        var logger = new WatchdogLogger(config.LogDirectory, filePrefix: "watchdog", retentionDays: config.LogRetentionDays, writeToConsole: !isServiceMode);

        var builder = Host.CreateApplicationBuilder(args);
        if (isServiceMode)
        {
            builder.Services.AddWindowsService(options =>
            {
                options.ServiceName = WatchdogConfig.ServiceName;
            });
        }

        builder.Services.AddSingleton(logger);
        builder.Services.AddHostedService(sp =>
            new WatchdogService(sp.GetRequiredService<IHostApplicationLifetime>(), sp.GetRequiredService<WatchdogLogger>(), isServiceMode));

        using var host = builder.Build();
        await host.RunAsync();
        return 0;
    }

    private static async Task<int> CheckUpdateOnceAsync()
    {
        var config = WatchdogConfig.Load();
        var updater = new GitHubAutoUpdater(
            "OneCWatchdog",
            "1C-Watchdog.exe",
            config.GitHubOwner,
            config.GitHubRepository,
            config.GitHubToken);
        var result = await updater.CheckForUpdateAsync(includePrereleases: config.IncludePrereleases);
        Console.WriteLine(result.Message);
        return 0;
    }

    private static void PrintHelp()
    {
        Console.WriteLine("""
1C Server Monitor Watchdog

Режимы запуска:
  (без параметров) / --gui  Открыть графический интерфейс (GUI) настройки и управления службой
  --setup / --install       Установка и настройка службы Windows через консоль (UAC, LocalSystem, ACL, конфиг)
  --uninstall               Остановка и удаление службы Windows
  --status                  Показать текущую конфигурацию и состояние службы
  --console                 Запуск службы в интерактивной консоли
  --service                 Запуск как службы Windows (используется SCM)
  --check-update            Проверить наличие новой версии на GitHub

Параметры --setup:
  --non-interactive         Не задавать интерактивных вопросов
  --in-place                Не копировать exe в Program Files, запускать из текущей папки
  --rac-path <путь>         Путь к rac.exe
  --endpoint <host:port>    Адрес RAS (по умолчанию 127.0.0.1:1545)
  --agent-endpoint <h:p>    Адрес агента 1С (по умолчанию 127.0.0.1:1540)
  --cluster-user <имя>      Администратор кластера
  --cluster-pwd <пароль>    Пароль администратора кластера (шифруется DPAPI)
  --interval <сек>          Интервал проверки сеансов (по умолчанию 30)
  --dup-min-age <сек>       Мин. возраст дубликата перед завершением (по умолчанию 30)
  --hung-call-min <мин>     Порог зависшего вызова, мин (по умолчанию 60)
  --hung-block-min <мин>    Порог блокировки, мин (по умолчанию 15)
  --hung-sleep-min <мин>    Порог спящего сеанса, мин (по умолчанию 180)
  --dry-run                 Только логирование без реального завершения сеансов
  --github-token <токен>    Токен GitHub для автообновления из приватного репозитория
  --no-auto-update          Отключить автообновление с GitHub
""");
    }
}
