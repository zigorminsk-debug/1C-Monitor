package monitor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// DecodeRac декодирует вывод rac.exe. В зависимости от релиза платформы, способа запуска и
// системной локали это UTF-8, OEM 866 или Windows-1251. Сначала строго проверяется UTF-8,
// затем из двух русских кодировок выбирается вариант с наиболее правдоподобным текстом.
func DecodeRac(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if utf8.Valid(data) {
		return strings.TrimPrefix(string(data), "\uFEFF")
	}
	cp866, _ := charmap.CodePage866.NewDecoder().Bytes(data)
	cp1251, _ := charmap.Windows1251.NewDecoder().Bytes(data)
	if russianTextScore(string(cp1251)) > russianTextScore(string(cp866)) {
		return string(cp1251)
	}
	return string(cp866)
}

var frequentRussianTokens = []string{"ов", "ен", "ер", "на", "ни", "ра", "ко", "ст", "ть", "ий", "польз", "админ"}

func russianTextScore(text string) int {
	score := 0
	for _, c := range text {
		switch {
		case (c >= 'А' && c <= 'я') || c == 'Ё' || c == 'ё':
			score += 4
		case c == utf8.RuneError || (unicode.IsControl(c) && c != '\r' && c != '\n' && c != '\t'):
			score -= 20
		case c >= 0x2500 && c <= 0x259F: // псевдографика при ошибочном выборе CP866
			score -= 8
		case c >= 0x0080 && c <= 0x00BF:
			score -= 2
		}
	}
	// Частые сочетания дополнительно отличают русский текст от случайного набора букв.
	lower := strings.ToLower(text)
	for _, token := range frequentRussianTokens {
		score += strings.Count(lower, token) * 3
	}
	return score
}
