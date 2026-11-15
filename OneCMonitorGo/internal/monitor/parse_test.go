package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

func eq[T comparable](t *testing.T, actual, expected T, name string) {
	t.Helper()
	if actual != expected {
		t.Errorf("%s: ожидалось <%v>, получено <%v>", name, expected, actual)
	}
}

func check(t *testing.T, condition bool, name string) {
	t.Helper()
	if !condition {
		t.Errorf("не выполнено: %s", name)
	}
}

func TestTechLog(t *testing.T) {
	hour := time.Date(2024, 5, 17, 12, 0, 0, 0, time.Local)
	eq(t, techLogFileHour("/x/rphost_1/24051712.log", time.Time{}), hour, "час из имени файла")
	text := strings.Join([]string{
		"ломаная первая строка без заголовка",
		"10:01.100000-2500,CALL,1,process=rphost,p:processName=trade,t:clientID=5,t:applicationName=1CV8C,t:computerName=PC-1,t:connectID=14,SessionID=5,Usr=Иванов,Context=Форма.Вызов : Документ.Реализация.Форма.ФормаДокумента.Модуль.ПровестиНаСервере,Memory=100",
		"10:02.200000-1500000,DBMSSQL,5,process=rphost,p:processName=trade,SessionID=5,Usr=Иванов,Sql='SELECT a, b",
		"FROM t WHERE x = ''1'', Context=не настоящий',Rows=3,Context='Форма.Вызов : Документ.Заказ.Форма.Модуль.Записать",
		"Документ.Заказ.МодульОбъекта : 120 : Запрос.Выполнить();'",
		`10:03.000000-10,CALL,1,process=rphost,p:processName=buh,SessionID=5,Usr="Петров, Пётр",Context=ОбщийМодуль.Расчёт.Модуль : 7 : Рассчитать();`,
		"10:04.000000-10,EXCP,1,process=rphost,p:processName=trade,SessionID=9,Usr=Сидоров,Descr='без контекста'",
	}, "\n")
	events := ParseTechLog(text, hour)
	if len(events) != 3 {
		t.Fatalf("событий с контекстом: %d, ожидалось 3", len(events))
	}
	eq(t, events[0].Context, "Форма.Вызов : Документ.Реализация.Форма.ФормаДокумента.Модуль.ПровестиНаСервере", "однострочный контекст")
	eq(t, events[0].DurationMs, 2.5, "длительность, мс")
	eq(t, events[0].At, hour.Add(10*time.Minute+1100*time.Millisecond), "время события")
	eq(t, events[1].Context, "Форма.Вызов : Документ.Заказ.Форма.Модуль.Записать → Документ.Заказ.МодульОбъекта : 120 : Запрос.Выполнить();", "многострочный контекст после Sql с запятыми")
	eq(t, events[1].SessionID, "5", "номер сеанса")
	eq(t, events[1].InfoBase, "trade", "база")
	eq(t, events[2].User, "Петров, Пётр", "пользователь в кавычках с запятой")

	index := NewTechLogIndex()
	for i := len(events) - 1; i >= 0; i-- { // порядок добавления не должен влиять
		index.Add(events[i])
	}
	find := func(infoBase, session, user string) string {
		found, ok := index.Find(infoBase, session, user)
		if !ok {
			return "<нет>"
		}
		return found.Context
	}
	eq(t, find("Trade", "5", "Иванов"), events[1].Context, "побеждает последнее событие, регистр базы не важен")
	eq(t, find("buh", "5", "кто-то"), events[2].Context, "тот же номер сеанса в другой базе не смешивается")
	eq(t, find("zup", "5", "Иванов2"), "<нет>", "префикс имени пользователя и другая база не совпадают")
	eq(t, find("aea71760-uuid", "5", "Иванов"), events[1].Context, "поиск по номеру сеанса и пользователю, когда имя базы неизвестно")
	eq(t, find("trade", "77", "Иванов"), events[1].Context, "поиск по пользователю внутри базы")
	index.Add(TechLogEvent{At: hour.Add(time.Minute), InfoBase: "trade", SessionID: "5", User: "Иванов", Context: "старое", DurationMs: 1})
	eq(t, find("trade", "5", "Иванов"), events[1].Context, "более старое событие, добавленное позже, не перекрывает новое")

	// Чтение хвоста файла: первая (обрезанная) строка и продолжение чужого события пропускаются.
	file := filepath.Join(t.TempDir(), "24051713.log")
	content := "\uFEFF" + strings.Repeat("x", 5000) + "\nхвост чужого события'\n" + "00:05.000001-7,CALL,1,p:processName=trade,SessionID=3,Usr=A,Context=К1\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := ReadTechLogTail(file, 200)
	if err != nil || len(tail) != 1 {
		t.Fatalf("хвост файла: %v, событий %d", err, len(tail))
	}
	eq(t, tail[0].Context, "К1", "контекст из хвоста")
	eq(t, tail[0].At.Hour(), 13, "час из имени файла")
}

const (
	testInfoBases = "infobase : aaaaaaaa-0000-0000-0000-000000000001\nname     : trade\ndescr    : \n\ninfobase : bbbbbbbb-0000-0000-0000-000000000002\nname     : buh\ndescr    : \n"
	testProcesses = "process : 11111111-1111-1111-1111-111111111111\nhost : SRV\nport : 1560\npid : 1111\navg-call-time : 0.250\navg-db-call-time : 0.100\nmemory-size : 2097152\n\nprocess : 22222222-2222-2222-2222-222222222222\nhost : SRV\nport : 1561\npid : 2222\navg-call-time : 0.010\navg-db-call-time : 0.001\nmemory-size : 524288\n"
	zeroUUID      = "00000000-0000-0000-0000-000000000000"
)

var testConnections = "connection : c0000000-0000-0000-0000-000000000001\nconn-id : 101\nhost : PC-1\nprocess : 11111111-1111-1111-1111-111111111111\ninfobase : aaaaaaaa-0000-0000-0000-000000000001\napplication : 1CV8C\nconnected-at : 2024-05-17T11:00:00\nsession-number : 5\nblocked-by-ls : 0\n\n" +
	"connection : c0000000-0000-0000-0000-000000000002\nconn-id : 102\nhost : PC-2\nprocess : 22222222-2222-2222-2222-222222222222\ninfobase : bbbbbbbb-0000-0000-0000-000000000002\napplication : 1CV8C\nconnected-at : 2024-05-17T11:30:00\nsession-number : 5\nblocked-by-ls : 0\n\n" +
	"connection : c0000000-0000-0000-0000-000000000003\nconn-id : 0\nhost : SRV\nprocess : 22222222-2222-2222-2222-222222222222\ninfobase : bbbbbbbb-0000-0000-0000-000000000002\napplication : SrvrConsole\nconnected-at : 2024-05-17T09:00:00\nsession-number : 0\nblocked-by-ls : 0\n"

func testSession(uuid string, id int, infoBase, user, host, app, started, extra string) string {
	return "session : " + uuid + "\nsession-id : " + itoa(id) + "\ninfobase : " + infoBase + "\nconnection : " + zeroUUID + "\nprocess : " + zeroUUID +
		"\nuser-name : " + user + "\nhost : " + host + "\napp-id : " + app + "\nstarted-at : " + started +
		"\nlast-active-at : 2024-05-17T12:00:00\nhibernate : no\nblocked-by-dbms : 0\nblocked-by-ls : 0\nmemory-current : 10485760\ncalls-last-5min : 3\n" + extra + "\n"
}

const (
	tradeUUID = "aaaaaaaa-0000-0000-0000-000000000001"
	buhUUID   = "bbbbbbbb-0000-0000-0000-000000000002"
)

var testSessions = testSession("50000000-0000-0000-0000-00000000000a", 5, tradeUUID, "Иванов", "PC-1", "1CV8C", "2024-05-17T08:00:00", "") +
	testSession("50000000-0000-0000-0000-00000000000b", 5, buhUUID, "Петров", "PC-2", "1CV8C", "2024-05-17T08:10:00", "") +
	testSession("50000000-0000-0000-0000-00000000000c", 8, tradeUUID, "Сидоров", "PC-3", "1CV8C", "2024-05-17T08:20:00", "hibernate : yes\n") +
	testSession("50000000-0000-0000-0000-00000000000d", 9, tradeUUID, "Иванов", "PC-1", "1CV8C", "2024-05-17T09:00:00", "") +
	testSession("50000000-0000-0000-0000-00000000000e", 10, tradeUUID, "", "SRV", "BackgroundJob", "2024-05-17T09:01:00", "") +
	testSession("50000000-0000-0000-0000-00000000000f", 11, tradeUUID, "", "SRV", "BackgroundJob", "2024-05-17T09:02:00", "") +
	testSession("50000000-0000-0000-0000-000000000010", 12, tradeUUID, "robot", "WEB", "WSConnection", "2024-05-17T09:03:00", "") +
	testSession("50000000-0000-0000-0000-000000000011", 13, tradeUUID, "robot", "WEB", "WSConnection", "2024-05-17T09:04:00", "")

func TestBuildSnapshot(t *testing.T) {
	snapshot := BuildSnapshot(testInfoBases, testProcesses, testConnections, testSessions)
	eq(t, len(snapshot.InfoBases), 2, "базы")
	eq(t, len(snapshot.Processes), 2, "процессы")
	if len(snapshot.Sessions) != 8 {
		t.Fatalf("сеансов %d, ожидалось 8", len(snapshot.Sessions))
	}
	eq(t, snapshot.Processes[1111].MemoryMb, 2048.0, "память процесса, МБ")
	eq(t, snapshot.Processes[1111].AvgCallMs, 250.0, "средний вызов, мс")
	a, b, c := snapshot.Sessions[0], snapshot.Sessions[1], snapshot.Sessions[2]
	eq(t, a.InfoBase, "trade", "имя базы")
	eq(t, a.SessionNumber, "5", "номер сеанса")
	eq(t, a.Pid, 1111, "сеанс 5 базы trade связан через session-number соединения")
	eq(t, b.Pid, 2222, "сеанс 5 базы buh связан со своим соединением")
	eq(t, c.Pid, 0, "спящий сеанс без соединения не имеет PID")
	check(t, c.IsSleeping, "признак hibernate")
	eq(t, snapshot.Sessions[4].UserName, UnknownUser, "заглушка пустого пользователя")
	for _, s := range snapshot.Sessions[3:] {
		eq(t, s.Pid, 0, "session-number 0 и conn-id 0 не связывают сеанс")
	}
	eq(t, a.MemoryCurrentMb, 10.0, "память сеанса")

	// Неизвестные форматы не должны приводить к панике.
	empty := BuildSnapshot("", "мусор без двоеточий", "connection", "session :\n\n:::\n")
	check(t, empty != nil, "снимок по мусорным данным")
}

func TestDecodeAndEndpoints(t *testing.T) {
	const text = "Администратор кластера не аутентифицирован"
	eq(t, DecodeRac([]byte(text)), text, "utf-8")
	cp866, _ := charmap.CodePage866.NewEncoder().String(text)
	eq(t, DecodeRac([]byte(cp866)), text, "cp866")
	cp1251, _ := charmap.Windows1251.NewEncoder().String(text)
	eq(t, DecodeRac([]byte(cp1251)), text, "cp1251")
	eq(t, DecodeRac(nil), "", "пустой вывод")
	eq(t, strings.Join(EndpointCandidates("localhost:1545"), "|"), "localhost:1545|127.0.0.1:1545", "варианты адреса RAS")
}

func TestFormat(t *testing.T) {
	eq(t, num(1234.56, 1), "1234,6", "десятичная запятая")
	eq(t, numInvariant(1234.56, 1), "1234.6", "точка в CSV")
	eq(t, csvQuote(`a"b`), `"a""b"`, "кавычки CSV")
	eq(t, h(`<a href="x">&`), "&lt;a href=&#34;x&#34;&gt;&amp;", "экранирование HTML")
	eq(t, compareVersions(parseVersion("8.3.24.1624"), parseVersion("8.3.9.2170")) > 0, true, "версии сравниваются по числам")
	eq(t, len(parseVersion("?")), 0, "неизвестная версия")
}

func TestPEArchitecture(t *testing.T) {
	dir := t.TempDir()
	pe := func(name string, machine uint16) string {
		b := make([]byte, 0x100)
		b[0], b[1], b[0x3C], b[0x80], b[0x81] = 0x4D, 0x5A, 0x80, 0x50, 0x45
		b[0x84], b[0x85] = byte(machine), byte(machine>>8)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	eq(t, ArchitectureLabel(pe("x86.exe", 0x014C)), "x86", "PE x86")
	eq(t, ArchitectureLabel(pe("x64.exe", 0x8664)), "x64", "PE x64")
	bad := filepath.Join(dir, "bad.exe")
	_ = os.WriteFile(bad, []byte{1, 2, 3}, 0o644)
	eq(t, ReadPEArchitecture(bad), "", "мусор вместо PE")
	zeroes := filepath.Join(dir, "zero.exe")
	_ = os.WriteFile(zeroes, make([]byte, 0x40), 0o644)
	eq(t, ReadPEArchitecture(zeroes), "", "нули вместо PE")
	eq(t, ReadPEArchitecture(filepath.Join(dir, "none.exe")), "", "файла нет")
}

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	eq(t, LoadSettings(dir).Endpoint, DefaultSettings().Endpoint, "настройки по умолчанию без файла")
	s := DefaultSettings()
	s.RacPath, s.ClusterUser, s.ClusterPassword, s.MemoryAlertMb = `C:\Program Files\1cv8\8.3.24.1624\bin\rac.exe`, "admin", "secret", 8192
	s.BackgroundJobsDisabledInfobases = []string{"a", "b"}
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	check(t, !strings.Contains(string(data), "secret"), "пароль не записывается в файл")
	check(t, strings.Contains(string(data), `"RacPath"`), "имена полей как в версии .NET")
	loaded := LoadSettings(dir)
	eq(t, loaded.RacPath, s.RacPath, "путь RAC")
	eq(t, loaded.ClusterUser, "admin", "администратор кластера")
	eq(t, loaded.ClusterPassword, "", "пароль не загружается")
	eq(t, loaded.MemoryAlertMb, 8192.0, "порог памяти")
	eq(t, len(loaded.BackgroundJobsDisabledInfobases), 2, "список баз")

	// Файл версии .NET: UTF-8 с BOM, часть полей отсутствует, часть равна null.
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte("\uFEFF{\"Endpoint\":null,\"RacPath\":null,\"IntervalSeconds\":7,\"BackgroundJobsDisabledInfobases\":null}"), 0o644)
	loaded = LoadSettings(dir)
	eq(t, loaded.IntervalSeconds, 7.0, "значение из файла с BOM")
	eq(t, loaded.RacPollSeconds, DefaultSettings().RacPollSeconds, "отсутствующее поле берётся по умолчанию")
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{ испорчено"), 0o644)
	eq(t, LoadSettings(dir).IntervalSeconds, DefaultSettings().IntervalSeconds, "испорченный файл заменяется настройками по умолчанию")
}

func TestDuplicateSessions(t *testing.T) {
	duplicates := DuplicateSessions(BuildSnapshot(testInfoBases, testProcesses, testConnections, testSessions).Sessions)
	if len(duplicates) != 1 {
		t.Fatalf("дубликатов %d, ожидался 1", len(duplicates))
	}
	eq(t, duplicates[0].SessionUUID, "50000000-0000-0000-0000-00000000000a", "завершается более ранний сеанс; фоновые задания и веб-сервисы не трогаются")
}
