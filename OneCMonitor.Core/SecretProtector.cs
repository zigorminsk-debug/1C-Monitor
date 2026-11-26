using System.Diagnostics;
using System.Runtime.Versioning;
using System.Security.Cryptography;
using System.Text;

namespace OneCMonitor.Core;

public static class SecretProtector
{
    private const string Prefix = "dpapi:";

    public static string Protect(string? plainText)
    {
        if (string.IsNullOrEmpty(plainText)) return "";
        if (plainText.StartsWith(Prefix, StringComparison.Ordinal)) return plainText;
        if (!OperatingSystem.IsWindows()) return plainText;

        try
        {
            var bytes = Encoding.UTF8.GetBytes(plainText);
            var protectedBytes = ProtectWindows(bytes);
            return Prefix + Convert.ToBase64String(protectedBytes);
        }
        catch
        {
            return plainText;
        }
    }

    public static string Unprotect(string? storedValue)
    {
        if (string.IsNullOrEmpty(storedValue)) return "";
        if (!storedValue.StartsWith(Prefix, StringComparison.Ordinal)) return storedValue;
        if (!OperatingSystem.IsWindows()) return "";

        try
        {
            var raw = Convert.FromBase64String(storedValue[Prefix.Length..]);
            var plainBytes = UnprotectWindows(raw);
            return Encoding.UTF8.GetString(plainBytes);
        }
        catch
        {
            return "";
        }
    }

    [SupportedOSPlatform("windows")]
    private static byte[] ProtectWindows(byte[] data) =>
        ProtectedData.Protect(data, null, DataProtectionScope.LocalMachine);

    [SupportedOSPlatform("windows")]
    private static byte[] UnprotectWindows(byte[] data) =>
        ProtectedData.Unprotect(data, null, DataProtectionScope.LocalMachine);
}
