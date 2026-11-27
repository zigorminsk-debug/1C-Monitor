using Avalonia.Controls;
using Avalonia.Interactivity;

namespace OneCWatchdog;

public partial class WatchdogMainWindow : Window
{
    private TaskCompletionSource<bool>? _confirmTcs;

    public WatchdogMainWindow()
    {
        InitializeComponent();
        DataContextChanged += (_, _) =>
        {
            if (DataContext is WatchdogGuiViewModel vm)
                vm.ConfirmActionAsync = ConfirmAsync;
        };
    }

    private async Task<bool> ConfirmAsync(string message)
    {
        _confirmTcs?.TrySetResult(false);
        _confirmTcs = new TaskCompletionSource<bool>();
        ConfirmMessageText.Text = message;
        ConfirmOverlay.IsVisible = true;
        return await _confirmTcs.Task;
    }

    private void OnConfirmOk(object? sender, RoutedEventArgs e)
    {
        ConfirmOverlay.IsVisible = false;
        _confirmTcs?.TrySetResult(true);
    }

    private void OnConfirmCancel(object? sender, RoutedEventArgs e)
    {
        ConfirmOverlay.IsVisible = false;
        _confirmTcs?.TrySetResult(false);
    }
}
