using System.Diagnostics;
using System.Text;
using System.Text.RegularExpressions;

namespace OneCMonitor.Core;

public sealed class RacClient
{
    private readonly List<string> _cachedRacCandidates = new();
    private DateTime _racCandidatesCachedAt = DateTime.MinValue;
    private string? _lastClusterId;

    public string? LastClusterId => _lastClusterId;

    public void InvalidateRacCache() => _racCandidatesCachedAt = DateTime.MinValue;

    public static IEnumerable<string> EndpointCandidates(string? configured)
    {
        var value = string.IsNullOrWhiteSpace(configured) ? "127.0.0.1:1545" : configured.Trim();
        yield return value;
        if (value.StartsWith("localhost:", StringComparison.OrdinalIgnoreCase))
            yield return "127.0.0.1:" + value[(value.IndexOf(':') + 1)..];
        else if (value.StartsWith("127.0.0.1:", StringComparison.OrdinalIgnoreCase))
            yield return "localhost:" + value[(value.IndexOf(':') + 1)..];
    }

    public string? FindRac(string? configuredPath = null)
    {
        var found = FindRacCandidates(configuredPath).FirstOrDefault();
        if (found != null) return found;
        _racCandidatesCachedAt = DateTime.MinValue;
        return FindRacCandidates(configuredPath).FirstOrDefault();
    }

    public List<string> FindRacCandidates(string? configuredPath = null)
    {
        var result = new List<string>();
        if (!OperatingSystem.IsWindows()) return result;

        if (DateTime.UtcNow - _racCandidatesCachedAt < TimeSpan.FromMinutes(10) && _cachedRacCandidates.All(File.Exists))
        {
            var cached = new List<string>(_cachedRacCandidates);
            if (!string.IsNullOrWhiteSpace(configuredPath) &&
                File.Exists(configuredPath) &&
                File.Exists(Path.Combine(Path.GetDirectoryName(configuredPath)!, "ras.exe")) &&
                !cached.Contains(configuredPath, StringComparer.OrdinalIgnoreCase))
            {
                cached.Insert(0, configuredPath);
            }
            return cached;
        }

        Add(configuredPath);

        foreach (var view in new[] { Microsoft.Win32.RegistryView.Registry64, Microsoft.Win32.RegistryView.Registry32 })
        {
            try
            {
                using var hklm = Microsoft.Win32.RegistryKey.OpenBaseKey(Microsoft.Win32.RegistryHive.LocalMachine, view);
                using var services = hklm.OpenSubKey(@"SYSTEM\CurrentControlSet\Services");
                if (services == null) continue;
                foreach (var serviceName in services.GetSubKeyNames())
                {
                    try
                    {
                        using var service = services.OpenSubKey(serviceName);
                        var image = service?.GetValue("ImagePath")?.ToString() ?? "";
                        var match = Regex.Match(image, "(?:\\\"(?<p>[^\\\"]*ragent\\.exe)\\\"|(?<p>[A-Za-z]:\\\\.*?ragent\\.exe))", RegexOptions.IgnoreCase);
                        if (match.Success)
                            Add(Path.Combine(Path.GetDirectoryName(Environment.ExpandEnvironmentVariables(match.Groups["p"].Value))!, "rac.exe"));
                    }
                    catch { }
                }
            }
            catch { }
        }

        foreach (var name in new[] { "ragent", "rphost", "ras" })
        {
            foreach (var process in Process.GetProcessesByName(name))
            {
                try
                {
                    var module = process.MainModule?.FileName;
                    if (!string.IsNullOrWhiteSpace(module))
                        Add(Path.Combine(Path.GetDirectoryName(module)!, "rac.exe"));
                }
                catch { }
                finally { process.Dispose(); }
            }
        }

        Add(Path.Combine(AppContext.BaseDirectory, "rac.exe"));

        foreach (var view in new[] { Microsoft.Win32.RegistryView.Registry64, Microsoft.Win32.RegistryView.Registry32 })
        {
            try
            {
                using var hklm = Microsoft.Win32.RegistryKey.OpenBaseKey(Microsoft.Win32.RegistryHive.LocalMachine, view);
                foreach (var uninstallPath in new[]
                {
                    @"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall",
                    @"SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall"
                })
                {
                    using var uninstall = hklm.OpenSubKey(uninstallPath);
                    if (uninstall == null) continue;
                    foreach (var keyName in uninstall.GetSubKeyNames())
                    {
                        try
                        {
                            using var item = uninstall.OpenSubKey(keyName);
                            var name = item?.GetValue("DisplayName")?.ToString() ?? "";
                            if (!name.Contains("1C", StringComparison.OrdinalIgnoreCase) && !name.Contains("1С", StringComparison.OrdinalIgnoreCase)) continue;
                            var location = Environment.ExpandEnvironmentVariables(item?.GetValue("InstallLocation")?.ToString() ?? "");
                            AddFromDirectory(location);
                        }
                        catch { }
                    }
                }
            }
            catch { }
        }

        var roots = new HashSet<string>(StringComparer.OrdinalIgnoreCase)
        {
            Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles),
            Environment.GetFolderPath(Environment.SpecialFolder.ProgramFilesX86)
        };
        try
        {
            foreach (var drive in DriveInfo.GetDrives().Where(x => x.IsReady && x.DriveType == DriveType.Fixed))
            {
                foreach (var relative in new[] { "Program Files", "Program Files (x86)", "1cv8", "1C", "1С" })
                {
                    var candidate = Path.Combine(drive.RootDirectory.FullName, relative);
                    if (Directory.Exists(candidate)) roots.Add(candidate);
                }
            }
        }
        catch { }

        foreach (var root in roots.Where(Directory.Exists))
        {
            AddFromDirectory(Path.Combine(root, "1cv8"));
            if (Path.GetFileName(root).Equals("1cv8", StringComparison.OrdinalIgnoreCase)) AddFromDirectory(root);
        }

        _cachedRacCandidates.Clear();
        _cachedRacCandidates.AddRange(result);
        _racCandidatesCachedAt = DateTime.UtcNow;
        return result;

        void AddFromDirectory(string? directory)
        {
            if (string.IsNullOrWhiteSpace(directory) || !Directory.Exists(directory)) return;
            var found = new List<string>();
            var queue = new Queue<(string Path, int Depth)>();
            queue.Enqueue((directory, 0));
            var visited = 0;
            while (queue.Count > 0 && visited++ < 500)
            {
                var item = queue.Dequeue();
                try
                {
                    foreach (var candidate in new[] { Path.Combine(item.Path, "rac.exe"), Path.Combine(item.Path, "bin", "rac.exe") })
                        if (File.Exists(candidate)) found.Add(candidate);
                    if (item.Depth >= 2) continue;
                    foreach (var child in Directory.EnumerateDirectories(item.Path))
                        queue.Enqueue((child, item.Depth + 1));
                }
                catch { }
            }
            foreach (var file in found.Distinct(StringComparer.OrdinalIgnoreCase).OrderByDescending(GetExecutableVersion))
                Add(file);
        }

        static Version GetExecutableVersion(string path)
        {
            try { return Version.TryParse(FileVersionInfo.GetVersionInfo(path).FileVersion?.Split(' ')[0], out var version) ? version : new Version(); }
            catch { return new Version(); }
        }

        void Add(string? path)
        {
            if (!string.IsNullOrWhiteSpace(path) &&
                File.Exists(path) &&
                File.Exists(Path.Combine(Path.GetDirectoryName(path)!, "ras.exe")) &&
                !result.Contains(path, StringComparer.OrdinalIgnoreCase))
            {
                result.Add(path);
            }
        }
    }

    public async Task<RacSnapshot> ReadRacAsync(string rac, string endpoint, RacConnectionOptions options, Action<string>? log = null, CancellationToken cancellationToken = default)
    {
        endpoint = string.IsNullOrWhiteSpace(endpoint) ? options.Endpoint : endpoint;
        var timeout = Math.Max(15, options.TimeoutSeconds);
        string clusterOutput;
        try { clusterOutput = await RunAsync(rac, new[] { "cluster", "list", endpoint }, timeout, cancellationToken); }
        catch { clusterOutput = await RunAsync(rac, new[] { endpoint, "cluster", "list" }, timeout, cancellationToken); }

        var clusters = ParseRecords(clusterOutput);
        var clusterIds = clusters.Select(x => RacHelpers.Normalize(x.Get("cluster")))
            .Where(x => x.Length > 0)
            .Distinct(StringComparer.OrdinalIgnoreCase)
            .OrderByDescending(x => x.Equals(_lastClusterId, StringComparison.OrdinalIgnoreCase))
            .ToList();

        if (clusterIds.Count == 0)
            throw new Exception("Кластер не найден. Проверьте RAS, порт 1545 и версию RAC.");

        string? cluster = null;
        string sessionOutput = "";
        var bestSessionCount = -1;
        Exception? clusterError = null;

        foreach (var candidateCluster in clusterIds)
        {
            try
            {
                var candidateSessions = await RunListAsync(new[] { "session", "list" }, candidateCluster);
                var count = ParseRecords(candidateSessions).Count;
                log?.Invoke($"RAC: кластер {candidateCluster}, найдено сеансов: {count}");
                if (count > bestSessionCount)
                {
                    bestSessionCount = count;
                    cluster = candidateCluster;
                    sessionOutput = candidateSessions;
                }
                if (count > 0 && candidateCluster.Equals(_lastClusterId, StringComparison.OrdinalIgnoreCase)) break;
            }
            catch (Exception ex)
            {
                clusterError = ex;
                log?.Invoke($"RAC: кластер {candidateCluster} недоступен: {ex.Message}");
            }
        }

        if (cluster == null)
            throw new Exception("Не удалось прочитать сеансы ни одного кластера: " + clusterError?.Message);

        _lastClusterId = cluster;

        var infobaseOut = await TryListAsync(new[] { "infobase", "summary", "list" }, cluster);
        var processOut = await TryListAsync(new[] { "process", "list" }, cluster);
        var connectionOut = await TryListAsync(new[] { "connection", "list" }, cluster);

        var ibRecords = ParseRecords(infobaseOut);
        var ib = ibRecords.Where(x => RacHelpers.Normalize(x.Get("infobase")).Length > 0)
            .GroupBy(x => RacHelpers.Normalize(x.Get("infobase")))
            .ToDictionary(x => x.Key, x => x.First().Get("name"));
        var pr = ParseRecords(processOut);
        var cn = ParseRecords(connectionOut);
        var ss = ParseRecords(sessionOutput);

        var procUuidToPid = pr.Where(x => RacHelpers.ParseInt(x.Get("pid")) > 0)
            .GroupBy(x => RacHelpers.Normalize(x.Get("process")))
            .ToDictionary(x => x.Key, x => RacHelpers.ParseInt(x.First().Get("pid")));

        var connectionLinks = new Dictionary<string, ConnectionLink>();
        var connectionCandidates = new List<ConnectionLink>();
        foreach (var c in cn)
        {
            var pid = RacHelpers.ParseInt(c.Get("pid"));
            if (pid <= 0) procUuidToPid.TryGetValue(RacHelpers.Normalize(c.Get("process")), out pid);
            if (pid <= 0) pid = RacHelpers.ParseInt(c.Get("process-id"));
            var link = new ConnectionLink(
                pid,
                RacHelpers.EmptyFallback(c.Get("current-service-call"), c.Get("current-service-name")),
                c.Get("current-dbms-request"),
                RacHelpers.Normalize(c.Get("host")),
                RacHelpers.Normalize(c.Get("infobase")),
                RacHelpers.Normalize(c.Get("application")),
                RacHelpers.ParseRacDate(c.Get("connected-at")),
                RacHelpers.ParseInt(c.Get("conn-id")));
            connectionCandidates.Add(link);
            AddLink("uuid", c.Get("connection"), link);
            AddLink("id", c.Get("conn-id"), link);
            AddLink("id", c.Get("connection-id"), link);
            AddLink("session", c.Get("session-number"), link);
        }

        var result = new RacSnapshot
        {
            RacPath = rac,
            Endpoint = endpoint,
            ClusterId = cluster,
            ClusterOutput = clusterOutput,
            InfoBaseOutput = infobaseOut,
            ProcessOutput = processOut,
            ConnectionOutput = connectionOut,
            SessionOutput = sessionOutput
        };
        result.ClusterIds.AddRange(clusterIds);

        foreach (var b in ibRecords)
        {
            var uuid = RacHelpers.Normalize(b.Get("infobase"));
            if (uuid.Length == 0) continue;
            result.InfoBases[uuid] = new InfoBaseRef(uuid, RacHelpers.EmptyFallback(b.Get("name"), uuid), RacHelpers.IsTrue(b.Get("scheduled-jobs-deny")));
        }

        foreach (var p in pr)
        {
            var pid = RacHelpers.ParseInt(p.Get("pid"));
            if (pid <= 0) continue;
            result.Processes[pid] = new RacProcess(
                pid,
                RacHelpers.Normalize(p.Get("process")),
                RacHelpers.ParseNum(p.Get("memory-size")) / 1024d,
                RacHelpers.ParseNum(p.Get("avg-call-time")) * 1000d,
                RacHelpers.ParseNum(p.Get("avg-db-call-time")) * 1000d);
        }

        foreach (var s in ss)
        {
            var link = FindLink(s);
            var pid = link?.Pid ?? 0;
            if (pid <= 0 && procUuidToPid.TryGetValue(RacHelpers.Normalize(s.Get("process")), out var directPid)) pid = directPid;
            if (pid <= 0) pid = RacHelpers.ParseInt(s.Get("pid"));
            ib.TryGetValue(RacHelpers.Normalize(s.Get("infobase")), out var db);
            var b1 = s.Get("blocked-by-dbms");
            var b2 = s.Get("blocked-by-ls");
            var sleeping = RacHelpers.IsTrue(s.Get("hibernate"));
            var operation = RacHelpers.EmptyFallback(s.Get("current-service-call"), RacHelpers.EmptyFallback(s.Get("current-service-name"), link?.Operation ?? ""));
            var dbRequest = RacHelpers.EmptyFallback(s.Get("current-dbms-request"), link?.DbRequest ?? "");

            result.Sessions.Add(new SessionRow
            {
                SessionUuid = RacHelpers.Normalize(s.Get("session")),
                UserName = RacHelpers.EmptyFallback(s.Get("user-name"), "<не указан>"),
                InfoBase = RacHelpers.EmptyFallback(db, RacHelpers.Normalize(s.Get("infobase"))),
                AppId = s.Get("app-id"),
                Host = s.Get("host"),
                Pid = pid,
                SessionNumber = RacHelpers.EmptyFallback(s.Get("session-id"), RacHelpers.EmptyFallback(s.Get("session-number"), s.Get("session"))),
                StartedAt = s.Get("started-at"),
                LastActiveAt = s.Get("last-active-at"),
                IsSleeping = sleeping,
                MemoryCurrentMb = RacHelpers.ParseNum(s.Get("memory-current")) / 1048576d,
                MemoryLast5MinMb = RacHelpers.ParseNum(s.Get("memory-last-5min")) / 1048576d,
                CpuTimeCurrent = RacHelpers.ParseNum(s.Get("cpu-time-current")),
                CpuTimeLast5Min = RacHelpers.ParseNum(s.Get("cpu-time-last-5min")),
                DurationCurrentMs = RacHelpers.ParseNum(s.Get("duration-current")),
                DurationDbCurrentMs = RacHelpers.ParseNum(s.Get("duration-current-dbms")),
                DurationLast5MinMs = RacHelpers.ParseNum(s.Get("duration-last-5min")),
                DurationDbLast5MinMs = RacHelpers.ParseNum(s.Get("duration-last-5min-dbms")),
                CallsLast5Min = RacHelpers.ParseInt(s.Get("calls-last-5min")),
                BytesLast5Min = RacHelpers.ParseNum(s.Get("bytes-last-5min")),
                DbBytesLast5Min = RacHelpers.ParseNum(s.Get("dbms-bytes-last-5min")),
                ReadCurrent = RacHelpers.ParseNum(s.Get("read-current")),
                WriteCurrent = RacHelpers.ParseNum(s.Get("write-current")),
                ReadLast5Min = RacHelpers.ParseNum(s.Get("read-last-5min")),
                WriteLast5Min = RacHelpers.ParseNum(s.Get("write-last-5min")),
                Operation = operation,
                DbRequest = dbRequest,
                Blocked = RacHelpers.IsTrue(b1) || RacHelpers.IsTrue(b2) ? $"да (DBMS={b1}, LS={b2})" : "нет"
            });
        }

        return result;

        async Task<string> TryListAsync(string[] command, string cl)
        {
            try { return await RunListAsync(command, cl); }
            catch (Exception ex) { log?.Invoke($"RAC: {string.Join(' ', command)} недоступна, продолжаем по session list: {ex.Message}"); return ""; }
        }

        async Task<string> RunListAsync(string[] command, string cl)
        {
            var auth = new List<string> { $"--cluster={cl}" };
            if (!string.IsNullOrWhiteSpace(options.ClusterUser))
            {
                auth.Add($"--cluster-user={options.ClusterUser}");
                auth.Add($"--cluster-pwd={options.ClusterPassword}");
            }
            var variants = new List<List<string>>
            {
                command.Concat(auth).Append(endpoint).ToList(),
                new[] { endpoint }.Concat(command).Concat(auth).ToList(),
                new[] { command[0] }.Concat(auth).Concat(command.Skip(1)).Append(endpoint).ToList()
            };
            Exception? last = null;
            foreach (var args in variants)
            {
                try { return await RunAsync(rac, args, timeout, cancellationToken); }
                catch (Exception ex) { last = ex; }
            }
            throw last ?? new Exception("Команда RAC не выполнена");
        }

        void AddLink(string kind, string value, ConnectionLink link)
        {
            var key = RacHelpers.Normalize(value);
            if (key.Length > 0) connectionLinks[$"{kind}:{key}"] = link;
        }

        ConnectionLink? FindLink(RacRecord s)
        {
            var keys = new[]
            {
                $"uuid:{RacHelpers.Normalize(s.Get("connection"))}",
                $"id:{RacHelpers.Normalize(s.Get("conn-id"))}",
                $"id:{RacHelpers.Normalize(s.Get("connection-id"))}",
                $"session:{RacHelpers.Normalize(s.Get("session-number"))}"
            };
            foreach (var key in keys)
            {
                if (!key.EndsWith(':') && !key.Contains("00000000-0000-0000-0000-000000000000") && connectionLinks.TryGetValue(key, out var found))
                    return found;
            }

            var host = RacHelpers.Normalize(s.Get("host"));
            var infobase = RacHelpers.Normalize(s.Get("infobase"));
            var app = RacHelpers.Normalize(s.Get("app-id"));
            var started = RacHelpers.ParseRacDate(s.Get("started-at"));
            var candidates = connectionCandidates.Where(c => c.Pid > 0 && c.Host == host && c.InfoBase == infobase && c.Application == app).ToList();
            if (started != DateTime.MinValue)
            {
                var close = candidates.Where(c => c.ConnectedAt != DateTime.MinValue && Math.Abs((c.ConnectedAt - started).TotalSeconds) <= 5).ToList();
                if (close.Count == 0) return null;
                candidates = close;
            }
            return candidates.OrderByDescending(c => c.ConnId > 0)
                .ThenBy(c => started == DateTime.MinValue || c.ConnectedAt == DateTime.MinValue ? double.MaxValue : Math.Abs((c.ConnectedAt - started).TotalSeconds))
                .FirstOrDefault();
        }
    }

    public static async Task<string> EnsureRasRunningAsync(string racPath, RacConnectionOptions options, Action<string>? log = null, CancellationToken cancellationToken = default)
    {
        var ras = Path.Combine(Path.GetDirectoryName(racPath)!, "ras.exe");
        if (!File.Exists(ras)) throw new Exception("ras.exe не найден рядом с rac.exe");

        var timeout = Math.Max(30, options.TimeoutSeconds);
        var probeTimeout = Math.Min(30, timeout);

        foreach (var endpoint in EndpointCandidates(options.Endpoint))
        {
            try
            {
                var existing = await RunAsync(racPath, new[] { "cluster", "list", endpoint }, probeTimeout, cancellationToken);
                if (ParseRecords(existing).Count > 0)
                {
                    log?.Invoke($"RAS уже работает: {endpoint}");
                    return endpoint;
                }
            }
            catch { }
        }

        var port = options.Endpoint.Split(':').LastOrDefault() ?? "1545";
        var psi = new ProcessStartInfo(ras) { UseShellExecute = false, CreateNoWindow = true };
        psi.ArgumentList.Add("cluster");
        psi.ArgumentList.Add($"--port={port}");
        psi.ArgumentList.Add(options.AgentEndpoint);
        var rasProcess = Process.Start(psi) ?? throw new Exception("Не удалось запустить ras.exe");
        log?.Invoke($"RAS запускается: {ras} → {options.AgentEndpoint}, порт {port}…");

        var deadline = DateTime.UtcNow.AddSeconds(timeout);
        Exception? last = null;
        while (DateTime.UtcNow < deadline && !cancellationToken.IsCancellationRequested)
        {
            if (rasProcess.HasExited)
                throw new Exception($"ras.exe завершился с кодом {rasProcess.ExitCode}. Проверьте порт {port}, адрес агента {options.AgentEndpoint} и совпадение версии платформы.");

            await Task.Delay(2000, cancellationToken);
            foreach (var endpoint in EndpointCandidates(options.Endpoint))
            {
                try
                {
                    var output = await RunAsync(racPath, new[] { "cluster", "list", endpoint }, probeTimeout, cancellationToken);
                    if (ParseRecords(output).Count > 0)
                    {
                        log?.Invoke($"RAS готов: {endpoint} · агент {options.AgentEndpoint}");
                        return endpoint;
                    }
                }
                catch (Exception ex) { last = ex; }
            }
        }

        throw new Exception($"RAS не ответил за {timeout} с. RAC: {racPath}; RAS: {ras}; конечная точка: {options.Endpoint}; агент: {options.AgentEndpoint}. Последняя ошибка: {last?.Message}");
    }

    public static async Task TerminateSessionAsync(string racPath, string endpoint, string clusterId, string sessionUuid, string? clusterUser, string? clusterPassword, int timeoutSeconds = 15, CancellationToken cancellationToken = default)
    {
        var auth = new List<string> { $"--cluster={clusterId}" };
        if (!string.IsNullOrWhiteSpace(clusterUser))
        {
            auth.Add($"--cluster-user={clusterUser}");
            auth.Add($"--cluster-pwd={clusterPassword ?? ""}");
        }

        var variants = new List<List<string>>
        {
            new() { "session" },
            new() { "session", "terminate" },
            new() { endpoint, "session" }
        };
        variants[0].AddRange(auth); variants[0].Add("terminate"); variants[0].Add($"--session={sessionUuid}"); variants[0].Add(endpoint);
        variants[1].AddRange(auth); variants[1].Add($"--session={sessionUuid}"); variants[1].Add(endpoint);
        variants[2].AddRange(auth); variants[2].Add("terminate"); variants[2].Add($"--session={sessionUuid}");

        Exception? last = null;
        foreach (var args in variants)
        {
            try
            {
                await RunAsync(racPath, args, timeoutSeconds, cancellationToken);
                return;
            }
            catch (Exception ex) { last = ex; }
        }
        throw last ?? new Exception("Не удалось завершить сеанс через RAC");
    }

    public static async Task<string> RunAsync(string exe, IEnumerable<string> args, int timeoutSeconds = 15, CancellationToken cancellationToken = default)
    {
        var psi = new ProcessStartInfo(exe)
        {
            UseShellExecute = false,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            CreateNoWindow = true
        };
        foreach (var a in args) psi.ArgumentList.Add(a);
        using var p = Process.Start(psi) ?? throw new Exception($"Не удалось запустить {Path.GetFileName(exe)}");
        var stdout = ReadBytesAsync(p.StandardOutput.BaseStream, cancellationToken);
        var stderr = ReadBytesAsync(p.StandardError.BaseStream, cancellationToken);

        using var timeoutCts = new CancellationTokenSource(TimeSpan.FromSeconds(timeoutSeconds));
        using var linkedCts = CancellationTokenSource.CreateLinkedTokenSource(timeoutCts.Token, cancellationToken);
        try
        {
            await p.WaitForExitAsync(linkedCts.Token);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            try { p.Kill(true); } catch { }
            throw new Exception($"Превышено время ожидания {Path.GetFileName(exe)}: {timeoutSeconds} с");
        }

        var o = DecodeRac(await stdout);
        var e = DecodeRac(await stderr);
        if (p.ExitCode != 0) throw new Exception(string.IsNullOrWhiteSpace(e) ? $"RAC завершился с кодом {p.ExitCode}" : e.Trim());
        return o;
    }

    public static List<RacRecord> ParseRecords(string text)
    {
        var list = new List<RacRecord>();
        RacRecord? cur = null;
        foreach (var raw in text.Replace("\r", "").Split('\n'))
        {
            var line = raw.Trim();
            if (line.Length == 0)
            {
                if (cur is { Count: > 0 }) list.Add(cur);
                cur = null;
                continue;
            }
            var i = line.IndexOf(':');
            if (i < 1) continue;
            cur ??= new RacRecord();
            cur[line[..i].Trim()] = line[(i + 1)..].Trim();
        }
        if (cur is { Count: > 0 }) list.Add(cur);
        return list;
    }

    private static async Task<byte[]> ReadBytesAsync(Stream input, CancellationToken cancellationToken)
    {
        using var memory = new MemoryStream();
        await input.CopyToAsync(memory, cancellationToken);
        return memory.ToArray();
    }

    public static string DecodeRac(byte[] data)
    {
        if (data.Length == 0) return string.Empty;
        try { return new UTF8Encoding(false, true).GetString(data).TrimStart('\uFEFF'); }
        catch (DecoderFallbackException)
        {
            var cp866 = Encoding.GetEncoding(866, EncoderFallback.ReplacementFallback, DecoderFallback.ReplacementFallback).GetString(data);
            var cp1251 = Encoding.GetEncoding(1251, EncoderFallback.ReplacementFallback, DecoderFallback.ReplacementFallback).GetString(data);
            return RussianTextScore(cp1251) > RussianTextScore(cp866) ? cp1251 : cp866;
        }
    }

    private static int RussianTextScore(string text)
    {
        var score = 0;
        foreach (var c in text)
        {
            if ((c >= 'А' && c <= 'я') || c is 'Ё' or 'ё') score += 4;
            else if (c == '\uFFFD' || (char.IsControl(c) && c is not '\r' and not '\n' and not '\t')) score -= 20;
            else if (c >= '\u2500' && c <= '\u259F') score -= 8;
            else if (c >= '\u0080' && c <= '\u00BF') score -= 2;
        }
        foreach (var token in new[] { "ов", "ен", "ер", "на", "ни", "ра", "ко", "ст", "ть", "ий", "Польз", "Админ" })
            score += CountOccurrences(text, token) * 3;
        return score;
    }

    private static int CountOccurrences(string text, string token)
    {
        var count = 0;
        var at = 0;
        while ((at = text.IndexOf(token, at, StringComparison.OrdinalIgnoreCase)) >= 0) { count++; at += token.Length; }
        return count;
    }
}
