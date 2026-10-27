package diag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Cofanie napraw z dziennika JSON (lib\Fixes.ps1, część „cofanie”): rejestr, typ uruchamiania usług, zapora i SMB1.
// DISM, SFC, czyszczenia plików ani instalacji sterowników program nie cofa - do tego służy punkt przywracania.

// UndoRecord - rekord cofania jednej zmiany. Kind: reg (wartość rejestru), svc (typ uruchamiania usługi),
// firewall (profile zapory), smb1 (serwer SMB1). Pola jak w dzienniku wersji PowerShell.
type UndoRecord struct {
	Kind     string   `json:"Kind"`
	Path     string   `json:"Path"`
	Name     string   `json:"Name"`
	Existed  bool     `json:"Existed"`
	Old      any      `json:"Old"`
	OldKind  string   `json:"OldKind"`
	New      any      `json:"New"`
	NewKind  string   `json:"NewKind"`
	Deleted  bool     `json:"Deleted"`
	Profiles []string `json:"Profiles"`
}

// MarshalJSON zapisuje tylko pola danego rodzaju, jak hashtable w wersji PowerShell.
func (u UndoRecord) MarshalJSON() ([]byte, error) {
	switch u.Kind {
	case "reg":
		return json.Marshal(struct {
			Kind, Path, Name string
			Existed          bool
			Old              any
			OldKind          string
			New              any
			NewKind          string
			Deleted          bool
		}{u.Kind, u.Path, u.Name, u.Existed, u.Old, u.OldKind, u.New, u.NewKind, u.Deleted})
	case "svc":
		return json.Marshal(struct {
			Kind, Name string
			Old, New   any
		}{u.Kind, u.Name, u.Old, u.New})
	case "firewall":
		return json.Marshal(struct {
			Kind     string
			Profiles []string
		}{u.Kind, u.Profiles})
	}
	return json.Marshal(struct{ Kind string }{u.Kind})
}

// ReadRepairLog odpowiada Read-SdRepairJson.
func ReadRepairLog(path string) (*RepairLog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	dec.UseNumber() // liczby z rejestru (QWord) bez utraty dokładności
	var rl RepairLog
	if err := dec.Decode(&rl); err != nil || rl.Kind != "repair-log" {
		return nil, errors.New("To nie jest dziennik napraw programu SerwisDiag (plik *_naprawy.json).")
	}
	return &rl, nil
}

// FindRepairLogs odpowiada Find-SdRepairJson: dzienniki napraw komputera w folderze raportów, od najnowszego.
func FindRepairLogs(dir, computer string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*_naprawy.json"))
	type f struct {
		path string
		mod  time.Time
	}
	var list []f
	for _, p := range files {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			list = append(list, f{p, st.ModTime()})
		}
	}
	slices.SortStableFunc(list, func(a, b f) int { return b.mod.Compare(a.mod) })
	var out []string
	for _, x := range list {
		if rl, err := ReadRepairLog(x.path); err == nil && eqi(rl.Computer, computer) {
			out = append(out, x.path)
		}
	}
	return out
}

// UndoItem - zmiana do cofnięcia z dziennika.
type UndoItem struct {
	FixID, FixTitle string
	Record          UndoRecord
	Text            string
}

// UndoItems odpowiada Get-SdUndoItems: zmiany do cofnięcia - w odwrotnej kolejności niż je wprowadzano.
func UndoItems(rl *RepairLog) []UndoItem {
	var items []UndoItem
	for _, res := range rl.Results {
		for _, u := range res.Undo {
			if u.Kind == "" {
				continue
			}
			items = append(items, UndoItem{FixID: res.FixID, FixTitle: res.Title, Record: u, Text: UndoText(u)})
		}
	}
	slices.Reverse(items)
	return items
}

// NotUndoable odpowiada Get-SdNotUndoable: wykonane naprawy bez zmian ustawień do cofnięcia (DISM, SFC, czyszczenie,
// sterowniki...) - do informacji.
func NotUndoable(rl *RepairLog) []string {
	var out []string
	for _, r := range rl.Results {
		if (r.Result == "ok" || r.Result == "warn") && !slices.ContainsFunc(r.Undo, func(u UndoRecord) bool { return u.Kind != "" }) {
			out = append(out, r.Title)
		}
	}
	return out
}

// UndoText odpowiada Get-SdUndoText: opis zmiany do cofnięcia, zrozumiały dla technika.
func UndoText(u UndoRecord) string {
	switch u.Kind {
	case "reg":
		old := "(brak - wartość zostanie usunięta)"
		if u.Existed {
			old = logValue(regTyped(u.Old, u.OldKind))
		}
		return fmt.Sprintf("Rejestr %s [%s]: przywróć %s", u.Path, u.Name, old)
	case "svc":
		return fmt.Sprintf("Usługa %s: przywróć typ uruchamiania %s", u.Name, win.AnyString(u.Old))
	case "firewall":
		return "Zapora Windows: wyłącz ponownie w profilach " + strings.Join(u.Profiles, ", ")
	case "smb1":
		return "Serwer SMB1: włącz ponownie (stare urządzenia sieciowe)"
	}
	return "Nieznana zmiana: " + u.Kind
}

func numberOf(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			if f > math.MaxInt64 { // QWord powyżej zakresu Int64 - tak jak [int64] w PowerShellu nie przejdzie
				return int64(uint64(f)), true
			}
			return int64(f), true
		}
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case string:
		var n int64
		if _, err := fmt.Sscan(strings.TrimSpace(x), &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func listOf(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []int:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out
	case []byte:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = int(n)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case nil:
		return nil
	}
	return []any{v}
}

// regTyped odpowiada ConvertTo-SdRegTyped: wartość z dziennika (liczby, tablice liczb) -> typ wymagany przez rejestr.
func regTyped(v any, kind string) any {
	if v == nil {
		return nil
	}
	switch kind {
	case "Binary":
		l := listOf(v)
		b := make([]byte, len(l))
		for i, e := range l {
			n, _ := numberOf(e)
			b[i] = byte(n)
		}
		return b
	case "MultiString":
		l := listOf(v)
		s := make([]string, len(l))
		for i, e := range l {
			s[i] = logValue(e)
		}
		return s
	case "DWord":
		n, _ := numberOf(v)
		return int64(int32(uint32(n)))
	case "QWord":
		n, _ := numberOf(v)
		return n
	}
	return logValue(v)
}

// UndoResult - wynik cofnięcia jednej zmiany.
type UndoResult struct {
	FixTitle, Text string
	Result         string // ok | skip | error
	Message        string
}

// undoRecord odpowiada Invoke-SdUndoRecord: cofa jedną zmianę. Gdy stan zmienił się po naprawie (ktoś zmienił
// ustawienie ponownie), zmianę pomija.
func undoRecord(u UndoRecord) (string, string, error) {
	switch u.Kind {
	case "reg":
		cur := win.RegRaw(u.Path, u.Name)
		var expected any
		if !u.Deleted {
			expected = regTyped(u.New, u.NewKind)
		}
		if cur.Exists == u.Deleted || (!u.Deleted && logValue(cur.Value) != logValue(expected)) {
			now := "brak"
			if cur.Exists {
				now = logValue(cur.Value)
			}
			return "skip", "wartość zmieniła się po naprawie (obecnie: " + now + ") - pominięto", nil
		}
		if u.Existed {
			old := regTyped(u.Old, u.OldKind)
			if err := win.RegSet(u.Path, u.Name, old, u.OldKind); err != nil {
				return "", "", err
			}
			return "ok", "przywrócono: " + logValue(old), nil
		}
		if err := win.RegDeleteValue(u.Path, u.Name); err != nil {
			return "", "", err
		}
		return "ok", "usunięto wartość dodaną przez naprawę", nil
	case "svc":
		toParam := map[string]string{"Auto": "Automatic", "Manual": "Manual", "Disabled": "Disabled"}
		toMode := map[string]string{"Automatic": "Auto", "Manual": "Manual", "Disabled": "Disabled"}
		old := win.AnyString(u.Old)
		if _, ok := toParam[old]; !ok {
			return "skip", "nieznany poprzedni typ uruchamiania: " + old, nil
		}
		cur, ok := win.ServiceStartMode(u.Name)
		if !ok {
			return "error", "usługa nie istnieje", nil
		}
		if cur != toMode[win.AnyString(u.New)] {
			return "skip", "typ uruchamiania zmienił się po naprawie (obecnie: " + cur + ") - pominięto", nil
		}
		if err := win.SetServiceStartMode(u.Name, toParam[old]); err != nil {
			return "", "", err
		}
		return "ok", "typ uruchamiania: " + old, nil
	case "firewall":
		if err := setFirewallProfiles(u.Profiles, false); err != nil {
			return "", "", err
		}
		return "ok", "zapora wyłączona w profilach: " + strings.Join(u.Profiles, ", "), nil
	case "smb1":
		if err := wmiCall(`root\Microsoft\Windows\SMB`, "MSFT_SmbServerConfiguration", "SetConfiguration", map[string]any{"EnableSMB1Protocol": true}); err != nil {
			return "", "", err
		}
		return "ok", "serwer SMB1 włączony", nil
	}
	return "skip", "nieznany rodzaj zmiany: " + u.Kind, nil
}

// RunUndo odpowiada Invoke-SdUndo: cofa wskazane zmiany i zwraca wynik każdej.
func RunUndo(items []UndoItem) []UndoResult {
	out := make([]UndoResult, 0, len(items))
	for _, it := range items {
		var res, msg string
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("błąd programu: %v", p)
				}
			}()
			win.WithCOM(func() { res, msg, err = undoRecord(it.Record) })
		}()
		if err != nil {
			res, msg = "error", err.Error()
		}
		out = append(out, UndoResult{FixTitle: it.FixTitle, Text: it.Text, Result: res, Message: msg})
	}
	return out
}

var (
	repairJSONRx = regexp.MustCompile(`(?i)_naprawy\.json$`)
	jsonExtRx    = regexp.MustCompile(`(?i)\.json$`)
)

// SaveUndoLog odpowiada Save-SdUndoLog: dziennik cofnięcia obok dziennika napraw (<dziennik>_cofniecie_<data>.txt).
func SaveUndoLog(results []UndoResult, repairJSON string) (string, error) {
	labels := map[string]string{"ok": "OK", "skip": "POMINIĘTO", "error": "BŁĄD"}
	now := time.Now()
	lines := []string{"COFNIĘCIE NAPRAW - SerwisDiag " + Version + " (Go)", "Data: " + fmtDt(now), "Komputer: " + getenv("COMPUTERNAME"), "Dziennik napraw: " + repairJSON, ""}
	for _, r := range results {
		msg := ""
		if r.Message != "" {
			msg = " - " + r.Message
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s%s", labels[r.Result], r.FixTitle, r.Text, msg))
	}
	suffix := "_cofniecie_" + now.Format("20060102-1504") + ".txt"
	path := jsonExtRx.ReplaceAllString(repairJSON, suffix)
	if repairJSONRx.MatchString(repairJSON) {
		path = repairJSONRx.ReplaceAllString(repairJSON, suffix)
	}
	return path, os.WriteFile(path, []byte("\ufeff"+strings.Join(lines, "\r\n")), 0o644)
}
