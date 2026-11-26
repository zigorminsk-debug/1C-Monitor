namespace OneCMonitor.Core;

public sealed record SessionTerminationCandidate(SessionRow Session, string Reason);

public static class DuplicateGroupingModes
{
    public const string Full = "Full";               // InfoBase + UserName + Host + AppId
    public const string UserAndBase = "UserAndBase"; // InfoBase + UserName (строго 1 сеанс на пользователя в базе)
    public const string UserBaseApp = "UserBaseApp"; // InfoBase + UserName + AppId
}

public sealed class SessionPolicyOptions
{
    public bool TerminateDuplicates { get; set; } = true;
    public string DuplicateGroupingMode { get; set; } = DuplicateGroupingModes.Full;
    public int DuplicateKeepNewestCount { get; set; } = 1;
    public int DuplicateMinAgeSeconds { get; set; } = 30;

    public bool TerminateHung { get; set; } = true;
    public int HungMaxCurrentCallMinutes { get; set; } = 60;
    public int HungMaxDbCallMinutes { get; set; } = 45;
    public int HungMaxBlockedMinutes { get; set; } = 15;
    public int HungMaxSleepingMinutes { get; set; } = 180;
    public bool TerminateHungOnlyWhenBlockingOthers { get; set; } = false;

    /// <summary>
    /// Количество последовательных проверок, в которых сеанс должен оставаться нарушителем перед завершением (grace period).
    /// </summary>
    public int ConsecutiveChecksRequired { get; set; } = 1;

    public bool IgnoreConfigurator { get; set; } = true;
    public bool IgnoreBackgroundJobs { get; set; } = true;
    public List<string> ExcludedUsers { get; set; } = new();
    public List<string> ExcludedInfoBases { get; set; } = new();
    public List<string> TargetInfoBases { get; set; } = new();
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

    public static string BuildDuplicateKey(SessionRow s, string? groupingMode = null)
    {
        var clusterPart = string.IsNullOrWhiteSpace(s.ClusterId) ? "" : s.ClusterId;
        return groupingMode switch
        {
            DuplicateGroupingModes.UserAndBase => $"{clusterPart}|{s.InfoBase}|{s.UserName}",
            DuplicateGroupingModes.UserBaseApp => $"{clusterPart}|{s.InfoBase}|{s.UserName}|{s.AppId}",
            _ => $"{clusterPart}|{s.InfoBase}|{s.UserName}|{s.Host}|{s.AppId}"
        };
    }

    public static List<SessionRow> GetDuplicateSessions(
        IEnumerable<SessionRow> sessions,
        int keepNewestCount = 1,
        string? groupingMode = DuplicateGroupingModes.Full) =>
        sessions.GroupBy(x => BuildDuplicateKey(x, groupingMode), StringComparer.OrdinalIgnoreCase)
            .SelectMany(g => g
                .OrderByDescending(x => RacHelpers.ParseRacDate(x.StartedAt))
                .ThenByDescending(x => RacHelpers.ParseRacDate(x.LastActiveAt))
                .Skip(Math.Max(1, keepNewestCount)))
            .ToList();

    public static List<SessionTerminationCandidate> EvaluateWatchdogTargets(
        IReadOnlyCollection<SessionRow> sessions,
        SessionPolicyOptions policy,
        IDictionary<string, DateTime> blockedSince,
        DateTime nowLocal) =>
        EvaluateWatchdogTargets(sessions, policy, blockedSince, nowLocal, consecutiveHits: null);

    public static List<SessionTerminationCandidate> EvaluateWatchdogTargets(
        IReadOnlyCollection<SessionRow> sessions,
        SessionPolicyOptions policy,
        IDictionary<string, DateTime> blockedSince,
        DateTime nowLocal,
        IDictionary<string, int>? consecutiveHits)
    {
        var rawCandidates = new List<SessionTerminationCandidate>();
        var selectedUuids = new HashSet<string>(StringComparer.OrdinalIgnoreCase);

        var activeUuids = sessions.Where(x => !string.IsNullOrWhiteSpace(x.SessionUuid))
            .Select(x => x.SessionUuid)
            .ToHashSet(StringComparer.OrdinalIgnoreCase);

        foreach (var stale in blockedSince.Keys.Where(k => !activeUuids.Contains(k)).ToList())
            blockedSince.Remove(stale);

        var targetBases = policy.TargetInfoBases
            .Where(b => !string.IsNullOrWhiteSpace(b))
            .Select(b => b.Trim())
            .ToHashSet(StringComparer.OrdinalIgnoreCase);

        bool IsExcluded(SessionRow s)
        {
            if (string.IsNullOrWhiteSpace(s.SessionUuid)) return true;
            if (string.IsNullOrWhiteSpace(s.UserName) || s.UserName == "<не указан>") return true;
            if (policy.IgnoreConfigurator && IsConfigurator(s)) return true;
            if (policy.IgnoreBackgroundJobs && IsBackgroundSession(s)) return true;
            if (policy.ExcludedUsers.Any(u => string.Equals(u.Trim(), s.UserName.Trim(), StringComparison.OrdinalIgnoreCase))) return true;
            if (policy.ExcludedInfoBases.Any(b => string.Equals(b.Trim(), s.InfoBase.Trim(), StringComparison.OrdinalIgnoreCase))) return true;
            if (targetBases.Count > 0 && !targetBases.Contains(s.InfoBase.Trim())) return true;
            return false;
        }

        var eligible = sessions.Where(s => !IsExcluded(s)).ToList();

        if (policy.TerminateDuplicates)
        {
            var groups = eligible.GroupBy(x => BuildDuplicateKey(x, policy.DuplicateGroupingMode), StringComparer.OrdinalIgnoreCase);
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
                        rawCandidates.Add(new SessionTerminationCandidate(
                            old,
                            $"Дубликат сеанса (более ранний от {old.StartedAt}; сохранён новый от {newest.StartedAt})"));
                    }
                }
            }
        }

        if (policy.TerminateHung)
        {
            var blockedBases = sessions
                .Where(s => s.Blocked != "нет")
                .GroupBy(s => s.InfoBase, StringComparer.OrdinalIgnoreCase)
                .ToDictionary(g => g.Key, g => g.Count(), StringComparer.OrdinalIgnoreCase);

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

                var hasBlockingActivityInBase = blockedBases.TryGetValue(s.InfoBase, out var bCount) && bCount > 0;
                if (policy.TerminateHungOnlyWhenBlockingOthers && !hasBlockingActivityInBase)
                    continue;

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
                    rawCandidates.Add(new SessionTerminationCandidate(s, reason));
            }
        }

        var requiredHits = Math.Max(1, policy.ConsecutiveChecksRequired);
        if (consecutiveHits != null)
        {
            var rawUuids = rawCandidates.Select(c => c.Session.SessionUuid).ToHashSet(StringComparer.OrdinalIgnoreCase);
            foreach (var key in consecutiveHits.Keys.Where(k => !rawUuids.Contains(k)).ToList())
                consecutiveHits.Remove(key);

            var confirmed = new List<SessionTerminationCandidate>();
            foreach (var c in rawCandidates)
            {
                var hits = consecutiveHits.TryGetValue(c.Session.SessionUuid, out var prev) ? prev + 1 : 1;
                consecutiveHits[c.Session.SessionUuid] = hits;
                if (hits >= requiredHits)
                    confirmed.Add(c);
            }
            rawCandidates = confirmed;
        }

        return rawCandidates
            .OrderBy(x => RacHelpers.ParseRacDate(x.Session.StartedAt))
            .ToList();
    }
}
