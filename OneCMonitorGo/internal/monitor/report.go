package monitor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func (m *Monitor) accumulateDailyStatsLocked(at time.Time, procs []ProcessRow, sessions []SessionRow, disk DiskSnapshot, network NetworkSnapshot) {
	cpu, ram := 0.0, 0.0
	for _, p := range procs {
		cpu += p.Cpu
		ram += p.MemoryMb
	}
	active, sleeping := 0, 0
	for _, s := range sessions {
		if s.IsSleeping {
			sleeping++
		} else {
			active++
		}
	}
	m.serverHistory = append(m.serverHistory, serverHistoryRow{at, cpu, ram, disk.UsagePercent, disk.ReadBytesPerSec, disk.WriteBytesPerSec, disk.QueueLength,
		network.ReceiveBytesPerSec, network.SendBytesPerSec, network.UtilizationPercent, len(sessions), active, sleeping})
	if len(m.serverHistory) > 30000 {
		m.serverHistory = append([]serverHistoryRow(nil), m.serverHistory[len(m.serverHistory)-30000:]...)
	}
	day := dateOf(at)
	for _, s := range sessions {
		key := at.Format("20060102") + "|" + s.InfoBase + "|" + s.UserName
		stat, ok := m.dailyUsers[key]
		if !ok {
			stat = &dailyUserStat{Date: day, UserName: s.UserName, InfoBase: s.InfoBase}
			m.dailyUsers[key] = stat
		}
		stat.Samples++
		if !containsFold(stat.Hosts, s.Host) {
			stat.Hosts = append(stat.Hosts, s.Host)
		}
		if s.Pid > 0 {
			known := false
			for _, pid := range stat.Pids {
				known = known || pid == s.Pid
			}
			if !known {
				stat.Pids = append(stat.Pids, s.Pid)
			}
		}
		stat.MaxProcessCpu = math.Max(stat.MaxProcessCpu, s.ProcessCpu)
		stat.MaxProcessMemory = math.Max(stat.MaxProcessMemory, s.ProcessMemoryMb)
		stat.MaxCpu5Min = math.Max(stat.MaxCpu5Min, s.CpuTimeLast5Min)
		stat.MaxDuration5Min = math.Max(stat.MaxDuration5Min, s.DurationLast5MinMs)
		stat.MaxDbDuration5Min = math.Max(stat.MaxDbDuration5Min, s.DurationDbLast5MinMs)
		if s.CallsLast5Min > stat.MaxCalls5Min {
			stat.MaxCalls5Min = s.CallsLast5Min
		}
		stat.MaxMemory5MinMb = math.Max(stat.MaxMemory5MinMb, math.Abs(s.MemoryLast5MinMb))
		if strings.TrimSpace(s.TechContext) != "" {
			stat.LastContext = s.TechContext
		} else if strings.TrimSpace(s.Operation) != "" {
			stat.LastContext = s.Operation
		}
	}
	oldest := day.AddDate(0, 0, -2)
	for key, stat := range m.dailyUsers {
		if stat.Date.Before(oldest) {
			delete(m.dailyUsers, key)
		}
	}
}

func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// appendDailyHistory дописывает измерение в CSV-файл истории за текущие сутки.
func (m *Monitor) appendDailyHistory(rows []ProcessRow, disk DiskSnapshot, network NetworkSnapshot, at time.Time) {
	dir := filepath.Join(m.dataDir, "history")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	file := filepath.Join(dir, "history-"+at.Format("2006-01-02")+".csv")
	var b strings.Builder
	if !fileExists(file) {
		b.Write(utf8BOM)
		b.WriteString("Время;PID;CPU %;RAM МБ;Сеансы;Пользователи;Риск;Диагноз;Диск %;Чтение байт/с;Запись байт/с;Очередь диска;Сеть приём байт/с;Сеть отправка байт/с;Сеть %\r\n")
	}
	f2 := func(v float64) string { return numInvariant(v, 2) }
	for _, p := range rows {
		fmt.Fprintf(&b, "%s;%d;%s;%s;%d;%s;%d;%s;%s;%s;%s;%s;%s;%s;%s\r\n", at.Format("02.01.2006 15:04:05"), p.Pid, numInvariant(p.Cpu, 1), numInvariant(p.MemoryMb, 1),
			p.SessionCount, csvQuote(p.Users), p.Risk, csvQuote(p.Diagnosis), f2(disk.UsagePercent), f2(disk.ReadBytesPerSec), f2(disk.WriteBytesPerSec), f2(disk.QueueLength),
			f2(network.ReceiveBytesPerSec), f2(network.SendBytesPerSec), f2(network.UtilizationPercent))
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(b.String())
}

func (m *Monitor) checkAutomaticDailyReport(now time.Time) {
	m.mu.Lock()
	if !m.settings.AutoDailyReport {
		m.mu.Unlock()
		return
	}
	var lastReported time.Time
	if t, err := time.ParseInLocation("2006-01-02", m.settings.LastAutoReportDate, now.Location()); err == nil {
		lastReported = t
	}
	// После 23:55 формируется отчёт за текущий день. Если в это время мониторинг стоял на паузе
	// или цикл был занят, отчёт за вчерашний день создаётся при первом измерении нового дня.
	target := dateOf(now)
	if now.Hour() < 23 || now.Minute() < 55 {
		target = target.AddDate(0, 0, -1)
	}
	if !lastReported.Before(target) {
		m.mu.Unlock()
		return
	}
	m.settings.LastAutoReportDate = target.Format("2006-01-02")
	hasData := sameDay(target, now)
	for _, row := range m.serverHistory {
		if hasData {
			break
		}
		hasData = sameDay(row.At, target)
	}
	m.mu.Unlock()
	if !hasData {
		return // за вчера данных нет — отчёт не нужен
	}
	m.generateDailyReport(target, true)
	m.trySave(m.Settings(), "дата автоотчёта")
}

// GenerateDailyReport формирует суточный отчёт за сегодня и открывает его.
func (m *Monitor) GenerateDailyReport() { m.generateDailyReport(m.now(), false) }

type hourStat struct {
	hour, count                                      int
	sumCpu, maxCpu, sumRam, maxRam, sumDisk, maxDisk float64
	maxRead, maxWrite, maxQueue, maxNetIn, maxNetOut float64
	maxSessions                                      int
}

func (m *Monitor) generateDailyReport(date time.Time, automatic bool) {
	m.mu.Lock()
	var points []serverHistoryRow
	for _, row := range m.serverHistory {
		if sameDay(row.At, date) {
			points = append(points, row)
		}
	}
	var users []dailyUserStat
	for _, stat := range m.dailyUsers {
		if sameDay(stat.Date, date) {
			users = append(users, *stat)
		}
	}
	s := m.settings.clone()
	view := m.view
	racPath, racVersion := m.lastRacPath, m.lastRacVersion
	virtualization, physicalDisks := m.virtualizationInfo, m.physicalDiskInfo
	m.mu.Unlock()

	if len(points) == 0 {
		m.setAdminStatus("Нет накопленных данных для суточного отчёта")
		return
	}
	sort.SliceStable(users, func(i, j int) bool {
		if users[i].MaxProcessCpu != users[j].MaxProcessCpu {
			return users[i].MaxProcessCpu > users[j].MaxProcessCpu
		}
		if users[i].MaxDuration5Min != users[j].MaxDuration5Min {
			return users[i].MaxDuration5Min > users[j].MaxDuration5Min
		}
		return users[i].UserName < users[j].UserName
	})
	folder := strings.TrimSpace(s.ReportFolder)
	if folder == "" {
		folder = m.defaultReportFolder()
	}
	machine := m.sys.MachineName()
	file := filepath.Join(folder, "1C-daily-"+machine+"-"+date.Format("2006-01-02")+".html")

	hours := map[int]*hourStat{}
	var total hourStat
	for _, p := range points {
		hs, ok := hours[p.At.Hour()]
		if !ok {
			hs = &hourStat{hour: p.At.Hour()}
			hours[p.At.Hour()] = hs
		}
		for _, x := range []*hourStat{hs, &total} {
			x.count++
			x.sumCpu += p.Cpu
			x.sumRam += p.RamMb
			x.sumDisk += p.DiskUsage
			x.maxCpu, x.maxRam, x.maxDisk = math.Max(x.maxCpu, p.Cpu), math.Max(x.maxRam, p.RamMb), math.Max(x.maxDisk, p.DiskUsage)
			x.maxRead, x.maxWrite, x.maxQueue = math.Max(x.maxRead, p.DiskRead), math.Max(x.maxWrite, p.DiskWrite), math.Max(x.maxQueue, p.DiskQueue)
			x.maxNetIn, x.maxNetOut = math.Max(x.maxNetIn, p.NetIn), math.Max(x.maxNetOut, p.NetOut)
			if p.Sessions > x.maxSessions {
				x.maxSessions = p.Sessions
			}
		}
	}
	var hourly []*hourStat
	for _, hs := range hours {
		hourly = append(hourly, hs)
	}
	sort.Slice(hourly, func(i, j int) bool { return hourly[i].hour < hourly[j].hour })

	first, last := points[0].At, points[len(points)-1].At
	var b strings.Builder
	b.Grow(200000)
	b.WriteString(`<!doctype html><html lang="ru"><head><meta charset="utf-8"><title>Суточный отчёт 1С</title><style>body{font:14px Segoe UI,Arial;background:#f4f7fb;color:#172033;margin:0;padding:24px}h1,h2{margin:0 0 12px}.sub{color:#64748b;margin-bottom:18px}.card,section{background:#fff;border:1px solid #dbe3ef;border-radius:10px;padding:16px;margin:12px 0;overflow-x:auto}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:10px}.cards .card{margin:0}.v{font-size:22px;font-weight:700}table{border-collapse:collapse;width:100%;table-layout:auto}th,td{padding:8px 10px;border-bottom:1px solid #dbe3ef;text-align:left;vertical-align:top;white-space:normal;overflow-wrap:anywhere}th{background:#eef3fa;white-space:nowrap}.bad{color:#b91c1c;font-weight:700}.wide{min-width:260px}@media print{@page{size:A4 landscape;margin:8mm}body{background:#fff;padding:0}}</style></head><body>`)
	fmt.Fprintf(&b, `<h1>%s — нагрузка сервера 1С за %s</h1><div class="sub">Покрытие %s–%s, измерений: %d · Разработчик: Zakharevich Igor</div><div class="cards">`,
		h(machine), date.Format("02.01.2006"), first.Format("15:04:05"), last.Format("15:04:05"), len(points))
	card := func(label, value string) {
		fmt.Fprintf(&b, `<div class="card"><div>%s</div><div class="v">%s</div></div>`, h(label), h(value))
	}
	card("Максимальный CPU", num(total.maxCpu, 1)+"%")
	card("Максимальная RAM", FormatMb(total.maxRam))
	card("Максимум диска", num(total.maxDisk, 1)+"%")
	card("Очередь диска", num(total.maxQueue, 1))
	card("Сеть приём", FormatRate(total.maxNetIn))
	card("Сеть отправка", FormatRate(total.maxNetOut))
	card("Максимум сеансов", itoa(total.maxSessions))

	racDescription := "не подключён"
	if strings.TrimSpace(racPath) != "" && fileExists(racPath) {
		racDescription = racVersion + " · " + ArchitectureLabel(racPath) + " · " + racPath
	}
	b.WriteString(`</div><section><h2>Описание и параметры сервера</h2><table><tbody>`)
	param := func(name, value string) { fmt.Fprintf(&b, `<tr><th>%s</th><td>%s</td></tr>`, h(name), h(value)) }
	param("Имя сервера", machine)
	param("Операционная система", strings.TrimSpace(m.sys.OSDescription()))
	param("Архитектура ОС", m.sys.OSArchitecture())
	param("Процессор", m.sys.CPUDescription())
	param("Логические процессоры", itoa(m.sys.LogicalCPUs()))
	param("Оперативная память", m.physicalMemoryText())
	param("Тип сервера и виртуализация", virtualization)
	param("Физические накопители", physicalDisks)
	param("Тома, занято и свободно", VolumeSummary(m.sys.Volumes()))
	param("Платформа 1С / RAC", racDescription)
	param("Адрес RAS", s.Endpoint)
	param("Процессы rphost на момент отчёта", itoa(view.ProcessCount))
	param("Текущая сеть", view.NetworkLoadText)
	param("Текущий диск", view.DiskUsageText+" · "+view.DiskIoText+" · "+view.DiskQueueText)
	param("Период сбора", first.Format("02.01.2006 15:04:05")+" — "+last.Format("02.01.2006 15:04:05"))
	b.WriteString(`</tbody></table><p class="sub">Отчёт описывает фактически наблюдавшийся период. Средние и максимальные значения рассчитаны по измерениям программы, а нагрузка пользователей сопоставлена с сеансами и процессами 1С через RAC.</p></section>`)
	b.WriteString(`<section><h2>Рост нагрузки по часам</h2><table><thead><tr><th>Час</th><th>CPU сред.</th><th>CPU макс.</th><th>RAM сред.</th><th>RAM макс.</th><th>Диск сред.</th><th>Диск макс.</th><th>Чтение макс.</th><th>Запись макс.</th><th>Очередь</th><th>Сеть ↓</th><th>Сеть ↑</th><th>Сеансы</th></tr></thead><tbody>`)
	for _, x := range hourly {
		n := float64(x.count)
		bad := ""
		if x.maxDisk >= 90 {
			bad = "bad"
		}
		fmt.Fprintf(&b, `<tr><td>%02d:00</td><td>%s%%</td><td>%s%%</td><td>%s</td><td>%s</td><td>%s%%</td><td class="%s">%s%%</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td></tr>`,
			x.hour, num(x.sumCpu/n, 1), num(x.maxCpu, 1), FormatMb(x.sumRam/n), FormatMb(x.maxRam), num(x.sumDisk/n, 1), bad, num(x.maxDisk, 1),
			FormatRate(x.maxRead), FormatRate(x.maxWrite), num(x.maxQueue, 1), FormatRate(x.maxNetIn), FormatRate(x.maxNetOut), x.maxSessions)
	}
	b.WriteString(`</tbody></table></section><section><h2>Кто генерировал нагрузку</h2><table><thead><tr><th>Пользователь</th><th>База</th><th>Компьютеры</th><th>PID</th><th>CPU rphost макс.</th><th>RAM rphost макс.</th><th>CPU-время 5 мин</th><th>Вызовы 5 мин</th><th>Время 5 мин</th><th>СУБД 5 мин</th><th>ΔRAM 5 мин</th><th class="wide">Документ/контекст</th></tr></thead><tbody>`)
	for _, u := range users {
		pids := make([]string, len(u.Pids))
		for i, pid := range u.Pids {
			pids[i] = itoa(pid)
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s%%</td><td>%s</td><td>%s мс</td><td>%d</td><td>%s с</td><td>%s с</td><td>%s МБ</td><td class="wide">%s</td></tr>`,
			h(u.UserName), h(u.InfoBase), h(strings.Join(u.Hosts, ", ")), h(strings.Join(pids, ", ")), num(u.MaxProcessCpu, 1), FormatMb(u.MaxProcessMemory), num(u.MaxCpu5Min, 0),
			u.MaxCalls5Min, num(u.MaxDuration5Min/1000, 1), num(u.MaxDbDuration5Min/1000, 1), num(u.MaxMemory5MinMb, 1), h(u.LastContext))
	}
	b.WriteString(`</tbody></table></section><section><b>Контакты:</b> Zakharevich Igor · <a href="https://csl.by">csl.by</a> · <a href="mailto:info@csl.by">info@csl.by</a> · +375 29 337 14 12</section></body></html>`)

	err := os.MkdirAll(folder, 0o755)
	if err == nil {
		err = os.WriteFile(file, append(append([]byte(nil), utf8BOM...), b.String()...), 0o644)
	}
	if err != nil {
		status := "Ошибка суточного отчёта: " + err.Error()
		m.setAdminStatus(status)
		m.Log(status)
		return
	}
	status := "Отчёт создан: " + file
	if automatic {
		status = "Автоотчёт создан: " + file
	}
	m.setAdminStatus(status)
	m.Log(status)
	if !automatic {
		m.OpenURI(file)
	}
}

const reportStyle = `:root{color-scheme:light;--bg:#f4f7fb;--card:#fff;--line:#d9e2ef;--text:#172033;--muted:#607089;--blue:#2563eb;--red:#b91c1c;--orange:#c2410c;--green:#047857}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 "Segoe UI",Arial,sans-serif}.page{max-width:1800px;margin:auto;padding:24px}h1{font-size:25px;margin:0 0 4px}h2{font-size:18px;margin:0 0 12px}.sub{color:var(--muted);margin-bottom:20px}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:12px;margin-bottom:18px}.card,.section{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px}.value{font-size:24px;font-weight:700;margin-top:5px}.label{font-size:12px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}.section{margin:14px 0;overflow-x:auto}table{border-collapse:collapse;width:100%;table-layout:auto}th,td{border-bottom:1px solid var(--line);padding:8px 10px;text-align:left;vertical-align:top;white-space:normal;overflow:visible;text-overflow:clip;overflow-wrap:anywhere;word-break:normal;min-width:80px}th{background:#eef3fa;font-size:12px;position:sticky;top:0}td.nowrap,th.nowrap{white-space:nowrap;overflow-wrap:normal;min-width:max-content}.wide{min-width:240px}.medium{min-width:150px}.ok{color:var(--green);font-weight:600}.warn{color:var(--orange);font-weight:700}.critical{color:var(--red);font-weight:700}.note{padding:10px 12px;background:#eff6ff;border-left:4px solid var(--blue);margin:12px 0;color:#334155}details summary{cursor:pointer;font-weight:600;padding:5px}@media print{body{background:#fff}.page{max-width:none;padding:8px}.section,.card{break-inside:avoid;box-shadow:none}th{position:static}@page{size:A4 landscape;margin:8mm}}`

func riskCSS(risk int) string {
	if risk >= 20 {
		if risk >= 80 {
			return "critical"
		}
		return "warn"
	}
	return "ok"
}

// ExportHTMLReport сохраняет автономный HTML-отчёт о текущем состоянии на рабочий стол и открывает его.
// Возвращает путь к файлу (пустой при ошибке).
func (m *Monitor) ExportHTMLReport() string {
	m.mu.Lock()
	view := m.view
	history := m.history
	if len(history) > 5000 {
		history = history[len(history)-5000:]
	}
	history = append([]historyRow(nil), history...)
	m.mu.Unlock()

	dir := m.sys.DesktopDir()
	if strings.TrimSpace(dir) == "" || !dirExists(dir) {
		dir = m.sys.ExecutableDir()
	}
	now := m.now()
	file := filepath.Join(dir, "1C-monitor-"+now.Format("20060102-150405")+".html")

	var b strings.Builder
	b.Grow(256000)
	b.WriteString(`<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Отчёт мониторинга сервера 1С</title><style>` + "\n" + reportStyle + "\n" + `</style></head><body><main class="page">`)
	fmt.Fprintf(&b, `<h1>Отчёт мониторинга сервера 1С</h1><div class="sub">Сформирован %s · %s · Разработчик: Zakharevich Igor</div>`, now.Format("02.01.2006 15:04:05"), h(view.Status))
	b.WriteString(`<div class="cards">`)
	card := func(label, value string) {
		fmt.Fprintf(&b, `<div class="card"><div class="label">%s</div><div class="value">%s</div></div>`, h(label), h(value))
	}
	card("Процессы rphost", itoa(view.ProcessCount))
	card("CPU rphost", view.TotalCpuText)
	card("Память rphost", view.TotalMemoryText)
	card("Активные сеансы", itoa(view.ActiveSessionCount))
	card("Спящие сеансы", itoa(view.SleepingSessionCount))
	card("Тревоги", itoa(view.AlertCount))
	card("Загрузка диска", view.DiskUsageText)
	card("Ввод-вывод", view.DiskIoText)
	card("Очередь диска", view.DiskQueueText)
	card("Сеть", view.NetworkLoadText)
	b.WriteString(`</div>`)
	fmt.Fprintf(&b, `<section class="section"><h2>Сервер и накопители</h2><p>%s</p><p><strong>Оборудование:</strong> %s</p><p><strong>Сеть:</strong> %s</p></section>`,
		strings.ReplaceAll(h(view.ServerHeaderDetails), "\n", "<br>"), h(view.ServerHardwareText), h(view.NetworkLoadText))

	b.WriteString(`<section class="section"><h2>Рабочие процессы</h2><table><thead><tr><th>Риск</th><th>PID</th><th>Состояние процесса</th><th>CPU</th><th>RAM</th><th>Сеансы</th><th>Средний вызов</th><th>Средний запрос БД</th><th class="wide">Пользователи</th><th class="wide">Диагноз</th></tr></thead><tbody>`)
	for _, p := range view.Processes {
		fmt.Fprintf(&b, `<tr><td class="%s nowrap">%d</td><td class="nowrap">%d</td><td>%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td>%d</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="wide">%s</td><td class="wide">%s</td></tr>`,
			riskCSS(p.Risk), p.Risk, p.Pid, h(p.DrainStatus), h(p.CpuText()), h(p.MemoryText()), p.SessionCount, h(p.AvgCallText()), h(p.AvgDbText()), h(p.Users), h(p.Diagnosis))
	}
	b.WriteString(`</tbody></table></section>`)

	b.WriteString(`<section class="section"><h2>Пользователи и сеансы</h2><div class="note">CPU и RAM измеряются на уровне rphost. CPU пользователя — оценочная доля. Контекст документа берётся из технологического журнала.</div><table><thead><tr><th>Состояние</th><th class="medium">Пользователь</th><th>База</th><th>Приложение</th><th class="medium">Компьютер</th><th>PID</th><th>Память вызова RAC</th><th>CPU-время RAC</th><th>Текущий вызов</th><th>Вызов СУБД</th><th>I/O сеанса</th><th class="wide">Нагрузка за 5 минут</th><th class="wide">I/O за 5 минут</th><th>CPU rphost</th><th>RAM rphost</th><th>CPU пользователя*</th><th class="nowrap">Начало</th><th class="nowrap">Последняя активность</th><th class="wide">Метод/операция RAC</th><th class="wide">Документ/контекст ТЖ</th><th>Время вызова ТЖ</th><th class="wide">Запрос СУБД</th><th>Блокировка</th></tr></thead><tbody>`)
	for _, s := range view.Sessions {
		stateClass, blockedClass := "ok", ""
		if s.IsSleeping {
			stateClass = ""
		}
		if s.IsBlocked() {
			blockedClass = "critical"
		}
		fmt.Fprintf(&b, `<tr><td class="%s nowrap">%s</td><td class="medium">%s</td><td>%s</td><td>%s</td><td class="medium">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="wide">%s</td><td class="wide">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td class="wide">%s</td><td class="wide">%s</td><td class="nowrap">%s</td><td class="wide">%s</td><td class="%s">%s</td></tr>`,
			stateClass, h(s.SessionState()), h(s.UserName), h(s.InfoBase), h(s.AppID), h(s.Host), h(s.PidText()), h(s.SessionMemoryText()), h(s.SessionCpuTimeText()), h(s.CurrentCallText()),
			h(s.CurrentDbText()), h(s.IoCurrentText()), h(s.Load5MinText()), h(s.Io5MinText()), h(s.ProcessCpuText()), h(s.ProcessMemoryText()), h(s.EstimatedCpuText()), h(s.StartedAt),
			h(s.LastActiveAt), h(s.Operation), h(s.TechContext), h(s.LastCallText()), h(s.DbRequest), blockedClass, h(s.Blocked))
	}
	b.WriteString(`</tbody></table></section>`)

	b.WriteString(`<section class="section"><h2>Тревоги</h2><table><thead><tr><th class="nowrap">Время</th><th>Уровень</th><th>Источник</th><th class="wide">Сообщение</th></tr></thead><tbody>`)
	for _, a := range view.Alerts {
		fmt.Fprintf(&b, `<tr><td class="nowrap">%s</td><td class="critical">%s</td><td>%s</td><td class="wide">%s</td></tr>`, h(a.TimeText()), h(a.Severity), h(a.Source), h(a.Message))
	}
	b.WriteString(`</tbody></table></section>`)

	b.WriteString(`<section class="section"><details><summary>История измерений (последние 5000 точек)</summary><table><thead><tr><th>Время</th><th>PID</th><th>CPU %</th><th>RAM МБ</th><th>Сеансы</th><th class="wide">Пользователи</th><th>Риск</th><th>Диск %</th><th>Чтение</th><th>Запись</th><th>Очередь</th><th>Сеть ↓</th><th>Сеть ↑</th><th>Сеть %</th><th class="wide">Диагноз</th></tr></thead><tbody>`)
	for _, x := range history {
		fmt.Fprintf(&b, `<tr><td class="nowrap">%s</td><td>%d</td><td>%s</td><td>%s</td><td>%d</td><td class="wide">%s</td><td class="%s">%d</td><td>%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td>%s</td><td class="nowrap">%s</td><td class="nowrap">%s</td><td>%s</td><td class="wide">%s</td></tr>`,
			x.At.Format("02.01.2006 15:04:05"), x.Pid, num(x.Cpu, 1), num(x.MemoryMb, 1), x.Sessions, h(x.Users), riskCSS(x.Risk), x.Risk, num(x.DiskUsage, 1), h(FormatRate(x.DiskRead)),
			h(FormatRate(x.DiskWrite)), num(x.DiskQueue, 2), h(FormatRate(x.NetworkReceive)), h(FormatRate(x.NetworkSend)), num(x.NetworkUsage, 1), h(x.Diagnosis))
	}
	b.WriteString(`</tbody></table></details></section><section class="section"><h2>Контакты разработчика</h2><table><tbody><tr><th>Разработчик</th><td>Zakharevich Igor</td></tr><tr><th>Организация</th><td>ООО «Компьютерная безопасность»</td></tr><tr><th>Сайт</th><td><a href="https://csl.by">https://csl.by</a></td></tr><tr><th>Email</th><td><a href="mailto:info@csl.by">info@csl.by</a></td></tr><tr><th>Телефон</th><td><a href="tel:+375293371412">+375 29 337 14 12</a></td></tr><tr><th>Адрес</th><td>Республика Беларусь, г. Минск, ул. Фабрициуса, 9</td></tr><tr><th>Время работы</th><td>Понедельник–пятница, 9:00–17:00</td></tr></tbody></table></section><div class="sub">1C Server Monitor · автономный HTML-отчёт · Zakharevich Igor · csl.by</div></main></body></html>`)

	if err := os.WriteFile(file, append(append([]byte(nil), utf8BOM...), b.String()...), 0o644); err != nil {
		m.Log("Ошибка создания HTML-отчёта: " + err.Error())
		return ""
	}
	m.Log("HTML-отчёт сохранён: " + file)
	m.setStatus("HTML-отчёт сохранён: "+file, LevelOK)
	m.OpenURI(file)
	return file
}
