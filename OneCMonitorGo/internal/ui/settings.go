package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

func formatNumber(v float64) string {
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",")
}

func parseNumber(text string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(text), ",", "."), 64)
	return v, err == nil
}

func uriPath(uri fyne.URI) string { return filepath.FromSlash(uri.Path()) }

func (u *UI) settingsTab() fyne.CanvasObject {
	var reload []func(s monitor.Settings)

	text := func(placeholder string, get func(monitor.Settings) string, set func(*monitor.Settings, string)) *widget.Entry {
		entry := widget.NewEntry()
		entry.SetPlaceHolder(placeholder)
		entry.SetText(get(u.m.Settings()))
		entry.OnChanged = func(value string) { u.m.UpdateSettings(func(s *monitor.Settings) { set(s, value) }) }
		reload = append(reload, func(s monitor.Settings) {
			if value := get(s); entry.Text != value {
				entry.SetText(value)
			}
		})
		return entry
	}
	number := func(min, max float64, get func(monitor.Settings) float64, set func(*monitor.Settings, float64)) *widget.Entry {
		entry := widget.NewEntry()
		entry.SetText(formatNumber(get(u.m.Settings())))
		entry.Validator = func(value string) error {
			v, ok := parseNumber(value)
			if !ok {
				return errors.New("введите число")
			}
			if v < min || v > max {
				return fmt.Errorf("допустимо от %s до %s", formatNumber(min), formatNumber(max))
			}
			return nil
		}
		entry.OnChanged = func(value string) {
			if v, ok := parseNumber(value); ok && v >= min && v <= max {
				u.m.UpdateSettings(func(s *monitor.Settings) { set(s, v) })
			}
		}
		return entry
	}
	form := func(items ...fyne.CanvasObject) fyne.CanvasObject {
		return container.New(layout.NewFormLayout(), items...)
	}
	field := func(name string) *widget.Label { return widget.NewLabel(name) }
	limits := func(name string, min, max float64) *widget.Label {
		return field(fmt.Sprintf("%s (%s–%s)", name, formatNumber(min), formatNumber(max)))
	}
	withButton := func(entry *widget.Entry, label string, tapped func()) fyne.CanvasObject {
		return container.NewBorder(nil, nil, nil, widget.NewButton(label, tapped), entry)
	}
	chooseFolder := func(entry *widget.Entry) func() {
		return func() {
			dialog.ShowFolderOpen(func(folder fyne.ListableURI, err error) {
				if err == nil && folder != nil {
					entry.SetText(uriPath(folder))
				}
			}, u.win)
		}
	}

	// Подключение.
	racPath := text("Автопоиск", func(s monitor.Settings) string { return s.RacPath }, func(s *monitor.Settings, v string) { s.RacPath = v })
	chooseRac := func() {
		open := dialog.NewFileOpen(func(file fyne.URIReadCloser, err error) {
			if err == nil && file != nil {
				racPath.SetText(uriPath(file.URI()))
				_ = file.Close()
			}
		}, u.win)
		open.SetFilter(storage.NewExtensionFileFilter([]string{".exe"}))
		open.Show()
	}
	endpoint := text("localhost:1545", func(s monitor.Settings) string { return s.Endpoint }, func(s *monitor.Settings, v string) { s.Endpoint = v })
	agent := text("localhost:1540", func(s monitor.Settings) string { return s.AgentEndpoint }, func(s *monitor.Settings, v string) { s.AgentEndpoint = v })
	clusterUser := text("Имя из свойств кластера (не пользователь базы)", func(s monitor.Settings) string { return s.ClusterUser }, func(s *monitor.Settings, v string) { s.ClusterUser = v })
	password := widget.NewPasswordEntry()
	password.SetPlaceHolder("Оставьте пустым, если пароль не задан")
	password.SetText(u.m.Settings().ClusterPassword)
	password.OnChanged = func(value string) { u.m.UpdateSettings(func(s *monitor.Settings) { s.ClusterPassword = value }) }
	techLog := text(`Например D:\1C-TechLog`, func(s monitor.Settings) string { return s.TechLogPath }, func(s *monitor.Settings, v string) { s.TechLogPath = v })
	racTimeout := number(15, 300, func(s monitor.Settings) float64 { return s.RacTimeoutSeconds }, func(s *monitor.Settings, v float64) { s.RacTimeoutSeconds = v })
	racStatus := boldLabel(u.view.RacStatus)
	racStatus.Wrapping = fyne.TextWrapWord
	save := u.asyncButton("Сохранить и проверить", u.m.SaveSettingsAndCheck)
	save.Importance = widget.HighImportance
	connection := card(container.NewVBox(heading("Подключение к администрированию 1С"),
		note("Программа найдёт rac.exe автоматически. Если RAS не запущен, нажмите «Запустить RAS». Это не перезапускает кластер 1С."),
		form(field("Путь к rac.exe"), withButton(racPath, "Выбрать…", chooseRac),
			field("Адрес RAS"), endpoint,
			field("Адрес агента 1С"), agent,
			field("Администратор кластера"), clusterUser,
			field("Пароль (может быть пуст)"), password,
			field("Каталог техжурнала"), withButton(techLog, "Выбрать…", chooseFolder(techLog)),
			limits("Тайм-аут RAC, сек.", 15, 300), racTimeout),
		container.NewHBox(save, u.asyncButton("Запустить RAS", u.m.StartRas)),
		racStatus))

	// Суточный отчёт.
	autoReport := widget.NewCheck("Включён, ежедневно в 23:55", func(on bool) { u.m.UpdateSettings(func(s *monitor.Settings) { s.AutoDailyReport = on }) })
	autoReport.SetChecked(u.m.Settings().AutoDailyReport)
	reportFolder := text("", func(s monitor.Settings) string { return s.ReportFolder }, func(s *monitor.Settings, v string) { s.ReportFolder = v })
	report := card(container.NewVBox(heading("Автоматический суточный отчёт"), autoReport,
		form(field("Каталог отчётов"), withButton(reportFolder, "Выбрать…", chooseFolder(reportFolder))),
		container.NewHBox(u.asyncButton("Сформировать отчёт за сегодня", u.m.GenerateDailyReport)),
		note("Отчёт содержит почасовой рост CPU, RAM, диска, сети, количества сеансов и таблицу пользователей, генерировавших нагрузку. Для полного дня программа должна оставаться запущенной.")))

	// Автовывод rphost.
	autoDrain := widget.NewCheck("Автовывод включён", func(on bool) { u.m.UpdateSettings(func(s *monitor.Settings) { s.AutoDrainEnabled = on }) })
	autoDrain.SetChecked(u.m.Settings().AutoDrainEnabled)
	drain := card(container.NewVBox(heading("Плавный автоматический вывод rphost"), autoDrain,
		note("При длительном превышении памяти выполняется только RAC process turn-off: новая нагрузка направляется в другие процессы, текущие вызовы не убиваются. Принудительный taskkill автоматически не применяется."),
		form(limits("Порог памяти rphost, МБ", 512, 131072), number(512, 131072, func(s monitor.Settings) float64 { return s.AutoDrainMemoryMb }, func(s *monitor.Settings, v float64) { s.AutoDrainMemoryMb = v }),
			limits("Время превышения, минут", 1, 1440), number(1, 1440, func(s monitor.Settings) float64 { return s.AutoDrainMinutes }, func(s *monitor.Settings, v float64) { s.AutoDrainMinutes = v }),
			limits("Минимум остающихся rphost", 1, 64), number(1, 64, func(s monitor.Settings) float64 { return s.AutoDrainMinProcesses }, func(s *monitor.Settings, v float64) { s.AutoDrainMinProcesses = v }),
			limits("Пауза между выводами, минут", 5, 1440), number(5, 1440, func(s monitor.Settings) float64 { return s.AutoDrainCooldownMinutes }, func(s *monitor.Settings, v float64) { s.AutoDrainCooldownMinutes = v }))))

	// Интервалы.
	performance := card(container.NewVBox(heading("Производительность монитора"),
		note("Локальные CPU/RAM остаются оперативными, а тяжёлые RAC, техжурнал и запись истории выполняются реже. Рекомендуемые значения уже установлены по умолчанию."),
		form(limits("Локальные метрики, сек.", 2, 60), number(2, 60, func(s monitor.Settings) float64 { return s.IntervalSeconds }, func(s *monitor.Settings, v float64) { s.IntervalSeconds = v }),
			limits("Опрос RAC, сек.", 5, 300), number(5, 300, func(s monitor.Settings) float64 { return s.RacPollSeconds }, func(s *monitor.Settings, v float64) { s.RacPollSeconds = v }),
			limits("Сканирование техжурнала, сек.", 15, 600), number(15, 600, func(s monitor.Settings) float64 { return s.TechLogPollSeconds }, func(s *monitor.Settings, v float64) { s.TechLogPollSeconds = v }),
			limits("Запись истории на диск, сек.", 30, 600), number(30, 600, func(s monitor.Settings) float64 { return s.HistoryWriteSeconds }, func(s *monitor.Settings, v float64) { s.HistoryWriteSeconds = v }),
			limits("Повтор одинаковой тревоги, сек.", 60, 3600), number(60, 3600, func(s monitor.Settings) float64 { return s.AlertRepeatSeconds }, func(s *monitor.Settings, v float64) { s.AlertRepeatSeconds = v }))))

	// Пороги.
	thresholds := card(container.NewVBox(heading("Пороги тревог"),
		form(limits("CPU процесса, %", 10, 100), number(10, 100, func(s monitor.Settings) float64 { return s.CpuAlert }, func(s *monitor.Settings, v float64) { s.CpuAlert = v }),
			limits("RAM процесса, МБ", 256, 131072), number(256, 131072, func(s monitor.Settings) float64 { return s.MemoryAlertMb }, func(s *monitor.Settings, v float64) { s.MemoryAlertMb = v }),
			limits("Средний вызов, мс", 100, 600000), number(100, 600000, func(s monitor.Settings) float64 { return s.CallAlertMs }, func(s *monitor.Settings, v float64) { s.CallAlertMs = v }),
			limits("Загрузка диска, %", 10, 100), number(10, 100, func(s monitor.Settings) float64 { return s.DiskAlert }, func(s *monitor.Settings, v float64) { s.DiskAlert = v }))))

	about := card(container.NewVBox(heading("О программе"),
		form(field("Разработчик"), boldLabel("Zakharevich Igor"),
			field("Организация"), widget.NewLabel("ООО «Компьютерная безопасность»"),
			field("Сайт и email"), container.NewHBox(u.link("https://csl.by", "https://csl.by"), u.link("info@csl.by", "mailto:info@csl.by")),
			field("Телефон"), container.NewHBox(u.link("+375 29 337 14 12", "tel:+375293371412")),
			field("Адрес и время"), widget.NewLabel("Минск, ул. Фабрициуса, 9 • Пн–Пт 9:00–17:00"),
			field("Версия"), widget.NewLabel("Go + Fyne, сборка для Windows"))))

	start := card(container.NewVBox(heading("Как начать"),
		wrapLabel("1. Запустите программу от имени администратора на сервере 1С.\n2. Нажмите «Запустить RAS», затем «Сохранить и проверить».\n3. Если кластер защищён, укажите учётную запись администратора кластера.\n4. Оставьте мониторинг включённым до момента зависания и сформируйте HTML-отчёт.")))

	version := u.view.SettingsVersion
	u.onUpdate(func(view monitor.View) {
		setText(racStatus, view.RacStatus)
		importance := widget.WarningImportance
		if strings.HasPrefix(view.RacStatus, "Подключено") || strings.HasPrefix(view.RacStatus, "RAS готов") || strings.HasPrefix(view.RacStatus, "RAS уже работает") {
			importance = widget.SuccessImportance
		}
		setImportance(racStatus, importance)
		// Монитор сам записал найденный путь RAC или рабочий адрес RAS — поля ввода обновляются.
		if view.SettingsVersion != version {
			version = view.SettingsVersion
			settings := u.m.Settings()
			for _, apply := range reload {
				apply(settings)
			}
		}
	})
	left := container.NewVBox(connection, report, start)
	right := container.NewVBox(drain, performance, thresholds, about)
	return container.NewScroll(container.NewGridWithColumns(2, left, right))
}
