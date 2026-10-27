package diag

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"serwisdiag/internal/win"
)

// Testy kategorii: Awarie i zdarzenia (checks\60-events.ps1). Zdarzenia czytamy po identyfikatorach i polach
// EventData, a nie po treści komunikatów - działa to tak samo w każdej wersji językowej Windows.

var eventChecks = []*Check{
	{ID: "ev.crash", Category: "events", Title: "Awarie systemu: BSOD i nieoczekiwane restarty", Level: "Quick", Run: checkCrash},
	{ID: "ev.stability", Category: "events", Title: "Indeks stabilności systemu (Monitor niezawodności)", Level: "Quick", Run: checkStability},
	{ID: "ev.hardware", Category: "events", Title: "Błędy sprzętowe w dzienniku: dysk, WHEA, grafika, throttling", Level: "Quick", Run: checkHardwareEvents},
	{ID: "ev.errors", Category: "events", Title: "Najczęstsze błędy w dzienniku zdarzeń", Level: "Quick", Run: checkEventErrors},
	{ID: "ev.apps", Category: "events", Title: "Awarie i zawieszenia aplikacji", Level: "Quick", Run: checkAppCrashes},
	{ID: "ev.services", Category: "events", Title: "Błędy usług i sterowników", Level: "Quick", Run: checkServiceEvents},
	{ID: "ev.memdiag", Category: "events", Title: "Wynik diagnostyki pamięci Windows", Level: "Quick", Run: checkMemDiag},
}

// latestTime odpowiada Get-SdLatestTime dla zdarzeń.
func latestTime(ev []*win.Event) time.Time {
	var t time.Time
	for _, e := range ev {
		if e.Time.After(t) {
			t = e.Time
		}
	}
	return t
}

// group - grupa elementów o wspólnym kluczu (Group-Object: kolejność pierwszego wystąpienia, klucz bez względu
// na wielkość liter).
type group[T any] struct {
	Name  string
	Items []T
}

func groupBy[T any](items []T, key func(T) string) []*group[T] {
	idx := map[string]*group[T]{}
	var out []*group[T]
	for _, it := range items {
		k := key(it)
		lk := strings.ToLower(k)
		g := idx[lk]
		if g == nil {
			g = &group[T]{Name: k}
			idx[lk] = g
			out = append(out, g)
		}
		g.Items = append(g.Items, it)
	}
	return out
}

// sortByCountDesc: Sort-Object Count -Descending (stabilnie).
func sortByCountDesc[T any](g []*group[T]) {
	psSort(g, func(a, b *group[T]) int { return len(b.Items) - len(a.Items) })
}

func newestEvent(ev []*win.Event) *win.Event {
	var n *win.Event
	for _, e := range ev {
		if n == nil || e.Time.After(n.Time) {
			n = e
		}
	}
	return n
}

// werEventTime odpowiada Get-SdWerEventTime: EventTime z Report.wer, a bez dostępu - data utworzenia folderu.
func werEventTime(dir string, created time.Time) time.Time {
	b, err := os.ReadFile(filepath.Join(dir, "Report.wer"))
	if err == nil && len(b) >= 2 {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		if m := regexp.MustCompile(`EventTime=(\d{15,19})`).FindStringSubmatch(string(utf16.Decode(u))); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				return time.Unix(0, (n-116444736000000000)*100).Local()
			}
		}
	}
	return created
}

// ---------------------------------------------------------------- przypisywanie zdarzeń dyskowych do nośników

var externalBusTypes = []int{7, 12, 13} // STORAGE_BUS_TYPE: USB, SD, MMC

type volumeMaps struct {
	devToDisk    map[string]int
	devToLetter  map[string]string
	letterToDisk map[string]int
}

var volGuidRx = regexp.MustCompile(`^\\\\\?\\(Volume\{[0-9a-fA-F-]+\})\\$`)

// volumeMapsFor odpowiada Get-SdVolumeMaps: \Device\HarddiskVolumeN -> numer dysku i litera.
func volumeMapsFor(c *Ctx) *volumeMaps {
	return cached(c, "VolumeMaps", func() *volumeMaps {
		m := &volumeMaps{devToDisk: map[string]int{}, devToLetter: map[string]string{}, letterToDisk: map[string]int{}}
		for _, p := range c.Cim(Q{NS: `root\Microsoft\Windows\Storage`, Class: "MSFT_Partition", Props: []string{"DiskNumber", "DriveLetter", "AccessPaths"}}) {
			letter := ""
			if dl := p.Int("DriveLetter"); (dl >= 'A' && dl <= 'Z') || (dl >= 'a' && dl <= 'z') {
				letter = strings.ToUpper(string(rune(dl))) + ":"
			}
			disk := int(p.Int("DiskNumber"))
			if letter != "" {
				m.letterToDisk[letter] = disk
			}
			for _, ap := range p.Strs("AccessPaths") {
				if g := volGuidRx.FindStringSubmatch(ap); g != nil {
					if dev := win.DosDevice(g[1]); dev != "" {
						m.devToDisk[dev] = disk
						if letter != "" {
							m.devToLetter[dev] = letter
						}
					}
				}
			}
		}
		for _, ld := range c.cim("Win32_LogicalDisk", "DeviceID") {
			l := strings.ToUpper(ld.Str("DeviceID"))
			if l == "" {
				continue
			}
			if dev := win.DosDevice(l); dev != "" {
				if _, ok := m.devToLetter[dev]; !ok {
					m.devToLetter[dev] = l
				}
			}
		}
		return m
	})
}

type diskEventRef struct {
	diskIndex    int // -1 = nieznany
	volume       string
	letter       string
	busType      int // -1 = brak
	device, port string
}

var (
	hdDiskRx = rx(`\\Device\\Harddisk(\d+)\\DR\d+`)
	hdVolRx  = rx(`(\\Device\\HarddiskVolume\d+)`)
	portRx   = rx(`\\Device\\(RaidPort\d+|ScsiPort\d+|Ide\\[^\s,.]+)`)
	letterRx = regexp.MustCompile(`^[A-Za-z]:$`)
	digitsRx = regexp.MustCompile(`^\d+$`)
)

// diskEventRefFor odpowiada Get-SdDiskEventRef.
func diskEventRefFor(e *win.Event, message string) diskEventRef {
	r := diskEventRef{diskIndex: -1, busType: -1}
	first := ""
	if len(e.Props) > 0 {
		first = e.Props[0]
	}
	text := first + " " + message
	if m := hdDiskRx.FindStringSubmatch(text); m != nil {
		r.diskIndex, _ = strconv.Atoi(m[1])
	}
	if v := e.Data["VolumeId"]; letterRx.MatchString(v) {
		r.letter = strings.ToUpper(v)
	}
	if v := e.Data["DeviceName"]; v != "" {
		r.volume = v
	}
	if v := e.Data["BusType"]; digitsRx.MatchString(v) {
		r.busType, _ = strconv.Atoi(v)
	}
	r.device = joinNonEmpty(" ", e.Data["VendorId"], e.Data["ProductId"])
	if r.volume == "" {
		if m := hdVolRx.FindStringSubmatch(text); m != nil {
			r.volume = m[1]
		}
	}
	if m := portRx.FindStringSubmatch(text); m != nil {
		r.port = m[1]
	}
	return r
}

type diskTarget struct{ Kind, Key, Label string }

// resolveDiskEventTarget odpowiada Resolve-SdDiskEventTarget.
// Kind: internal | external | absent | controller | unknown.
func resolveDiskEventTarget(ref diskEventRef, disks []*Disk, maps *volumeMaps) diskTarget {
	letter := ref.letter
	if letter == "" && ref.volume != "" {
		letter = maps.devToLetter[ref.volume]
	}
	idx := ref.diskIndex
	if idx < 0 && ref.volume != "" {
		if d, ok := maps.devToDisk[ref.volume]; ok {
			idx = d
		}
	}
	if idx < 0 && letter != "" {
		if d, ok := maps.letterToDisk[letter]; ok {
			idx = d
		}
	}
	extBus := ref.busType >= 0 && slices.Contains(externalBusTypes, ref.busType)
	var extra []string
	if ref.device != "" {
		extra = append(extra, strings.TrimSpace(ref.device))
	}
	if extBus {
		extra = append(extra, map[int]string{7: "USB", 12: "karta SD", 13: "karta MMC"}[ref.busType])
	}
	extraTxt := ""
	if len(extra) > 0 {
		extraTxt = " (" + strings.Join(extra, ", ") + ")"
	}
	letterTxt := ""
	if letter != "" {
		letterTxt = " " + letter
	}
	if idx >= 0 {
		for _, d := range disks {
			if int(d.Index) != idx {
				continue
			}
			isExt := extBus || eqi(d.Bus, "USB") || eqi(d.Iface, "USB") || strings.HasPrefix(strings.ToUpper(diskTypeName(d)), "USB")
			label := fmt.Sprintf("Dysk %d: %s", idx, d.Model)
			if letter != "" {
				label += " (" + letter + ")"
			}
			if isExt {
				return diskTarget{"external", fmt.Sprintf("disk%d", idx), label + " - dysk zewnętrzny"}
			}
			return diskTarget{"internal", fmt.Sprintf("disk%d", idx), label}
		}
		return diskTarget{"absent", fmt.Sprintf("disk%d", idx), fmt.Sprintf("Harddisk%d%s%s - nośnik obecnie niepodłączony", idx, letterTxt, extraTxt)}
	}
	if letter != "" || ref.volume != "" {
		name := letter
		if name == "" {
			name = ref.volume
		}
		return diskTarget{"absent", "vol" + name, fmt.Sprintf("Wolumin %s%s - nośnik obecnie niepodłączony", name, extraTxt)}
	}
	if ref.port != "" {
		return diskTarget{"controller", "port" + ref.port, fmt.Sprintf("Kontroler pamięci masowej (%s)", ref.port)}
	}
	return diskTarget{"unknown", "unknown", "Nie udało się ustalić nośnika"}
}

// diskEventStatus odpowiada Get-SdDiskEventStatus: tylko dyski komputera mogą dać "krytyczne".
func diskEventStatus(kind string, hintSev Status) Status {
	switch kind {
	case "internal", "controller":
		return hintSev
	case "unknown", "external":
		return Warn
	}
	return Info
}

// ---------------------------------------------------------------- BSOD i nieoczekiwane restarty

var hexCodeRx = regexp.MustCompile(`(0x[0-9a-fA-F]{1,8})`)

type crash struct {
	t    time.Time
	info bugcheckInfo
}

func checkCrash(c *Ctx) {
	const days = 90
	since := time.Now().AddDate(0, 0, -days)
	since30 := time.Now().AddDate(0, 0, -30)

	// --- niebieskie ekrany (BugCheck 1001)
	bug := c.events(win.EventFilter{Logs: []string{"System"}, IDs: []int{1001}, Providers: []string{"Microsoft-Windows-WER-SystemErrorReporting", "BugCheck"}, Since: since}, 300, false)
	var crashes []crash
	for _, e := range bug {
		s := e.Data["param1"]
		if s == "" && len(e.Props) > 0 {
			s = e.Props[0]
		}
		if m := hexCodeRx.FindStringSubmatch(s); m != nil {
			if info, ok := bugcheckFromText(m[1]); ok {
				crashes = append(crashes, crash{e.Time, info})
			}
		}
	}
	// --- Kernel-Power 41 (nieoczekiwany restart)
	kp := c.events(win.EventFilter{Logs: []string{"System"}, IDs: []int{41}, Providers: []string{"Microsoft-Windows-Kernel-Power"}, Since: since}, 300, false)
	type unexpected struct {
		t    time.Time
		kind string
		code int64
	}
	var unexp []unexpected
	pi := func(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
	for _, e := range kp {
		code := pi(e.Data["BugcheckCode"])
		kind := "power"
		switch {
		case code != 0:
			kind = "bsod"
		case pi(e.Data["PowerButtonTimestamp"]) != 0:
			kind = "button"
		case pi(e.Data["SleepInProgress"]) != 0:
			kind = "sleep"
		}
		unexp = append(unexp, unexpected{e.Time, kind, code})
		// BSOD bez zdarzenia 1001 (np. nie powstał zrzut) - dolicz z Kernel-Power
		if kind == "bsod" {
			dup := false
			for _, cr := range crashes {
				if d := cr.t.Sub(e.Time).Minutes(); d <= 5 && d >= -5 {
					dup = true
					break
				}
			}
			if !dup {
				crashes = append(crashes, crash{e.Time, bugcheckFor(code)})
			}
		}
	}

	// --- BSOD: podsumowanie
	c30 := 0
	for _, cr := range crashes {
		if !cr.t.Before(since30) {
			c30++
		}
	}
	if len(crashes) > 0 {
		byCode := groupBy(crashes, func(x crash) string { return x.info.Code })
		sortByCountDesc(byCode)
		var rows []Row
		for _, g := range byCode {
			i := g.Items[0].info
			last := g.Items[0].t
			for _, x := range g.Items {
				if x.t.After(last) {
					last = x.t
				}
			}
			rows = append(rows, row("Kod STOP", i.Code, "Nazwa", i.Name, "Liczba", int64(len(g.Items)), "Ostatnio", fmtDt(last), "Przyczyna", i.Hint))
		}
		c.AddTable(T{Name: "Niebieskie ekrany (BSOD)", Rows: rows})
		causeLabels := map[string]string{"driver": "sterowniki", "memory": "pamięć RAM", "disk": "dysk / kontroler", "hardware": "sprzęt (CPU/płyta/zasilanie)", "gpu": "karta graficzna", "power": "zasilanie", "system": "pliki systemowe", "unknown": "nieustalona"}
		causes := groupBy(crashes, func(x crash) string { return x.info.Cause })
		sortByCountDesc(causes)
		causeTxt := fmt.Sprintf("%s (%d z %d)", causeLabels[strings.ToLower(causes[0].Name)], len(causes[0].Items), len(crashes))
		var top []string
		for i, g := range byCode {
			if i < 3 {
				top = append(top, fmt.Sprintf("%s %s ×%d", g.Items[0].info.Code, g.Items[0].info.Name, len(g.Items)))
			}
		}
		last := crashes[0]
		for _, x := range crashes {
			if x.t.After(last.t) {
				last = x
			}
		}
		d := []string{
			fmt.Sprintf("Awarie w ciągu %d dni: %d (w ostatnich 30 dniach: %d); ostatnia: %s", days, len(crashes), c30, fmtDt(last.t)),
			"Najczęstsze kody: " + strings.Join(top, "; "),
			"Najbardziej prawdopodobna grupa przyczyn: " + causeTxt,
			"",
			"Wskazówka dla najczęstszego kodu: " + byCode[0].Items[0].info.Hint,
		}
		st := Info
		if c30 >= 3 {
			st = Crit
		} else if c30 >= 1 {
			st = Warn
		}
		c.Add(F{Title: "Niebieskie ekrany (BSOD)", Status: st, Value: fmt.Sprintf("%d w ciągu %d dni (ostatnie 30 dni: %d); najczęściej: %s", len(crashes), days, c30, top[0]),
			Details: strings.Join(d, "\n"), LastEvent: last.t,
			Advice: "Najpierw wyeliminuj grupę przyczyn: " + causeTxt + ". Przeanalizuj zrzuty (WinDbg !analyze -v / BlueScreenView), zaktualizuj sterowniki i BIOS, przetestuj RAM (MemTest86) i dysk (SMART), sprawdź temperatury."})
	} else {
		c.Add(F{Title: "Niebieskie ekrany (BSOD)", Status: OK, Value: fmt.Sprintf("Brak w ciągu %d dni", days)})
	}

	// --- nieoczekiwane restarty bez BSOD
	kindLabel := map[string]string{"power": "nagła utrata zasilania / reset / zabezpieczenie termiczne", "button": "wymuszone wyłączenie przyciskiem zasilania (po zawieszeniu)", "sleep": "awaria podczas przejścia w uśpienie lub wznawiania", "bsod": "niebieski ekran"}
	if len(unexp) > 0 {
		s := slices.Clone(unexp)
		psSort(s, func(a, b unexpected) int { return b.t.Compare(a.t) })
		var rows []Row
		for i, u := range s {
			if i >= 15 {
				break
			}
			code := "-"
			if u.code != 0 {
				code = "0x" + strings.ToUpper(pad8(strconv.FormatUint(uint64(u.code), 16)))
			}
			rows = append(rows, row("Czas", fmtDt(u.t), "Rodzaj", kindLabel[u.kind], "Kod STOP", code))
		}
		c.AddTable(T{Name: "Nieoczekiwane restarty (Kernel-Power 41)", Rows: rows})
	}
	var nb []unexpected
	for _, u := range unexp {
		if u.kind != "bsod" {
			nb = append(nb, u)
		}
	}
	if len(nb) > 0 {
		n30 := 0
		var lastNb time.Time
		for _, u := range nb {
			if !u.t.Before(since30) {
				n30++
			}
			if u.t.After(lastNb) {
				lastNb = u.t
			}
		}
		kinds := groupBy(nb, func(u unexpected) string { return u.kind })
		sortByCountDesc(kinds)
		d := []string{fmt.Sprintf("Nieoczekiwane restarty bez niebieskiego ekranu: %d w ciągu %d dni (ostatnie 30 dni: %d); ostatni: %s", len(nb), days, n30, fmtDt(lastNb))}
		for _, k := range kinds {
			d = append(d, fmt.Sprintf(" - %s: %d", kindLabel[k.Name], len(k.Items)))
		}
		var adv string
		switch kinds[0].Name {
		case "sleep":
			adv = "Awarie w trakcie uśpienia: zaktualizuj BIOS oraz sterowniki (chipset, grafika, Intel ME/AMD PSP), wyłącz szybkie uruchamianie i hibernację jako test, sprawdź baterię i zasilacz."
		case "button":
			adv = "System zawiesza się na tyle, że użytkownik wymusza wyłączenie. Szukaj przyczyny zawieszeń: sterowniki grafiki/dysku, błędy dysku, przegrzanie, uszkodzony RAM."
		default:
			adv = "Nagłe wyłączenia: sprawdź zasilacz/ładowarkę, baterię, gniazdo i listwę, przegrzewanie (czyszczenie układu chłodzenia) oraz zasilacz PSU w komputerach stacjonarnych."
		}
		st := Info
		if n30 >= 6 {
			st = Crit
		} else if n30 >= 2 {
			st = Warn
		}
		c.Add(F{Title: "Nieoczekiwane restarty (bez BSOD)", Status: st, Value: fmt.Sprintf("%d w ciągu %d dni (ostatnie 30 dni: %d)", len(nb), days, n30),
			Details: strings.Join(d, "\n"), Advice: adv, LastEvent: lastNb})
	} else {
		c.Add(F{Title: "Nieoczekiwane restarty (bez BSOD)", Status: OK, Value: fmt.Sprintf("Brak w ciągu %d dni", days)})
	}

	// --- ostatnie restarty / wyłączenia zainicjowane przez system lub użytkownika (zdarzenie 1074)
	if sd := c.events(win.EventFilter{Logs: []string{"System"}, IDs: []int{1074}, Providers: []string{"User32"}, Since: since30}, 40, false); len(sd) > 0 {
		var rows []Row
		prop := func(e *win.Event, i int) string {
			if i < len(e.Props) {
				return e.Props[i]
			}
			return ""
		}
		for i, e := range sd {
			if i >= 15 {
				break
			}
			rows = append(rows, row("Czas", fmtDt(e.Time), "Proces", filepath.Base(prop(e, 0)), "Rodzaj", prop(e, 4), "Powód", limitText(prop(e, 2), 80), "Użytkownik", prop(e, 6)))
		}
		c.AddTable(T{Name: "Ostatnie restarty i wyłączenia (zainicjowane)", Rows: rows, Private: true})
	}

	// --- zrzuty pamięci
	type dump struct {
		name, kind string
		size       int64
		t          time.Time
	}
	var dumps []dump
	winDir := getenv("SystemRoot")
	if ents, err := os.ReadDir(filepath.Join(winDir, "Minidump")); err == nil {
		var ds []dump
		for _, e := range ents {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".dmp") {
				if fi, err := e.Info(); err == nil {
					ds = append(ds, dump{e.Name(), "Minidump", fi.Size(), fi.ModTime()})
				}
			}
		}
		psSort(ds, func(a, b dump) int { return b.t.Compare(a.t) })
		dumps = append(dumps, ds...)
	}
	if fi, err := os.Stat(filepath.Join(winDir, "MEMORY.DMP")); err == nil {
		dumps = append(dumps, dump{"MEMORY.DMP", "Zrzut jądra/pełny", fi.Size(), fi.ModTime()})
	}
	var lkr []dump
	_ = filepath.WalkDir(filepath.Join(winDir, "LiveKernelReports"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".dmp") {
			if fi, err := d.Info(); err == nil {
				lkr = append(lkr, dump{d.Name(), "LiveKernelReport (" + filepath.Base(filepath.Dir(p)) + ")", fi.Size(), fi.ModTime()})
			}
		}
		return nil
	})
	psSort(lkr, func(a, b dump) int { return b.t.Compare(a.t) })
	if len(lkr) > 10 {
		lkr = lkr[:10]
	}
	dumps = append(dumps, lkr...)
	const ccKey = `HKLM\SYSTEM\CurrentControlSet\Control\CrashControl`
	dumpMode := int64(-1)
	if win.RegKeyExists(ccKey) {
		dumpMode = regInt(ccKey, "CrashDumpEnabled", 0)
	}
	modeTxt := map[int64]string{0: "wyłączone", 1: "pełny zrzut pamięci", 2: "zrzut jądra", 3: "mały zrzut (minidump)", 7: "automatyczny zrzut"}[dumpMode]
	if modeTxt == "" {
		modeTxt = "nieznany"
	}
	if len(dumps) > 0 {
		var rows []Row
		for _, d := range dumps {
			rows = append(rows, row("Plik", d.name, "Rodzaj", d.kind, "Rozmiar", fmtSize(float64(d.size), 1), "Data", fmtDt(d.t)))
		}
		c.AddTable(T{Name: "Zrzuty pamięci (dumpy)", Rows: rows})
		c.Add(F{Title: "Zrzuty pamięci", Status: Info, Value: fmt.Sprintf("%s; ostatni: %s; tryb zapisu: %s", fmtCount(len(dumps), "plik", "pliki", "plików"), fmtDt(dumps[0].t), modeTxt),
			Advice: "Analiza zrzutu wskaże sterownik/moduł powodujący awarię: WinDbg (!analyze -v), BlueScreenView lub WhoCrashed."})
	} else if len(crashes) > 0 && dumpMode == 0 {
		c.Add(F{Title: "Zrzuty pamięci", Status: Warn, Value: "Zapis zrzutów jest wyłączony, a system ulegał awariom",
			Advice: "Włącz zapis małych zrzutów pamięci (Zaawansowane ustawienia systemu > Uruchamianie i odzyskiwanie), aby kolejna awaria mogła zostać przeanalizowana."})
	}
	if len(crashes) > 0 && win.RegKeyExists(ccKey) && regInt(ccKey, "AutoReboot", 0) == 1 {
		c.Add(F{Title: "Automatyczny restart po awarii", Status: Info, Value: "Włączony - niebieski ekran znika po chwili",
			Advice: "Na czas diagnozy wyłącz automatyczne ponowne uruchamianie, aby odczytać kod STOP z ekranu."})
	}

	// --- raporty WER typu Kernel_<kod>_...: kod w nazwie to kod STOP / LiveKernelEvent zapisany szesnastkowo
	wer := filepath.Join(getenv("ProgramData"), `Microsoft\Windows\WER`)
	kernelRx := regexp.MustCompile(`(?i)^Kernel_[0-9a-f]+_`)
	type kr struct {
		name string
		t    time.Time
	}
	var reports []kr
	for _, sub := range []string{"ReportArchive", "ReportQueue"} {
		ents, err := os.ReadDir(filepath.Join(wer, sub))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || !kernelRx.MatchString(e.Name()) {
				continue
			}
			created := time.Time{}
			if fi, err := e.Info(); err == nil {
				created = fileCreationTime(fi)
			}
			if when := werEventTime(filepath.Join(wer, sub, e.Name()), created); !when.Before(since) {
				reports = append(reports, kr{e.Name(), when})
			}
		}
	}
	if len(reports) > 0 {
		grp := groupBy(reports, func(x kr) string { return strings.Split(x.name, "_")[1] })
		sortByCountDesc(grp)
		var lines, short []string
		gpu := 0
		var lastAll time.Time
		for i, g := range grp {
			info, _ := bugcheckFromText("0x" + g.Name)
			known := info.Name != "" && info.Name != "Nieznany kod"
			nm := "kod 0x" + g.Name
			if known {
				nm = info.Code + " " + info.Name
				if info.Cause == "gpu" {
					gpu += len(g.Items)
				}
			}
			last := g.Items[0].t
			for _, x := range g.Items {
				if x.t.After(last) {
					last = x.t
				}
				if x.t.After(lastAll) {
					lastAll = x.t
				}
			}
			lines = append(lines, fmt.Sprintf("%s ×%d, ostatnio %s", nm, len(g.Items), fmtDt(last)))
			if known {
				lines = append(lines, "    "+info.Hint)
			}
			if i < 3 {
				sn := "kod 0x" + g.Name
				if known {
					sn = info.Name
				}
				short = append(short, fmt.Sprintf("%s ×%d", sn, len(g.Items)))
			}
		}
		st := Info
		if gpu >= 2 {
			st = Warn
		}
		c.Add(F{Title: "Raporty awarii jądra (WER)", Status: st, Value: strings.Join(short, "; "), LastEvent: lastAll,
			Details: fmt.Sprintf("Raporty Kernel_* w ProgramData\\Microsoft\\Windows\\WER z ostatnich %d dni (także zawieszenia sterowników bez niebieskiego ekranu).\n", days) + strings.Join(lines, "\n"),
			Advice:  "Powtarzające się zawieszenia GPU (kody 0x141/0x117): zainstaluj czysto sterownik grafiki, sprawdź temperatury, zasilanie i stabilność podkręcenia/VRAM."})
	}
}

// ---------------------------------------------------------------- indeks stabilności

type stabilityPoint struct {
	t   time.Time
	idx float64
}

type dailyStability struct {
	day       string
	min, last float64
}

type stabilitySummary struct {
	current, min7, min30 float64
	weekAgo              *float64
	t                    time.Time
	drop, stale          bool
	daily                []dailyStability
	status               Status
}

// stabilitySummaryFor odpowiada Get-SdStabilitySummary (funkcja czysta).
func stabilitySummaryFor(points []stabilityPoint, now time.Time) *stabilitySummary {
	if len(points) == 0 {
		return nil
	}
	p := slices.Clone(points)
	psSort(p, func(a, b stabilityPoint) int { return a.t.Compare(b.t) })
	cur := p[len(p)-1]
	d7, d30 := now.AddDate(0, 0, -7), now.AddDate(0, 0, -30)
	var last7, last30, older []stabilityPoint
	for _, x := range p {
		if !x.t.Before(d7) {
			last7 = append(last7, x)
		}
		if !x.t.Before(d30) {
			last30 = append(last30, x)
		}
		if !x.t.After(d7) {
			older = append(older, x)
		}
	}
	if len(last7) == 0 {
		last7 = []stabilityPoint{cur}
	}
	if len(last30) == 0 {
		last30 = []stabilityPoint{cur}
	}
	minOf := func(s []stabilityPoint) float64 {
		m := s[0].idx
		for _, x := range s {
			if x.idx < m {
				m = x.idx
			}
		}
		return m
	}
	s := &stabilitySummary{current: cur.idx, t: cur.t, min7: minOf(last7), min30: minOf(last30)}
	if len(older) > 0 {
		v := older[len(older)-1].idx
		s.weekAgo = &v
	}
	for _, g := range groupBy(last30, func(x stabilityPoint) string { return x.t.Format("2006-01-02") }) {
		s.daily = append(s.daily, dailyStability{day: g.Name, min: minOf(g.Items), last: g.Items[len(g.Items)-1].idx})
	}
	psSort(s.daily, func(a, b dailyStability) int { return strings.Compare(a.day, b.day) })
	s.status = OK
	if s.current < 5 {
		s.status = Warn
	} else if s.current < 7 || s.min7 < 5 {
		s.status = Info
	}
	s.drop = s.weekAgo != nil && *s.weekAgo-s.current >= 2
	if s.drop && s.status == OK {
		s.status = Info
	}
	s.stale = now.Sub(cur.t).Hours()/24 > 3
	return s
}

func checkStability(c *Ctx) {
	var pts []stabilityPoint
	for _, r := range c.Cim(Q{Class: "Win32_ReliabilityStabilityMetrics", Props: []string{"SystemStabilityIndex", "TimeGenerated"}, Timeout: 60e9}) {
		t, ok := r.Time("TimeGenerated")
		if r.Has("SystemStabilityIndex") && ok {
			pts = append(pts, stabilityPoint{t, r.Float("SystemStabilityIndex")})
		}
	}
	s := stabilitySummaryFor(pts, time.Now())
	if s == nil {
		c.Add(F{Title: "Indeks stabilności systemu", Status: Info, Value: "Brak danych Monitora niezawodności",
			Details: `Windows nie zebrał danych o niezawodności (np. wyłączone zadanie harmonogramu \Microsoft\Windows\RAC\RacTask lub zasady firmowe).`,
			Advice:  "Jeśli Monitor niezawodności (perfmon /rel) jest pusty, włącz zadanie RacTask w Harmonogramie zadań - dane zbierają się co godzinę."})
		return
	}
	val := fmt.Sprintf("%s / 10 (najniższy w 30 dniach: %s", fmtNum(s.current, 1), fmtNum(s.min30, 1))
	if s.weekAgo != nil {
		val += "; tydzień temu: " + fmtNum(*s.weekAgo, 1)
	}
	val += ")"
	d := []string{
		"Indeks 1-10 wylicza co godzinę Monitor niezawodności Windows na podstawie awarii aplikacji, systemu i sterowników oraz nieudanych instalacji. 10 = brak problemów; po awarii indeks spada i powoli wraca.",
		fmt.Sprintf("Ostatni pomiar: %s; najniższa wartość w ostatnich 7 dniach: %s", fmtDt(s.t), fmtNum(s.min7, 1)),
	}
	if s.drop {
		d = append(d, "W ciągu tygodnia indeks spadł o co najmniej 2 punkty - pojawiły się nowe awarie.")
	}
	if s.stale {
		d = append(d, "Ostatni pomiar jest starszy niż 3 dni - Monitor niezawodności mógł przestać zbierać dane.")
	}
	var rows []Row
	for i := len(s.daily) - 1; i >= 0; i-- {
		x := s.daily[i]
		rows = append(rows, row("Dzień", x.day, "Minimum", fmtNum(x.min, 2), "Na koniec dnia", fmtNum(x.last, 2)))
	}
	c.AddTable(T{Name: "Indeks stabilności - minimum dzienne (30 dni)", Rows: rows})
	adv := ""
	if s.status != OK {
		adv = "Otwórz Monitor niezawodności (perfmon /rel lub Narzędzia serwisowe), aby zobaczyć, które awarie obniżyły indeks; porównaj z wynikami testów BSOD i awarii aplikacji."
	}
	c.SetMeta("Stability", fmtNum(s.current, 1))
	c.Add(F{Title: "Indeks stabilności systemu", Status: s.status, Value: val, Details: strings.Join(d, "\n"), Advice: adv})
}

// ---------------------------------------------------------------- błędy sprzętowe w dzienniku

func hintSevFor(provider string, id int) (eventHint, bool) { return eventHintFor(provider, id) }

func checkHardwareEvents(c *Ctx) {
	const days = 30
	since := time.Now().AddDate(0, 0, -days)
	// --- dyski i system plików: każde zdarzenie przypisujemy do konkretnego nośnika
	diskEv := c.events(win.EventFilter{Logs: []string{"System"}, Providers: []string{"disk", "Ntfs", "Microsoft-Windows-Ntfs", "storahci", "stornvme", "iaStorA", "iaStorAC", "iaStorAVC", "nvstor", "amdsata", "volmgr", "Microsoft-Windows-StorPort"},
		IDs: []int{7, 9, 11, 15, 51, 52, 55, 129, 137, 140, 153}, Since: since}, 500, true)
	if len(diskEv) == 0 {
		c.Add(F{Title: "Błędy dysku i systemu plików", Status: OK, Value: fmt.Sprintf("Brak zdarzeń w ciągu %d dni", days)})
	} else {
		disks := diskInventory(c)
		maps := volumeMapsFor(c)
		type tgroup struct {
			target diskTarget
			events []*win.Event
		}
		var groups []*tgroup
		byKey := map[string]*tgroup{}
		for _, e := range diskEv {
			t := resolveDiskEventTarget(diskEventRefFor(e, firstLine(e.Message(), 300)), disks, maps)
			g := byKey[t.Key]
			if g == nil {
				g = &tgroup{target: t}
				byKey[t.Key] = g
				groups = append(groups, g)
			}
			g.events = append(g.events, e)
		}
		order := []string{"internal", "controller", "unknown", "external", "absent"}
		psSort(groups, func(a, b *tgroup) int { return slices.Index(order, a.target.Kind) - slices.Index(order, b.target.Kind) })
		var lines []string
		var statuses []Status
		count := map[string]int{}
		for _, g := range groups {
			k := g.target.Kind
			count[k] += len(g.events)
			lines = append(lines, g.target.Label+":")
			hintSev := Warn
			subs := groupBy(g.events, func(e *win.Event) string { return fmt.Sprintf("%s|%d", e.Provider, e.ID) })
			sortByCountDesc(subs)
			for _, sub := range subs {
				p := strings.SplitN(sub.Name, "|", 2)
				id, _ := strconv.Atoi(p[1])
				hint, ok := eventHintFor(p[0], id)
				if ok && hint.Sev == "crit" {
					hintSev = Crit
				}
				last := newestEvent(sub.Items)
				txt := hint.Text
				if !ok {
					txt = firstLine(last.Message(), 140)
				}
				lines = append(lines, fmt.Sprintf("    %s/%s ×%d, ostatnio %s - %s", p[0], p[1], len(sub.Items), fmtDt(last.Time), txt))
			}
			statuses = append(statuses, diskEventStatus(k, hintSev))
		}
		status := worst(statuses...)
		onComputer := count["internal"] + count["controller"] + count["unknown"]
		var parts []string
		if onComputer > 0 {
			parts = append(parts, fmtCount(onComputer, "zdarzenie", "zdarzenia", "zdarzeń")+" na dyskach komputera")
		}
		if count["external"] > 0 {
			parts = append(parts, fmtCount(count["external"], "zdarzenie", "zdarzenia", "zdarzeń")+" na dysku zewnętrznym")
		}
		if count["absent"] > 0 {
			parts = append(parts, fmtCount(count["absent"], "zdarzenie", "zdarzenia", "zdarzeń")+" na nośniku, który nie jest teraz podłączony")
		}
		var advice string
		switch {
		case onComputer > 0:
			advice = "Zdarzenia na dyskach komputera wskazują na problem z dyskiem, kablem SATA, kontrolerem lub zasilaniem. Sprawdź SMART, wykonaj kopię danych, wymień kabel; sam chkdsk /r na niesprawnym dysku może pogorszyć sytuację."
		case count["external"] > 0:
			advice = "Błędy dotyczą dysku zewnętrznego (USB / karta pamięci). Sprawdź ten nośnik osobno, wymień kabel lub port i nie odłączaj go w trakcie zapisu."
		default:
			advice = "Błędy dotyczą nośnika, którego teraz nie ma (pendrive, dysk USB, karta pamięci) - nie świadczą o awarii dysków tego komputera. Jeśli nośnik należy do klienta, sprawdź go osobno; nagłe odłączenie podczas zapisu też wywołuje takie błędy."
		}
		c.Add(F{Title: "Błędy dysku i systemu plików", Status: status, Value: fmt.Sprintf("W ciągu %d dni: ", days) + strings.Join(parts, "; "),
			Details: strings.Join(lines, "\n"), Advice: advice, LastEvent: latestTime(diskEv)})
	}

	// --- pozostałe zdarzenia sprzętowe: tylko poziomy krytyczny, błąd i ostrzeżenie
	defs := []struct {
		title  string
		filter win.EventFilter
		advice string
	}{
		{"Błędy sprzętowe zgłoszone przez WHEA", win.EventFilter{Logs: []string{"System"}, Providers: []string{"Microsoft-Windows-WHEA-Logger"}, Levels: []int{1, 2, 3}, Since: since},
			"WHEA raportuje błędy sprzętowe procesora, pamięci lub magistrali PCIe. Sprawdź temperatury, RAM (MemTest86), zasilanie, zresetuj podkręcanie/XMP i zaktualizuj BIOS."},
		{"Zawieszenia sterownika grafiki (TDR)", win.EventFilter{Logs: []string{"System"}, Providers: []string{"Display", "nvlddmkm", "amdkmdag", "amdwddmg", "igfx"}, Levels: []int{1, 2, 3}, Since: since},
			"Sterownik grafiki przestaje odpowiadać. Zainstaluj czysto aktualny sterownik, sprawdź temperatury i zasilanie karty, wyłącz podkręcanie GPU."},
		{"Ograniczanie wydajności procesora (throttling)", win.EventFilter{Logs: []string{"System"}, Providers: []string{"Microsoft-Windows-Kernel-Processor-Power"}, IDs: []int{37}, Since: since},
			"Firmware ogranicza taktowanie procesora - zwykle przegrzanie lub niewystarczające zasilanie. Oczyść chłodzenie, wymień pastę termoprzewodzącą, sprawdź zasilacz/ładowarkę."},
	}
	for _, def := range defs {
		ev := c.events(def.filter, 500, false)
		if len(ev) == 0 {
			c.Add(F{Title: def.title, Status: OK, Value: fmt.Sprintf("Brak zdarzeń w ciągu %d dni", days)})
			continue
		}
		worstSt := Warn
		var lines, sum []string
		grp := groupBy(ev, func(e *win.Event) string { return fmt.Sprintf("%s|%d", e.Provider, e.ID) })
		sortByCountDesc(grp)
		for i, g := range grp {
			p := strings.SplitN(g.Name, "|", 2)
			id, _ := strconv.Atoi(p[1])
			hint, ok := eventHintFor(p[0], id)
			if ok && hint.Sev == "crit" {
				worstSt = Crit
			}
			last := newestEvent(g.Items)
			txt := hint.Text
			if !ok {
				txt = firstLine(last.Message(), 140)
			}
			lines = append(lines, fmt.Sprintf("%s (ID %s) ×%d, ostatnio %s - %s", p[0], p[1], len(g.Items), fmtDt(last.Time), txt))
			if i < 3 {
				sum = append(sum, fmt.Sprintf("%s/%s ×%d", p[0], p[1], len(g.Items)))
			}
		}
		c.Add(F{Title: def.title, Status: worstSt, Value: fmt.Sprintf("%s w ciągu %d dni: %s", fmtCount(len(ev), "zdarzenie", "zdarzenia", "zdarzeń"), days, strings.Join(sum, ", ")),
			Details: strings.Join(lines, "\n"), Advice: def.advice, LastEvent: latestTime(ev)})
	}
}

// ---------------------------------------------------------------- najczęstsze błędy

func checkEventErrors(c *Ctx) {
	days := 14
	if c.Full() {
		days = 30
	}
	ev := c.events(win.EventFilter{Logs: []string{"System", "Application"}, Levels: []int{1, 2}, Since: time.Now().AddDate(0, 0, -days)}, 4000, false)
	if len(ev) == 0 {
		c.Add(F{Title: "Błędy w dzienniku zdarzeń", Status: OK, Value: fmt.Sprintf("Brak błędów i zdarzeń krytycznych w ciągu %d dni", days)})
		return
	}
	// Kernel-Power 41 (zdarzenie krytyczne) jest oceniane w teście nieoczekiwanych restartów - nie liczymy go drugi raz.
	var critEv []*win.Event
	sysN, appN := 0, 0
	for _, e := range ev {
		if e.Level == 1 && !(eqi(e.Provider, "Microsoft-Windows-Kernel-Power") && e.ID == 41) {
			critEv = append(critEv, e)
		}
		switch {
		case eqi(e.Log, "System"):
			sysN++
		case eqi(e.Log, "Application"):
			appN++
		}
	}
	grp := groupBy(ev, func(e *win.Event) string { return fmt.Sprintf("%s|%s|%d", e.Log, e.Provider, e.ID) })
	sortByCountDesc(grp)
	var rows []Row
	var top []string
	real := 0
	for i, g := range grp {
		p := strings.SplitN(g.Name, "|", 3)
		id, _ := strconv.Atoi(p[2])
		hint, ok := eventHintFor(p[1], id)
		noise := ok && hint.Sev == "noise"
		if !noise {
			real += len(g.Items)
		}
		if i >= 25 {
			continue
		}
		last := newestEvent(g.Items)
		mean := hint.Text
		if !ok {
			mean = firstLine(last.Message(), 150)
		}
		if noise {
			mean = "(nieszkodliwe) " + mean
		} else if len(top) < 3 {
			top = append(top, fmt.Sprintf("%s/%d ×%d", p[1], id, len(g.Items)))
		}
		rows = append(rows, row("Dziennik", p[0], "Źródło", p[1], "ID", int64(id), "Liczba", int64(len(g.Items)), "Ostatnio", fmtDt(last.Time), "Opis", mean))
	}
	c.AddTable(T{Name: "Najczęstsze błędy w dzienniku zdarzeń", Rows: rows})
	val := fmt.Sprintf("w ciągu %d dni - System: %d, Aplikacje: %d; zdarzeń krytycznych: %d", days, sysN, appN, len(critEv))
	if len(top) > 0 {
		val += "; najczęściej: " + strings.Join(top, ", ")
	}
	switch {
	case len(critEv) > 0:
		// ostrzeżenie wynika ze zdarzeń krytycznych - liczy się ostatnie z nich, a nie najnowszy zwykły błąd
		c.Add(F{Title: "Błędy w dzienniku zdarzeń", Status: Warn, Value: val, LastEvent: latestTime(critEv),
			Details: "Zdarzenia krytyczne (poziom 1) zwykle oznaczają awarię lub nagłe wyłączenie systemu - zob. test o BSOD.",
			Advice:  "Przejrzyj tabelę „Najczęstsze błędy w dzienniku zdarzeń” oraz Podgląd zdarzeń (eventvwr.msc) dla wskazanych źródeł."})
	case real >= 300:
		c.Add(F{Title: "Błędy w dzienniku zdarzeń", Status: Info, Value: val, Details: "Duża liczba błędów jednego źródła często wskazuje źródło problemu.",
			Advice: "Sprawdź źródła z największą liczbą błędów w tabeli „Najczęstsze błędy w dzienniku zdarzeń”."})
	default:
		c.Add(F{Title: "Błędy w dzienniku zdarzeń", Status: OK, Value: val})
	}
}

// ---------------------------------------------------------------- awarie aplikacji

var gpuModRx = rx(`^(nv|atidxx|amdxx|ig[a-z0-9]*icd|nvwgf2|nvlddmkm|aticfx|amdvlk)`)

func checkAppCrashes(c *Ctx) {
	const days = 30
	ev := c.events(win.EventFilter{Logs: []string{"Application"}, IDs: []int{1000, 1002}, Providers: []string{"Application Error", "Application Hang"}, Since: time.Now().AddDate(0, 0, -days)}, 1500, false)
	if len(ev) == 0 {
		c.Add(F{Title: "Awarie aplikacji", Status: OK, Value: fmt.Sprintf("Brak awarii aplikacji w ciągu %d dni", days)})
		return
	}
	type agg struct {
		app, mod, kind, ver, code string
		count                     int
		last                      time.Time
	}
	byKey := map[string]*agg{}
	var list []*agg
	for _, e := range ev {
		app := e.Data["AppName"]
		if app == "" {
			app = "?"
		}
		mod := e.Data["ModuleName"]
		kind := "awaria"
		if e.ID == 1002 {
			kind = "zawieszenie"
		}
		k := strings.ToLower(app + "|" + mod + "|" + kind)
		a := byKey[k]
		if a == nil {
			a = &agg{app: app, mod: mod, kind: kind, last: e.Time, ver: e.Data["AppVersion"], code: e.Data["ExceptionCode"]}
			byKey[k] = a
			list = append(list, a)
		}
		a.count++
		if e.Time.After(a.last) {
			a.last = e.Time
		}
	}
	psSort(list, func(a, b *agg) int { return b.count - a.count })
	var rows []Row
	var sum []string
	gpu := false
	for i, a := range list {
		if i < 20 {
			rows = append(rows, row("Aplikacja", a.app, "Wersja", a.ver, "Rodzaj", a.kind, "Moduł powodujący", a.mod, "Kod wyjątku", a.code, "Liczba", int64(a.count), "Ostatnio", fmtDt(a.last)))
		}
		if i < 3 {
			sum = append(sum, fmt.Sprintf("%s ×%d", a.app, a.count))
		}
		if gpuModRx.MatchString(a.mod) {
			gpu = true
		}
	}
	c.AddTable(T{Name: "Awarie i zawieszenia aplikacji", Rows: rows})
	st := Info
	if list[0].count >= 10 {
		st = Warn
	}
	adv := "Zaktualizuj lub zainstaluj ponownie najczęściej wysypujące się aplikacje; sprawdź ich zgodność i dostępne poprawki."
	if gpu {
		adv += " Awarie w modułach sterownika grafiki wskazują na problem ze sterownikiem GPU - wykonaj czystą instalację sterownika."
	}
	c.Add(F{Title: "Awarie aplikacji", Status: st, Value: fmt.Sprintf("%s w ciągu %d dni; najczęściej: %s", fmtCount(len(ev), "zdarzenie", "zdarzenia", "zdarzeń"), days, strings.Join(sum, ", ")),
		Details: "Pełna lista w tabeli „Awarie i zawieszenia aplikacji”.", Advice: adv, LastEvent: latestTime(ev)})
}

// ---------------------------------------------------------------- usługi i sterowniki

var umdfRx = rx(`WUDFRd`)

func checkServiceEvents(c *Ctx) {
	const days = 14
	since := time.Now().AddDate(0, 0, -days)
	ev := c.events(win.EventFilter{Logs: []string{"System"}, Providers: []string{"Service Control Manager"}, IDs: []int{7000, 7001, 7009, 7011, 7023, 7024, 7031, 7034}, Since: since}, 800, false)
	if len(ev) == 0 {
		c.Add(F{Title: "Błędy usług", Status: OK, Value: fmt.Sprintf("Brak błędów uruchamiania i awarii usług w ciągu %d dni", days)})
	} else {
		type agg struct {
			name  string
			count int
			ids   map[int]bool
			last  time.Time
		}
		byName := map[string]*agg{}
		var list []*agg
		for _, e := range ev {
			if len(e.Props) == 0 || e.Props[0] == "" {
				continue
			}
			name := e.Props[0]
			a := byName[strings.ToLower(name)]
			if a == nil {
				a = &agg{name: name, ids: map[int]bool{}, last: e.Time}
				byName[strings.ToLower(name)] = a
				list = append(list, a)
			}
			a.count++
			a.ids[e.ID] = true
			if e.Time.After(a.last) {
				a.last = e.Time
			}
		}
		psSort(list, func(a, b *agg) int { return b.count - a.count })
		var rows []Row
		var sum []string
		for i, a := range list {
			if i < 15 {
				var ids []string
				for id := range a.ids {
					ids = append(ids, strconv.Itoa(id))
				}
				slices.Sort(ids)
				rows = append(rows, row("Usługa", a.name, "Liczba", int64(a.count), "ID zdarzeń", strings.Join(ids, ", "), "Ostatnio", fmtDt(a.last)))
			}
			if i < 3 {
				sum = append(sum, fmt.Sprintf("%s ×%d", a.name, a.count))
			}
		}
		c.AddTable(T{Name: "Usługi kończące się błędem", Rows: rows})
		st := Info
		if len(ev) >= 10 {
			st = Warn
		}
		c.Add(F{Title: "Błędy usług", Status: st, Value: fmt.Sprintf("%s w ciągu %d dni: %s", fmtCount(len(ev), "zdarzenie", "zdarzenia", "zdarzeń"), days, strings.Join(sum, ", ")),
			Details: "Zdarzenia 7000/7001/7009/7011/7023/7024/7031/7034 menedżera usług.", LastEvent: latestTime(ev),
			Advice: "Sprawdź w services.msc wskazane usługi (typ uruchamiania, konto, zależności). Powtarzające się awarie usługi zwykle wymagają aktualizacji lub ponownej instalacji powiązanego programu."})
	}
	pnp := c.events(win.EventFilter{Logs: []string{"System"}, Providers: []string{"Microsoft-Windows-Kernel-PnP"}, IDs: []int{219}, Since: since}, 200, true)
	if len(pnp) == 0 {
		return
	}
	grp := groupBy(pnp, func(e *win.Event) string { return firstLine(e.Message(), 110) })
	sortByCountDesc(grp)
	var names []string
	for i, g := range grp {
		if i < 5 {
			names = append(names, fmt.Sprintf("%s (×%d)", g.Name, len(g.Items)))
		}
	}
	// sterownik UMDF (WUDFRd) zgłasza to przy wielu urządzeniach wirtualnych/przenośnych - zwykle nieszkodliwe
	onlyUmdf := true
	for _, e := range pnp {
		if !umdfRx.MatchString(firstLine(e.Message(), 200)) {
			onlyUmdf = false
			break
		}
	}
	cnt := fmtCount(len(pnp), "zdarzenie", "zdarzenia", "zdarzeń")
	if onlyUmdf {
		c.Add(F{Title: "Sterowniki, których nie udało się załadować", Status: Info, Value: fmt.Sprintf("%s w ciągu %d dni (wyłącznie sterownik UMDF WUDFRd - zwykle nieszkodliwe)", cnt, days),
			Details: strings.Join(names, "\n"), LastEvent: latestTime(pnp)})
	} else {
		c.Add(F{Title: "Sterowniki, których nie udało się załadować", Status: Warn, Value: fmt.Sprintf("%s w ciągu %d dni", cnt, days), Details: strings.Join(names, "\n"), LastEvent: latestTime(pnp),
			Advice: "Zainstaluj ponownie lub zaktualizuj sterowniki wskazanych urządzeń (Menedżer urządzeń); sprawdź też, czy nie blokuje ich Izolacja rdzenia (Memory integrity)."})
	}
}

var (
	memOkRx   = rx(`nie wykry|no errors|did not detect|nie znalaz`)
	memCritRx = rx(`sprzętow|hardware errors|wykry[łl]o błędy|detected hardware|detected errors`)
)

func checkMemDiag(c *Ctx) {
	ev := c.events(win.EventFilter{Logs: []string{"System"}, Providers: []string{"Microsoft-Windows-MemoryDiagnostics-Results"}, Since: time.Now().AddDate(0, 0, -365)}, 10, false)
	if len(ev) == 0 {
		return
	}
	e := newestEvent(ev)
	msg := firstLine(e.Message(), 200)
	st, adv := Info, ""
	if memOkRx.MatchString(msg) {
		st = OK
	} else if memCritRx.MatchString(msg) {
		st = Crit
		adv = "Test pamięci wykrył błędy - wymień uszkodzony moduł RAM (testuj moduły pojedynczo) i sprawdź gniazda."
	}
	c.Add(F{Title: "Diagnostyka pamięci Windows", Status: st, Value: fmt.Sprintf("%s: %s", fmtDt(e.Time), msg), Advice: adv})
}
