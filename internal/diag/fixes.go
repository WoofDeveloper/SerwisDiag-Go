package diag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"serwisdiag/internal/win"
)

// Naprawy po diagnostyce (lib\Fixes.ps1): problemy, które program może usunąć sam, z wyborem technika.
// Zasady jak w wersji PowerShell: nic nie dzieje się bez zaznaczenia i potwierdzenia; przed zmianami punkt
// przywracania; każda zmiana trafia do dziennika napraw z poprzednią wartością, a zmiany ustawień (rejestr, usługi,
// zapora, SMB1) także do dziennika JSON, z którego program potrafi je cofnąć; usterek sprzętowych program nie
// "naprawia". Dzienniki są zgodne z wersją PowerShell - jedna wersja cofa zmiany drugiej.

// Fix - naprawa z katalogu (Register-Fix).
type Fix struct {
	ID, Group, Title, Description string
	Order                         int
	Risk                          string // low | medium | high
	Covers                        []string
	NeedsAdmin, NeedsInternet     bool
	Reboot, Long, Interactive     bool
	DefaultOff                    bool
	// Detect tylko czyta stan systemu; każda zwrócona pozycja to osobna naprawa na liście.
	Detect func(fc *FixContext) []FixItem
	// Apply wykonuje naprawę; błąd = niepowodzenie. Każdą zmianę zapisuje przez FixRun (dziennik i cofanie).
	Apply func(r *FixRun, data any) (FixOutcome, error)
}

// FixItem - pozycja zwrócona przez Detect.
type FixItem struct {
	Key, Title, Problem, CustomerTitle string
	Data                               any
	Default                            *bool // nil - jak w katalogu (DefaultOff)
}

// FixOutcome - wynik Apply: Result pusty = ok; Reboot nil - jak w katalogu.
type FixOutcome struct {
	Result  string
	Message string
	Reboot  *bool
}

func done(msg string) (FixOutcome, error) { return FixOutcome{Message: msg}, nil }

func boolp(b bool) *bool { return &b }

// FixCandidate - pozycja listy napraw (Get-SdFixCandidates).
type FixCandidate struct {
	FixID, Key, Group, Title, CustomerTitle string
	Problem, Description, Risk              string
	Reboot, Long, Interactive               bool
	NeedsAdmin, NeedsInternet               bool
	Covers                                  []string
	Default                                 bool
	Blocked                                 string
	Order                                   int
	Data                                    any
}

// FixResult - wynik jednej naprawy.
type FixResult struct {
	Title, CustomerTitle, FixID string
	Result                      string // ok | warn | error | skip
	Message                     string
	Reboot                      bool
	Changes                     []string
	Undo                        []UndoRecord
}

// FixContext - kontekst wykrywania (@{ Result; IsAdmin; Offline }); c - pomocnicze dane systemu (pamięć podręczna).
type FixContext struct {
	Result  *Result
	IsAdmin bool
	Offline bool
	// Log - błędy wykrywania (Write-SdLog ... 'WARN').
	Log []string
	c   *Ctx
}

// NewFixContext przygotowuje wykrywanie napraw dla wyniku diagnostyki.
func NewFixContext(res *Result, isAdmin, offline bool) *FixContext {
	s := NewScan(Options{Offline: offline})
	return &FixContext{Result: res, IsAdmin: isAdmin, Offline: offline, c: &Ctx{s: s, r: &checkRun{}}}
}

// findings odpowiada Get-SdCtxFindings: wyniki testu (opcjonalnie o tytule pasującym do wyrażenia).
func (fc *FixContext) findings(checkID, titleRx string) []Finding {
	if fc.Result == nil {
		return nil
	}
	var re *regexp.Regexp
	if titleRx != "" {
		re = rx(titleRx)
	}
	var out []Finding
	for _, f := range fc.Result.Findings {
		if eqi(f.CheckID, checkID) && (re == nil || re.MatchString(f.Title)) {
			out = append(out, f)
		}
	}
	return out
}

func isProblem(f Finding) bool { return f.Status == Warn || f.Status == Crit }

// problem odpowiada Test-SdCtxProblem.
func (fc *FixContext) problem(checkID, titleRx string) bool {
	return slices.ContainsFunc(fc.findings(checkID, titleRx), isProblem)
}

// first zwraca pierwszy wynik spełniający warunek.
func (fc *FixContext) first(checkID, titleRx string, cond func(Finding) bool) (Finding, bool) {
	for _, f := range fc.findings(checkID, titleRx) {
		if cond(f) {
			return f, true
		}
	}
	return Finding{}, false
}

func fixByID(id string) *Fix {
	for _, f := range fixCatalog {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// Fixes zwraca katalog napraw w kolejności wykonywania.
func Fixes() []*Fix {
	s := slices.Clone(fixCatalog)
	slices.SortStableFunc(s, func(a, b *Fix) int { return a.Order - b.Order })
	return s
}

// FixCandidates odpowiada Get-SdFixCandidates: naprawy dla bieżącego stanu komputera.
func FixCandidates(fc *FixContext) []FixCandidate {
	var out []FixCandidate
	for _, f := range Fixes() {
		items := func() (items []FixItem) {
			defer func() {
				if p := recover(); p != nil {
					fc.Log = append(fc.Log, fmt.Sprintf("Naprawa %s - wykrywanie: %v", f.ID, p))
					items = nil
				}
			}()
			return f.Detect(fc)
		}()
		for _, it := range items {
			blocked := ""
			switch {
			case f.NeedsAdmin && !fc.IsAdmin:
				blocked = "wymaga uprawnień administratora"
			case f.NeedsInternet && fc.Offline:
				blocked = "wymaga internetu (włączony tryb bez internetu)"
			}
			def := !f.DefaultOff
			if it.Default != nil {
				def = *it.Default
			}
			title := f.Title
			if it.Title != "" {
				title = it.Title
			}
			out = append(out, FixCandidate{FixID: f.ID, Key: f.ID + "|" + it.Key, Group: f.Group, Title: title, CustomerTitle: it.CustomerTitle,
				Problem: it.Problem, Description: f.Description, Risk: f.Risk, Reboot: f.Reboot, Long: f.Long, Interactive: f.Interactive,
				NeedsAdmin: f.NeedsAdmin, NeedsInternet: f.NeedsInternet, Covers: f.Covers, Default: def && blocked == "", Blocked: blocked,
				Order: f.Order, Data: it.Data})
		}
	}
	return out
}

// UnfixedProblems odpowiada Get-SdUnfixedProblems: problemy z diagnostyki (krytyczne i ostrzeżenia), których żadna
// proponowana naprawa nie obejmuje - wymagają serwisanta.
func UnfixedProblems(res *Result, cands []FixCandidate) []Finding {
	if res == nil {
		return nil
	}
	var covers []string
	for _, c := range cands {
		for _, cv := range c.Covers {
			if cv != "" {
				covers = append(covers, cv)
			}
		}
	}
	var out []Finding
	for _, f := range res.Findings {
		if !isProblem(f) {
			continue
		}
		hit := false
		for _, cv := range covers {
			id, titleRx, hasRx := strings.Cut(cv, "|")
			if eqi(f.CheckID, id) && (!hasRx || rx(titleRx).MatchString(f.Title)) {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, f)
		}
	}
	return out
}

// CreateRepairRestorePoint odpowiada New-SdRepairRestorePoint: punkt przywracania przed naprawami (okno i konsola).
// Windows tworzy najwyżej jeden taki punkt na 24 godziny.
func CreateRepairRestorePoint() error {
	return win.CreateRestorePoint("SerwisDiag - przed naprawami", win.RestoreModifySettings)
}

// RunFixes odpowiada Invoke-SdFixes: wykonuje wybrane naprawy po kolei (wg kolejności z katalogu). status (może być
// nil) dostaje numer, liczbę i tytuł bieżącej naprawy, a na końcu pusty tytuł.
func RunFixes(cands []FixCandidate, status func(i, n int, title string)) []FixResult {
	list := slices.Clone(cands)
	slices.SortStableFunc(list, func(a, b FixCandidate) int {
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		return cmpText(a.Key, b.Key)
	})
	results := make([]FixResult, 0, len(list))
	for i, c := range list {
		if status != nil {
			status(i+1, len(list), c.Title)
		}
		r := FixResult{Title: c.Title, CustomerTitle: c.CustomerTitle, FixID: c.FixID, Result: "ok"}
		f := fixByID(c.FixID)
		switch {
		case c.Blocked != "":
			r.Result, r.Message = "skip", "Pominięto: "+c.Blocked
		case f == nil:
			r.Result, r.Message = "error", "Nieznana naprawa: "+c.FixID
		default:
			run := &FixRun{}
			if status != nil {
				run.Status = func(text string) { status(i+1, len(list), text) }
			}
			out, err := run.apply(f, c.Data)
			r.Reboot = f.Reboot
			if err != nil {
				r.Result, r.Message, r.Reboot = "error", err.Error(), false
			} else {
				if out.Result != "" {
					r.Result = out.Result
				}
				r.Message = out.Message
				if out.Reboot != nil {
					r.Reboot = *out.Reboot
				}
			}
			// zmiany sprzed błędu też trafiają do dziennika - da się je cofnąć
			r.Changes, r.Undo = run.log, run.undo
		}
		results = append(results, r)
	}
	if status != nil {
		status(len(list), len(list), "")
	}
	return results
}

// ---------------------------------------------------------------- wykonanie naprawy: dziennik zmian i rekordy cofania

// FixRun - wykonanie jednej naprawy: dziennik zmian (poprzednia -> nowa wartość) i rekordy cofania.
type FixRun struct {
	log  []string
	undo []UndoRecord
	// Status - opis bieżącego kroku długiej naprawy (sterowniki); może być nil.
	Status func(text string)
}

func (r *FixRun) apply(f *Fix, data any) (out FixOutcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("błąd programu: %v", p)
			appendFixLog(fmt.Sprintf("%s: %v\n%s", f.ID, p, debug.Stack()))
		}
	}()
	win.WithCOM(func() { out, err = f.Apply(r, data) })
	return
}

// appendFixLog: błędy programu podczas napraw (do pliku obok dziennika okna).
func appendFixLog(msg string) {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		p := filepath.Join(dir, "SerwisDiag", "naprawy-go.log")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s\r\n", time.Now().Format("2006-01-02 15:04:05"), msg)
			f.Close()
		}
	}
}

// Log dopisuje zmianę do dziennika napraw.
func (r *FixRun) Log(s string) { r.log = append(r.log, s) }

func (r *FixRun) say(text string) {
	if r.Status != nil {
		r.Status(text)
	}
}

func (r *FixRun) addUndo(u UndoRecord) { r.undo = append(r.undo, u) }

// SetReg odpowiada Set-SdRegValueLogged: ustawia wartość w rejestrze i zapisuje w dzienniku poprzednią.
// kind: DWord, QWord, String, ExpandString, MultiString, Binary.
func (r *FixRun) SetReg(path, name string, value any, kind string) error {
	old, _ := win.RegValue(path, name)
	raw := win.RegRaw(path, name)
	if err := win.RegSet(path, name, value, kind); err != nil {
		return err
	}
	ps := win.PSRegPath(path)
	r.Log(fmt.Sprintf("%s [%s]: %s -> %s", ps, name, logValue(old), logValue(value)))
	r.addUndo(UndoRecord{Kind: "reg", Path: ps, Name: name, Existed: raw.Exists, Old: jsonValue(raw.Value), OldKind: raw.Kind, New: jsonValue(value), NewKind: kind})
	return nil
}

// RemoveReg odpowiada Remove-SdRegValueLogged.
func (r *FixRun) RemoveReg(path, name string) error {
	old, ok := win.RegValue(path, name)
	if !ok || old == nil {
		return nil
	}
	raw := win.RegRaw(path, name)
	if err := win.RegDeleteValue(path, name); err != nil {
		return err
	}
	ps := win.PSRegPath(path)
	r.Log(fmt.Sprintf("%s [%s]: %s -> (usunięto)", ps, name, logValue(old)))
	if raw.Exists {
		r.addUndo(UndoRecord{Kind: "reg", Path: ps, Name: name, Existed: true, Old: jsonValue(raw.Value), OldKind: raw.Kind, Deleted: true})
	}
	return nil
}

// regSnapshot odpowiada Get-SdRegSnapshot: stan kilku wartości klucza przed zmianą wykonywaną zewnętrznym poleceniem.
func regSnapshot(path string, names ...string) map[string]win.RegRawValue {
	s := make(map[string]win.RegRawValue, len(names))
	for _, n := range names {
		s[n] = win.RegRaw(path, n)
	}
	return s
}

// snapshotUndo odpowiada Add-SdRegSnapshotUndo: rekordy cofania dla wartości zmienionych od zrobienia migawki.
func (r *FixRun) snapshotUndo(path string, before map[string]win.RegRawValue) {
	names := make([]string, 0, len(before))
	for n := range before {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		a, b := before[n], win.RegRaw(path, n)
		if a.Exists == b.Exists && logValue(a.Value) == logValue(b.Value) {
			continue
		}
		r.addUndo(UndoRecord{Kind: "reg", Path: win.PSRegPath(path), Name: n, Existed: a.Exists, Old: jsonValue(a.Value), OldKind: a.Kind,
			New: jsonValue(b.Value), NewKind: b.Kind, Deleted: !b.Exists})
	}
}

// SetServiceStart odpowiada Set-SdServiceStartLogged; mode jak Set-Service -StartupType: Automatic, Manual, Disabled.
func (r *FixRun) SetServiceStart(name, mode string) error {
	old, ok := win.ServiceStartMode(name)
	if !ok {
		old = "?"
	}
	if err := win.SetServiceStartMode(name, mode); err != nil {
		return err
	}
	r.Log(fmt.Sprintf("Usługa %s: %s -> %s", name, old, mode))
	r.addUndo(UndoRecord{Kind: "svc", Name: name, Old: old, New: mode})
	return nil
}

// logValue odpowiada Format-SdLogValue.
func logValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "(brak)"
	case []byte:
		parts := make([]string, len(x))
		for i, b := range x {
			parts[i] = fmt.Sprintf("%02X", b)
		}
		return strings.Join(parts, " ")
	case []string:
		return strings.Join(x, "; ")
	case []int:
		parts := make([]string, len(x))
		for i, n := range x {
			parts[i] = strconv.Itoa(n)
		}
		return strings.Join(parts, "; ")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = logValue(e)
		}
		return strings.Join(parts, "; ")
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// jsonValue: wartość do dziennika JSON jak w wersji PowerShell (bajty jako tablica liczb, nie base64).
func jsonValue(v any) any {
	if b, ok := v.([]byte); ok {
		out := make([]int, len(b))
		for i, x := range b {
			out[i] = int(x)
		}
		return out
	}
	return v
}

// ---------------------------------------------------------------- uruchamianie programów (Invoke-Native)

type nativeResult struct {
	ExitCode int
	Out      string
	Err      string
	TimedOut bool
}

// runNative uruchamia program systemowy bez okna; wyjście jak w konsoli (strona kodowa OEM).
func runNative(timeout time.Duration, file string, args ...string) nativeResult {
	r := win.Run(timeout, file, args...)
	n := nativeResult{ExitCode: r.ExitCode, Out: win.OEMString(r.Stdout), Err: win.OEMString(r.Stderr), TimedOut: r.TimedOut}
	if r.Err != nil && n.Err == "" {
		n.Err = r.Err.Error()
	}
	return n
}

// utf16Text: wyjście sfc.exe przekierowane do pliku albo potoku jest w UTF-16.
func utf16Text(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

var lineSplitRx = regexp.MustCompile(`[\r\n]+`)

// lastLine odpowiada Get-SdLastLine: ostatni niepusty wiersz wyjścia programu (bez znaków NUL; postęp typu „45%”
// bywa nadpisywany samym znakiem CR).
func lastLine(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	last := ""
	for _, l := range lineSplitRx.Split(text, -1) {
		if t := strings.TrimSpace(l); t != "" {
			last = t
		}
	}
	return last
}

func sysPath(parts ...string) string {
	return filepath.Join(append([]string{getenv("SystemRoot")}, parts...)...)
}

// wmiCall wywołuje metodę WMI i sprawdza ReturnValue (0 = sukces).
func wmiCall(ns, path, method string, in map[string]any) error {
	out, err := win.WmiExecMethod(ns, path, method, in)
	if err != nil {
		return err
	}
	if rv, ok := out["returnvalue"]; ok {
		if code := win.AnyInt(rv); code != 0 {
			msg := win.SystemMessage(uint32(code))
			if msg == "" {
				msg = fmtHr(code)
			}
			return fmt.Errorf("%s.%s: %s (kod %d)", path, method, msg, code)
		}
	}
	return nil
}

// ---------------------------------------------------------------- dziennik napraw (Save-SdRepairLog)

var fileNameJunkRx = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

var fixLabels = map[string]string{"ok": "OK", "warn": "UWAGA", "error": "BŁĄD", "skip": "POMINIĘTO"}

// RepairLog - dziennik napraw w formacie JSON (do cofania zmian), zgodny z wersją PowerShell.
type RepairLog struct {
	Tool     string            `json:"tool"`
	Version  string            `json:"version"`
	Engine   string            `json:"engine,omitempty"`
	Kind     string            `json:"kind"`
	Created  string            `json:"created"`
	Computer string            `json:"computer"`
	Ticket   string            `json:"ticket"`
	Results  []RepairLogResult `json:"results"`
}

type RepairLogResult struct {
	FixID   string       `json:"fixId"`
	Title   string       `json:"title"`
	Result  string       `json:"result"`
	Message string       `json:"message"`
	Undo    []UndoRecord `json:"undo"`
}

// SaveRepairLog odpowiada Save-SdRepairLog: dziennik napraw obok raportów - co zrobiono, z jakim wynikiem i jakie
// wartości zmieniono, a obok wersja JSON do cofania zmian. Zwraca ścieżkę pliku tekstowego.
func SaveRepairLog(results []FixResult, meta map[string]any, outDir string) (string, error) {
	ticket, tech := win.AnyString(meta["Ticket"]), win.AnyString(meta["Technician"])
	computer := getenv("COMPUTERNAME")
	now := time.Now()
	base := fileNameJunkRx.ReplaceAllString(joinNonEmpty("_", ticket, computer, now.Format("20060102-1504")), "_") + "_naprawy.txt"
	lines := []string{"DZIENNIK NAPRAW - SerwisDiag " + Version + " (Go)", "Data: " + fmtDt(now), "Komputer: " + computer}
	if ticket != "" {
		lines = append(lines, "Zlecenie: "+ticket)
	}
	if tech != "" {
		lines = append(lines, "Technik: "+tech)
	}
	lines = append(lines, "")
	reboot := false
	for _, r := range results {
		msg := ""
		if r.Message != "" {
			msg = " - " + r.Message
		}
		lines = append(lines, fmt.Sprintf("[%s] %s%s", fixLabels[r.Result], r.Title, msg))
		for _, ch := range r.Changes {
			lines = append(lines, "    zmiana: "+ch)
		}
		reboot = reboot || r.Reboot
	}
	if reboot {
		lines = append(lines, "", "Część napraw zadziała po ponownym uruchomieniu komputera.")
	}
	lines = append(lines, "",
		"Zmiany ustawień (rejestr, usługi, zapora) cofa program: Więcej > Cofnij naprawy z dziennika (plik .json obok tego dziennika).",
		"Operacji takich jak DISM, SFC, czyszczenie plików czy instalacja sterowników program nie cofa - służy do tego punkt przywracania utworzony przed naprawami.")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, base)
	if err := os.WriteFile(path, []byte("\ufeff"+strings.Join(lines, "\r\n")), 0o644); err != nil {
		return "", err
	}
	rl := RepairLog{Tool: "SerwisDiag", Version: Version, Engine: "go", Kind: "repair-log", Created: now.Format("2006-01-02T15:04:05"),
		Computer: computer, Ticket: ticket, Results: []RepairLogResult{}}
	for _, r := range results {
		u := r.Undo
		if u == nil {
			u = []UndoRecord{}
		}
		rl.Results = append(rl.Results, RepairLogResult{FixID: r.FixID, Title: r.Title, Result: r.Result, Message: r.Message, Undo: u})
	}
	b, err := json.MarshalIndent(rl, "", "    ")
	if err != nil {
		return path, err
	}
	return path, os.WriteFile(strings.TrimSuffix(path, ".txt")+".json", b, 0o644)
}

// RepairWorkText odpowiada Get-SdRepairWorkText: opis wykonanych napraw do raportu „przed / po” (bez nazw
// „aktywatorów” - te w wersji neutralnej).
func RepairWorkText(results []FixResult) string {
	var lines []string
	for _, r := range results {
		if r.Result != "ok" && r.Result != "warn" {
			continue
		}
		t := r.Title
		if r.CustomerTitle != "" {
			t = r.CustomerTitle
		}
		lines = append(lines, "- "+t)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(append([]string{"Naprawy wykonane programem SerwisDiag:"}, lines...), "\r\n")
}
