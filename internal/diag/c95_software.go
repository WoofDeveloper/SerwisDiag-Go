package diag

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"serwisdiag/internal/win"
)

// Testy kategorii: Oprogramowanie i autostart (checks\95-software.ps1)

var softwareChecks = []*Check{
	{ID: "sw.installed", Category: "software", Title: "Zainstalowane programy", Level: "Quick", Run: checkInstalled},
	{ID: "sw.startup", Category: "software", Title: "Programy uruchamiane z systemem (autostart)", Level: "Quick", Run: checkStartup},
	{ID: "sw.services", Category: "software", Title: "Usługi spoza systemu Windows", Level: "Quick", Run: checkThirdPartyServices},
	{ID: "sw.tasks", Category: "software", Title: "Zadania harmonogramu spoza systemu Windows", Level: "Full", Run: checkScheduledTasks},
	{ID: "sw.processes", Category: "software", Title: "Podejrzane procesy", Level: "Quick", Run: checkProcesses},
}

var (
	suspiciousPathRx = rx(`\\(Temp|Tmp)\\|\\AppData\\Local\\Temp|\\Users\\Public\\|\\\$Recycle\.Bin|\\Downloads\\|\\ProgramData\\[^\\]+\.(exe|bat|cmd|vbs|ps1)$`)
	// Tylko silne wskaźniki: zakodowane polecenia i pobieranie/wykonywanie kodu.
	suspiciousCmdRx = rx(`-enc(odedcommand)?\s|frombase64string|downloadstring|downloadfile|invoke-expression|\biex\b|mshta(\.exe)?\s+(http|vbscript|javascript)|certutil.*-urlcache|bitsadmin.*/transfer|regsvr32.*/i:http`)
	appDataRx       = rx(`\\AppData\\`)
)

// FileInfo - informacje o pliku wykonywalnym (Get-SdFileInfo).
type FileInfo struct {
	Exists      bool
	Company     string
	Description string
	Signer      string
	Signed      bool
}

// fileInfo odpowiada Get-SdFileInfo: producent, opis i (na żądanie) wystawca podpisu; wyniki zapamiętywane.
func fileInfo(c *Ctx, path string, signature bool) FileInfo {
	if path == "" {
		return FileInfo{}
	}
	key := fmt.Sprintf("FI|%s|%v", strings.ToLower(path), signature)
	return cached(c, key, func() FileInfo {
		var r FileInfo
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			return r
		}
		r.Exists = true
		r.Company, r.Description = win.FileVersionStrings(path)
		if signature {
			r.Signer, r.Signed = win.EmbeddedSigner(path)
		}
		return r
	})
}

var exeLikeRx = rx(`\.(exe|bat|cmd|com|lnk|vbs|ps1|msc|scr|dll|js)$`)

func expandEnv(s string) string {
	if x, err := registry.ExpandString(s); err == nil {
		return x
	}
	return s
}

// resolveCommandPath odpowiada Resolve-SdCommandPath: z polecenia wyciąga ścieżkę pliku wykonywalnego.
func resolveCommandPath(command string) string {
	if command == "" {
		return ""
	}
	cmd := expandEnv(strings.TrimSpace(command))
	if strings.HasPrefix(cmd, `"`) {
		if e := strings.Index(cmd[1:], `"`); e > 0 {
			return cmd[1 : e+1]
		}
	}
	parts := strings.Split(cmd, " ")
	for n := len(parts); n >= 1; n-- {
		cand := strings.TrimSpace(strings.Join(parts[:n], " "))
		if exeLikeRx.MatchString(cand) {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	return parts[0]
}

var (
	hostExeRx = rx(`^(rundll32|regsvr32|wscript|cscript|mshta|powershell|pwsh|cmd|conhost|msiexec|forfiles)$`)
	payloadRx = regexp.MustCompile(`(?i)(?:\b[a-z]:|\\\\)[^"*?<>|,;]*?\.(?:dll|ocx|cpl|exe|scr|com|bat|cmd|vbs|vbe|js|jse|wsf|wsh|hta|ps1|psm1)(?:$|[\s",;])`)
)

// hostPayload odpowiada Get-SdHostPayload: gdy autostart uruchamia program-pośrednika z Windows (rundll32,
// powershell...), właściwy kod jest w argumentach - zwraca wskazane w nich pliki.
func hostPayload(command, hostPath string) []string {
	leaf := strings.TrimSuffix(filepath.Base(hostPath), filepath.Ext(hostPath))
	if !hostExeRx.MatchString(leaf) {
		return nil
	}
	c := expandEnv(command)
	rest := c
	if i := strings.Index(strings.ToLower(c), strings.ToLower(leaf)); i >= 0 {
		rest = c[i+len(leaf):]
	}
	var out []string
	for _, m := range payloadRx.FindAllString(rest, -1) {
		p := strings.TrimSpace(strings.TrimRight(m, `",;`))
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// serviceDll odpowiada Get-SdServiceDll: biblioteka usługi współdzielonej (svchost.exe -k ...).
func serviceDll(name string) string {
	base := `HKLM\SYSTEM\CurrentControlSet\Services\` + name
	v := regStr(base+`\Parameters`, "ServiceDll", "")
	if v == "" {
		v = regStr(base, "ServiceDll", "")
	}
	if v == "" {
		return ""
	}
	return expandEnv(v)
}

// UserHive - załadowana gałąź użytkownika w HKEY_USERS.
type UserHive struct {
	SID, Name, Path string
}

var userSidRx = regexp.MustCompile(`^S-1-5-21-\d+-\d+-\d+-\d+$`)

func userHives(c *Ctx) []UserHive {
	return cached(c, "UserHives", func() []UserHive {
		var out []UserHive
		for _, k := range win.RegSubKeys(`HKU`) {
			if !userSidRx.MatchString(k) {
				continue
			}
			name := win.SIDAccountName(k)
			if name == "" {
				name = k
			}
			out = append(out, UserHive{SID: k, Name: name, Path: `HKU\` + k})
		}
		return out
	})
}

// InstalledApp - zainstalowany program (Get-SdInstalledApps).
type InstalledApp struct {
	Name, Version, Publisher string
	Installed                time.Time
	Size                     float64
	Scope                    string
}

var (
	updateTypeRx  = rx(`Update|Hotfix|Security`)
	updateNameRx  = rx(`^(KB\d+|Security Update for|Update for|Hotfix for)`)
	eightDigitsRx = regexp.MustCompile(`^\d{8}$`)
)

func installedApps(c *Ctx) []InstalledApp {
	type src struct{ path, scope string }
	srcs := []src{
		{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "komputer"},
		{`HKLM\SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "komputer (32-bit)"},
	}
	for _, h := range userHives(c) {
		srcs = append(srcs, src{h.Path + `\Software\Microsoft\Windows\CurrentVersion\Uninstall`, "użytkownik " + h.Name})
	}
	seen := map[string]bool{}
	var apps []InstalledApp
	for _, s := range srcs {
		for _, k := range win.RegSubKeys(s.path) {
			_, p, ok := win.RegValues(s.path + `\` + k)
			if !ok {
				continue
			}
			name := win.AnyString(p["displayname"])
			if name == "" {
				continue
			}
			if v, ok := p["systemcomponent"]; ok && win.AnyInt(v) == 1 {
				continue
			}
			if win.AnyString(p["parentkeyname"]) != "" || updateTypeRx.MatchString(win.AnyString(p["releasetype"])) || updateNameRx.MatchString(name) {
				continue
			}
			var date time.Time
			if d := win.AnyString(p["installdate"]); eightDigitsRx.MatchString(d) {
				date, _ = time.ParseInLocation("20060102", d, time.Local)
			}
			size := 0.0
			if v := p["estimatedsize"]; v != nil && win.AnyInt(v) != 0 {
				size = float64(uint32(win.AnyInt(v))) * 1024
			}
			ver := win.AnyString(p["displayversion"])
			id := strings.ToLower(name + "|" + ver)
			if seen[id] {
				continue
			}
			seen[id] = true
			apps = append(apps, InstalledApp{Name: name, Version: ver, Publisher: win.AnyString(p["publisher"]), Installed: date, Size: size, Scope: s.scope})
		}
	}
	psSort(apps, func(a, b InstalledApp) int { return cmpText(a.Name, b.Name) })
	return apps
}

var avNameSuffixRx = regexp.MustCompile(`\s+\d.*$`)

func checkInstalled(c *Ctx) {
	list := installedApps(c)
	if len(list) == 0 {
		c.Add(F{Title: "Zainstalowane programy", Status: Info, Value: "Nie odczytano listy programów"})
		return
	}
	var rows, recentRows []Row
	var recent []InstalledApp
	limit := time.Now().AddDate(0, 0, -30)
	for _, a := range list {
		size := ""
		if a.Size > 0 {
			size = fmtSize(a.Size, 0)
		}
		rows = append(rows, row("Nazwa", a.Name, "Wersja", a.Version, "Producent", a.Publisher, "Zainstalowano", fmtDate(a.Installed), "Rozmiar", size, "Zakres", a.Scope))
		if !a.Installed.IsZero() && !a.Installed.Before(limit) {
			recent = append(recent, a)
		}
	}
	c.AddTable(T{Name: "Zainstalowane programy", Rows: rows})
	psSort(recent, func(a, b InstalledApp) int { return b.Installed.Compare(a.Installed) })
	for _, a := range recent {
		recentRows = append(recentRows, row("Zainstalowano", fmtDate(a.Installed), "Nazwa", a.Name, "Wersja", a.Version, "Producent", a.Publisher))
	}
	c.AddTable(T{Name: "Programy zainstalowane w ostatnich 30 dniach", Rows: recentRows})
	many := len(list) > 150
	val := fmt.Sprintf("Liczba programów: %d", len(list))
	if len(recent) > 0 {
		val += fmt.Sprintf("; w ostatnich 30 dniach: %d", len(recent))
	}
	st, adv := OK, ""
	if many {
		st, adv = Info, "Bardzo dużo zainstalowanych programów - warto usunąć nieużywane, co odciąża system i autostart."
	}
	c.Add(F{Title: "Zainstalowane programy", Status: st, Value: val, Advice: adv})

	// --- reguły oceny
	type hit struct{ app, ver, note, sev string }
	byType := map[string][]hit{}
	today := time.Now()
	for _, a := range list {
		for _, f := range data().flags {
			if !f.rx.MatchString(a.Name) {
				continue
			}
			if f.Type == "eol" && parseDay(f.End).After(today) {
				continue
			}
			byType[f.Type] = append(byType[f.Type], hit{a.Name, a.Version, f.Note, f.Severity})
		}
	}
	titles := map[string]string{"optimizer": "Optymalizatory i aktualizatory sterowników", "pup": "Programy potencjalnie niechciane (PUP)", "crack": "Narzędzia do obchodzenia aktywacji",
		"remote": "Narzędzia zdalnego dostępu", "eol": "Programy bez wsparcia producenta", "trial": "Fabryczne wersje próbne"}
	advices := map[string]string{
		"crack":     "Zweryfikuj legalność oprogramowania i przeskanuj system; usuń narzędzie po uzyskaniu legalnej licencji.",
		"pup":       "Odinstaluj wskazane programy i przeskanuj system oraz przeglądarki (AdwCleaner / Malwarebytes).",
		"optimizer": "Rozważ odinstalowanie - takie programy rzadko poprawiają wydajność, a bywają przyczyną problemów.",
		"eol":       "Zaktualizuj lub usuń oprogramowanie bez wsparcia - nie otrzymuje poprawek bezpieczeństwa.",
		"remote":    "Potwierdź z klientem, że narzędzie zdalnego dostępu zostało zainstalowane świadomie (oszustwa „na wsparcie techniczne”). Zablokuj nieużywane.",
		"trial":     "Jeśli licencja wygasła, odinstaluj program próbny (dedykowanym narzędziem producenta) i pozostaw jeden aktywny program antywirusowy.",
	}
	for _, t := range []string{"crack", "pup", "optimizer", "eol", "remote", "trial"} {
		hits := byType[t]
		if len(hits) == 0 {
			continue
		}
		// Sort-Object App -Unique
		psSort(hits, func(a, b hit) int { return cmpText(a.app, b.app) })
		var items []hit
		for _, h := range hits {
			if len(items) == 0 || !eqi(items[len(items)-1].app, h.app) {
				items = append(items, h)
			}
		}
		sev := Info
		for _, h := range items {
			if h.sev == "warn" {
				sev = Warn
			}
		}
		if t == "remote" || t == "trial" {
			sev = Info
		}
		var det, val []string
		for i, h := range items {
			det = append(det, fmt.Sprintf("%s %s - %s", h.app, h.ver, h.note))
			if i < 5 {
				val = append(val, h.app)
			}
		}
		f := F{Title: titles[t], Status: sev, Value: strings.Join(val, "; "), Details: strings.Join(det, "\n"), Advice: advices[t]}
		if t == "crack" {
			// raport dla klienta bez nazw "aktywatorów" - technik omawia temat osobiście; ryzyko bezpieczeństwa zostaje wskazane
			f.CustomerTitle, f.CustomerStatus = "Programy wysokiego ryzyka", sev
			f.CustomerValue = "Wykryto programy, które często zawierają złośliwe oprogramowanie - szczegóły omówi serwisant."
			f.CustomerAdvice = "Zalecamy usunięcie tych programów i skanowanie antywirusowe systemu."
		}
		c.Add(f)
	}
	if hits := byType["av"]; len(hits) > 0 {
		var avs []string
		for _, h := range hits {
			n := avNameSuffixRx.ReplaceAllString(h.app, "")
			if !slices.Contains(avs, n) {
				avs = append(avs, n)
			}
		}
		if len(avs) > 1 {
			c.Add(F{Title: "Zainstalowane pakiety antywirusowe", Status: Info, Value: strings.Join(avs, "; "),
				Advice: "Kilka zainstalowanych pakietów AV (nawet nieaktywnych) to często pozostałości po poprzednich - usuń nieużywane dedykowanymi narzędziami producentów."})
		}
	}
}

// startupApprovedEnabled odpowiada Test-SdStartupApprovedEnabled: ustawiony bit 0 pierwszego bajtu = wyłączony.
func startupApprovedEnabled(v any) bool {
	if b, ok := v.([]byte); ok && len(b) > 0 && b[0]&1 == 1 {
		return false
	}
	return true
}

// StartupEntry - wpis autostartu (Get-SdStartupEntries).
type StartupEntry struct {
	Name, Command, Source, Owner string
	Enabled                      bool
	Approved, ApprovedName, Kind string
}

func startupEntries(c *Ctx) []StartupEntry {
	var entries []StartupEntry
	const sa = `\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	type target struct{ key, src, approved, owner, kind string }
	targets := []target{
		{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, "Rejestr: Run (komputer)", `HKLM` + sa + "Run", "wszyscy", "Run"},
		{`HKLM\SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\Run`, "Rejestr: Run (komputer, 32-bit)", `HKLM` + sa + "Run32", "wszyscy", "Run"},
		{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`, "Rejestr: RunOnce (komputer)", "", "wszyscy", "RunOnce"},
	}
	for _, h := range userHives(c) {
		targets = append(targets,
			target{h.Path + `\Software\Microsoft\Windows\CurrentVersion\Run`, "Rejestr: Run (użytkownik)", h.Path + sa + "Run", h.Name, "Run"},
			target{h.Path + `\Software\Microsoft\Windows\CurrentVersion\RunOnce`, "Rejestr: RunOnce (użytkownik)", "", h.Name, "RunOnce"})
	}
	for _, t := range targets {
		names, vals, ok := win.RegValues(t.key)
		if !ok {
			continue
		}
		var approved map[string]any
		if t.approved != "" {
			_, approved, _ = win.RegValues(t.approved)
		}
		for _, n := range names {
			enabled := true
			if v, ok := approved[strings.ToLower(n)]; ok {
				enabled = startupApprovedEnabled(v)
			}
			entries = append(entries, StartupEntry{Name: n, Command: win.AnyString(vals[strings.ToLower(n)]), Source: t.src, Enabled: enabled, Owner: t.owner, Approved: t.approved, ApprovedName: n, Kind: t.kind})
		}
	}
	// foldery Autostart (wspólny i użytkowników; stan włączenia w StartupApproved\StartupFolder)
	type folder struct{ path, owner, approved string }
	folders := []folder{{filepath.Join(getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\StartUp`), "wszyscy", `HKLM` + sa + "StartupFolder"}}
	for _, up := range userProfiles(c) {
		hive := ""
		if up.SID != "" && win.RegKeyExists(`HKU\`+up.SID) {
			hive = `HKU\` + up.SID + sa + "StartupFolder"
		}
		folders = append(folders, folder{filepath.Join(up.LocalPath, `AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup`), filepath.Base(up.LocalPath), hive})
	}
	for _, f := range folders {
		var approved map[string]any
		if f.approved != "" {
			_, approved, _ = win.RegValues(f.approved)
		}
		ents, err := os.ReadDir(f.path)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || eqi(e.Name(), "desktop.ini") {
				continue
			}
			full := filepath.Join(f.path, e.Name())
			target := full
			if eqi(filepath.Ext(e.Name()), ".lnk") {
				if t, ok := win.ShortcutTarget(full); ok {
					target = t
				}
			}
			enabled := true
			if v, ok := approved[strings.ToLower(e.Name())]; ok {
				enabled = startupApprovedEnabled(v)
			}
			entries = append(entries, StartupEntry{Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), Command: `"` + target + `"`, Source: "Folder Autostart",
				Enabled: enabled, Owner: f.owner, Approved: f.approved, ApprovedName: e.Name(), Kind: "Folder"})
		}
	}
	return entries
}

var systemHostsRx = rx(`^(cmd|rundll32|wscript|cscript|powershell|mshta|regsvr32)`)

func checkStartup(c *Ctx) {
	entries := startupEntries(c)
	if len(entries) == 0 {
		c.Add(F{Title: "Autostart", Status: OK, Value: "Brak wpisów autostartu"})
		return
	}
	sysRoot := getenv("SystemRoot")
	type out struct {
		row     Row
		enabled bool
		name    string
	}
	var rows []out
	var susp, unsigned, missing []string
	sigChecked := 0
	for _, e := range entries {
		exe := resolveCommandPath(e.Command)
		sys := exe != "" && strings.HasPrefix(strings.ToLower(exe), strings.ToLower(sysRoot))
		wantSig := !sys && e.Enabled && sigChecked < 70
		var fi FileInfo
		hasFi := false
		if exe != "" {
			fi, hasFi = fileInfo(c, exe, wantSig), true
			if wantSig {
				sigChecked++
			}
		}
		var note []string
		if exe != "" && hasFi && !fi.Exists && !systemHostsRx.MatchString(exe) {
			note = append(note, "plik nie istnieje")
			if e.Enabled {
				missing = append(missing, e.Name)
			}
		}
		if suspiciousPathRx.MatchString(exe) {
			note = append(note, "podejrzana lokalizacja")
			if e.Enabled {
				susp = append(susp, fmt.Sprintf("%s (%s)", e.Name, exe))
			}
		}
		if wantSig && hasFi && fi.Exists && !fi.Signed && fi.Company == "" {
			note = append(note, "brak podpisu i producenta")
			unsigned = append(unsigned, e.Name)
		}
		if suspiciousCmdRx.MatchString(e.Command) {
			note = append(note, "polecenie typowe dla złośliwego oprogramowania")
			if e.Enabled {
				susp = append(susp, fmt.Sprintf("%s (polecenie: %s)", e.Name, limitText(e.Command, 80)))
			}
		}
		maker := ""
		if hasFi {
			maker = fi.Company
			if fi.Signer != "" {
				maker = fi.Signer
			}
		}
		// program-pośrednik Windows (rundll32, wscript, powershell...) - oceniamy plik, który faktycznie uruchamia
		for _, pl := range hostPayload(e.Command, exe) {
			plSys := strings.HasPrefix(strings.ToLower(pl), strings.ToLower(sysRoot))
			plSig := !plSys && e.Enabled && sigChecked < 70
			pfi := fileInfo(c, pl, plSig)
			if plSig {
				sigChecked++
			}
			hostName := filepath.Base(exe)
			switch {
			case suspiciousPathRx.MatchString(pl) || appDataRx.MatchString(pl):
				note = append(note, hostName+" uruchamia plik z podejrzanej lokalizacji")
				if e.Enabled {
					susp = append(susp, fmt.Sprintf("%s (%s -> %s)", e.Name, hostName, pl))
				}
			case !pfi.Exists:
				note = append(note, "plik wskazany w poleceniu nie istnieje")
				if e.Enabled {
					missing = append(missing, e.Name)
				}
			case plSig && !pfi.Signed && pfi.Company == "":
				note = append(note, "plik uruchamiany przez "+hostName+" nie ma podpisu ani producenta")
				unsigned = append(unsigned, e.Name)
			}
			if pfi.Exists {
				maker = pfi.Company
				if pfi.Signer != "" {
					maker = pfi.Signer
				}
			}
		}
		stan := "wyłączony"
		if e.Enabled {
			stan = "włączony"
		}
		rows = append(rows, out{row("Nazwa", e.Name, "Stan", stan, "Producent", strings.TrimSpace(maker), "Polecenie", limitText(e.Command, 110),
			"Źródło", e.Source, "Użytkownik", e.Owner, "Uwagi", strings.Join(note, "; ")), e.Enabled, e.Name})
	}
	psSort(rows, func(a, b out) int {
		ea, eb := 1, 1
		if a.enabled {
			ea = 0
		}
		if b.enabled {
			eb = 0
		}
		if ea != eb {
			return ea - eb
		}
		return cmpText(a.name, b.name)
	})
	var tbl []Row
	active := 0
	for _, r := range rows {
		tbl = append(tbl, r.row)
		if r.enabled {
			active++
		}
	}
	c.AddTable(T{Name: "Programy w autostarcie", Rows: tbl})
	d := []string{fmt.Sprintf("Włączone wpisy: %d (łącznie wpisów: %d)", active, len(rows))}
	if len(susp) > 0 {
		d = append(d, "Podejrzane: "+strings.Join(susp, "; "))
	}
	if len(missing) > 0 {
		d = append(d, "Wskazują na nieistniejący plik: "+strings.Join(missing, ", "))
	}
	det := strings.Join(d, "\n")
	switch {
	case len(susp) > 0:
		c.Add(F{Title: "Autostart", Status: Warn, Value: fmt.Sprintf("Włączone wpisy: %d; podejrzane: %d", active, len(susp)), Details: det,
			Advice: "Sprawdź podejrzane wpisy (Autoruns / Menedżer zadań > Autostart): pliki uruchamiane z katalogów tymczasowych lub z ukrywanymi poleceniami są typowe dla złośliwego oprogramowania."})
	case active > 15:
		c.Add(F{Title: "Autostart", Status: Warn, Value: fmt.Sprintf("Włączone wpisy: %d", active), Details: det,
			Advice: "Bardzo wiele programów startuje z systemem - spowalnia to uruchamianie. Wyłącz zbędne (Menedżer zadań > Autostart)."})
	case active > 8:
		c.Add(F{Title: "Autostart", Status: Info, Value: fmt.Sprintf("Włączone wpisy: %d", active), Details: det,
			Advice: "Przejrzyj listę i wyłącz programy, które nie muszą startować z systemem (Menedżer zadań > Autostart)."})
	default:
		c.Add(F{Title: "Autostart", Status: OK, Value: fmt.Sprintf("Włączone wpisy: %d", active), Details: det})
	}
	if len(unsigned) > 0 {
		c.Add(F{Title: "Autostart: pliki bez podpisu", Status: Info, Value: strings.Join(unsigned, ", "), Details: "Brak podpisu cyfrowego i informacji o producencie w pliku wykonywalnym.",
			Advice: "Zweryfikuj pochodzenie tych programów (nazwa pliku w wyszukiwarce, skan VirusTotal)."})
	}
}

var (
	svchostRx   = rx(`\\svchost\.exe$`)
	microsoftRx = rx(`Microsoft`)
)

func checkThirdPartyServices(c *Ctx) {
	svcs, err := win.Services()
	if err != nil {
		c.Log("WARN", "Usługi: "+err.Error())
	}
	if len(svcs) == 0 {
		return
	}
	sysRoot := strings.ToLower(getenv("SystemRoot"))
	var rows []Row
	var susp []string
	running := 0
	for _, s := range svcs {
		path := resolveCommandPath(s.PathName)
		if path == "" {
			continue
		}
		isDll := false
		if svchostRx.MatchString(path) {
			// usługa w svchost.exe: oceniamy bibliotekę ServiceDll - złośliwa biblioteka spoza Windows ukryłaby się za svchost
			if dll := serviceDll(s.Name); dll != "" && !strings.HasPrefix(strings.ToLower(dll), sysRoot) {
				path, isDll = dll, true
			}
		}
		if !isDll && strings.HasPrefix(strings.ToLower(path), sysRoot) {
			continue
		}
		fi := fileInfo(c, path, isDll)
		if microsoftRx.MatchString(fi.Company) && !isDll {
			continue
		}
		var note []string
		active := s.State == "Running" || s.StartMode == "Auto"
		if isDll {
			note = append(note, "biblioteka usługi svchost spoza katalogu Windows")
			if fi.Exists && !fi.Signed && fi.Company == "" && active {
				susp = append(susp, fmt.Sprintf("%s (svchost, biblioteka bez podpisu i producenta: %s)", s.Name, path))
			}
		}
		if suspiciousPathRx.MatchString(path) || appDataRx.MatchString(path) {
			note = append(note, "podejrzana lokalizacja")
			if active {
				susp = append(susp, fmt.Sprintf("%s (%s)", s.Name, path))
			}
		}
		if !fi.Exists {
			note = append(note, "plik nie istnieje")
		}
		if s.State == "Running" {
			running++
		}
		rows = append(rows, row("Usługa", s.DisplayName, "Nazwa", s.Name, "Stan", s.State, "Uruchamianie", s.StartMode, "Konto", s.StartName,
			"Producent", fi.Company, "Ścieżka", limitText(path, 100), "Uwagi", strings.Join(note, "; ")))
	}
	if len(rows) == 0 {
		c.Add(F{Title: "Usługi firm trzecich", Status: OK, Value: "Brak usług spoza Windows"})
		return
	}
	psSort(rows, func(a, b Row) int { return cmpText(fmt.Sprint(a.Get("Usługa")), fmt.Sprint(b.Get("Usługa"))) })
	c.AddTable(T{Name: "Usługi spoza systemu Windows", Rows: rows})
	if len(susp) > 0 {
		c.Add(F{Title: "Usługi firm trzecich", Status: Warn, Value: fmt.Sprintf("Usług: %d (działa: %d); podejrzane: %d", len(rows), running, len(susp)), Details: strings.Join(susp, "\n"),
			Advice: "Usługi uruchamiane z katalogów użytkownika lub tymczasowych są podejrzane - zweryfikuj ich pochodzenie (services.msc, Autoruns)."})
	} else {
		c.Add(F{Title: "Usługi firm trzecich", Status: Info, Value: fmt.Sprintf("Usług: %d (działa: %d)", len(rows), running), Details: "Pełna lista w tabeli „Usługi spoza systemu Windows”."})
	}
}

func checkScheduledTasks(c *Ctx) {
	tasks, err := win.ScheduledTasks(func(folder string) bool { return strings.HasPrefix(strings.ToLower(folder), `\microsoft`) })
	if err != nil {
		c.Log("WARN", "Harmonogram zadań: "+err.Error())
		return
	}
	var rows []Row
	var susp []string
	for _, t := range tasks {
		if strings.HasPrefix(strings.ToLower(t.Path), `\microsoft\`) || t.State == "Disabled" {
			continue
		}
		cmd := strings.TrimSpace(t.Exec + " " + t.Args)
		var note []string
		if suspiciousPathRx.MatchString(resolveCommandPath(cmd)) {
			note = append(note, "podejrzana lokalizacja")
		}
		if suspiciousCmdRx.MatchString(cmd) {
			note = append(note, "polecenie typowe dla złośliwego oprogramowania")
		}
		if len(note) > 0 {
			susp = append(susp, fmt.Sprintf("%s%s - %s: %s", t.Path, t.Name, strings.Join(note, "; "), limitText(cmd, 140)))
		}
		rows = append(rows, row("Zadanie", t.Name, "Ścieżka", t.Path, "Stan", t.State, "Wyzwalacze", strings.Join(t.Triggers, ", "), "Polecenie", limitText(cmd, 110), "Uwagi", strings.Join(note, "; ")))
	}
	if len(rows) == 0 {
		return
	}
	psSort(rows, func(a, b Row) int { return cmpText(fmt.Sprint(a.Get("Zadanie")), fmt.Sprint(b.Get("Zadanie"))) })
	c.AddTable(T{Name: "Zadania harmonogramu (spoza Microsoft)", Rows: rows})
	if len(susp) > 0 {
		c.Add(F{Title: "Zadania harmonogramu", Status: Warn, Value: fmt.Sprintf("Zadań: %d; podejrzane: %d", len(rows), len(susp)), Details: strings.Join(susp, "\n"),
			Advice: "Zweryfikuj podejrzane zadania (taskschd.msc) - złośliwe oprogramowanie często zapewnia sobie trwałość przez harmonogram zadań."})
	} else {
		c.Add(F{Title: "Zadania harmonogramu", Status: Info, Value: fmt.Sprintf("Zadania spoza Microsoft: %d", len(rows)), Details: "Lista w tabeli „Zadania harmonogramu (spoza Microsoft)”."})
	}
}

var (
	minersRx   = rx(`xmrig|minerd|cpuminer|nicehash|nbminer|phoenixminer|claymore|t-rex|lolminer|cgminer|bfgminer|ethminer|cryptonight|monero|kdevtmpfsi`)
	toolsRx    = rx(`mimikatz|meterpreter|beacon\.exe|cobaltstrike|netcat|ncat\.exe|^nc\.exe`)
	sysNamesRx = rx(`^(svchost|lsass|csrss|winlogon|services|smss|wininit|explorer)\.exe$`)
	severeRx   = rx(`koparki|ofensywne|poza katalogiem Windows`)
)

func checkProcesses(c *Ctx) {
	procs, err := win.Processes()
	if err != nil {
		c.Log("WARN", "Procesy: "+err.Error())
	}
	sysRoot := strings.ToLower(getenv("SystemRoot")) + `\`
	var rows []Row
	var names, det []string
	severe := false
	for _, p := range procs {
		var why []string
		if minersRx.MatchString(p.Name) {
			why = append(why, "nazwa typowa dla koparki kryptowalut")
		}
		if toolsRx.MatchString(p.Name) {
			why = append(why, "narzędzie ofensywne / backdoor")
		}
		path := ""
		if p.PID != 0 && p.PID != 4 {
			path = win.ProcessPath(p.PID)
		}
		if path != "" && suspiciousPathRx.MatchString(path) {
			why = append(why, "uruchomiony z katalogu tymczasowego/publicznego")
		}
		if path != "" && sysNamesRx.MatchString(p.Name) && !strings.HasPrefix(strings.ToLower(path), sysRoot) {
			why = append(why, "nazwa procesu systemowego, ale poza katalogiem Windows")
		}
		if len(why) == 0 {
			continue
		}
		w := strings.Join(why, "; ")
		if severeRx.MatchString(w) {
			severe = true
		}
		rows = append(rows, row("Proces", p.Name, "PID", int64(p.PID), "Powód", w, "Ścieżka", path))
		if len(names) < 4 {
			names = append(names, p.Name)
		}
		det = append(det, fmt.Sprintf("%s (PID %d): %s - %s", p.Name, p.PID, w, path))
	}
	if len(rows) == 0 {
		c.Add(F{Title: "Podejrzane procesy", Status: OK, Value: fmt.Sprintf("Nie wykryto (sprawdzono procesów: %d)", len(procs))})
		return
	}
	c.AddTable(T{Name: "Podejrzane procesy", Rows: rows})
	st := Warn
	if severe {
		st = Crit
	}
	c.Add(F{Title: "Podejrzane procesy", Status: st, Value: strings.Join(names, ", "), Details: strings.Join(det, "\n"),
		Advice: "Wskazane procesy należy zweryfikować (ścieżka, podpis, VirusTotal) i w razie potrzeby przeskanować system. Dopasowanie po nazwie bywa mylące - to wskazówka, nie wyrok."})
}
