namespace OneCMonitor.Core;

public sealed record SessionTerminationCandidate(SessionRow Session, string Reason);

public sealed class SessionPolicyOptions
{
    public bool TerminateDuplicates { get; set; } = true;
    public int DuplicateKeepNewestCount { get; set; } = 1;
    public int DuplicateMinAgeSeconds { get; set; } = 30;

    public bool TerminateHung { get; set; } = true;
    public int HungMaxCurrentCallMinutes { get; set; } = 60;
    public int HungMaxDbCallMinutes { get; set; } = 45;
    public int HungMaxBlockedMinutes { get; set; } = 15;
    public int HungMaxSleepingMinutes { get; set; } = 180;

    public bool IgnoreConfigurator { get; set; } = true;
    public bool IgnoreBackgroundJobs { get; set; } = true;
    public List<string> ExcludedUsers { get; set; } = new();
    public List<string> ExcludedInfoBases { get; set; } = new();
}

public static class SessionAnalyzer
{
    public static bool IsConfigurator(SessionRow s) =>
        s.AppId.Equals("Designer", StringComparison.OrdinalIgnoreCase) ||
        s.AppId.Contains("Configurator", StringComparison.OrdinalIgnoreCase) ||
        s.AppId.Contains("Конфигуратор", StringComparison.OrdinalIgnoreCase);

    public static bool IsBackgroundSession(SessionRow s) =>
        s.AppId.Contains("Background", StringComparison.OrdinalIgnoreCase) ||
        s.AppId.Contains("Фонов", StringComparison.OrdinalIgnoreCase);

    public static List<SessionRow> GetDuplicateSessions(IEnumerable<SessionRow> sessions, int keepNewestCount = 1) =>
        sessions.GroupBy(x => $"{x.InfoBase}|{x.UserName}|{x.Host}|{x.AppId}", StringComparer.OrdinalIgnoreCase)
            .SelectMany(g => g
                .OrderByDescending(x => RacHelpers.ParseRacDate(x.StartedAt))
                .ThenByDescending(x => RacHelpers.ParseRacDate(x.LastActiveAt))
                .Skip(Math.Max(1, keepNewestCount)))
            .ToList();

    public static List<SessionTerminationCandidate> EvaluateWatchdogTargets(
        IReadOnlyCollection<SessionRow> sessions,
        SessionPolicyOptions policy,
        IDictionary<string, DateTime> blockedSince,
        DateTime nowLocal)
    {
        var candidates = new List<SessionTerminationCandidate>();
        var selectedUuids = new HashSet<string>(StringComparer.OrdinalIgnoreCase);

        var activeUuids = sessions.Where(x => !string.IsNullOrWhiteSpace(x.SessionUuid))
            .Select(x => x.SessionUuid)
            .ToHashSet(StringComparer.OrdinalIgnoreCase);

        foreach (var stale in blockedSince.Keys.Where(k => !activeUuids.Contains(k)).ToList())
            blockedSince.Remove(stale);

        bool IsExcluded(SessionRow s)
        {
            if (string.IsNullOrWhiteSpace(s.SessionUuid)) return true;
            if (string.IsNullOrWhiteSpace(s.UserName) || s.UserName == "<не указан>") return true;
            if (policy.IgnoreConfigurator && IsConfigurator(s)) return true;
            if (policy.IgnoreBackgroundJobs && IsBackgroundSession(s)) return true;
            if (policy.ExcludedUsers.Any(u => string.Equals(u.Trim(), s.UserName.Trim(), StringComparison.OrdinalIgnoreCase))) return true;
            if (policy.ExcludedInfoBases.Any(b => string.Equals(b.Trim(), s.InfoBase.Trim(), StringComparison.OrdinalIgnoreCase))) return true;
            return false;
        }

        var eligible = sessions.Where(s => !IsExcluded(s)).ToList();

        if (policy.TerminateDuplicates)
        {
            var groups = eligible.GroupBy(x => $"{x.InfoBase}|{x.UserName}|{x.Host}|{x.AppId}", StringComparer.OrdinalIgnoreCase);
            foreach (var group in groups)
            {
                var ordered = group
                    .OrderByDescending(x => RacHelpers.ParseRacDate(x.StartedAt))
                    .ThenByDescending(x => RacHelpers.ParseRacDate(x.LastActiveAt))
                    .ToList();

                foreach (var old in ordered.Skip(Math.Max(1, policy.DuplicateKeepNewestCount)))
                {
                    var started = RacHelpers.ParseRacDate(old.StartedAt);
                    if (policy.DuplicateMinAgeSeconds > 0 && started != DateTime.MinValue && (nowLocal - started).TotalSeconds < policy.DuplicateMinAgeSeconds)
                        continue;

                    if (selectedUuids.Add(old.SessionUuid))
                    {
                        var newest = ordered[0];
                        candidates.Add(new SessionTerminationCandidate(
                            old,
                            $"Дубликат сеанса (более ранний от {old.StartedAt}; сохранён новый от {newest.StartedAt})"));
                    }
                }
            }
        }

        if (policy.TerminateHung)
        {
            foreach (var s in eligible.OrderBy(x => RacHelpers.ParseRacDate(x.StartedAt)))
            {
                if (s.Blocked != "нет")
                {
                    if (!blockedSince.ContainsKey(s.SessionUuid)) blockedSince[s.SessionUuid] = nowLocal;
                }
                else
                {
                    blockedSince.Remove(s.SessionUuid);
                }

                if (selectedUuids.Contains(s.SessionUuid)) continue;

                string? reason = null;
                if (policy.HungMaxCurrentCallMinutes > 0 && !s.IsSleeping && s.DurationCurrentMs >= policy.HungMaxCurrentCallMinutes * 60_000d)
                {
                    reason = $"Зависший вызов сервера: длительность {s.DurationCurrentMs / 60_000d:0.0} мин ≥ {policy.HungMaxCurrentCallMinutes} мин";
                }
                else if (policy.HungMaxDbCallMinutes > 0 && !s.IsSleeping && s.DurationDbCurrentMs >= policy.HungMaxDbCallMinutes * 60_000d)
                {
                    reason = $"Зависший запрос СУБД: длительность {s.DurationDbCurrentMs / 60_000d:0.0} мин ≥ {policy.HungMaxDbCallMinutes} мин";
                }
                else if (policy.HungMaxBlockedMinutes > 0 && blockedSince.TryGetValue(s.SessionUuid, out var bSince) && (nowLocal - bSince).TotalMinutes >= policy.HungMaxBlockedMinutes)
                {
                    reason = $"Зависшая блокировка ({s.Blocked}): длительность {(nowLocal - bSince).TotalMinutes:0.0} мин ≥ {policy.HungMaxBlockedMinutes} мин";
                }
                else if (policy.HungMaxSleepingMinutes > 0 && s.IsSleeping)
                {
                    var lastActive = RacHelpers.ParseRacDate(s.LastActiveAt);
                    if (lastActive != DateTime.MinValue && (nowLocal - lastActive).TotalMinutes >= policy.HungMaxSleepingMinutes)
                    {
                        reason = $"Зависший/брошенный спящий сеанс: неактивен {(nowLocal - lastActive).TotalMinutes:0} мин ≥ {policy.HungMaxSleepingMinutes} мин";
                    }
                }

                if (reason != null && selectedUuids.Add(s.SessionUuid))
                    candidates.Add(new SessionTerminationCandidate(s, reason));
            }
        }

        return candidates
            .OrderBy(x => RacHelpers.ParseRacDate(x.Session.StartedAt))
            .ToList();
    }
}
