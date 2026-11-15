using System.Windows.Input;

namespace OneCMonitor;

/// <summary>
/// Команда для асинхронных обработчиков. В отличие от <c>RelayCommand(async () => …)</c> (async void)
/// необработанное исключение не завершает приложение, а передаётся в <c>onError</c>.
/// Пока обработчик выполняется, команда недоступна: повторное нажатие кнопки не запускает
/// вторую копию операции.
/// </summary>
public sealed class AsyncRelayCommand : ICommand
{
    private readonly Func<Task> _action;
    private readonly Action<Exception> _onError;
    private bool _running;

    public AsyncRelayCommand(Func<Task> action, Action<Exception> onError)
    {
        _action = action;
        _onError = onError;
    }

    public event EventHandler? CanExecuteChanged;

    public bool CanExecute(object? parameter) => !_running;

    public async void Execute(object? parameter)
    {
        if (_running) return;
        SetRunning(true);
        try { await _action(); }
        catch (Exception ex)
        {
            try { _onError(ex); } catch { /* обработчик ошибок не должен ронять приложение */ }
        }
        finally { SetRunning(false); }
    }

    private void SetRunning(bool value)
    {
        _running = value;
        try { CanExecuteChanged?.Invoke(this, EventArgs.Empty); } catch { }
    }
}
