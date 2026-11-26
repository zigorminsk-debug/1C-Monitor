using OneCMonitor.Core;
using Xunit;

namespace OneCMonitor.Tests;

public sealed class SessionAnalyzerTests
{
    private static SessionRow MakeSession(
        string uuid,
        string user = "Иванов",
        string db = "ProdDB",
        string host = "PC-01",
        string app = "1CV8C",
        string startedAt = "2026-11-26T08:00:00",
        string lastActiveAt = "2026-11-26T10:00:00",
        bool sleeping = false,
        double currentCallMs = 0,
        double dbCallMs = 0,
        string blocked = "нет") => new()
        {
            SessionUuid = uuid,
            UserName = user,
            InfoBase = db,
            Host = host,
            AppId = app,
            StartedAt = startedAt,
            LastActiveAt = lastActiveAt,
            IsSleeping = sleeping,
            DurationCurrentMs = currentCallMs,
            DurationDbCurrentMs = dbCallMs,
            Blocked = blocked
        };

    [Fact]
    public void EvaluateWatchdogTargets_TerminatesOlderDuplicates_RespectsMinAgeAndExclusions()
    {
        var now = new DateTime(2026, 11, 26, 10, 0, 0);
        var sessions = new[]
        {
            MakeSession("old-dup", startedAt: "2026-11-26T08:00:00"),
            MakeSession("too-young-dup", startedAt: "2026-11-26T09:59:50"),
            MakeSession("newest", startedAt: "2026-11-26T09:59:55"),
            MakeSession("cfg-1", app: "Designer", startedAt: "2026-11-26T07:00:00"),
            MakeSession("cfg-2", app: "Designer", startedAt: "2026-11-26T09:00:00"),
            MakeSession("bg-1", app: "BackgroundJob", startedAt: "2026-11-26T07:00:00"),
            MakeSession("bg-2", app: "BackgroundJob", startedAt: "2026-11-26T09:00:00")
        };

        var policy = new SessionPolicyOptions
        {
            TerminateDuplicates = true,
            DuplicateMinAgeSeconds = 30,
            TerminateHung = false,
            IgnoreConfigurator = true,
            IgnoreBackgroundJobs = true
        };

        var targets = SessionAnalyzer.EvaluateWatchdogTargets(sessions, policy, new Dictionary<string, DateTime>(), now);

        Assert.Single(targets);
        Assert.Equal("old-dup", targets[0].Session.SessionUuid);
    }

    [Fact]
    public void EvaluateWatchdogTargets_UserAndBaseGrouping_DetectsDuplicatesAcrossHosts()
    {
        var now = new DateTime(2026, 11, 26, 10, 0, 0);
        var sessions = new[]
        {
            MakeSession("s1", host: "RDP-01", startedAt: "2026-11-26T08:00:00"),
            MakeSession("s2", host: "RDP-02", startedAt: "2026-11-26T09:00:00")
        };

        var policy = new SessionPolicyOptions
        {
            TerminateDuplicates = true,
            DuplicateGroupingMode = DuplicateGroupingModes.UserAndBase,
            TerminateHung = false
        };

        var targets = SessionAnalyzer.EvaluateWatchdogTargets(sessions, policy, new Dictionary<string, DateTime>(), now);
        Assert.Single(targets);
        Assert.Equal("s1", targets[0].Session.SessionUuid);
    }

    [Fact]
    public void EvaluateWatchdogTargets_DetectsHungCall_Blocked_And_Sleeping_WithGraceChecks()
    {
        var now = new DateTime(2026, 11, 26, 12, 0, 0);
        var blockedSince = new Dictionary<string, DateTime>
        {
            ["blk"] = now.AddMinutes(-20)
        };
        var consecutiveHits = new Dictionary<string, int>();

        var sessions = new[]
        {
            MakeSession("call-hung", user: "U1", currentCallMs: 65 * 60_000d),
            MakeSession("blk", user: "U2", blocked: "да (DBMS=12, LS=0)"),
            MakeSession("sleep-old", user: "U3", sleeping: true, lastActiveAt: "2026-11-26T08:00:00")
        };

        var policy = new SessionPolicyOptions
        {
            TerminateDuplicates = false,
            TerminateHung = true,
            HungMaxCurrentCallMinutes = 60,
            HungMaxBlockedMinutes = 15,
            HungMaxSleepingMinutes = 180,
            ConsecutiveChecksRequired = 2
        };

        // Первая проверка — только накапливает счётчик (grace period = 2)
        var firstPass = SessionAnalyzer.EvaluateWatchdogTargets(sessions, policy, blockedSince, now, consecutiveHits);
        Assert.Empty(firstPass);

        // Вторая подряд проверка — подтверждает завершение всех 3 сеансов
        var secondPass = SessionAnalyzer.EvaluateWatchdogTargets(sessions, policy, blockedSince, now.AddSeconds(30), consecutiveHits);
        Assert.Equal(3, secondPass.Count);
    }

    [Fact]
    public void EvaluateWatchdogTargets_RespectsTargetInfoBasesWhitelist()
    {
        var now = new DateTime(2026, 11, 26, 12, 0, 0);
        var sessions = new[]
        {
            MakeSession("prod-old", db: "ProdDB", startedAt: "2026-11-26T08:00:00"),
            MakeSession("prod-new", db: "ProdDB", startedAt: "2026-11-26T09:00:00"),
            MakeSession("test-old", db: "TestDB", startedAt: "2026-11-26T08:00:00"),
            MakeSession("test-new", db: "TestDB", startedAt: "2026-11-26T09:00:00")
        };

        var policy = new SessionPolicyOptions
        {
            TerminateDuplicates = true,
            TerminateHung = false,
            TargetInfoBases = new List<string> { "ProdDB" }
        };

        var targets = SessionAnalyzer.EvaluateWatchdogTargets(sessions, policy, new Dictionary<string, DateTime>(), now);
        Assert.Single(targets);
        Assert.Equal("prod-old", targets[0].Session.SessionUuid);
    }
}
