using System.Text.Json;
using System.Text.Json.Serialization;
using OneCMonitor.Core;

namespace OneCWatchdog;

public sealed class WatchdogConfig
{
    public const string ServiceName = "1CServerWatchdog";
    public const string ServiceDisplayName = "1C Server Monitor Watchdog";

    public string RacPath { get; set; } = "";
    public string Endpoint { get; set; } = "127.0.0.1:1545";
    public string AgentEndpoint { get; set; } = "127.0.0.1:1540";
    public string ClusterUser { get; set; } = "";
    public string EncryptedClusterPassword { get; set; } = "";
    public int RacTimeoutSeconds { get; set; } = 60;
    public bool AutoStartRas { get; set; } = true;

    public int CheckIntervalSeconds { get; set; } = 30;
    public bool DryRun { get; set; } = false;

    public SessionPolicyOptions Policy { get; set; } = new();

    public string LogDirectory { get; set; } = DefaultLogDirectory;
    public int LogRetentionDays { get; set; } = 30;

    public bool AutoUpdateEnabled { get; set; } = true;
    public int AutoUpdateCheckHours { get; set; } = 6;
    public string GitHubOwner { get; set; } = "zigorminsk-debug";
    public string GitHubRepository { get; set; } = "1C-Monitor";
    public string EncryptedGitHubToken { get; set; } = "";

    [JsonIgnore]
    public string ClusterPassword
    {
        get => SecretProtector.Unprotect(EncryptedClusterPassword);
        set => EncryptedClusterPassword = SecretProtector.Protect(value);
    }

    [JsonIgnore]
    public string GitHubToken
    {
        get => SecretProtector.Unprotect(EncryptedGitHubToken);
        set => EncryptedGitHubToken = SecretProtector.Protect(value);
    }

    public static string BaseDataDirectory =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "1CServerMonitor");

    public static string DefaultConfigPath => Path.Combine(BaseDataDirectory, "watchdog.json");
    public static string DefaultLogDirectory => Path.Combine(BaseDataDirectory, "logs");
    public static string DefaultUpdateStagingDirectory => Path.Combine(BaseDataDirectory, "updates");

    public RacConnectionOptions ToRacOptions() => new()
    {
        RacPath = RacPath,
        Endpoint = Endpoint,
        AgentEndpoint = AgentEndpoint,
        ClusterUser = ClusterUser,
        ClusterPassword = ClusterPassword,
        TimeoutSeconds = RacTimeoutSeconds
    };

    public static WatchdogConfig Load(string? path = null)
    {
        var file = path ?? DefaultConfigPath;
        try
        {
            if (File.Exists(file))
            {
                var loaded = JsonSerializer.Deserialize<WatchdogConfig>(File.ReadAllText(file));
                if (loaded != null)
                {
                    loaded.Policy ??= new SessionPolicyOptions();
                    loaded.Policy.ExcludedUsers ??= new();
                    loaded.Policy.ExcludedInfoBases ??= new();
                    if (string.IsNullOrWhiteSpace(loaded.LogDirectory))
                        loaded.LogDirectory = DefaultLogDirectory;
                    return loaded;
                }
            }
        }
        catch { }

        return new WatchdogConfig();
    }

    public void Save(string? path = null)
    {
        var file = path ?? DefaultConfigPath;
        Directory.CreateDirectory(Path.GetDirectoryName(file)!);
        var json = JsonSerializer.Serialize(this, new JsonSerializerOptions { WriteIndented = true });
        File.WriteAllText(file, json);
    }
}
