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
        BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
    }
    public static AppBuilder BuildAvaloniaApp() => AppBuilder.Configure<App>().UsePlatformDetect().LogToTrace();
}
