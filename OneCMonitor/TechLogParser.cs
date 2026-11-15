using System.Globalization;
using System.Text;
using System.Text.RegularExpressions;

namespace OneCMonitor;

/// <summary>Событие технологического журнала 1С, у которого заполнено свойство Context.</summary>
internal sealed record TechLogEvent(DateTime At, string InfoBase, string SessionId, string User, string Context, double DurationMs);

/// <summary>
/// Разбор технологического журнала 1С. Событие начинается строкой «мм:сс.микросекунды-длительность,ИМЯ,уровень,свойство=значение,…»
/// и может занимать несколько строк: значения в кавычках (Context, Sql) содержат переводы строк.
/// </summary>
internal static class TechLogParser
{
    private static readonly Regex EventStart = new(@"^(\d{2}):(\d{2})\.(\d+)-(\d+),", RegexOptions.Compiled | RegexOptions.CultureInvariant);

    /// <summary>Читает события с контекстом из последних <paramref name="tailBytes"/> байт файла.</summary>
    public static List<TechLogEvent> ReadTail(string file, int tailBytes)
    {
        using var stream = new FileStream(file, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
        var fromMiddle = stream.Length > tailBytes;
        if (fromMiddle) stream.Seek(-tailBytes, SeekOrigin.End);
        using var reader = new StreamReader(stream, Encoding.UTF8, true);
        if (fromMiddle) reader.ReadLine(); // первая строка после перехода в середину файла неполная
        return Parse(reader, GetFileHour(file));
    }

    /// <summary>Имя файла техжурнала — ггММддчч.log; минуты и секунды записаны в самих событиях.</summary>
    internal static DateTime GetFileHour(string file)
    {
        if (DateTime.TryParseExact(Path.GetFileNameWithoutExtension(file), "yyMMddHH", CultureInfo.InvariantCulture, DateTimeStyles.None, out var hour)) return hour;
        try { var written = File.GetLastWriteTime(file); return new DateTime(written.Year, written.Month, written.Day, written.Hour, 0, 0); }
        catch { return DateTime.Today; }
    }

    internal static List<TechLogEvent> Parse(TextReader reader, DateTime fileHour)
    {
        var events = new List<TechLogEvent>();
        StringBuilder? current = null;
        while (reader.ReadLine() is { } line)
        {
            if (EventStart.IsMatch(line)) { Flush(); current = new StringBuilder(line); }
            else current?.Append('\n').Append(line); // продолжение многострочного события
        }
        Flush();
        return events;

        void Flush()
        {
            if (current == null) return;
            var parsed = ParseEvent(current.ToString(), fileHour);
            if (parsed != null) events.Add(parsed);
            current = null;
        }
    }

    internal static TechLogEvent? ParseEvent(string text, DateTime fileHour)
    {
        if (text.IndexOf("Context=", StringComparison.OrdinalIgnoreCase) < 0) return null;
        var header = EventStart.Match(text);
        if (!header.Success) return null;
        var properties = ParseProperties(text, header.Length);
        if (!properties.TryGetValue("Context", out var rawContext)) return null;
        var context = SummarizeContext(rawContext);
        if (context.Length == 0) return null;

        var at = fileHour.AddMinutes(int.Parse(header.Groups[1].Value, CultureInfo.InvariantCulture)).AddSeconds(int.Parse(header.Groups[2].Value, CultureInfo.InvariantCulture));
        if (double.TryParse("0." + header.Groups[3].Value, NumberStyles.AllowDecimalPoint, CultureInfo.InvariantCulture, out var fraction)) at = at.AddSeconds(fraction);
        double.TryParse(header.Groups[4].Value, NumberStyles.None, CultureInfo.InvariantCulture, out var microseconds);
        return new TechLogEvent(at, Get("p:processName"), Get("SessionID"), Get("Usr"), context, microseconds / 1000d);

        string Get(string name) => properties.TryGetValue(name, out var value) ? value.Trim() : "";
    }

    /// <summary>
    /// Разбирает «ключ=значение» через запятую. Значение может быть в одинарных или двойных кавычках
    /// (кавычка внутри удваивается) и тогда может содержать запятые и переводы строк.
    /// </summary>
    internal static Dictionary<string, string> ParseProperties(string text, int start)
    {
        var result = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        var i = start; var n = text.Length;
        while (i < n)
        {
            var equals = text.IndexOf('=', i);
            if (equals < 0) break;
            var comma = text.IndexOf(',', i);
            if (comma >= 0 && comma < equals) { i = comma + 1; continue; } // поле без «=»: имя события, уровень
            var key = text[i..equals].Trim();
            i = equals + 1;
            string value;
            if (i < n && (text[i] == '\'' || text[i] == '"'))
            {
                var quote = text[i++]; var buffer = new StringBuilder();
                while (i < n)
                {
                    if (text[i] == quote)
                    {
                        if (i + 1 < n && text[i + 1] == quote) { buffer.Append(quote); i += 2; continue; }
                        i++; break;
                    }
                    buffer.Append(text[i++]);
                }
                value = buffer.ToString();
                var next = text.IndexOf(',', i); i = next < 0 ? n : next + 1;
            }
            else
            {
                var end = text.IndexOf(',', i); if (end < 0) end = n;
                value = text[i..end].Trim(); i = end + 1;
            }
            if (key.Length > 0) result[key] = value;
        }
        return result;
    }

    /// <summary>Многострочный стек контекста сворачивается в «точка входа → самая глубокая строка кода».</summary>
    internal static string SummarizeContext(string context)
    {
        var lines = context.Split('\n', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        if (lines.Length == 0) return "";
        var summary = lines.Length == 1 || lines[0] == lines[^1] ? lines[0] : $"{lines[0]} → {lines[^1]}";
        return summary.Length > 500 ? summary[..500] + "…" : summary;
    }
}

/// <summary>
/// Поиск последнего события техжурнала для сеанса. Номер сеанса (SessionID) уникален только в пределах
/// информационной базы, поэтому сопоставление всегда учитывает базу (p:processName) либо пару «номер + пользователь».
/// </summary>
internal sealed class TechLogIndex
{
    private readonly Dictionary<string, TechLogEvent> _byInfoBaseAndSession = new(StringComparer.OrdinalIgnoreCase);
    private readonly Dictionary<string, TechLogEvent> _byInfoBaseAndUser = new(StringComparer.OrdinalIgnoreCase);
    private readonly Dictionary<string, TechLogEvent> _bySessionAndUser = new(StringComparer.OrdinalIgnoreCase);

    public void Add(TechLogEvent item)
    {
        if (item.InfoBase.Length > 0 && item.SessionId.Length > 0) Put(_byInfoBaseAndSession, Key(item.InfoBase, item.SessionId), item);
        if (item.InfoBase.Length > 0 && item.User.Length > 0) Put(_byInfoBaseAndUser, Key(item.InfoBase, item.User), item);
        if (item.SessionId.Length > 0 && item.User.Length > 0) Put(_bySessionAndUser, Key(item.SessionId, item.User), item);
    }

    public TechLogEvent? Find(string infoBase, string sessionNumber, string user)
    {
        infoBase = infoBase.Trim(); sessionNumber = sessionNumber.Trim(); user = user.Trim();
        if (infoBase.Length > 0 && sessionNumber.Length > 0 && _byInfoBaseAndSession.TryGetValue(Key(infoBase, sessionNumber), out var exact)) return exact;
        if (sessionNumber.Length > 0 && user.Length > 0 && _bySessionAndUser.TryGetValue(Key(sessionNumber, user), out var bySession)) return bySession;
        if (infoBase.Length > 0 && user.Length > 0 && _byInfoBaseAndUser.TryGetValue(Key(infoBase, user), out var byUser)) return byUser;
        return null;
    }

    private static string Key(string first, string second) => first + "\u001F" + second;

    // В индексе остаётся самое позднее событие, независимо от порядка чтения файлов.
    private static void Put(Dictionary<string, TechLogEvent> map, string key, TechLogEvent item)
    {
        if (!map.TryGetValue(key, out var existing) || item.At >= existing.At) map[key] = item;
    }
}
