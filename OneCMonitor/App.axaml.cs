using Avalonia;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Markup.Xaml;
namespace OneCMonitor;
public partial class App : Application
{
    public override void Initialize() => AvaloniaXamlLoader.Load(this);
    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime d)
        {
            var vm = new MainViewModel();
            d.MainWindow = new MainWindow { DataContext = vm };
            d.Exit += (_,__) => vm.Dispose();
        }
        base.OnFrameworkInitializationCompleted();
    }
}
