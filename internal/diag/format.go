package diag

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"serwisdiag/internal/win"
)

// Formatowanie zawsze po polsku i dokładnie tak jak .NET Framework w Windows PowerShell 5.1 (wersja PowerShell
// programu): separator tysięcy to twarda spacja (U+00A0), przecinek dziesiętny, zaokrąglanie "połówka od zera"
// liczone na 15 cyfrach znaczących (np. 2,5 -> 3; 1,005 -> 1,01).

const nbsp = " "

// fmtNum odpowiada Format-Num: ToString("N<cyfry>", pl-PL).
func fmtNum(v float64, digits int) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 0) {
		if v > 0 {
			return "∞"
		}
		return "-∞"
	}
	neg := v < 0
	// 15 cyfr znaczących, jak w .NET Framework
	e := strconv.FormatFloat(math.Abs(v), 'e', 14, 64) // d.dddddddddddddde±XX
	mant := e[:1] + e[2:16]
	exp, _ := strconv.Atoi(e[17:])
	// cyfry przed i po przecinku
	var intPart, frac string
	pointPos := exp + 1 // liczba cyfr części całkowitej
	switch {
	case pointPos <= 0:
		intPart = "0"
		frac = strings.Repeat("0", -pointPos) + mant
	case pointPos >= len(mant):
		intPart = mant + strings.Repeat("0", pointPos-len(mant))
		frac = ""
	default:
		intPart = mant[:pointPos]
		frac = mant[pointPos:]
	}
	// zaokrąglenie do digits miejsc: połówka od zera
	if len(frac) < digits {
		frac += strings.Repeat("0", digits-len(frac))
	}
	roundUp := len(frac) > digits && frac[digits] >= '5'
	frac = frac[:digits]
	if roundUp {
		b := []byte(intPart + frac)
		i := len(b) - 1
		for ; i >= 0; i-- {
			if b[i] == '9' {
				b[i] = '0'
				continue
			}
			b[i]++
			break
		}
		s := string(b)
		if i < 0 {
			s = "1" + s
		}
		intPart, frac = s[:len(s)-digits], s[len(s)-digits:]
	}
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	// grupy tysięcy
	var sb strings.Builder
	if neg && strings.Trim(intPart+frac, "0") != "" {
		sb.WriteByte('-')
	}
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			sb.WriteString(nbsp)
		}
		sb.WriteRune(ch)
	}
	if digits > 0 {
		sb.WriteByte(',')
		sb.WriteString(frac)
	}
	return sb.String()
}

// fmtNumAny: jak Format-Num dla dowolnej wartości - pusta wartość to "-".
func fmtNumAny(v any, digits int) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		if strings.TrimSpace(x) == "" {
			return "-"
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return x
		}
		return fmtNum(f, digits)
	}
	return fmtNum(win.AnyFloat(v), digits)
}

// fmtSize odpowiada Format-Size (jednostki 1024, bajty bez miejsc po przecinku).
func fmtSize(b float64, digits int) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(b) >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmtNum(b, 0) + " B"
	}
	return fmtNum(b, digits) + " " + units[i]
}

// fmtDt odpowiada Format-Dt: "2026-10-26 21:05" albo "-" dla pustej daty.
func fmtDt(t time.Time) string {
	if t.IsZero() || t.Year() <= 1601 {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

// fmtDate odpowiada Format-Dt -DateOnly.
func fmtDate(t time.Time) string {
	if t.IsZero() || t.Year() <= 1601 {
		return "-"
	}
	return t.Format("2006-01-02")
}

// psRound: [math]::Round i rzutowanie [int] w .NET zaokrąglają połówki do parzystej.
func psRound(x float64) float64 { return math.RoundToEven(x) }

// fmtSpan odpowiada Format-Span.
func fmtSpan(d time.Duration) string {
	days := d.Hours() / 24
	switch {
	case days >= 1:
		return strconv.Itoa(int(math.Floor(days))) + " d " + strconv.Itoa(int(d.Hours())%24) + " h"
	case d.Hours() >= 1:
		return strconv.Itoa(int(math.Floor(d.Hours()))) + " h " + strconv.Itoa(int(d.Minutes())%60) + " min"
	case d.Minutes() < 1:
		return strconv.Itoa(int(psRound(d.Seconds()))) + " s"
	}
	return strconv.Itoa(int(math.Floor(d.Minutes()))) + " min " + strconv.Itoa(int(d.Seconds())%60) + " s"
}

// joinNonEmpty odpowiada Join-NonEmpty: pomija puste, przycina, łączy separatorem.
func joinNonEmpty(sep string, items ...string) string {
	var out []string
	for _, s := range items {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, sep)
}

// limitText odpowiada Limit-Text (długość w znakach, wielokropek na końcu).
func limitText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// plural: polskie formy liczby mnogiej (1 zdarzenie, 2 zdarzenia, 5 zdarzeń, 22 zdarzenia, 12 zdarzeń).
func plural(n int64, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	if n == 1 {
		return one
	}
	m10, m100 := n%10, n%100
	if m10 >= 2 && m10 <= 4 && !(m100 >= 12 && m100 <= 14) {
		return few
	}
	return many
}

func fmtCount(n int, one, few, many string) string {
	return strconv.Itoa(n) + " " + plural(int64(n), one, few, many)
}

var junkRx = regexp.MustCompile(`(?i)^(to be filled by o\.?e\.?m\.?|default string|system serial number|system product name|system manufacturer|system version|not specified|not applicable|none|n/a|0+|o\.e\.m\.?|123456789|unknown|ffffffff.*)$`)

// isJunk odpowiada Test-SdJunkString: wartości-śmieci wpisywane przez producentów płyt i BIOS-ów.
func isJunk(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	return junkRx.MatchString(s)
}

// cleanStr odpowiada Get-SdCleanString.
func cleanStr(s, fallback string) string {
	if isJunk(s) {
		return fallback
	}
	return strings.TrimSpace(s)
}

// firstLine odpowiada Get-SdFirstLine: pierwszy niepusty wiersz, skrócony.
func firstLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return limitText(t, max)
		}
	}
	return ""
}

// eqi: porównanie napisów bez względu na wielkość liter (jak -eq w PowerShellu).
func eqi(a, b string) bool { return strings.EqualFold(a, b) }

// containsi: jak -contains dla napisów.
func containsi(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// rx kompiluje wyrażenie bez rozróżniania wielkości liter (operator -match w PowerShellu).
func rx(pattern string) *regexp.Regexp { return regexp.MustCompile(`(?i)` + pattern) }
