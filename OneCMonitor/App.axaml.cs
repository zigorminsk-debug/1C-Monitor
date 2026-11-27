using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Markup.Xaml;
using Avalonia.Platform;

namespace OneCMonitor;

public partial class App : Application
{
    private TrayIcon? _trayIcon;

    public override void Initialize() => AvaloniaXamlLoader.Load(this);

    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime d)
        {
            var vm = new MainViewModel();
            var mainWindow = new MainWindow { DataContext = vm };
            d.MainWindow = mainWindow;
            SetupTrayIcon(d, mainWindow, vm);
            d.Exit += (_, _) =>
            {
                _trayIcon?.Dispose();
                vm.Dispose();
            };
        }
        base.OnFrameworkInitializationCompleted();
    }

    private void SetupTrayIcon(IClassicDesktopStyleApplicationLifetime desktop, MainWindow mainWindow, MainViewModel vm)
    {
        try
        {
            WindowIcon? icon = null;
            using (var stream = AssetLoader.Open(new Uri("avares://OneCMonitor/Assets/app.ico")))
            {
                icon = new WindowIcon(stream);
            }

            var menu = new NativeMenu();

            var openItem = new NativeMenuItem("Открыть 1C Server Monitor");
            openItem.Click += (_, _) => mainWindow.RestoreFromTray();

            var refreshItem = new NativeMenuItem("Обновить данные сейчас");
            refreshItem.Click += (_, _) => vm.RefreshCommand.Execute(null);

            var toggleItem = new NativeMenuItem("Пауза / Возобновить мониторинг");
            toggleItem.Click += (_, _) => vm.IsRunning = !vm.IsRunning;

            var updateItem = new NativeMenuItem("Проверить обновления на GitHub");
            updateItem.Click += (_, _) => vm.CheckUpdatesCommand.Execute(null);

            var exitItem = new NativeMenuItem("Выход");
            exitItem.Click += (_, _) =>
            {
                mainWindow.AllowRealClose = true;
                desktop.Shutdown();
            };

            menu.Items.Add(openItem);
            menu.Items.Add(new NativeMenuItemSeparator());
            menu.Items.Add(refreshItem);
            menu.Items.Add(toggleItem);
            menu.Items.Add(updateItem);
            menu.Items.Add(new NativeMenuItemSeparator());
            menu.Items.Add(exitItem);

            _trayIcon = new TrayIcon
            {
                Icon = icon,
                ToolTipText = "1C Server Monitor",
                Menu = menu,
                IsVisible = true
            };
            _trayIcon.Clicked += (_, _) => mainWindow.RestoreFromTray();

            TrayIcon.SetIcons(this, new TrayIcons { _trayIcon });
        }
        catch
        {
            // В окружениях без поддержки системного трея приложение продолжает работу штатно
        }
    }
}
