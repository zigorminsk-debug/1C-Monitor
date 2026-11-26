using System.Text;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using OneCMonitor.Core;

namespace OneCWatchdog;

internal static class Program
{
    public static async Task<int> Main(string[] args)
    {
        Encoding.RegisterProvider(CodePagesEncodingProvider.Instance);

        if (args.Any(a => a.Equals("--help", StringComparison.OrdinalIgnoreCase) || a.Equals("-h", StringComparison.OrdinalIgnoreCase)))
        {
            PrintHelp();
            return 0;
        }

        if (args.Any(a => a.Equals("--setup", StringComparison.OrdinalIgnoreCase) || a.Equals("--install", StringComparison.OrdinalIgnoreCase)))
            return await WatchdogSetup.RunSetupAsync(args);

        if (args.Any(a => a.Equals("--uninstall", StringComparison.OrdinalIgnoreCase)))
            return await WatchdogSetup.RunUninstallAsync(args);

        if (args.Any(a => a.Equals("--status", StringComparison.OrdinalIgnoreCase)))
            return await WatchdogSetup.PrintStatusAsync();

        if (args.Any(a => a.Equals("--check-update", StringComparison.OrdinalIgnoreCase)))
            return await CheckUpdateOnceAsync();

        var isConsoleFlag = args.Any(a => a.Equals("--console", StringComparison.OrdinalIgnoreCase));
        var isServiceFlag = args.Any(a => a.Equals("--service", StringComparison.OrdinalIgnoreCase));

        if (!isServiceFlag && !isConsoleFlag && Environment.UserInteractive && args.Length == 0)
        {
            Console.WriteLine("1C Server Monitor Watchdog v" + GitHubAutoUpdater.GetCurrentVersion());
            Console.WriteLine("Выберите действие:");
            Console.WriteLine("  1 — Установить и настроить службу Windows (с запросом прав администратора)");
            Console.WriteLine("  2 — Запустить Watchdog в консольном режиме (тест/отладка)");
            Console.WriteLine("  3 — Показать статус и текущую конфигурацию");
            Console.WriteLine("  4 — Удалить службу Windows");
            Console.Write("Выбор [1]: ");
            var choice = Console.ReadLine()?.Trim();
            return choice switch
            {
                "2" => await RunHostAsync(isServiceMode: false, args),
                "3" => await WatchdogSetup.PrintStatusAsync(),
                "4" => await WatchdogSetup.RunUninstallAsync(new[] { "--uninstall" }),
                _ => await WatchdogSetup.RunSetupAsync(new[] { "--setup" })
            };
        }

        return await RunHostAsync(isServiceMode: isServiceFlag || !Environment.UserInteractive, args);
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
        var result = await updater.CheckForUpdateAsync();
        Console.WriteLine(result.Message);
        return 0;
    }

    private static void PrintHelp()
    {
        Console.WriteLine("""
1C Server Monitor Watchdog

Режимы запуска:
  --setup / --install    Установка и настройка службы Windows (UAC, LocalSystem, ACL, конфиг)
  --uninstall            Остановка и удаление службы Windows
  --status               Показать текущую конфигурацию и состояние службы
  --console              Запуск в интерактивной консоли вместо службы
  --service              Запуск как службы Windows (используется SCM)
  --check-update         Проверить наличие новой версии на GitHub

Параметры --setup:
  --non-interactive      Не задавать интерактивных вопросов
  --in-place             Не копировать exe в Program Files, запускать из текущей папки
  --rac-path <путь>      Путь к rac.exe
  --endpoint <host:port> Адрес RAS (по умолчанию 127.0.0.1:1545)
  --agent-endpoint <h:p> Адрес агента 1С (по умолчанию 127.0.0.1:1540)
  --cluster-user <имя>   Администратор кластера
  --cluster-pwd <пароль> Пароль администратора кластера (шифруется DPAPI)
  --interval <сек>       Интервал проверки сеансов (по умолчанию 30)
  --dup-min-age <сек>    Мин. возраст дубликата перед завершением (по умолчанию 30)
  --hung-call-min <мин>  Порог зависшего вызова, мин (по умолчанию 60)
  --hung-block-min <мин> Порог блокировки, мин (по умолчанию 15)
  --hung-sleep-min <мин> Порог спящего сеанса, мин (по умолчанию 180)
  --dry-run              Только логирование без реального завершения сеансов
  --github-token <токен> Токен GitHub для автообновления из приватного репозитория
  --no-auto-update       Отключить автообновление с GitHub
""");
    }
}
