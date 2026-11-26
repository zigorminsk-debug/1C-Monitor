using System.Globalization;

namespace OneCMonitor.Core;

public sealed class ProcessRow
{
    public int Pid { get; set; }
    public string ProcessUuid { get; set; } = "";
    public double Cpu { get; set; }
    public double MemoryMb { get; set; }
    public double RacMemoryMb { get; set; }
    public string DrainStatus { get; set; } = "Работает";
    public int SessionCount { get; set; }
    public double AvgCallMs { get; set; }
    public double AvgDbMs { get; set; }
    public string Users { get; set; } = "";
    public int Risk { get; set; }
    public string Diagnosis { get; set; } = "";

    public string CpuText => $"{Cpu:0.0} %";
    public string MemoryText => MemoryMb >= 1024 ? $"{MemoryMb / 1024:0.00} ГБ" : $"{MemoryMb:0} МБ";
    public string AvgCallText => $"{AvgCallMs:0} мс";
    public string AvgDbText => $"{AvgDbMs:0} мс";
    public string RiskText => Risk >= 80 ? $"🔴 {Risk}" : Risk >= 50 ? $"🟠 {Risk}" : Risk >= 20 ? $"🟡 {Risk}" : $"🟢 {Risk}";
}

public sealed class SessionRow
{
    public string SessionUuid { get; set; } = "";
    public string UserName { get; set; } = "";
    public string InfoBase { get; set; } = "";
    public string AppId { get; set; } = "";
    public string Host { get; set; } = "";
    public int Pid { get; set; }
    public string SessionNumber { get; set; } = "";
    public string StartedAt { get; set; } = "";
    public string LastActiveAt { get; set; } = "";
    public bool IsSleeping { get; set; }

    public string SessionState => IsSleeping ? "💤 Спящий" : DurationCurrentMs > 0 ? "▶ Выполняет вызов" : CallsLast5Min > 0 ? "● Активен за 5 мин" : "○ Ожидает";

    public double MemoryCurrentMb { get; set; }
    public double MemoryLast5MinMb { get; set; }
    public double CpuTimeCurrent { get; set; }
    public double CpuTimeLast5Min { get; set; }
    public double DurationCurrentMs { get; set; }
    public double DurationDbCurrentMs { get; set; }
    public double DurationLast5MinMs { get; set; }
    public double DurationDbLast5MinMs { get; set; }
    public int CallsLast5Min { get; set; }
    public double BytesLast5Min { get; set; }
    public double DbBytesLast5Min { get; set; }
    public double ReadCurrent { get; set; }
    public double WriteCurrent { get; set; }
    public double ReadLast5Min { get; set; }
    public double WriteLast5Min { get; set; }

    public string SessionMemoryText => MemoryCurrentMb >= 1024 ? $"{MemoryCurrentMb / 1024:0.00} ГБ" : $"{MemoryCurrentMb:0.0} МБ";
    public string SessionCpuTimeText => CpuTimeCurrent > 0 ? $"{CpuTimeCurrent:0} мс" : "0 мс";
    public string CurrentCallText => DurationCurrentMs > 0 ? $"{DurationCurrentMs / 1000:0.0} с" : "—";
    public string CurrentDbText => DurationDbCurrentMs > 0 ? $"{DurationDbCurrentMs / 1000:0.0} с" : "—";
    public string IoCurrentText => ReadCurrent > 0 || WriteCurrent > 0 ? $"R {ReadCurrent / 1048576:0.0} / W {WriteCurrent / 1048576:0.0} МБ" : "—";
    public string Load5MinText => $"вызовы {CallsLast5Min} • CPU {CpuTimeLast5Min:0} мс • время {DurationLast5MinMs / 1000:0.0} с • БД {DurationDbLast5MinMs / 1000:0.0} с • ΔRAM {MemoryLast5MinMb:+0.0;-0.0;0.0} МБ";
    public string Io5MinText => $"R {ReadLast5Min / 1024:0.0} / W {WriteLast5Min / 1024:0.0} КБ • сеть {BytesLast5Min / 1024:0.0} КБ • СУБД {DbBytesLast5Min / 1024:0.0} КБ";

    public double ProcessCpu { get; set; }
    public double ProcessMemoryMb { get; set; }
    public double EstimatedCpu { get; set; }
    public string ProcessCpuText => Pid > 0 ? $"{ProcessCpu:0.0} %" : "—";
    public string ProcessMemoryText => Pid > 0 ? (ProcessMemoryMb >= 1024 ? $"{ProcessMemoryMb / 1024:0.00} ГБ" : $"{ProcessMemoryMb:0} МБ") : "—";
    public string EstimatedCpuText => Pid > 0 ? $"≈ {EstimatedCpu:0.0} %" : "—";

    public string TechContext { get; set; } = "";
    public double LastCallMs { get; set; }
    public string LastCallText => LastCallMs > 0 ? $"{LastCallMs:0} мс" : "—";
    public string Operation { get; set; } = "";
    public string DbRequest { get; set; } = "";
    public string Blocked { get; set; } = "нет";

    public DateTime StartedAtUtcOrLocal => RacHelpers.ParseRacDate(StartedAt);
    public DateTime LastActiveAtLocal => RacHelpers.ParseRacDate(LastActiveAt);
}

public sealed record RacProcess(int Pid, string ProcessUuid, double MemoryMb, double AvgCallMs, double AvgDbMs);
public sealed record InfoBaseRef(string Uuid, string Name, bool ScheduledJobsDenied);
public sealed record ConnectionLink(int Pid, string Operation, string DbRequest, string Host, string InfoBase, string Application, DateTime ConnectedAt, int ConnId);

public sealed class RacSnapshot
{
    public string RacPath { get; init; } = "";
    public string Endpoint { get; init; } = "";
    public string ClusterId { get; init; } = "";
    public string ClusterOutput { get; init; } = "";
    public string InfoBaseOutput { get; init; } = "";
    public string ProcessOutput { get; init; } = "";
    public string ConnectionOutput { get; init; } = "";
    public string SessionOutput { get; init; } = "";
    public List<string> ClusterIds { get; } = new();
    public Dictionary<int, RacProcess> Processes { get; } = new();
    public Dictionary<string, InfoBaseRef> InfoBases { get; } = new(StringComparer.OrdinalIgnoreCase);
    public List<SessionRow> Sessions { get; } = new();
}

public sealed class RacRecord : Dictionary<string, string>
{
    public string Get(string key) => TryGetValue(key, out var x) ? x : "";
}

public sealed class RacConnectionOptions
{
    public string RacPath { get; set; } = "";
    public string Endpoint { get; set; } = "127.0.0.1:1545";
    public string AgentEndpoint { get; set; } = "127.0.0.1:1540";
    public string ClusterUser { get; set; } = "";
    public string ClusterPassword { get; set; } = "";
    public int TimeoutSeconds { get; set; } = 60;
}

public static class RacHelpers
{
    public static string Normalize(string? s) => (s ?? "").Trim().Trim('"', '\'', '{', '}').ToLowerInvariant();
    public static string EmptyFallback(string? s, string fallback) => string.IsNullOrWhiteSpace(s) ? fallback : s;
    public static int ParseInt(string s) => int.TryParse(s, out var n) ? n : 0;
    public static double ParseNum(string s) => double.TryParse(s.Replace(',', '.'), NumberStyles.Any, CultureInfo.InvariantCulture, out var n) ? n : 0;
    public static DateTime ParseRacDate(string s) => DateTime.TryParse(s, CultureInfo.InvariantCulture, DateTimeStyles.AssumeLocal, out var value) ? value : DateTime.MinValue;
    public static bool IsTrue(string s) => s.Equals("yes", StringComparison.OrdinalIgnoreCase) || s.Equals("true", StringComparison.OrdinalIgnoreCase) || s.Equals("on", StringComparison.OrdinalIgnoreCase) || (int.TryParse(s, out var n) && n > 0);
}
