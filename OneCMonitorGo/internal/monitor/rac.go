package monitor

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func racTimeout(s Settings) time.Duration {
	return time.Duration(math.Max(15, math.Floor(s.RacTimeoutSeconds))) * time.Second
}

// clusterAuth возвращает параметры авторизации администратора кластера. Для администратора
// с пустым паролем RAC всё равно должен получить --cluster-pwd=.
func clusterAuth(s Settings) []string {
	if strings.TrimSpace(s.ClusterUser) == "" {
		return nil
	}
	return []string{"--cluster-user=" + s.ClusterUser, "--cluster-pwd=" + s.ClusterPassword}
}

func concat(parts ...[]string) []string {
	var result []string
	for _, part := range parts {
		result = append(result, part...)
	}
	return result
}

// runVariants пробует варианты командной строки по очереди: разные выпуски RAC принимают
// адрес RAS и параметры авторизации в разных позициях.
func (m *Monitor) runVariants(rac string, variants [][]string, timeout time.Duration) (string, error) {
	var last error
	for _, args := range variants {
		output, err := m.run(rac, args, timeout)
		if err == nil {
			return output, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("Команда RAC не выполнена")
	}
	return "", last
}

// pollRac опрашивает кластер через rac.exe. Выполняется в отдельной горутине.
func (m *Monitor) pollRac(scanTechLog bool) {
	defer func() {
		if r := recover(); r != nil {
			m.setRacStatus(fmt.Sprintf("Ошибка опроса RAC: %v", r))
			m.Log(fmt.Sprintf("Опрос RAC: %v", r))
		}
	}()
	s := m.Settings()
	m.mu.Lock()
	lastWorking := m.lastRacPath
	m.mu.Unlock()
	candidates := m.finder.candidates(m.sys, s.RacPath, lastWorking)
	var lastErr error
	for _, racPath := range candidates {
		for _, endpoint := range EndpointCandidates(s.Endpoint) {
			snapshot, err := m.readRac(racPath, endpoint, s)
			if err != nil {
				lastErr = err
				m.LogOnce("rac:"+racPath+":"+endpoint+":"+err.Error(), fmt.Sprintf("RAC %s, %s: %v", racPath, endpoint, err), 5*time.Minute)
				continue
			}
			m.applyTechLog(snapshot.Sessions, scanTechLog, s)
			version := m.sys.FileVersion(racPath)

			m.mu.Lock()
			// Найденные путь и адрес записываются в настройки, только если пользователь не успел
			// изменить эти поля за время опроса.
			changed := false
			if strings.EqualFold(m.settings.RacPath, s.RacPath) && m.settings.RacPath != racPath {
				changed = changed || !strings.EqualFold(m.settings.RacPath, racPath)
				m.settings.RacPath = racPath
			}
			if strings.EqualFold(m.settings.Endpoint, s.Endpoint) && m.settings.Endpoint != endpoint {
				changed = changed || !strings.EqualFold(m.settings.Endpoint, endpoint)
				m.settings.Endpoint = endpoint
			}
			if changed {
				m.view.SettingsVersion++
			}
			m.lastRacPath, m.lastRacVersion, m.cachedRac, m.cachedRacAt = racPath, version, snapshot, m.now()
			m.lastInfoBases = snapshot.InfoBases
			m.view.RacStatus = fmt.Sprintf("Подключено: %s • RAC %s • %s • сеансов %d • опрос раз в %s с", endpoint, version, ArchitectureLabel(racPath), len(snapshot.Sessions), num(s.RacPollSeconds, 0))
			toSave := m.settings.clone()
			m.mu.Unlock()
			if changed {
				m.trySave(toSave, "найденный путь RAC")
			}
			m.notify()
			return
		}
	}

	message := "нет данных"
	if lastErr != nil {
		message = lastErr.Error()
	}
	var unavailable string
	switch {
	case len(candidates) == 0:
		unavailable = "RAC/RAS не найдены. Запустите от администратора или укажите rac.exe вручную."
	case strings.Contains(strings.ToLower(message), "не аутентифицирован"):
		unavailable = "Требуется имя администратора кластера. Если пароль не задан — заполните только имя."
	default:
		unavailable = fmt.Sprintf("Проверено комплектов RAC/RAS: %d. Недоступны: %s", len(candidates), message)
	}
	m.mu.Lock()
	if m.cachedRac != nil {
		unavailable += " • показаны последние полученные данные"
	}
	m.view.RacStatus = unavailable
	m.mu.Unlock()
	m.notify()
}

// readRac читает состояние кластера: список кластеров, сеансы, базы, процессы и соединения.
func (m *Monitor) readRac(rac, endpoint string, s Settings) (*RacSnapshot, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = s.Endpoint
	}
	timeout := racTimeout(s)
	clusterOutput, err := m.run(rac, []string{"cluster", "list", endpoint}, timeout)
	if err != nil {
		if clusterOutput, err = m.run(rac, []string{endpoint, "cluster", "list"}, timeout); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	lastCluster := m.lastClusterID
	m.mu.Unlock()
	var clusterIDs []string
	seen := map[string]bool{}
	for _, record := range ParseRecords(clusterOutput) {
		if id := norm(record.Get("cluster")); id != "" && !seen[id] {
			seen[id] = true
			clusterIDs = append(clusterIDs, id)
		}
	}
	sort.SliceStable(clusterIDs, func(i, j int) bool { return clusterIDs[i] == lastCluster && clusterIDs[j] != lastCluster })
	if len(clusterIDs) == 0 {
		return nil, errors.New("Кластер не найден. Проверьте RAS, порт 1545 и версию RAC.")
	}

	runList := func(command []string, cluster string) (string, error) {
		auth := append([]string{"--cluster=" + cluster}, clusterAuth(s)...)
		return m.runVariants(rac, [][]string{
			concat(command, auth, []string{endpoint}),
			concat([]string{endpoint}, command, auth),
			concat(command[:1], auth, command[1:], []string{endpoint}),
		}, timeout)
	}

	// На одном ragent могут быть зарегистрированы несколько кластеров, и первый нередко пуст.
	// Проверяется каждый кластер и выбирается тот, где реально больше всего сеансов.
	cluster, sessionOutput, best := "", "", -1
	var clusterErr error
	for _, candidate := range clusterIDs {
		output, err := runList([]string{"session", "list"}, candidate)
		if err != nil {
			clusterErr = err
			m.LogOnce("rac-cluster-error:"+candidate+":"+err.Error(), fmt.Sprintf("RAC: кластер %s недоступен: %v", candidate, err), 5*time.Minute)
			continue
		}
		count := len(ParseRecords(output))
		m.LogOnce("rac-cluster:"+candidate, fmt.Sprintf("RAC: кластер %s, найдено сеансов: %d", candidate, count), 10*time.Minute)
		if count > best {
			best, cluster, sessionOutput = count, candidate, output
		}
		if count > 0 && candidate == lastCluster {
			break
		}
	}
	if cluster == "" {
		return nil, fmt.Errorf("Не удалось прочитать сеансы ни одного кластера: %v", clusterErr)
	}
	m.mu.Lock()
	m.lastClusterID = cluster
	m.mu.Unlock()

	tryList := func(command ...string) string {
		output, err := runList(command, cluster)
		if err != nil {
			m.LogOnce("rac-list:"+command[0]+":"+err.Error(), fmt.Sprintf("RAC: %s недоступна, продолжаем по session list: %v", strings.Join(command, " "), err), 5*time.Minute)
			return ""
		}
		return output
	}
	infoBases := tryList("infobase", "summary", "list")
	processes := tryList("process", "list")
	connections := tryList("connection", "list")
	result := BuildSnapshot(infoBases, processes, connections, sessionOutput)

	// Диагностический файл пишется не чаще раза в 10 минут: сеансы без PID — обычное явление.
	withoutPid := false
	for _, x := range result.Sessions {
		if x.Pid <= 0 {
			withoutPid = true
			break
		}
	}
	m.techMu.Lock()
	writeDiagnostic := (len(result.Sessions) == 0 || withoutPid) && time.Since(m.lastRacDiagnostic) >= 10*time.Minute
	if writeDiagnostic {
		m.lastRacDiagnostic = time.Now()
	}
	m.techMu.Unlock()
	if writeDiagnostic {
		text := fmt.Sprintf("Generated: %s\r\nRAC: %s\r\nEndpoint: %s\r\nClusters found: %s\r\nSelected cluster: %s\r\nParsed sessions: %d\r\n\r\n=== CLUSTER LIST ===\r\n%s\r\n=== INFOBASE LIST ===\r\n%s\r\n=== PROCESS LIST ===\r\n%s\r\n=== CONNECTION LIST ===\r\n%s\r\n=== SESSION LIST ===\r\n%s",
			time.Now().Format(time.RFC3339Nano), rac, endpoint, strings.Join(clusterIDs, ", "), cluster, len(result.Sessions), clusterOutput, infoBases, processes, connections, sessionOutput)
		if file, err := m.writeDiagnostic("last-rac-diagnostic.txt", text); err == nil && len(result.Sessions) == 0 {
			m.LogOnce("rac-empty", "RAC подключён, но session list пуст. Диагностика: "+file, 30*time.Minute)
		}
	}
	return result, nil
}

// applyTechLog дополняет сеансы контекстом из технологического журнала. Файлы читаются только при
// rescan; между сканированиями новым снимкам RAC подставляется сохранённый контекст — иначе
// колонка пустела бы на каждом опросе без сканирования.
func (m *Monitor) applyTechLog(sessions []SessionRow, rescan bool, s Settings) {
	for _, message := range m.applyTechLogLocked(sessions, rescan, s) {
		m.LogOnce(message.key, message.text, message.repeat)
	}
}

type onceMessage struct {
	key, text string
	repeat    time.Duration
}

func (m *Monitor) applyTechLogLocked(sessions []SessionRow, rescan bool, s Settings) (messages []onceMessage) {
	m.techMu.Lock()
	defer m.techMu.Unlock()
	path := strings.TrimSpace(s.TechLogPath)
	if path == "" {
		m.techContexts = map[string]techContext{}
		return nil
	}
	if rescan && len(sessions) > 0 {
		if !dirExists(path) {
			messages = append(messages, onceMessage{"techlog-dir", "Технологический журнал: каталог не найден: " + path, 30 * time.Minute})
		} else {
			// Файлы техжурнала сменяются каждый час (ггММддчч.log), поэтому список обновляется и при смене часа.
			if time.Since(m.techFilesAt) >= 10*time.Minute || len(m.techFiles) == 0 || m.techFilesHour != time.Now().Hour() {
				files, err := newestLogFiles(path, 8)
				if err != nil {
					messages = append(messages, onceMessage{"techlog:" + err.Error(), "Технологический журнал: " + err.Error(), 5 * time.Minute})
				}
				m.techFiles, m.techFilesAt, m.techFilesHour = files, time.Now(), time.Now().Hour()
			}
			index := NewTechLogIndex()
			for _, file := range m.techFiles {
				events, err := ReadTechLogTail(file, 131072)
				if err != nil {
					continue // файл удалён или переименован ротацией журнала
				}
				for _, event := range events {
					index.Add(event)
				}
			}
			m.techContexts = map[string]techContext{}
			for _, session := range sessions {
				if strings.TrimSpace(session.SessionUUID) == "" {
					continue
				}
				user := session.UserName
				if user == UnknownUser {
					user = ""
				}
				if found, ok := index.Find(session.InfoBase, session.SessionNumber, user); ok {
					m.techContexts[session.SessionUUID] = techContext{found.Context, found.DurationMs}
				}
			}
		}
	}
	for i := range sessions {
		if cached, ok := m.techContexts[sessions[i].SessionUUID]; ok {
			sessions[i].TechContext, sessions[i].LastCallMs = cached.Context, cached.LastCallMs
		}
	}
	return messages
}

// newestLogFiles возвращает limit самых свежих файлов *.log в каталоге и его подкаталогах.
func newestLogFiles(root string, limit int) ([]string, error) {
	type entry struct {
		path     string
		modified time.Time
	}
	var entries []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // недоступный подкаталог пропускается
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".log") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			entries = append(entries, entry{path, info.ModTime()})
		}
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].modified.After(entries[j].modified) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	files := make([]string, len(entries))
	for i, e := range entries {
		files[i] = e.path
	}
	return files, err
}

// StartRas запускает ras.exe рядом с найденным rac.exe и ждёт, пока он ответит.
// Второй экземпляр не запускается, если RAS уже отвечает.
func (m *Monitor) StartRas() {
	if err := m.startRas(); err != nil {
		m.setRacStatus("Ошибка запуска RAS: " + err.Error())
		m.Log("Ошибка запуска RAS: " + err.Error())
	}
}

func (m *Monitor) findRac() string {
	s := m.Settings()
	m.mu.Lock()
	last := m.lastRacPath
	m.mu.Unlock()
	if found := m.finder.candidates(m.sys, s.RacPath, last); len(found) > 0 {
		return found[0]
	}
	m.finder.invalidate()
	if found := m.finder.candidates(m.sys, s.RacPath, last); len(found) > 0 {
		return found[0]
	}
	return ""
}

func (m *Monitor) startRas() error {
	rac := m.findRac()
	if rac == "" {
		return errors.New("Сначала укажите путь к rac.exe")
	}
	ras := filepath.Join(filepath.Dir(rac), "ras.exe")
	if !fileExists(ras) {
		return errors.New("ras.exe не найден рядом с rac.exe")
	}
	s := m.Settings()
	timeout := time.Duration(math.Max(30, math.Floor(s.RacTimeoutSeconds))) * time.Second
	probeTimeout := timeout
	if probeTimeout > 30*time.Second {
		probeTimeout = 30 * time.Second
	}
	probe := func() (string, error) {
		var last error
		for _, endpoint := range EndpointCandidates(s.Endpoint) {
			output, err := m.run(rac, []string{"cluster", "list", endpoint}, probeTimeout)
			if err != nil {
				last = err
				continue
			}
			if len(ParseRecords(output)) > 0 {
				return endpoint, nil
			}
		}
		return "", last
	}
	ready := func(endpoint, status string) {
		m.mu.Lock()
		if m.settings.Endpoint != endpoint {
			m.settings.Endpoint = endpoint
			m.view.SettingsVersion++
		}
		m.view.RacStatus = status
		m.mu.Unlock()
		m.Log(status)
		m.Refresh(true)
	}

	// Не запускаем второй RAS, если он уже отвечает через localhost или IPv4.
	if endpoint, _ := probe(); endpoint != "" {
		ready(endpoint, "RAS уже работает: "+endpoint)
		return nil
	}

	// Порт берётся только из корректного «хост:порт»; иначе используется стандартный 1545.
	port := "1545"
	if parts := strings.Split(strings.TrimSpace(s.Endpoint), ":"); len(parts) > 1 {
		if n := toInt(parts[len(parts)-1]); n > 0 && n < 65536 && itoa(n) == parts[len(parts)-1] {
			port = itoa(n)
		}
	}
	agent := strings.TrimSpace(s.AgentEndpoint)
	if agent == "" {
		agent = "localhost:1540"
	}
	exited, err := startDetached(ras, []string{"cluster", "--port=" + port, agent})
	if err != nil {
		return fmt.Errorf("Не удалось запустить ras.exe: %v", err)
	}
	status := fmt.Sprintf("RAS запускается: %s → %s, порт %s…", ras, agent, port)
	m.setRacStatus(status)
	m.Log(status)

	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		select {
		case code := <-exited:
			return fmt.Errorf("ras.exe завершился с кодом %d. Проверьте порт %s, адрес агента %s и совпадение версии платформы.", code, port, agent)
		case <-m.stop:
			return errors.New("программа закрывается")
		case <-time.After(m.rasProbeInterval):
		}
		endpoint, err := probe()
		if endpoint != "" {
			ready(endpoint, fmt.Sprintf("RAS готов: %s · агент %s", endpoint, agent))
			return nil
		}
		if err != nil {
			last = err
		}
		m.setRacStatus(fmt.Sprintf("Ожидание RAS… осталось %.0f с", math.Max(0, time.Until(deadline).Seconds())))
	}
	return fmt.Errorf("RAS не ответил за %.0f с. RAC: %s; RAS: %s; конечная точка: %s; агент: %s. Последняя ошибка: %v", timeout.Seconds(), rac, ras, s.Endpoint, agent, last)
}

// startDetached подменяется в тестах.
var startDetached = startDetachedProcess
