using System.Net;
using System.Net.Mail;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace OneCMonitor.Core;

public sealed class NotificationOptions
{
    public bool Enabled { get; set; } = false;
    public bool NotifyOnWatchdogTermination { get; set; } = true;
    public bool NotifyOnCriticalAlerts { get; set; } = true;
    public bool NotifyOnUpdates { get; set; } = true;
    public int CooldownSeconds { get; set; } = 60;

    // Telegram Bot API
    public bool TelegramEnabled { get; set; } = false;
    public string EncryptedTelegramBotToken { get; set; } = "";
    public string TelegramChatId { get; set; } = "";

    // Generic JSON Webhook (Slack, Mattermost, Teams, custom HTTP)
    public bool WebhookEnabled { get; set; } = false;
    public string WebhookUrl { get; set; } = "";

    // Email SMTP
    public bool EmailEnabled { get; set; } = false;
    public string SmtpHost { get; set; } = "";
    public int SmtpPort { get; set; } = 587;
    public bool SmtpUseSsl { get; set; } = true;
    public string SmtpUser { get; set; } = "";
    public string EncryptedSmtpPassword { get; set; } = "";
    public string EmailFrom { get; set; } = "";
    public string EmailTo { get; set; } = "";

    [JsonIgnore]
    public string TelegramBotToken
    {
        get => SecretProtector.Unprotect(EncryptedTelegramBotToken);
        set => EncryptedTelegramBotToken = SecretProtector.Protect(value);
    }

    [JsonIgnore]
    public string SmtpPassword
    {
        get => SecretProtector.Unprotect(EncryptedSmtpPassword);
        set => EncryptedSmtpPassword = SecretProtector.Protect(value);
    }
}

public sealed class NotificationService
{
    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(15) };
    private readonly Dictionary<string, DateTime> _lastSentUtc = new(StringComparer.OrdinalIgnoreCase);
    private readonly object _sync = new();

    public async Task<List<string>> SendAsync(
        NotificationOptions options,
        string title,
        string message,
        string? dedupKey = null,
        bool bypassCooldown = false,
        CancellationToken cancellationToken = default)
    {
        var errors = new List<string>();
        if (!options.Enabled) return errors;

        if (!bypassCooldown && !string.IsNullOrWhiteSpace(dedupKey))
        {
            var cooldown = TimeSpan.FromSeconds(Math.Max(5, options.CooldownSeconds));
            lock (_sync)
            {
                if (_lastSentUtc.TryGetValue(dedupKey, out var last) && DateTime.UtcNow - last < cooldown)
                    return errors;
                _lastSentUtc[dedupKey] = DateTime.UtcNow;
                foreach (var old in _lastSentUtc.Where(x => DateTime.UtcNow - x.Value > TimeSpan.FromDays(1)).Select(x => x.Key).ToList())
                    _lastSentUtc.Remove(old);
            }
        }

        var fullText = $"[{Environment.MachineName}] {title}\n{message}";

        if (options.TelegramEnabled && !string.IsNullOrWhiteSpace(options.TelegramBotToken) && !string.IsNullOrWhiteSpace(options.TelegramChatId))
        {
            try
            {
                var url = $"https://api.telegram.org/bot{options.TelegramBotToken.Trim()}/sendMessage";
                var payload = JsonSerializer.Serialize(new
                {
                    chat_id = options.TelegramChatId.Trim(),
                    text = fullText
                });
                using var content = new StringContent(payload, Encoding.UTF8, "application/json");
                using var resp = await Http.PostAsync(url, content, cancellationToken);
                if (!resp.IsSuccessStatusCode)
                    errors.Add($"Telegram HTTP {(int)resp.StatusCode}: {await resp.Content.ReadAsStringAsync(cancellationToken)}");
            }
            catch (Exception ex)
            {
                errors.Add("Telegram: " + ex.Message);
            }
        }

        if (options.WebhookEnabled && !string.IsNullOrWhiteSpace(options.WebhookUrl))
        {
            try
            {
                var payload = JsonSerializer.Serialize(new
                {
                    server = Environment.MachineName,
                    title,
                    message,
                    text = fullText,
                    timestamp = DateTimeOffset.UtcNow
                });
                using var content = new StringContent(payload, Encoding.UTF8, "application/json");
                using var resp = await Http.PostAsync(options.WebhookUrl.Trim(), content, cancellationToken);
                if (!resp.IsSuccessStatusCode)
                    errors.Add($"Webhook HTTP {(int)resp.StatusCode}");
            }
            catch (Exception ex)
            {
                errors.Add("Webhook: " + ex.Message);
            }
        }

        if (options.EmailEnabled && !string.IsNullOrWhiteSpace(options.SmtpHost) && !string.IsNullOrWhiteSpace(options.EmailTo))
        {
            try
            {
                var fromAddress = string.IsNullOrWhiteSpace(options.EmailFrom) ? options.SmtpUser : options.EmailFrom;
                using var mail = new MailMessage(fromAddress, options.EmailTo)
                {
                    Subject = $"[{Environment.MachineName}] {title}",
                    Body = fullText
                };
                using var client = new SmtpClient(options.SmtpHost.Trim(), options.SmtpPort <= 0 ? 587 : options.SmtpPort)
                {
                    EnableSsl = options.SmtpUseSsl,
                    Timeout = 15000
                };
                if (!string.IsNullOrWhiteSpace(options.SmtpUser))
                    client.Credentials = new NetworkCredential(options.SmtpUser, options.SmtpPassword);
                await client.SendMailAsync(mail, cancellationToken);
            }
            catch (Exception ex)
            {
                errors.Add("Email: " + ex.Message);
            }
        }

        return errors;
    }
}
