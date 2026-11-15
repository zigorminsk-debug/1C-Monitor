// 1C Server Monitor — монитор сервера «1С:Предприятие» для Windows (Go + Fyne).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/monitor"
	"github.com/zigorminsk-debug/1C-Monitor/OneCMonitorGo/internal/ui"
)

const windowTitle = "1C Server Monitor"

// graphicsHelp показывается, когда Windows не может создать окно OpenGL (типично для сеанса RDP
// и виртуальных машин без драйвера видеокарты).
const graphicsHelp = "Не удалось создать окно программы: для интерфейса нужен OpenGL 2.1, а текущий сеанс Windows его не предоставляет " +
	"(так бывает при подключении по RDP и на виртуальных машинах без драйвера видеокарты).\n\n" +
	"Что можно сделать:\n" +
	"1. Положить рядом с программой файл opengl32.dll из набора Mesa3D для Windows (программная отрисовка, сборка той же разрядности, что и программа) и запустить снова.\n" +
	"2. Либо разрешить аппаратную графику в сеансах RDP: gpedit.msc → Конфигурация компьютера → Административные шаблоны → Компоненты Windows → " +
	"Службы удалённых рабочих столов → Узел сеансов удалённых рабочих столов → Среда удалённых сеансов → «Использовать аппаратные графические адаптеры для всех сеансов служб удалённых рабочих столов».\n" +
	"3. Либо запустить программу в консольном сеансе сервера или использовать версию на .NET (каталог OneCMonitor).\n\n" +
	"Сведения об ошибке: "

// startupLog принимает сообщения Fyne и стандартного журнала: записывает их в файл и замечает
// ошибку создания окна. Без окна работать нечему, поэтому после пояснения процесс завершается.
type startupLog struct {
	mu      sync.Mutex
	file    *os.File
	failure bool
	shown   bool
}

func (l *startupLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.Write(data)
	}
	text := strings.TrimSpace(string(data))
	switch {
	case l.shown:
	case l.failure:
		// следующая строка после сообщения об ошибке содержит причину
		l.shown = true
		showError(windowTitle, graphicsHelp+text)
		if l.file != nil {
			_ = l.file.Close()
		}
		os.Exit(1)
	case strings.Contains(text, "window creation error") || strings.Contains(text, "failed to initialise GLFW"):
		l.failure = true
	}
	return len(data), nil
}

func main() {
	dataDir := monitor.DefaultDataDir()
	_ = os.MkdirAll(dataDir, 0o755)

	// Аварийное завершение (в том числе в фоновых горутинах) записывается в crash.log.
	crashPath := filepath.Join(dataDir, "crash.log")
	if crash, err := os.OpenFile(crashPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		fmt.Fprintf(crash, "\n=== запуск %s ===\n", time.Now().Format("02.01.2006 15:04:05"))
		_ = debug.SetCrashOutput(crash, debug.CrashOptions{})
	}
	messages := &startupLog{}
	if file, err := os.OpenFile(filepath.Join(dataDir, "interface.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		messages.file = file
	}
	log.SetOutput(messages)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic: %v\n%s", r, debug.Stack())
			showError(windowTitle, fmt.Sprintf("Программа завершилась с ошибкой: %v\n\nПодробности: %s", r, filepath.Join(dataDir, "interface.log")))
			os.Exit(2)
		}
	}()

	application := app.NewWithID("by.csl.onec-server-monitor")
	application.Settings().SetTheme(ui.NewTheme())
	application.SetIcon(ui.Icon)
	window := application.NewWindow(windowTitle)
	window.SetMaster()
	window.SetIcon(ui.Icon)

	m := monitor.New(monitor.Options{DataDir: dataDir})
	view := ui.New(application, window, m)
	window.Resize(fyne.NewSize(1450, 900))
	window.CenterOnScreen()
	window.SetOnClosed(func() {
		m.SetOnChange(nil)
		m.SaveSettings()
		m.Close()
	})
	view.Attach()
	m.Start()
	window.ShowAndRun()
}
