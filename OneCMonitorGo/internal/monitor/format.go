package monitor

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// num форматирует число с фиксированным количеством знаков и десятичной запятой,
// как это принято в русскоязычном интерфейсе.
func num(v float64, decimals int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', decimals, 64), ".", ",", 1)
}

// numInvariant — формат с точкой для CSV-файлов истории.
func numInvariant(v float64, decimals int) string {
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

// FormatMb выводит объём памяти в МБ или ГБ.
func FormatMb(mb float64) string {
	if mb >= 1024 {
		return num(mb/1024, 2) + " ГБ"
	}
	return num(mb, 0) + " МБ"
}

// FormatRate выводит скорость в КБ/с, МБ/с или ГБ/с.
func FormatRate(bytes float64) string {
	switch {
	case bytes >= 1073741824:
		return num(bytes/1073741824, 2) + " ГБ/с"
	case bytes >= 1048576:
		return num(bytes/1048576, 1) + " МБ/с"
	default:
		return num(bytes/1024, 0) + " КБ/с"
	}
}

// diskQueueLabel не выдаёт нулевую очередь за достоверную: некоторые драйверы SAN/RAID
// и виртуальные диски не сообщают её в Windows PDH даже при интенсивном вводе-выводе.
func diskQueueLabel(d DiskSnapshot) string {
	if d.QueueLength < 0.005 && d.UsagePercent >= 90 && d.ReadBytesPerSec+d.WriteBytesPerSec >= 1048576 {
		return "0,00 (возможно, драйвер SAN/RAID не сообщает)"
	}
	return num(d.QueueLength, 2)
}

func csvQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func h(s string) string { return html.EscapeString(s) }

func itoa(n int) string { return strconv.Itoa(n) }

func plural(format string, args ...any) string { return fmt.Sprintf(format, args...) }
