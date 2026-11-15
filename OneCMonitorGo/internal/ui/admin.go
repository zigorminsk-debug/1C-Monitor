package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
)

func (u *UI) adminTab() fyne.CanvasObject {
	var buttons []*widget.Button
	action := func(label string, importance widget.Importance, run func()) *widget.Button {
		button := widget.NewButton(label, func() { go run() })
		button.Importance = importance
		buttons = append(buttons, button)
		return button
	}

	title := heading("ОПАСНЫЕ АДМИНИСТРАТИВНЫЕ ДЕЙСТВИЯ")
	title.Importance = widget.DangerImportance
	confirmation := widget.NewEntry()
	confirmation.SetPlaceHolder("Контрольная фраза")
	confirmation.OnChanged = u.m.SetAdminConfirmation
	status := boldLabel(u.view.AdminActionStatus)
	status.Wrapping = fyne.TextWrapWord
	status.Importance = widget.WarningImportance
	warning := card(container.NewVBox(title,
		wrapLabel("Завершение сеансов прерывает работу пользователей. Перезапуск служб останавливает весь кластер на этом сервере. Принудительное завершение rphost может оборвать транзакции и вызвать откат данных. Используйте только в согласованное окно и запускайте программу от имени администратора."),
		note("Для операций с сеансами и службами введите: "+monitor.ConfirmPhrase+". Для принудительного завершения rphost: "+monitor.ConfirmKillPhrase),
		container.NewGridWithColumns(2, confirmation), status))

	sessions := card(container.NewVBox(heading("Управление сеансами через RAC"),
		container.NewGridWithColumns(4,
			action("Завершить все сеансы", widget.DangerImportance, u.m.TerminateAllSessions),
			action("Выгрузить всех, кроме Конфигуратора", widget.WarningImportance, u.m.TerminateExceptConfigurator),
			action("Удалить ранние дубликаты", widget.MediumImportance, u.m.TerminateDuplicateSessions),
			action("Завершить спящие сеансы", widget.MediumImportance, u.m.TerminateSleepingSessions)),
		note("Дубликаты определяются по базе, пользователю, компьютеру и приложению. Сохраняется самый новый сеанс, более ранние завершаются. Фоновые задания и служебные соединения (COM, веб-сервисы, консоль кластера) дубликатами не считаются.")))

	jobs := card(container.NewVBox(heading("Фоновые и регламентные задания"),
		container.NewGridWithColumns(4,
			action("Выключить фоновые задания", widget.WarningImportance, u.m.DisableBackgroundJobs),
			action("Включить фоновые задания", widget.MediumImportance, u.m.EnableBackgroundJobs)),
		note("Выключение устанавливает scheduled-jobs-deny=on во всех найденных базах и завершает уже работающие фоновые сеансы. Включение снимает запрет только с баз, которые были выключены этим монитором; изначально запрещённые базы не затрагиваются. Требуется фраза "+monitor.ConfirmPhrase+".")))

	services := card(container.NewVBox(heading("Службы и процессы Windows"),
		container.NewGridWithColumns(4,
			action("Плавно вывести выбранный rphost", widget.MediumImportance, u.m.GracefulTurnOffSelected),
			action("Перезапустить ВСЕ службы сервера 1С", widget.WarningImportance, u.m.RestartOneCServices),
			action("Принудительно завершить ВСЕ rphost", widget.DangerImportance, u.m.KillAllRphost)),
		note("Для плавного вывода сначала выберите строку процесса на вкладке «Обзор». Команда process turn-off прекращает назначение новой нагрузки, но не убивает текущие вызовы. Службы определяются по ragent.exe в PathName. Красная кнопка rphost выполняет taskkill /F /IM rphost.exe.")))

	resets := u.view.ConfirmationResets
	busy := false
	u.onUpdate(func(view monitor.View) {
		setText(status, view.AdminActionStatus)
		if view.ConfirmationResets != resets {
			resets = view.ConfirmationResets
			confirmation.SetText("")
		}
		if view.AdminBusy != busy {
			busy = view.AdminBusy
			for _, button := range buttons {
				if busy {
					button.Disable()
				} else {
					button.Enable()
				}
			}
		}
	})
	// Прокрутка в обе стороны: окно можно уменьшить, не ограничиваясь шириной ряда кнопок.
	return container.NewScroll(container.NewVBox(warning, sessions, jobs, services))
}
