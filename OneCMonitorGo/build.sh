#!/bin/sh
# Сборка 1C Server Monitor (Go + Fyne) для Windows.
#   ./build.sh            — обе разрядности, кросс-компиляция через zig cc
#   CC=gcc ./build.sh 64  — одна разрядность, компилятор из переменной CC (например MinGW-w64)
# Нужны: Go 1.23+, компилятор C для Windows (Fyne использует cgo). Файлы rsrc_windows_*.syso
# (значок и манифест) уже лежат в каталоге; пересоздать: REBUILD_RESOURCES=1 ./build.sh
# (нужен github.com/akavel/rsrc).
set -eu
cd "$(dirname "$0")"

build() { # $1 — GOARCH, $2 — цель zig, $3 — суффикс каталога
	if [ "${REBUILD_RESOURCES:-}" = 1 ]; then
		rsrc -arch "$1" -ico ../OneCMonitor/Assets/app.ico -manifest winres/app.manifest -o "rsrc_windows_$1.syso"
	fi
	target="../OneCServerMonitorGo-win-$3"
	mkdir -p "$target"
	CGO_ENABLED=1 GOOS=windows GOARCH="$1" CC="${CC:-zig cc -target $2}" CXX="${CXX:-zig c++ -target $2}" \
		go build -trimpath -ldflags "-H windowsgui -s -w -extldflags=-Wl,--subsystem,windows" -o "$target/1C-Server-Monitor-Go.exe" .
	echo "готово: $target/1C-Server-Monitor-Go.exe"
}

case "${1:-all}" in
64) build amd64 x86_64-windows-gnu x64 ;;
32) build 386 x86-windows-gnu x86 ;;
*)
	build amd64 x86_64-windows-gnu x64
	build 386 x86-windows-gnu x86
	;;
esac
