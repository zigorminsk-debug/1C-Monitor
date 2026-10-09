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
            // Кнопка «Выход» уже остановила мониторинг и дочерние процессы — здесь закрываем программу.
            vm.ExitRequested += exitCode => d.Shutdown(exitCode);
            // Перед выходом по кнопке спрашиваем подтверждение, чтобы не потерять накопленную историю.
            vm.ExitConfirmation = () => ExitDialog.Confirm(d.MainWindow);
            d.Exit += (_,__) => vm.Dispose();
        }
        base.OnFrameworkInitializationCompleted();
    }
}
