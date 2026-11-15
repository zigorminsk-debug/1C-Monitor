using Avalonia;

namespace OneCMonitor;
internal static class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
        // RAC in Russian Windows commonly writes redirected output in OEM CP866.
        // Register legacy Windows encodings before the first monitoring request.
        System.Text.Encoding.RegisterProvider(System.Text.CodePagesEncodingProvider.Instance);
        // Оконное приложение не имеет консоли: без этого журнала аварийное завершение не оставляет следов.
        AppDomain.CurrentDomain.UnhandledException += (_, e) => WriteCrashLog(e.ExceptionObject);
        BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }

    private static void WriteCrashLog(object? error)
    {
        try
        {
            var dir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "1CServerMonitor");
            Directory.CreateDirectory(dir);
            File.AppendAllText(Path.Combine(dir, "crash.log"), $"[{DateTime.Now:dd.MM.yyyy HH:mm:ss}] {error}{Environment.NewLine}{Environment.NewLine}");
        }
        catch { /* запись журнала не должна вызывать новое исключение */ }
    }
    public static AppBuilder BuildAvaloniaApp() => AppBuilder.Configure<App>().UsePlatformDetect().LogToTrace();
}
