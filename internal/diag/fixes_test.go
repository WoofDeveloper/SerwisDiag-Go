package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"serwisdiag/internal/win"
)

func TestFixCatalog(t *testing.T) {
	want := []string{"fx.restoreon", "fx.wmi", "fx.dism", "fx.sfc", "fx.wusvc", "fx.wupolicy", "fx.wureset", "fx.drivers", "fx.defender", "fx.defsig",
		"fx.firewall", "fx.uac", "fx.smb1", "fx.remotereg", "fx.winlogon", "fx.ifeo", "fx.appinit", "fx.dumps", "fx.trim", "fx.pagefile", "fx.cleanup",
		"fx.dns", "fx.time", "fx.proxy", "fx.winsock", "fx.startup", "fx.startupopt", "fx.uninstall"}
	var got []string
	prev := -1
	for _, f := range Fixes() {
		got = append(got, f.ID)
		if f.Order <= prev {
			t.Errorf("%s: kolejność %d nie rośnie", f.ID, f.Order)
		}
		prev = f.Order
		if f.Detect == nil || f.Apply == nil || f.Title == "" || f.Description == "" || f.Group == "" {
			t.Errorf("%s: niekompletna naprawa", f.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("katalog napraw:\n%v\noczekiwano:\n%v", got, want)
	}
}

func detect(t *testing.T, id string, offline bool, fs ...Finding) []FixItem {
	t.Helper()
	f := fixByID(id)
	if f == nil {
		t.Fatalf("brak naprawy %s", id)
	}
	return f.Detect(NewFixContext(&Result{Findings: fs}, true, offline))
}

func TestFixDetectFromFindings(t *testing.T) {
	sys := strings.ToUpper(sysDrive())
	cases := []struct {
		id      string
		offline bool
		fs      []Finding
		n       int
		def     *bool
		problem string
	}{
		{id: "fx.wmi", fs: []Finding{{CheckID: "sys.wmi", Status: Warn}}, n: 1},
		{id: "fx.wmi", fs: []Finding{{CheckID: "sys.wmi", Status: OK}}, n: 0},
		{id: "fx.dism", fs: []Finding{{CheckID: "up.cbs", Status: Crit}}, n: 1},
		{id: "fx.sfc", fs: []Finding{{CheckID: "up.cbs", Status: Warn}}, n: 1},
		{id: "fx.sfc", fs: []Finding{{CheckID: "ev.crash", Title: "Niebieskie ekrany (BSOD)", Status: Warn}}, n: 1, def: boolp(false)},
		{id: "fx.sfc", fs: []Finding{{CheckID: "ev.crash", Title: "Nieoczekiwane restarty (bez BSOD)", Status: Warn}}, n: 0},
		{id: "fx.wureset", fs: []Finding{{CheckID: "up.history", Title: "Nieudane aktualizacje", Status: Warn}}, n: 1},
		{id: "fx.drivers", fs: []Finding{{CheckID: "dr.wu", Status: Info, Value: "3 sterowniki"}}, n: 1, def: boolp(false), problem: "3 sterowniki"},
		{id: "fx.drivers", fs: []Finding{{CheckID: "dr.wu", Status: Warn, Value: "1 sterownik"}}, n: 1, def: boolp(true)},
		{id: "fx.drivers", fs: []Finding{{CheckID: "dr.wu", Status: OK}}, n: 0},
		{id: "fx.defsig", fs: []Finding{{CheckID: "se.av", Title: "Microsoft Defender", Status: Warn, Value: "Definicje sprzed 9 dni"}}, n: 1, problem: "Definicje sprzed 9 dni"},
		{id: "fx.defsig", fs: []Finding{{CheckID: "se.av", Title: "Microsoft Defender", Status: Warn, Value: "Ochrona wyłączona"}}, n: 0},
		{id: "fx.pagefile", fs: []Finding{{CheckID: "st.pagefile", Title: "Plik wymiany", Status: Warn, Value: "Wyłączony"}}, n: 1},
		{id: "fx.cleanup", fs: []Finding{{CheckID: "st.volumes", Title: "Wolumin " + sys, Status: Crit, Value: "wolne 2 GB"}}, n: 1, problem: "Mało miejsca na dysku systemowym: wolne 2 GB"},
		{id: "fx.cleanup", fs: []Finding{{CheckID: "st.cleanup", Status: Info, Value: "Do odzyskania 8 GB"}}, n: 1, def: boolp(false), problem: "Do odzyskania 8 GB"},
		{id: "fx.cleanup", n: 1, def: boolp(false)},
		{id: "fx.dns", fs: []Finding{{CheckID: "nw.connectivity", Title: "Rozpoznawanie nazw (DNS)", Status: Warn}}, n: 1},
		{id: "fx.time", fs: []Finding{{CheckID: "nw.connectivity", Title: "Zegar systemowy", Status: Warn, Value: "Różnica 5 min"}}, n: 1, problem: "Różnica 5 min"},
		{id: "fx.winsock", fs: []Finding{{CheckID: "nw.connectivity", Title: "Ping do internetu (8.8.8.8)", Status: Crit}}, n: 1},
		{id: "fx.winsock", offline: true, fs: []Finding{{CheckID: "nw.connectivity", Title: "Ping do internetu (8.8.8.8)", Status: Crit}}, n: 0},
		{id: "fx.winsock", fs: []Finding{{CheckID: "nw.connectivity", Title: "Dostęp do internetu (HTTPS)", Status: Warn, Value: "Brak odpowiedzi"}}, n: 1},
		{id: "fx.winsock", fs: []Finding{{CheckID: "nw.connectivity", Title: "Ping do internetu (8.8.8.8)", Status: Warn}}, n: 0},
	}
	for _, c := range cases {
		items := detect(t, c.id, c.offline, c.fs...)
		if len(items) != c.n {
			t.Errorf("%s %v: %d pozycji, oczekiwano %d", c.id, c.fs, len(items), c.n)
			continue
		}
		if c.n == 0 {
			continue
		}
		if c.def != nil && (items[0].Default == nil || *items[0].Default != *c.def) {
			t.Errorf("%s: domyślne zaznaczenie %v, oczekiwano %v", c.id, items[0].Default, *c.def)
		}
		if c.problem != "" && items[0].Problem != c.problem {
			t.Errorf("%s: problem %q, oczekiwano %q", c.id, items[0].Problem, c.problem)
		}
	}
	// ochrona systemu - tylko gdy zasada jej nie wyłącza
	if regInt(`HKLM\SOFTWARE\Policies\Microsoft\Windows NT\SystemRestore`, "DisableSR", 0) != 1 {
		if n := len(detect(t, "fx.restoreon", false, Finding{CheckID: "sys.restore", Status: Info, Value: "Brak punktów przywracania (ochrona systemu może być wyłączona)"})); n != 1 {
			t.Errorf("fx.restoreon: %d pozycji", n)
		}
	}
}

func TestFixCandidatesBlocked(t *testing.T) {
	fc := NewFixContext(&Result{Findings: []Finding{{CheckID: "sys.wmi", Status: Crit}, {CheckID: "dr.wu", Status: Warn, Value: "x"}}}, false, true)
	var wmi, drv *FixCandidate
	cands := FixCandidates(fc)
	for i := range cands {
		switch cands[i].FixID {
		case "fx.wmi":
			wmi = &cands[i]
		case "fx.drivers":
			drv = &cands[i]
		}
	}
	if wmi == nil || wmi.Blocked != "wymaga uprawnień administratora" || wmi.Default || wmi.Key != "fx.wmi|" {
		t.Fatalf("fx.wmi bez administratora: %+v", wmi)
	}
	if drv == nil || drv.Blocked == "" || drv.Default {
		t.Fatalf("fx.drivers: %+v", drv)
	}
}

func TestUnfixedProblems(t *testing.T) {
	res := &Result{Findings: []Finding{
		{CheckID: "st.volumes", Title: "Wolumin C:", Status: Warn},
		{CheckID: "st.volumes", Title: "Wolumin D:", Status: Warn},
		{CheckID: "sys.wmi", Title: "Usługa WMI", Status: Crit},
		{CheckID: "hw.cpu", Title: "Procesor", Status: Warn},
		{CheckID: "hw.ram", Title: "Pamięć", Status: Info},
	}}
	cands := []FixCandidate{{Covers: []string{"st.volumes|^Wolumin C:$", "st.cleanup"}}, {Covers: []string{"sys.wmi"}}}
	var titles []string
	for _, f := range UnfixedProblems(res, cands) {
		titles = append(titles, f.Title)
	}
	if !slices.Equal(titles, []string{"Wolumin D:", "Procesor"}) {
		t.Fatalf("problemy bez naprawy: %v", titles)
	}
}

func TestUninstallCommand(t *testing.T) {
	sr := getenv("SystemRoot")
	cases := []struct{ in, file, args string }{
		{`MsiExec.exe /I{12345678-1234-1234-1234-123456789012}`, "msiexec.exe", `/X {12345678-1234-1234-1234-123456789012}`},
		{`"C:\Windows\System32\MsiExec.exe" /X{ABC} /qb`, "msiexec.exe", `/X{ABC} /qb`},
		{`"C:\Program Files\App\unins000.exe" /SILENT`, `C:\Program Files\App\unins000.exe`, "/SILENT"},
		{`%SystemRoot%\System32\notepad.exe /x`, sr + `\System32\notepad.exe`, "/x"},
		{`C:\Brak\setup.exe /uninstall`, `C:\Brak\setup.exe`, "/uninstall"},
	}
	for _, c := range cases {
		f, a, ok := uninstallCommand(c.in)
		if !ok || !strings.EqualFold(f, c.file) || a != c.args {
			t.Errorf("uninstallCommand(%q) = %q, %q, %v; oczekiwano %q, %q", c.in, f, a, ok, c.file, c.args)
		}
	}
	if _, _, ok := uninstallCommand("  "); ok {
		t.Error("puste polecenie")
	}
}

func TestStartupDisabledValueAndLastLine(t *testing.T) {
	now := time.Now()
	b := startupDisabledValue(now)
	if len(b) != 12 || b[0] != 3 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		t.Fatalf("wartość wyłączenia: % X", b)
	}
	ft := int64(b[4]) | int64(b[5])<<8 | int64(b[6])<<16 | int64(b[7])<<24 | int64(b[8])<<32 | int64(b[9])<<40 | int64(b[10])<<48 | int64(b[11])<<56
	back := time.Unix(0, (ft-116444736000000000)*100)
	if d := back.Sub(now); d < -time.Second || d > time.Second {
		t.Fatalf("FILETIME: %v zamiast %v", back, now)
	}
	if got := lastLine("Weryfikacja 45%\r 100%\r\nOchrona zasobów nie znalazła naruszeń.\r\n\x00\r\n"); got != "Ochrona zasobów nie znalazła naruszeń." {
		t.Fatalf("lastLine: %q", got)
	}
	if got := utf16Text([]byte{0xFF, 0xFE, 'O', 0, 'K', 0}); got != "OK" {
		t.Fatalf("utf16Text: %q", got)
	}
}

func TestLogValueAndRegTyped(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nil, "(brak)"}, {[]byte{3, 0, 0xAB}, "03 00 AB"}, {[]string{"a", "b"}, "a; b"}, {int64(-1), "-1"}, {5, "5"}, {"tekst", "tekst"},
		{true, "True"}, {json.Number("42"), "42"},
	}
	for _, c := range cases {
		if got := logValue(c.v); got != c.want {
			t.Errorf("logValue(%#v) = %q, oczekiwano %q", c.v, got, c.want)
		}
	}
	if v := regTyped(json.Number("4294967295"), "DWord"); v != int64(-1) {
		t.Errorf("DWord > Int32: %#v", v)
	}
	if v := regTyped([]any{json.Number("3"), json.Number("0"), json.Number("255")}, "Binary"); !slices.Equal(v.([]byte), []byte{3, 0, 255}) {
		t.Errorf("Binary: %#v", v)
	}
	if v := regTyped([]any{"a", "b"}, "MultiString"); !slices.Equal(v.([]string), []string{"a", "b"}) {
		t.Errorf("MultiString: %#v", v)
	}
	if v := regTyped(json.Number("9007199254740993"), "QWord"); v != int64(9007199254740993) {
		t.Errorf("QWord: %#v", v)
	}
	if v := regTyped([]int{1, 2}, "Binary"); !slices.Equal(v.([]byte), []byte{1, 2}) {
		t.Errorf("Binary z []int: %#v", v)
	}
}

// psRepairLog - dziennik zapisany przez wersję PowerShell (ConvertTo-Json w Windows PowerShell 5.1).
const psRepairLog = `{
    "tool":  "SerwisDiag",
    "version":  "1.5.0",
    "kind":  "repair-log",
    "created":  "2026-10-26T15:10:00",
    "computer":  "PC-TEST",
    "ticket":  "Z-7",
    "results":  [
                    {
                        "fixId":  "fx.startup",
                        "title":  "Wyłącz z autostartu: Updater",
                        "result":  "ok",
                        "message":  "Wyłączono z autostartu.",
                        "undo":  [
                                     {
                                         "Deleted":  false,
                                         "NewKind":  "Binary",
                                         "Path":  "Registry::HKEY_USERS\\S-1-5-21-1-2-3-1001\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\StartupApproved\\Run",
                                         "OldKind":  "Binary",
                                         "Name":  "Updater",
                                         "New":  [3, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8],
                                         "Kind":  "reg",
                                         "Existed":  true,
                                         "Old":  [2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
                                     }
                                 ]
                    },
                    {
                        "fixId":  "fx.wusvc",
                        "title":  "Włącz usługę Windows Update",
                        "result":  "ok",
                        "message":  "",
                        "undo":  [
                                     {
                                         "Old":  "Disabled",
                                         "New":  "Manual",
                                         "Kind":  "svc",
                                         "Name":  "wuauserv"
                                     }
                                 ]
                    },
                    {
                        "fixId":  "fx.dism",
                        "title":  "Napraw magazyn składników Windows (DISM)",
                        "result":  "ok",
                        "message":  "Magazyn składników naprawiony.",
                        "undo":  [

                                 ]
                    },
                    {
                        "fixId":  "fx.firewall",
                        "title":  "Włącz zaporę systemu Windows",
                        "result":  "ok",
                        "message":  "",
                        "undo":  [
                                     {
                                         "Deleted":  true,
                                         "NewKind":  "",
                                         "Path":  "HKLM:\\SOFTWARE\\Policies\\Microsoft\\WindowsFirewall\\PublicProfile",
                                         "OldKind":  "DWord",
                                         "Name":  "EnableFirewall",
                                         "New":  null,
                                         "Kind":  "reg",
                                         "Existed":  true,
                                         "Old":  0
                                     },
                                     {
                                         "Profiles":  [
                                                          "Public"
                                                      ],
                                         "Kind":  "firewall"
                                     }
                                 ]
                    },
                    {
                        "fixId":  "fx.smb1",
                        "title":  "Wyłącz przestarzały protokół SMB1",
                        "result":  "error",
                        "message":  "Odmowa dostępu",
                        "undo":  [
                                     {
                                         "Kind":  "smb1"
                                     }
                                 ]
                    }
                ]
}`

func TestRepairLogFromPowerShell(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Z-7_PC-TEST_20261026-1510_naprawy.json")
	if err := os.WriteFile(p, []byte("\ufeff"+psRepairLog), 0o644); err != nil {
		t.Fatal(err)
	}
	rl, err := ReadRepairLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if rl.Computer != "PC-TEST" || rl.Ticket != "Z-7" || len(rl.Results) != 5 {
		t.Fatalf("dziennik: %+v", rl)
	}
	items := UndoItems(rl)
	var texts []string
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	want := []string{
		"Serwer SMB1: włącz ponownie (stare urządzenia sieciowe)",
		"Zapora Windows: wyłącz ponownie w profilach Public",
		`Rejestr HKLM:\SOFTWARE\Policies\Microsoft\WindowsFirewall\PublicProfile [EnableFirewall]: przywróć 0`,
		"Usługa wuauserv: przywróć typ uruchamiania Disabled",
		`Rejestr Registry::HKEY_USERS\S-1-5-21-1-2-3-1001\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run [Updater]: przywróć 02 00 00 00 00 00 00 00 00 00 00 00`,
	}
	if !slices.Equal(texts, want) {
		t.Fatalf("zmiany do cofnięcia:\n%s\noczekiwano:\n%s", strings.Join(texts, "\n"), strings.Join(want, "\n"))
	}
	if nu := NotUndoable(rl); !slices.Equal(nu, []string{"Napraw magazyn składników Windows (DISM)"}) {
		t.Fatalf("bez cofania: %v", nu)
	}
	// zapis Go: tylko pola danego rodzaju, bajty jako liczby
	b, _ := json.Marshal([]UndoRecord{{Kind: "reg", Path: `HKLM:\X`, Name: "A", Existed: true, Old: jsonValue([]byte{1, 2}), OldKind: "Binary", New: 0, NewKind: "DWord"},
		{Kind: "svc", Name: "wuauserv", Old: "Auto", New: "Manual"}, {Kind: "firewall", Profiles: []string{"Private"}}, {Kind: "smb1"}})
	wantJSON := `[{"Kind":"reg","Path":"HKLM:\\X","Name":"A","Existed":true,"Old":[1,2],"OldKind":"Binary","New":0,"NewKind":"DWord","Deleted":false},` +
		`{"Kind":"svc","Name":"wuauserv","Old":"Auto","New":"Manual"},{"Kind":"firewall","Profiles":["Private"]},{"Kind":"smb1"}]`
	if string(b) != wantJSON {
		t.Fatalf("JSON:\n%s\noczekiwano:\n%s", b, wantJSON)
	}
	if _, err := ReadRepairLog(filepath.Join(t.TempDir(), "brak.json")); err == nil {
		t.Fatal("brak pliku bez błędu")
	}
	other := filepath.Join(t.TempDir(), "raport.json")
	_ = os.WriteFile(other, []byte(`{"tool":"SerwisDiag","kind":"report"}`), 0o644)
	if _, err := ReadRepairLog(other); err == nil || !strings.Contains(err.Error(), "dziennik napraw") {
		t.Fatalf("raport zamiast dziennika: %v", err)
	}
}

// TestFixRunUndoLive: zmiany w rejestrze przez FixRun, dziennik, odczyt i cofnięcie - na kluczu testowym HKCU
// (tworzonym i usuwanym przez test).
func TestFixRunUndoLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie (rejestr HKCU)")
	}
	const sub = `Software\SerwisDiagGoTest`
	key := `HKCU\` + sub + `\Undo`
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, sub+`\Undo`)
		_ = registry.DeleteKey(registry.CURRENT_USER, sub)
	})
	if err := win.RegSet(key, "Existing", 5, "DWord"); err != nil {
		t.Fatal(err)
	}
	if err := win.RegSet(key, "Removable", "abc", "String"); err != nil {
		t.Fatal(err)
	}
	r := &FixRun{}
	steps := []func() error{
		func() error { return r.SetReg(key, "Existing", 1, "DWord") },
		func() error { return r.SetReg(key, "Bin", startupDisabledValue(time.Now()), "Binary") },
		func() error { return r.RemoveReg(key, "Removable") },
		func() error { return r.SetReg(key, "Multi", []string{"a", "b"}, "MultiString") },
		func() error { return r.SetReg(key, "Exp", `%TEMP%\x`, "ExpandString") },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.log) != 5 || len(r.undo) != 5 || !strings.HasPrefix(r.log[0], `HKCU:\Software\SerwisDiagGoTest\Undo [Existing]: 5 -> 1`) || !strings.HasSuffix(r.log[2], "abc -> (usunięto)") {
		t.Fatalf("dziennik zmian: %q", r.log)
	}
	dir := t.TempDir()
	res := []FixResult{{Title: "Test", FixID: "fx.test", Result: "ok", Message: "zmieniono", Changes: r.log, Undo: r.undo},
		{Title: "Bez zmian", FixID: "fx.dism", Result: "ok"}}
	txt, err := SaveRepairLog(res, map[string]any{"Ticket": "T 1/2"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(txt); !strings.HasPrefix(string(b), "\ufeffDZIENNIK NAPRAW") || !strings.Contains(string(b), "    zmiana: ") || !strings.HasPrefix(filepath.Base(txt), "T_1_2_") {
		t.Fatalf("dziennik tekstowy %s:\n%s", txt, b)
	}
	logs := FindRepairLogs(dir, getenv("COMPUTERNAME"))
	if len(logs) != 1 {
		t.Fatalf("dzienniki w folderze: %v", logs)
	}
	rl, err := ReadRepairLog(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	items := UndoItems(rl)
	if len(items) != 5 || items[0].Record.Name != "Exp" || !slices.Equal(NotUndoable(rl), []string{"Bez zmian"}) {
		t.Fatalf("zmiany do cofnięcia: %+v", items)
	}
	for _, u := range RunUndo(items) {
		if u.Result != "ok" {
			t.Errorf("cofanie %s: %s %s", u.Text, u.Result, u.Message)
		}
	}
	if v, _ := win.RegValue(key, "Existing"); win.AnyInt(v) != 5 {
		t.Errorf("Existing po cofnięciu: %v", v)
	}
	if v, _ := win.RegValue(key, "Removable"); v != "abc" {
		t.Errorf("Removable po cofnięciu: %v", v)
	}
	for _, n := range []string{"Bin", "Multi", "Exp"} {
		if win.RegRaw(key, n).Exists {
			t.Errorf("%s nie został usunięty", n)
		}
	}
	// wartość zmieniona ponownie po naprawie - cofanie ją pomija
	r2 := &FixRun{}
	if err := r2.SetReg(key, "Existing", 9, "DWord"); err != nil {
		t.Fatal(err)
	}
	_ = win.RegSet(key, "Existing", 7, "DWord")
	u := RunUndo([]UndoItem{{FixTitle: "Test", Record: r2.undo[0], Text: UndoText(r2.undo[0])}})
	if u[0].Result != "skip" || !strings.Contains(u[0].Message, "obecnie: 7") {
		t.Fatalf("zmiana po naprawie: %+v", u[0])
	}
	undoTxt, err := SaveUndoLog(u, logs[0])
	if err != nil || !strings.Contains(filepath.Base(undoTxt), "_cofniecie_") {
		t.Fatalf("dziennik cofnięcia: %s %v", undoTxt, err)
	}
}
