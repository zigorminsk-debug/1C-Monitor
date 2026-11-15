//go:build !windows

package monitor

import "os/exec"

// NewSystem возвращает реализацию System для текущей платформы.
func NewSystem() System { return GenericSystem{} }

func hideWindow(*exec.Cmd) {}
