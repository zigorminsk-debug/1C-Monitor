using System.Threading.Tasks;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Layout;
using Avalonia.Media;

namespace OneCMonitor;

/// <summary>
/// Диалог подтверждения перед выходом: защищает от случайного закрытия монитора,
/// который копит суточную историю и тревоги. Стили берутся из App.axaml (тёмная тема).
/// </summary>
internal static class ExitDialog
{
    public static Task<bool> Confirm(Window? owner)
    {
        var tcs = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var dialog = new Window
        {
            Title = "Выход — 1C Server Monitor",
            Width = 500,
            Height = 220,
            CanResize = false,
            ShowInTaskbar = false,
            WindowStartupLocation = owner != null ? WindowStartupLocation.CenterOwner : WindowStartupLocation.CenterScreen,
        };
        var text = new TextBlock
        {
            Text = "Остановить мониторинг и закрыть программу?\n" +
                   "Процессы, запущенные монитором (rac.exe, а также ras.exe, если он поднят кнопкой «Запустить RAS»), будут завершены. Процессы 1С не затрагиваются.\n" +
                   "Накопленная за этот сеанс суточная история в отчёт не попадёт.",
            TextWrapping = TextWrapping.Wrap,
            FontSize = 13,
            Margin = new Thickness(18, 16, 18, 8),
        };
        var yes = new Button { Content = "Выйти", Background = new SolidColorBrush(Color.Parse("#7F1D1D")), MinWidth = 90 };
        var no = new Button { Content = "Продолжить мониторинг", MinWidth = 90 };
        var buttons = new StackPanel { Orientation = Orientation.Horizontal, HorizontalAlignment = HorizontalAlignment.Right, Spacing = 10, Margin = new Thickness(0, 4, 18, 16) };
        buttons.Children.Add(yes);
        buttons.Children.Add(no);
        var panel = new DockPanel();
        DockPanel.SetDock(buttons, Dock.Bottom);
        panel.Children.Add(buttons);
        panel.Children.Add(text);
        dialog.Content = panel;

        yes.Click += (_, _) => { tcs.TrySetResult(true); dialog.Close(); };
        no.Click += (_, _) => { tcs.TrySetResult(false); dialog.Close(); };
        dialog.Closed += (_, _) => tcs.TrySetResult(false); // крестик диалога = отмена

        if (owner != null) _ = dialog.ShowDialog(owner);
        else dialog.Show();
        return tcs.Task;
    }
}
