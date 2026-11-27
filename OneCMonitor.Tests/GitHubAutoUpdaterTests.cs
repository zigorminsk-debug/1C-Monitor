using System.Security.Cryptography;
using OneCMonitor.Core;
using Xunit;

namespace OneCMonitor.Tests;

public sealed class GitHubAutoUpdaterTests
{
    [Theory]
    [InlineData("v1.5.0", 1, 5, 0)]
    [InlineData("1.4.2-beta.1", 1, 4, 2)]
    [InlineData("V2.0.10+abc123", 2, 0, 10)]
    public void ParseComparableVersion_NormalizesTags(string raw, int major, int minor, int build)
    {
        var parsed = GitHubAutoUpdater.ParseComparableVersion(raw);
        Assert.Equal(new Version(major, minor, build), parsed);
    }

    [Fact]
    public void VerifyFileSha256_PassesOnMatch_AndThrowsOnMismatch()
    {
        var tempDir = Path.Combine(Path.GetTempPath(), "1cm-test-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(tempDir);
        try
        {
            var payloadPath = Path.Combine(tempDir, "OneCWatchdog-win-x64.zip");
            var content = "test-binary-payload"u8.ToArray();
            File.WriteAllBytes(payloadPath, content);
            var expectedHash = Convert.ToHexString(SHA256.HashData(content)).ToLowerInvariant();

            var sumsPath = Path.Combine(tempDir, "SHA256SUMS.txt");
            File.WriteAllText(sumsPath, $"{expectedHash}  OneCWatchdog-win-x64.zip\n");

            GitHubAutoUpdater.VerifyFileSha256(payloadPath, "OneCWatchdog-win-x64.zip", sumsPath);

            File.WriteAllText(sumsPath, "0000000000000000000000000000000000000000000000000000000000000000  OneCWatchdog-win-x64.zip\n");
            Assert.Throws<CryptographicException>(() =>
                GitHubAutoUpdater.VerifyFileSha256(payloadPath, "OneCWatchdog-win-x64.zip", sumsPath));
        }
        finally
        {
            Directory.Delete(tempDir, true);
        }
    }
}
