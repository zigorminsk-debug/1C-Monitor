package monitor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunFunc запускает внешнюю программу и возвращает её стандартный вывод.
type RunFunc func(exe string, args []string, timeout time.Duration) (string, error)

// RunProcess запускает программу без окна консоли, ждёт не дольше timeout и декодирует вывод
// (UTF-8, CP866 или Windows-1251). При ненулевом коде возврата ошибкой становится текст stderr.
func RunProcess(exe string, args []string, timeout time.Duration) (string, error) {
	name := filepath.Base(exe)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Каналы вывода может удерживать дочерний процесс; без ограничения чтение ждало бы его бесконечно.
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Превышено время ожидания %s: %.0f с", name, timeout.Seconds())
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return "", fmt.Errorf("%s завершился, но не закрыл потоки вывода", name)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if message := strings.TrimSpace(DecodeRac(stderr.Bytes())); message != "" {
				return "", errors.New(message)
			}
			return "", fmt.Errorf("%s завершился с кодом %d", name, exit.ExitCode())
		}
		return "", fmt.Errorf("Не удалось запустить %s: %v", name, err)
	}
	return DecodeRac(stdout.Bytes()), nil
}
