using Avalonia;
using Avalonia.Controls;
using Avalonia.Layout;
using Avalonia.Media;

namespace OneCMonitor;

public partial class MainWindow : Window
{
    public bool AllowRealClose { get; set; }

    public MainWindow()
    {
        InitializeComponent();
        DataContextChanged += OnDataContextChanged;
        Closing += OnWindowClosing;
    }

    private void OnDataContextChanged(object? sender, EventArgs e)
    {
        if (DataContext is not MainViewModel vm) return;
        vm.NavigateToTab = idx =>
        {
            if (idx >= 0 && idx < MainTabs.ItemCount)
                MainTabs.SelectedIndex = idx;
        };
        vm.CopyToClipboardAsync = async text =>
        {
            var clipboard = TopLevel.GetTopLevel(this)?.Clipboard;
            if (clipboard != null)
                await clipboard.SetTextAsync(text);
        };
        vm.ConfirmActionAsync = ShowConfirmationDialogAsync;
    }

    private void OnWindowClosing(object? sender, WindowClosingEventArgs e)
    {
        if (AllowRealClose) return;
        if (DataContext is MainViewModel vm && vm.Settings.MinimizeToTrayOnClose)
        {
            e.Cancel = true;
            Hide();
        }
    }

    public void RestoreFromTray()
    {
        Show();
        if (WindowState == WindowState.Minimized)
            WindowState = WindowState.Normal;
        Activate();
    }

    private async Task<bool> ShowConfirmationDialogAsync(string message)
    {
        var result = false;
        var dialog = new Window
        {
            Title = "Подтверждение действия",
            Width = 480,
            Height = 190,
            CanResize = false,
            WindowStartupLocation = WindowStartupLocation.CenterOwner,
            Background = new SolidColorBrush(Color.Parse("#0D1422"))
        };

        var yesButton = new Button
        {
            Content = "Да, выполнить",
            Background = new SolidColorBrush(Color.Parse("#991B1B")),
            Foreground = Brushes.White
        };
        var cancelButton = new Button { Content = "Отмена" };

        yesButton.Click += (_, _) => { result = true; dialog.Close(); };
        cancelButton.Click += (_, _) => { result = false; dialog.Close(); };

        dialog.Content = new StackPanel
        {
            Margin = new Thickness(20),
            Spacing = 18,
            Children =
            {
                new TextBlock
                {
                    Text = message,
                    TextWrapping = TextWrapping.Wrap,
                    FontSize = 14,
                    Foreground = new SolidColorBrush(Color.Parse("#E2E8F0"))
                },
                new StackPanel
                {
                    Orientation = Orientation.Horizontal,
                    HorizontalAlignment = HorizontalAlignment.Right,
                    Spacing = 10,
                    Children = { cancelButton, yesButton }
                }
            }
        };

        await dialog.ShowDialog(this);
        return result;
    }
}
