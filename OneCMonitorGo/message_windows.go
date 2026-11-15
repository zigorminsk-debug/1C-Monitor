//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// showError показывает системное окно с сообщением: программа собрана без консоли,
// и до появления главного окна другого способа сообщить об ошибке нет.
func showError(title, text string) {
	titlePointer, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPointer, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	const iconError, setForeground = 0x10, 0x10000
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(textPointer)), uintptr(unsafe.Pointer(titlePointer)), iconError|setForeground)
}
