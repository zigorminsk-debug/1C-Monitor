using OneCMonitor.Core;
using Xunit;

namespace OneCMonitor.Tests;

public sealed class SecretProtectorTests
{
    [Fact]
    public void ProtectAndUnprotect_RoundTripsCorrectly()
    {
        var secret = "ClusterPwd!@#2026";
        var protectedValue = SecretProtector.Protect(secret);

        if (OperatingSystem.IsWindows())
            Assert.StartsWith("dpapi:", protectedValue);

        var restored = SecretProtector.Unprotect(protectedValue);
        Assert.Equal(secret, restored);
    }

    [Fact]
    public void EmptyAndPlainValues_HandledSafely()
    {
        Assert.Equal("", SecretProtector.Protect(""));
        Assert.Equal("", SecretProtector.Unprotect(""));
        Assert.Equal("legacy-plain", SecretProtector.Unprotect("legacy-plain"));
    }
}
