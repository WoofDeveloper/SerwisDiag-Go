package diag

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"serwisdiag/internal/win"
)

// Raport anonimowy (do pokazania na forum): bez danych identyfikujących komputer, użytkowników i klienta
// oraz bez wyników prywatnych. Wyrażenia .NET z warunkami "przed/po" (lookaround) zastąpiono sprawdzaniem znaków
// sąsiednich - silnik wyrażeń regularnych Go ich nie obsługuje.

var genericNames = []string{"administrator", "admin", "user", "użytkownik", "uzytkownik", "gość", "guest", "owner", "właściciel", "pc", "windows", "microsoft", "default", "public", "workgroup", "serwis"}

var serialColRx = rx(`seryjn|serial`)

// sensitiveTokens odpowiada Get-SdSensitiveTokens: napisy identyfikujące komputer, konta, sieć firmową i klienta.
func sensitiveTokens(r *Result) []string {
	var t []string
	for _, k := range []string{"ComputerName", "UserName", "Serial", "Ticket", "Customer", "Technician"} {
		t = append(t, metaStr(r.Meta, k))
	}
	t = append(t, os.Getenv("COMPUTERNAME"), os.Getenv("USERNAME"), os.Getenv("USERDOMAIN"), os.Getenv("USERDNSDOMAIN"))
	if cs, _ := win.WmiQuery(`root\cimv2`, "SELECT PartOfDomain,Domain FROM Win32_ComputerSystem", []string{"PartOfDomain", "Domain"}, 20*time.Second); len(cs) > 0 && cs[0].Bool("PartOfDomain") {
		t = append(t, cs[0].Str("Domain"))
	}
	if ents, err := os.ReadDir(filepath.Join(sysDrive()+`\`, "Users")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				t = append(t, e.Name())
			}
		}
	}
	for _, tb := range r.Tables {
		for _, col := range tb.Columns {
			if serialColRx.MatchString(col) {
				for _, rw := range tb.Rows {
					t = append(t, win.AnyString(rw.Get(col)))
				}
			}
		}
	}
	var out []string
	for _, s := range t {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) < 3 || containsi(genericNames, s) || isJunk(s) || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	// najpierw dłuższe: „Jan Kowalski” przed „Jan”
	psSort(out, func(a, b string) int { return utf8.RuneCountInString(b) - utf8.RuneCountInString(a) })
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func runeBefore(s string, i int) (rune, bool) {
	if i <= 0 {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r, true
}

func runeAfter(s string, i int) (rune, bool) {
	if i >= len(s) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r, true
}

// replaceBounded zamienia dopasowania, które spełniają warunek na znakach przed i po (zamiast lookaround).
func replaceBounded(s string, re *regexp.Regexp, ok func(before, after rune, hasB, hasA bool) bool, repl func(m string) string) string {
	var b strings.Builder
	pos := 0
	for pos <= len(s) {
		loc := re.FindStringIndex(s[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		rb, hb := runeBefore(s, start)
		ra, ha := runeAfter(s, end)
		if end > start && ok(rb, ra, hb, ha) {
			b.WriteString(s[pos:start])
			b.WriteString(repl(s[start:end]))
			pos = end
			continue
		}
		// brak dopasowania w tym miejscu - szukaj od następnego znaku
		_, w := utf8.DecodeRuneInString(s[start:])
		if w == 0 {
			w = 1
		}
		b.WriteString(s[pos : start+w])
		pos = start + w
	}
	if pos < len(s) {
		b.WriteString(s[pos:])
	}
	return b.String()
}

var (
	usersPathRx = regexp.MustCompile(`(?i)\\Users\\`)
	usersSkipRx = regexp.MustCompile(`(?i)^(?:(?:Public|Default|All Users)(?:[\\"\r\n]|$)|<)`)
	usersNameRx = regexp.MustCompile(`^[^\\"\r\n<>]+`)
	sidRx       = regexp.MustCompile(`S-1-5-21(?:-\d+){3,4}`)
	productKey  = regexp.MustCompile(`\b[A-Z0-9]{5}(?:-[A-Z0-9]{5}){4}\b`)
	emailRx     = regexp.MustCompile(`[\p{L}\p{N}_.+-]+@[\p{L}\p{N}_-]+(?:\.[\p{L}\p{N}_-]+)+`)
	macRx       = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	ipv6aRx     = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4}:){1,6}:(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,5})?(?:%[\p{L}\p{N}_]+)?`)
	ipv6bRx     = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4}:){4,7}[0-9a-f]{1,4}(?:%[\p{L}\p{N}_]+)?`)
	ipv4AnonRx  = regexp.MustCompile(`(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)`)
	ssidRx      = regexp.MustCompile(`(?im)(SSID[^:\r\n]*:[ \t]*)\S[^\r\n]*`)
	keepIPs     = []string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9", "127.0.0.1", "0.0.0.0"}
)

func isWordOrColon(r rune) bool { return isWordRune(r) || r == '_' || r == ':' }

// anonymizeText odpowiada ConvertTo-SdAnonymousText.
func anonymizeText(s string, tokens []string) string {
	if s == "" {
		return s
	}
	for _, tk := range tokens {
		if tk == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(tk))
		s = replaceBounded(s, re, func(b, a rune, hb, ha bool) bool { return !(hb && isWordRune(b)) && !(ha && isWordRune(a)) },
			func(string) string { return "<ukryte>" })
	}
	// nazwa profilu może zawierać spacje („Jan Kowalski”) - do najbliższego ukośnika; profile wspólne i już ukryte zostają
	var b strings.Builder
	pos := 0
	for {
		loc := usersPathRx.FindStringIndex(s[pos:])
		if loc == nil {
			b.WriteString(s[pos:])
			break
		}
		end := pos + loc[1]
		b.WriteString(s[pos:end])
		rest := s[end:]
		if !usersSkipRx.MatchString(rest) {
			if m := usersNameRx.FindStringIndex(rest); m != nil {
				b.WriteString("<użytkownik>")
				end += m[1]
			}
		}
		pos = end
	}
	s = b.String()
	s = sidRx.ReplaceAllString(s, "S-1-5-21-<ukryte>")
	s = productKey.ReplaceAllString(s, "XXXXX-XXXXX-XXXXX-XXXXX-XXXXX")
	s = emailRx.ReplaceAllString(s, "<e-mail>")
	s = macRx.ReplaceAllString(s, "XX-XX-XX-XX-XX-XX")
	notWordColon := func(b, a rune, hb, ha bool) bool { return !(hb && isWordOrColon(b)) && !(ha && isWordOrColon(a)) }
	s = replaceBounded(s, ipv6aRx, notWordColon, func(string) string { return "<IPv6>" })
	s = replaceBounded(s, ipv6bRx, notWordColon, func(string) string { return "<IPv6>" })
	s = replaceBounded(s, ipv4AnonRx, func(b, a rune, hb, ha bool) bool {
		return !(hb && (unicode.IsDigit(b) || b == '.')) && !(ha && (unicode.IsDigit(a) || a == '.'))
	}, func(m string) string {
		// adresy testowe (publiczne DNS), pętla zwrotna i maski podsieci niczego nie zdradzają
		if slices.Contains(keepIPs, m) || strings.HasPrefix(m, "255.") {
			return m
		}
		return "x.x.x.x"
	})
	return ssidRx.ReplaceAllString(s, "${1}<ukryte>")
}

var anonMetaKeys = []string{"Model", "OS", "Cpu", "Ram", "Disks", "Gpu", "Bios", "Board", "Chassis", "BatteryHealth", "Stability", "GoVersion", "ToolVersion", "Offline", "IsAdmin", "Knowledge", "Integrity"}

// anonymousResult odpowiada New-SdAnonymousResult.
func anonymousResult(r *Result) *Result {
	tokens := sensitiveTokens(r)
	A := func(v any) any {
		if s, ok := v.(string); ok {
			return anonymizeText(s, tokens)
		}
		return v
	}
	S := func(s string) string { return anonymizeText(s, tokens) }
	meta := map[string]any{}
	for _, k := range anonMetaKeys {
		if v, ok := r.Meta[k]; ok {
			meta[k] = A(v)
		}
	}
	var fs []Finding
	for _, f := range r.Findings {
		if f.Private {
			continue
		}
		fs = append(fs, Finding{CheckID: f.CheckID, Category: f.Category, Title: S(f.Title), Status: f.Status, Value: S(f.Value), Details: S(f.Details), Advice: S(f.Advice), Time: f.Time, LastEvent: f.LastEvent})
	}
	var ts []Table
	for _, t := range r.Tables {
		if t.Private {
			continue
		}
		var rows []Row
		for _, rw := range t.Rows {
			nr := make(Row, 0, len(t.Columns))
			for _, col := range t.Columns {
				v := rw.Get(col)
				if v == nil {
					v = ""
				}
				nr = append(nr, Cell{col, A(v)})
			}
			rows = append(rows, nr)
		}
		ts = append(ts, Table{Category: t.Category, Name: S(t.Name), Columns: t.Columns, Rows: rows, Note: S(t.Note)})
	}
	c := countFindings(fs)
	return &Result{Tool: r.Tool, Version: r.Version, Generated: r.Generated, Started: r.Started, Finished: r.Finished, Level: r.Level, Tests: r.Tests,
		IsAdmin: r.IsAdmin, Meta: meta, Counts: c, Verdict: verdictFor(c), Findings: fs, Tables: ts, Anonymous: true}
}

// SaveAnonymousReports odpowiada Save-SdAnonymousReports: HTML i krótkie podsumowanie tekstowe; w nazwie pliku
// nie ma nazwy komputera ani numeru zlecenia.
func SaveAnonymousReports(r *Result, outDir string) (htmlPath, textPath string, err error) {
	anon := anonymousResult(r)
	if err = os.MkdirAll(outDir, 0o755); err != nil {
		return
	}
	base := filepath.Join(outDir, "raport-anonimowy_"+time.Now().Format("20060102-1504"))
	htmlPath, textPath = base+".html", base+".txt"
	if err = writeText(htmlPath, HTMLReport(anon, false, Settings{}), true); err != nil {
		return
	}
	err = writeText(textPath, TextReport(anon, Settings{ShopName: "raport anonimowy"}, true), true)
	return
}

// ---------------------------------------------------------------- PDF (ConvertTo-SdPdf)

func findPdfBrowser() string {
	var cands []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base != "" {
			cands = append(cands, filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
		}
	}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if base != "" {
			cands = append(cands, filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// FormatCmdArg odpowiada Format-SdCmdArg: cytowanie argumentu wg reguł CommandLineToArgvW.
func FormatCmdArg(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\v\"") {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	bs := 0
	for _, ch := range v {
		switch ch {
		case '\\':
			bs++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*bs+1))
			b.WriteByte('"')
			bs = 0
			continue
		}
		if bs > 0 {
			b.WriteString(strings.Repeat(`\`, bs))
			bs = 0
		}
		b.WriteRune(ch)
	}
	b.WriteString(strings.Repeat(`\`, 2*bs))
	b.WriteByte('"')
	return b.String()
}

// SavePDF zapisuje raport HTML jako PDF (A4) przeglądarką bez okna, z osobnym tymczasowym profilem.
func SavePDF(htmlPath string) (string, error) {
	pdf := strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)) + ".pdf"
	browser := findPdfBrowser()
	if browser == "" {
		return pdf, errText("Nie znaleziono przeglądarki Microsoft Edge ani Google Chrome. Otwórz raport i wydrukuj go do pliku: Ctrl+P, drukarka „Microsoft Print to PDF”.")
	}
	profile := filepath.Join(os.TempDir(), "sd-pdf-"+time.Now().Format("20060102150405.000000000"))
	defer func() {
		for i := 0; i < 5; i++ {
			if os.RemoveAll(profile) == nil {
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
	}()
	os.Remove(pdf)
	abs, _ := filepath.Abs(htmlPath)
	url := "file:///" + strings.ReplaceAll(abs, `\`, "/")
	args := []string{"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-sync",
		"--no-pdf-header-footer", "--print-to-pdf-no-header", FormatCmdArg("--user-data-dir=" + profile), FormatCmdArg("--print-to-pdf=" + pdf), FormatCmdArg(url)}
	r := win.RunRaw(90*time.Second, browser, FormatCmdArg(browser)+" "+strings.Join(args, " "))
	if st, err := os.Stat(pdf); err == nil && st.Size() > 0 {
		return pdf, nil
	}
	msg := "Przeglądarka nie zapisała pliku PDF."
	if r.TimedOut {
		msg = "Przeglądarka nie zapisała PDF w ciągu 90 s."
	}
	return pdf, errText(msg + " Otwórz raport i wydrukuj go do pliku: Ctrl+P, drukarka „Microsoft Print to PDF”.")
}

type errText string

func (e errText) Error() string { return string(e) }
