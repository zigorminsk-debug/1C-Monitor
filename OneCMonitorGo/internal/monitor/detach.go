package monitor

import "os/exec"

// startDetachedProcess запускает программу без окна и не ждёт её завершения. Канал получает код
// возврата, если программа завершилась. Процесс продолжает работать после выхода из монитора.
func startDetachedProcess(exe string, args []string) (<-chan int, error) {
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState.ExitCode()
	}()
	return exited, nil
}
