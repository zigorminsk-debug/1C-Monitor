package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Контрольные фразы опасных операций.
const (
	ConfirmPhrase     = "ПОДТВЕРЖДАЮ"
	ConfirmKillPhrase = "УБИТЬ RPHOST"
)

// SetAdminConfirmation передаёт монитору текст, введённый в поле контрольной фразы.
func (m *Monitor) SetAdminConfirmation(text string) {
	m.mu.Lock()
	m.adminConfirmation = text
	m.mu.Unlock()
}

func (m *Monitor) clearConfirmation() {
	m.mu.Lock()
	m.adminConfirmation = ""
	m.view.ConfirmationResets++
	m.mu.Unlock()
	m.notify()
}

func (m *Monitor) checkConfirmation(killRphost bool) bool {
	expected := ConfirmPhrase
	if killRphost {
		expected = ConfirmKillPhrase
	}
	m.mu.Lock()
	entered := strings.TrimSpace(m.adminConfirmation)
	m.mu.Unlock()
	if !strings.EqualFold(entered, expected) {
		m.setAdminStatus("Введите контрольную фразу: " + expected)
		return false
	}
	return true
}

// admin выполняет опасную операцию; такие операции идут строго по одной.
func (m *Monitor) admin(action func()) {
	m.mu.Lock()
	if m.adminBusy {
		m.view.AdminActionStatus = "Дождитесь завершения предыдущей административной операции"
		m.mu.Unlock()
		m.notify()
		return
	}
	m.adminBusy, m.view.AdminBusy = true, true
	m.mu.Unlock()
	m.notify()
	defer func() {
		r := recover()
		m.mu.Lock()
		m.adminBusy, m.view.AdminBusy = false, false
		if r != nil {
			m.view.AdminActionStatus = fmt.Sprintf("Ошибка операции: %v", r)
		}
		m.mu.Unlock()
		if r != nil {
			m.Log(fmt.Sprintf("Ошибка административной операции: %v", r))
		}
		m.notify()
	}()
	action()
}

func (m *Monitor) racConnection() (rac, cluster string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRacPath, m.lastClusterID
}

func isConfigurator(s SessionRow) bool {
	app := strings.ToLower(s.AppID)
	return app == "designer" || strings.Contains(app, "configurator") || strings.Contains(app, "конфигуратор")
}

func isBackgroundSession(s SessionRow) bool {
	app := strings.ToLower(s.AppID)
	return strings.Contains(app, "background") || strings.Contains(app, "фонов")
}

// Служебные сеансы (фоновые задания, COM/веб-сервисы, консоль кластера) дубликатами не считаются:
// несколько таких сеансов одного пользователя с одного компьютера — штатная работа.
func isServiceSession(s SessionRow) bool {
	app := strings.ToLower(s.AppID)
	return isBackgroundSession(s) || strings.Contains(app, "connection") || strings.Contains(app, "console") || strings.Contains(app, "jobscheduler") || strings.Contains(app, "debugger")
}

// DuplicateSessions возвращает более ранние сеансы из групп с одинаковыми базой, пользователем,
// компьютером и приложением; самый новый сеанс группы остаётся.
func DuplicateSessions(sessions []SessionRow) []SessionRow {
	groups := map[string][]SessionRow{}
	var order []string
	for _, s := range sessions {
		if isServiceSession(s) {
			continue
		}
		key := strings.ToLower(s.InfoBase + "|" + s.UserName + "|" + s.Host + "|" + s.AppID)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], s)
	}
	var result []SessionRow
	for _, key := range order {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool { return parseRacDate(group[i].StartedAt).After(parseRacDate(group[j].StartedAt)) })
		result = append(result, group[1:]...)
	}
	return result
}

func filterSessions(sessions []SessionRow, keep func(SessionRow) bool) []SessionRow {
	var result []SessionRow
	for _, s := range sessions {
		if keep(s) {
			result = append(result, s)
		}
	}
	return result
}

// TerminateAllSessions завершает все сеансы кластера.
func (m *Monitor) TerminateAllSessions() {
	m.admin(func() { m.terminateSessions(m.View().Sessions, "всех сеансов") })
}

// TerminateDuplicateSessions завершает ранние дубликаты сеансов.
func (m *Monitor) TerminateDuplicateSessions() {
	m.admin(func() {
		m.terminateSessions(DuplicateSessions(m.View().Sessions), "дублирующихся ранних сеансов")
	})
}

// TerminateSleepingSessions завершает спящие сеансы.
func (m *Monitor) TerminateSleepingSessions() {
	m.admin(func() {
		m.terminateSessions(filterSessions(m.View().Sessions, func(s SessionRow) bool { return s.IsSleeping }), "спящих сеансов")
	})
}

// TerminateExceptConfigurator завершает все сеансы, кроме Конфигуратора.
func (m *Monitor) TerminateExceptConfigurator() {
	m.admin(func() {
		m.terminateSessions(filterSessions(m.View().Sessions, func(s SessionRow) bool { return !isConfigurator(s) }), "всех сеансов, кроме Конфигуратора")
	})
}

func (m *Monitor) terminateSessions(targets []SessionRow, description string) {
	if !m.checkConfirmation(false) {
		return
	}
	rac, cluster := m.racConnection()
	if rac == "" || cluster == "" {
		m.setAdminStatus("Нет подключения RAC")
		return
	}
	if len(targets) == 0 {
		m.setAdminStatus("Подходящие сеансы не найдены")
		return
	}
	targets = filterSessions(targets, func(s SessionRow) bool { return strings.TrimSpace(s.SessionUUID) != "" })
	s := m.Settings()
	ok, failed := 0, 0
	m.setAdminStatus(fmt.Sprintf("Завершение: %d…", len(targets)))
	for _, session := range targets {
		m.setAdminStatus(fmt.Sprintf("Завершается сеанс %s (%d/%d)…", session.UserName, ok+failed+1, len(targets)))
		auth := append([]string{"--cluster=" + cluster}, clusterAuth(s)...)
		target := []string{"--session=" + session.SessionUUID}
		_, err := m.runVariants(rac, [][]string{
			concat([]string{"session"}, auth, []string{"terminate"}, target, []string{s.Endpoint}),
			concat([]string{"session", "terminate"}, auth, target, []string{s.Endpoint}),
			concat([]string{s.Endpoint, "session"}, auth, []string{"terminate"}, target),
		}, 15*time.Second)
		if err == nil {
			ok++
			m.Log(fmt.Sprintf("Сеанс завершён: %s, UUID %s", session.UserName, session.SessionUUID))
		} else {
			failed++
			m.Log(fmt.Sprintf("Не завершён сеанс %s: %v", session.UserName, err))
		}
	}
	time.Sleep(m.settleDelay)
	m.clearConfirmation()
	status := fmt.Sprintf("Завершение %s: успешно %d, ошибок %d. Подробности во вкладке «Журнал».", description, ok, failed)
	m.setAdminStatus(status)
	m.Log(status)
	m.Refresh(true)
}

// DisableBackgroundJobs запрещает регламентные задания во всех базах и завершает работающие фоновые сеансы.
func (m *Monitor) DisableBackgroundJobs() { m.admin(func() { m.setBackgroundJobs(false) }) }

// EnableBackgroundJobs снимает запрет с баз, которые были выключены этим монитором.
func (m *Monitor) EnableBackgroundJobs() { m.admin(func() { m.setBackgroundJobs(true) }) }

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func (m *Monitor) setBackgroundJobs(enable bool) {
	if !m.checkConfirmation(false) {
		return
	}
	rac, cluster := m.racConnection()
	if rac == "" || cluster == "" {
		m.setAdminStatus("Нет подключения RAC")
		return
	}
	m.mu.Lock()
	infoBases := make([]InfoBaseRef, 0, len(m.lastInfoBases))
	for _, ib := range m.lastInfoBases {
		infoBases = append(infoBases, ib)
	}
	m.mu.Unlock()
	if len(infoBases) == 0 {
		m.setAdminStatus("Информационные базы не найдены через RAC")
		return
	}
	sort.Slice(infoBases, func(i, j int) bool { return strings.ToLower(infoBases[i].Name) < strings.ToLower(infoBases[j].Name) })

	s := m.Settings()
	tracked := s.BackgroundJobsDisabledInfobases
	targets := infoBases
	if enable {
		targets = nil
		for _, ib := range infoBases {
			if containsFold(tracked, ib.UUID) {
				targets = append(targets, ib)
			}
		}
	}
	if len(targets) == 0 {
		if enable {
			m.setAdminStatus("Нет баз, фоновые задания которых выключались этим монитором")
		} else {
			m.setAdminStatus("Во всех найденных базах фоновые задания уже запрещены")
		}
		return
	}

	timeout := racTimeout(s)
	base := func(ib InfoBaseRef) []string {
		return []string{"--cluster=" + cluster, "--infobase=" + ib.UUID}
	}
	alreadyDenied := func(ib InfoBaseRef) bool {
		options := append(base(ib), clusterAuth(s)...)
		for _, args := range [][]string{
			concat([]string{"infobase", "info"}, options, []string{s.Endpoint}),
			concat([]string{s.Endpoint, "infobase", "info"}, options),
			concat([]string{"infobase"}, options, []string{"info", s.Endpoint}),
		} {
			if output, err := m.run(rac, args, timeout); err == nil {
				if records := ParseRecords(output); len(records) > 0 {
					return isTrue(records[0].Get("scheduled-jobs-deny"))
				}
			}
		}
		return ib.ScheduledJobsDenied
	}

	ok, failed, skipped := 0, 0, 0
	state, verb, done := "on", "Выключение", "запрещены"
	if enable {
		state, verb, done = "off", "Включение", "разрешены"
	}
	for _, ib := range targets {
		if !enable && alreadyDenied(ib) {
			skipped++
			m.Log("Фоновые задания уже были запрещены до операции, база не будет добавлена в список обратного включения: " + ib.Name)
			continue
		}
		m.setAdminStatus(fmt.Sprintf("%s фоновых заданий: %s (%d/%d)…", verb, ib.Name, ok+failed+skipped+1, len(targets)))
		auth := concat(base(ib), []string{"--scheduled-jobs-deny=" + state}, clusterAuth(s))
		_, err := m.runVariants(rac, [][]string{
			concat([]string{"infobase", "update"}, auth, []string{s.Endpoint}),
			concat([]string{s.Endpoint, "infobase", "update"}, auth),
			concat([]string{"infobase"}, auth, []string{"update", s.Endpoint}),
		}, timeout)
		if err != nil {
			failed++
			m.Log(fmt.Sprintf("Не изменён режим фоновых заданий %s: %v", ib.Name, err))
			continue
		}
		ok++
		if enable {
			var rest []string
			for _, id := range tracked {
				if !strings.EqualFold(id, ib.UUID) {
					rest = append(rest, id)
				}
			}
			tracked = rest
		} else if !containsFold(tracked, ib.UUID) {
			tracked = append(tracked, ib.UUID)
		}
		m.Log(fmt.Sprintf("Фоновые задания %s: %s (%s)", done, ib.Name, ib.UUID))
	}
	m.mu.Lock()
	m.settings.BackgroundJobsDisabledInfobases = append([]string(nil), tracked...)
	toSave := m.settings.clone()
	m.mu.Unlock()
	m.trySave(toSave, "список баз с выключенными фоновыми заданиями")

	if !enable && ok > 0 {
		if running := filterSessions(m.View().Sessions, isBackgroundSession); len(running) > 0 {
			m.setAdminStatus(fmt.Sprintf("Новые фоновые задания запрещены в %d базах. Завершение %d уже работающих фоновых сеансов…", ok, len(running)))
			m.terminateSessions(running, "уже работающих фоновых заданий")
			return
		}
	}
	m.clearConfirmation()
	result := "выключены"
	if enable {
		result = "включены"
	}
	status := fmt.Sprintf("Фоновые задания %s: баз успешно %d, уже были запрещены %d, ошибок %d.", result, ok, skipped, failed)
	m.setAdminStatus(status)
	m.Log(status)
	m.Refresh(true)
}

const restartServicesScript = "$s=Get-CimInstance Win32_Service | Where-Object {$_.PathName -match 'ragent.exe'}; if(-not $s){throw 'Службы ragent.exe не найдены'}; $s | ForEach-Object { Restart-Service -Name $_.Name -Force -ErrorAction Stop; $_.Name }"

// RestartOneCServices перезапускает все службы Windows, запускающие ragent.exe.
func (m *Monitor) RestartOneCServices() {
	m.admin(func() {
		if !m.checkConfirmation(false) {
			return
		}
		defer m.clearConfirmation()
		m.setAdminStatus("Перезапуск всех служб сервера 1С…")
		output, err := m.run("powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", restartServicesScript}, 180*time.Second)
		status := ""
		if err != nil {
			status = "Ошибка перезапуска служб: " + err.Error()
		} else {
			status = "Перезапущены службы: " + strings.Trim(strings.ReplaceAll(strings.ReplaceAll(output, "\r", " "), "\n", ", "), " ,")
		}
		m.setAdminStatus(status)
		m.Log(status)
	})
}

// KillAllRphost принудительно завершает все процессы rphost (taskkill /F /IM rphost.exe).
func (m *Monitor) KillAllRphost() {
	m.admin(func() {
		if !m.checkConfirmation(true) {
			return
		}
		defer m.clearConfirmation()
		m.setAdminStatus("Принудительное завершение всех rphost…")
		status := "Все процессы rphost принудительно завершены"
		if _, err := m.run("taskkill.exe", []string{"/F", "/IM", "rphost.exe"}, 15*time.Second); err != nil {
			status = "Ошибка завершения rphost: " + err.Error()
		}
		m.setAdminStatus(status)
		m.Log(status)
	})
}

// GracefulTurnOffSelected плавно выводит из работы процесс, выбранный в таблице.
func (m *Monitor) GracefulTurnOffSelected() {
	m.admin(func() {
		if !m.checkConfirmation(false) {
			return
		}
		view := m.View()
		selected := m.SelectedProcess()
		var process *ProcessRow
		for i := range view.Processes {
			if view.Processes[i].Pid == selected && selected != 0 {
				process = &view.Processes[i]
			}
		}
		if process == nil {
			m.setAdminStatus("Сначала выберите процесс rphost в таблице на вкладке «Обзор»")
			return
		}
		if len(view.Processes) <= int(m.Settings().AutoDrainMinProcesses) {
			m.setAdminStatus("Операция отменена: после вывода останется слишком мало процессов")
			return
		}
		m.turnOffProcess(*process, false)
		m.clearConfirmation()
	})
}

// turnOffProcess выполняет «rac process turn-off»: кластер перестаёт назначать процессу новую
// нагрузку, текущие вызовы не прерываются.
func (m *Monitor) turnOffProcess(process ProcessRow, automatic bool) bool {
	rac, cluster := m.racConnection()
	if rac == "" || cluster == "" || strings.TrimSpace(process.ProcessUUID) == "" {
		m.setAdminStatus("Невозможно вывести процесс: отсутствует UUID процесса или подключение RAC")
		return false
	}
	s := m.Settings()
	auth := append([]string{"--cluster=" + cluster}, clusterAuth(s)...)
	target := []string{"--process=" + process.ProcessUUID}
	_, err := m.runVariants(rac, [][]string{
		concat([]string{"process"}, auth, []string{"turn-off"}, target, []string{s.Endpoint}),
		concat([]string{"process", "turn-off"}, auth, target, []string{s.Endpoint}),
		concat([]string{s.Endpoint, "process"}, auth, []string{"turn-off"}, target),
	}, 15*time.Second)
	var message string
	if err == nil {
		how := "Вручную"
		if automatic {
			how = "Автоматически"
		}
		message = fmt.Sprintf("%s запущен плавный вывод PID %d, память %s. Новая нагрузка будет направляться в другие rphost; принудительное завершение не выполняется.", how, process.Pid, process.MemoryText())
	} else {
		message = fmt.Sprintf("Не удалось выполнить process turn-off для PID %d: %v", process.Pid, err)
	}
	m.mu.Lock()
	if err == nil {
		m.draining[process.Pid] = drainState{process.Pid, process.ProcessUUID, time.Now(), process.MemoryMb}
	}
	m.view.AutoDrainStatus, m.view.AdminActionStatus = message, message
	m.mu.Unlock()
	m.Log(message)
	return err == nil
}

// evaluateGracefulDrain обновляет состояние плавного вывода и возвращает индекс процесса, который
// пора вывести автоматически, либо -1.
func (m *Monitor) evaluateGracefulDrain(procs []ProcessRow, sessions []SessionRow, now time.Time, s Settings) int {
	var finished []int
	m.mu.Lock()
	candidate := m.evaluateDrainLocked(procs, sessions, now, s, &finished)
	m.mu.Unlock()
	for _, pid := range finished {
		m.Log(fmt.Sprintf("Плавный вывод PID %d завершён: процесс исчез из кластера", pid))
	}
	return candidate
}

func (m *Monitor) evaluateDrainLocked(procs []ProcessRow, sessions []SessionRow, now time.Time, s Settings, finished *[]int) int {
	present := map[int]bool{}
	for _, p := range procs {
		present[p.Pid] = true
	}
	for pid := range m.draining {
		if !present[pid] {
			*finished = append(*finished, pid)
			delete(m.draining, pid)
		}
	}
	sort.Ints(*finished)
	for i := range procs {
		if drain, ok := m.draining[procs[i].Pid]; ok {
			procs[i].DrainStatus = "Выводится с " + drain.StartedAt.Format("15:04:05")
		}
	}
	status := func(text string) int {
		m.view.AutoDrainStatus = text
		return -1
	}
	if !s.AutoDrainEnabled {
		if len(m.draining) > 0 {
			return status(fmt.Sprintf("Выводятся процессов: %d", len(m.draining)))
		}
		return status("Автовывод rphost выключен")
	}
	if m.lastRacPath == "" || m.lastClusterID == "" {
		return status("Автовывод: нет подключения RAC")
	}
	for _, p := range procs {
		if p.MemoryMb >= s.AutoDrainMemoryMb {
			if _, ok := m.memoryOverSince[p.Pid]; !ok {
				m.memoryOverSince[p.Pid] = now
			}
		} else {
			delete(m.memoryOverSince, p.Pid)
		}
	}
	for pid := range m.memoryOverSince {
		if !present[pid] {
			delete(m.memoryOverSince, pid)
		}
	}
	if len(m.draining) > 0 {
		var pids []int
		for pid := range m.draining {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		texts := make([]string, len(pids))
		for i, pid := range pids {
			texts[i] = itoa(pid)
		}
		return status("Плавно выводится PID " + strings.Join(texts, ", ") + "; принудительное завершение не применяется")
	}
	if now.Sub(m.lastAutomaticDrain).Minutes() < s.AutoDrainCooldownMinutes {
		return status("Автовывод: пауза после предыдущей операции до " + m.lastAutomaticDrain.Add(time.Duration(s.AutoDrainCooldownMinutes*float64(time.Minute))).Format("15:04"))
	}
	if len(procs) <= int(s.AutoDrainMinProcesses) {
		return status(fmt.Sprintf("Автовывод запрещён: процессов %d, минимум должно остаться %s", len(procs), num(s.AutoDrainMinProcesses, 0)))
	}
	candidate := -1
	for i, p := range procs {
		since, over := m.memoryOverSince[p.Pid]
		if strings.TrimSpace(p.ProcessUUID) == "" || !over || now.Sub(since).Minutes() < s.AutoDrainMinutes {
			continue
		}
		if candidate < 0 || p.MemoryMb > procs[candidate].MemoryMb {
			candidate = i
		}
	}
	if candidate < 0 {
		if len(procs) == 0 {
			return status("Автовывод: процессы не найдены")
		}
		largest := procs[0]
		for _, p := range procs {
			if p.MemoryMb > largest.MemoryMb {
				largest = p
			}
		}
		return status(fmt.Sprintf("Автовывод активен · максимум PID %d: %s · порог %s МБ / %s мин", largest.Pid, largest.MemoryText(), num(s.AutoDrainMemoryMb, 0), num(s.AutoDrainMinutes, 0)))
	}
	active := 0
	for _, x := range sessions {
		if x.Pid == procs[candidate].Pid && (x.DurationCurrentMs > 0 || x.IsBlocked()) {
			active++
		}
	}
	if active > 0 {
		procs[candidate].DrainStatus = fmt.Sprintf("Ожидание %d активных вызовов", active)
		return status(fmt.Sprintf("PID %d превысил порог, ожидается завершение активных вызовов", procs[candidate].Pid))
	}
	return candidate
}

// SaveSettingsAndCheck записывает настройки и немедленно проверяет подключение
// (ручное обновление опрашивает RAC всегда, а поиск rac.exe выполняется заново).
func (m *Monitor) SaveSettingsAndCheck() {
	m.trySave(m.Settings(), "кнопка «Сохранить и проверить»")
	m.finder.invalidate()
	m.Refresh(true)
}

// SaveSettings записывает настройки на диск (вызывается при закрытии программы).
func (m *Monitor) SaveSettings() { m.trySave(m.Settings(), "закрытие программы") }

// DataDir возвращает каталог настроек, истории и диагностики.
func (m *Monitor) DataDir() string { return m.dataDir }
