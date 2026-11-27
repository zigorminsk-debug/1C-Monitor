using System.Text;
using OneCMonitor.Core;
using Xunit;

namespace OneCMonitor.Tests;

public sealed class RacClientTests
{
    static RacClientTests()
    {
        Encoding.RegisterProvider(CodePagesEncodingProvider.Instance);
    }

    [Fact]
    public void ParseRecords_ParsesMultipleRecordsAndPreservesColonsInValues()
    {
        var input = """
            session                          : a1b2c3d4-0000-0000-0000-000000000001
            user-name                        : Иванов Иван
            started-at                       : 2026-11-26T09:15:30
            host                             : RDP-01

            session                          : a1b2c3d4-0000-0000-0000-000000000002
            user-name                        : Петров Пётр
            started-at                       : 2026-11-26T10:20:45
            hibernate                        : yes
            """;

        var records = RacClient.ParseRecords(input);

        Assert.Equal(2, records.Count);
        Assert.Equal("Иванов Иван", records[0].Get("user-name"));
        Assert.Equal("2026-11-26T09:15:30", records[0].Get("started-at"));
        Assert.Equal("Петров Пётр", records[1].Get("user-name"));
        Assert.True(RacHelpers.IsTrue(records[1].Get("hibernate")));
    }

    [Fact]
    public void DecodeRac_HandlesUtf8WithBom_Cp866_And_Cp1251()
    {
        var sample = "Пользователь: Администратор кластера 1С";

        var utf8Bom = Encoding.UTF8.GetPreamble().Concat(Encoding.UTF8.GetBytes(sample)).ToArray();
        Assert.Equal(sample, RacClient.DecodeRac(utf8Bom));

        var cp866 = Encoding.GetEncoding(866).GetBytes(sample);
        Assert.Equal(sample, RacClient.DecodeRac(cp866));

        var cp1251 = Encoding.GetEncoding(1251).GetBytes(sample);
        Assert.Equal(sample, RacClient.DecodeRac(cp1251));
    }

    [Fact]
    public void EndpointCandidates_YieldsLocalhostAndIpv4Counterparts()
    {
        var fromIp = RacClient.EndpointCandidates("127.0.0.1:1545").ToList();
        Assert.Equal(new[] { "127.0.0.1:1545", "localhost:1545" }, fromIp);

        var fromHost = RacClient.EndpointCandidates("localhost:1545").ToList();
        Assert.Equal(new[] { "localhost:1545", "127.0.0.1:1545" }, fromHost);
    }

    [Fact]
    public void SessionTerminationHelpers_DetectNotFoundAuthAndExistingSession()
    {
        Assert.True(RacClient.IsSessionNotFoundMessage("Сеанс с указанным идентификатором не найден"));
        Assert.True(RacClient.IsSessionNotFoundMessage("Session not found"));
        Assert.False(RacClient.IsSessionNotFoundMessage("Превышено время ожидания rac.exe"));

        Assert.True(RacClient.IsClusterAuthErrorMessage("Администратор кластера не аутентифицирован"));
        Assert.False(RacClient.IsClusterAuthErrorMessage("Сеанс не найден"));

        var listOutput = """
            session : 11111111-1111-1111-1111-111111111111
            user-name : Иванов
            """;
        Assert.True(RacClient.SessionExistsInOutput(listOutput, "11111111-1111-1111-1111-111111111111"));
        Assert.False(RacClient.SessionExistsInOutput(listOutput, "22222222-2222-2222-2222-222222222222"));
    }
}
