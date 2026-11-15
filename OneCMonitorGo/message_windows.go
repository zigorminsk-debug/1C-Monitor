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

// openGLLibrary возвращает путь к opengl32.dll, загруженной в процесс: по нему видно, работает
// программа с системным OpenGL или с Mesa3D. Пустая строка — библиотека не загружена.
func openGLLibrary() string {
	name, err := syscall.UTF16PtrFromString("opengl32.dll")
	if err != nil {
		return ""
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	handle, _, _ := kernel.NewProc("GetModuleHandleW").Call(uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return ""
	}
	buffer := make([]uint16, 1024)
	length, _, _ := kernel.NewProc("GetModuleFileNameW").Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 || int(length) > len(buffer) {
		return ""
	}
	return syscall.UTF16ToString(buffer[:length])
}
