using System.Diagnostics;
using System.Runtime.Versioning;
using System.Text;

namespace OneCWatchdog;

public sealed class WatchdogLogger
{
    private readonly object _sync = new();
    private readonly string _logDir;
    private readonly string _filePrefix;
    private readonly int _retentionDays;
    private readonly bool _writeToConsole;
    private DateTime _lastPruneDate = DateTime.MinValue;

    public WatchdogLogger(string logDirectory, string filePrefix = "watchdog", int retentionDays = 30, bool writeToConsole = false)
    {
        _logDir = string.IsNullOrWhiteSpace(logDirectory) ? WatchdogConfig.DefaultLogDirectory : logDirectory;
        _filePrefix = filePrefix;
        _retentionDays = Math.Max(1, retentionDays);
        _writeToConsole = writeToConsole || Environment.UserInteractive;
        Directory.CreateDirectory(_logDir);
    }

    public string CurrentLogFile => Path.Combine(_logDir, $"{_filePrefix}-{DateTime.Now:yyyy-MM-dd}.log");

    public void Info(string message, bool eventLog = false) => Write("INFO", message, eventLog);
    public void Warn(string message, bool eventLog = true) => Write("WARN", message, eventLog);
    public void Error(string message, bool eventLog = true) => Write("ERROR", message, eventLog);

    private void Write(string level, string message, bool writeEventLog)
    {
        var line = $"[{DateTime.Now:yyyy-MM-dd HH:mm:ss.fff}] [{level}] {message}";
        lock (_sync)
        {
            try
            {
                Directory.CreateDirectory(_logDir);
                File.AppendAllText(CurrentLogFile, line + Environment.NewLine, new UTF8Encoding(false));
                PruneOldLogsIfNeeded();
            }
            catch { }

            if (_writeToConsole)
            {
                if (level == "ERROR") Console.Error.WriteLine(line);
                else Console.WriteLine(line);
            }
        }

        if (writeEventLog && OperatingSystem.IsWindows())
            TryWriteEventLog(message, level);
    }

    private void PruneOldLogsIfNeeded()
    {
        var today = DateTime.Today;
        if (_lastPruneDate == today) return;
        _lastPruneDate = today;

        try
        {
            var cutoff = today.AddDays(-_retentionDays);
            foreach (var file in Directory.EnumerateFiles(_logDir, "*.log"))
            {
                if (File.GetLastWriteTime(file) < cutoff)
                {
                    try { File.Delete(file); } catch { }
                }
            }
        }
        catch { }
    }

    [SupportedOSPlatform("windows")]
    private static void TryWriteEventLog(string message, string level)
    {
        try
        {
            if (EventLog.SourceExists(WatchdogConfig.ServiceName))
            {
                var type = level switch
                {
                    "ERROR" => EventLogEntryType.Error,
                    "WARN" => EventLogEntryType.Warning,
                    _ => EventLogEntryType.Information
                };
                EventLog.WriteEntry(WatchdogConfig.ServiceName, message, type);
            }
        }
        catch { }
    }
}
