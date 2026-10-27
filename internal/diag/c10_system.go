package diag

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: System (checks\10-system.ps1)

var chassisNames = map[int64]string{
	3: "Komputer stacjonarny", 4: "Komputer stacjonarny (niski)", 5: "Obudowa pizza-box", 6: "Mini tower", 7: "Tower",
	8: "Laptop (przenośny)", 9: "Laptop", 10: "Notebook", 11: "Urządzenie ręczne", 12: "Stacja dokująca",
	13: "All-in-One", 14: "Subnotebook", 15: "Obudowa SFF", 16: "Lunch box", 17: "Serwer",
	23: "Serwer rack", 24: "Zamknięta obudowa PC", 30: "Tablet", 31: "Laptop 2w1", 32: "Tablet z klawiaturą",
	35: "Mini PC", 36: "Stick PC",
}

func chassisName(types []int64) string {
	for _, t := range types {
		if n, ok := chassisNames[t]; ok {
			return n
		}
	}
	return "Nieokreślony"
}

var systemChecks = []*Check{
	{ID: "sys.os", Category: "system", Title: "System operacyjny i okres wsparcia", Level: "Quick", Run: checkOS},
	{ID: "sys.uptime", Category: "system", Title: "Czas pracy i szybkie uruchamianie", Level: "Quick", Run: checkUptime},
	{ID: "sys.identity", Category: "system", Title: "Komputer, płyta główna i BIOS/UEFI", Level: "Quick", Run: checkIdentity},
	{ID: "sys.win11", Category: "system", Title: "Gotowość na Windows 11", Level: "Quick", Run: checkWin11},
	{ID: "sys.restore", Category: "system", Title: "Punkty przywracania systemu", Level: "Quick", NeedsAdmin: true, Run: checkRestore},
}

func checkOS(c *Ctx) {
	w := winInfo(c)
	os := osObj(c)
	if os == nil {
		c.Add(F{Title: "System operacyjny", Status: Err, Value: "Nie udało się odczytać informacji o systemie (WMI jest niedostępne lub uszkodzone).",
			Advice: "Uszkodzone repozytorium WMI? Spróbuj: winmgmt /verifyrepository (jako administrator)."})
		return
	}
	name := joinNonEmpty(" ", w.Caption, w.Version)
	buildTxt := strconv.FormatInt(w.Build, 10)
	if w.Ubr > 0 {
		buildTxt = fmt.Sprintf("%d.%d", w.Build, w.Ubr)
	}
	c.SetMeta("OS", fmt.Sprintf("%s (kompilacja %s, %s)", name, buildTxt, w.Arch))
	c.SetMeta("OSFamily", w.Family)
	inst, _ := os.Time("InstallDate")
	details := []string{
		"Edycja: " + w.Edition,
		"Data instalacji: " + fmtDt(inst),
		"Katalog systemu: " + os.Str("WindowsDirectory"),
		"Język systemu: " + win.UILanguage(),
		"Strefa czasowa: " + win.TimeZoneDisplayName(),
		"Go: " + strings.TrimPrefix(runtime.Version(), "go"),
	}
	c.Add(F{Title: "System operacyjny", Status: Info, Value: fmt.Sprintf("%s, kompilacja %s, %s", name, buildTxt, w.Arch), Details: strings.Join(details, "\n")})

	if w.Arch == "x86" {
		c.Add(F{Title: "Architektura systemu", Status: Warn, Value: "System 32-bitowy",
			Details: "System 32-bitowy wykorzystuje maksymalnie ok. 4 GB RAM i nie uruchomi nowszych aplikacji 64-bitowych.",
			Advice:  "Jeśli procesor jest 64-bitowy, rozważ instalację systemu 64-bitowego."})
	}

	// --- okres wsparcia (z uwzględnieniem rozszerzonych aktualizacji ESU dla Windows 10 22H2)
	if w.IsServer {
		c.Add(F{Title: "Okres wsparcia systemu", Status: Info, Value: "Wersja serwerowa - sprawdź cykl wsparcia na stronie Microsoft."})
		return
	}
	sup := supportInfo(c)
	if sup == nil {
		maxKnown := int64(0)
		for k := range data().lifecycle.Builds {
			if n, _ := strconv.ParseInt(k, 10, 64); n > maxKnown {
				maxKnown = n
			}
		}
		if maxKnown > 0 && w.Build > maxKnown {
			c.Add(F{Title: "Okres wsparcia systemu", Status: Info, Value: fmt.Sprintf("Kompilacja %d jest nowsza niż baza programu", w.Build),
				Details: "Wersja systemu jest nowsza od danych w bazie wiedzy programu (prawdopodobnie bieżące wydanie lub Insider)."})
		} else {
			c.Add(F{Title: "Okres wsparcia systemu", Status: Info, Value: fmt.Sprintf("Brak danych o kompilacji %d", w.Build)})
		}
		return
	}
	var det []string
	if sup.Note != "" {
		det = append(det, sup.Note)
	}
	if esu := sup.Esu; esu != nil && esu.Applies {
		det = append(det, "", "Rozszerzone aktualizacje zabezpieczeń (ESU):")
		lic := "brak"
		if esu.LicenseYear > 0 {
			lic = fmt.Sprintf("rok %d, ważna do %s", esu.LicenseYear, fmtDate(esu.LicenseUntil))
		}
		det = append(det, " - licencja ESU (firmy/edukacja): "+lic)
		ct := esuConsumerText(esu.ConsumerCode)
		if ct == "" {
			ct = "brak danych w profilu użytkownika"
		}
		det = append(det, " - program konsumencki: "+ct)
		st := "brak poprawek wydanych po zakończeniu wsparcia"
		if esu.UbrAfterEos {
			st = fmt.Sprintf("zawiera poprawki ESU (nowsza niż ostatnia bezpłatna %d.%d)", w.Build, esu.LastFreeUbr)
		}
		det = append(det, fmt.Sprintf(" - kompilacja %d.%d: %s", w.Build, esu.Ubr, st))
	}
	if sup.LastPatch != nil {
		det = append(det, fmt.Sprintf("Ostatnia poprawka systemu: %s - %s", fmtDt(sup.LastPatch.Date), limitText(sup.LastPatch.Title, 100)))
	}
	det = append(det, "Dane o okresach wsparcia: "+knowledgeInfo(time.Now()).Text)
	st := sup.Result
	c.Add(F{Title: "Okres wsparcia systemu", Status: st.Status, Value: st.Value, Details: strings.Join(det, "\n"), Advice: st.Advice})
}

func checkUptime(c *Ctx) {
	os := osObj(c)
	if os == nil {
		return
	}
	boot, ok := os.Time("LastBootUpTime")
	if !ok {
		return
	}
	up := time.Since(boot)
	fast := regInt(`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Power`, "HiberbootEnabled", 0)
	details := "Ostatni start jądra systemu: " + fmtDt(boot)
	if fast == 1 {
		details += "\n" + "Szybkie uruchamianie jest włączone: „Zamknij komputer” nie zeruje tego licznika, liczy się wyłącznie „Uruchom ponownie”."
	}
	days := up.Hours() / 24
	switch {
	case days >= 30:
		c.Add(F{Title: "Czas pracy bez restartu", Status: Warn, Value: fmtSpan(up), Details: details,
			Advice: "Wykonaj pełny restart (opcja „Uruchom ponownie”). Restart stosuje oczekujące aktualizacje, zwalnia pamięć i usuwa część problemów."})
	case days >= 14:
		c.Add(F{Title: "Czas pracy bez restartu", Status: Info, Value: fmtSpan(up), Details: details})
	default:
		c.Add(F{Title: "Czas pracy bez restartu", Status: OK, Value: fmtSpan(up), Details: details})
	}
	if fast == 1 {
		c.Add(F{Title: "Szybkie uruchamianie (Fast Startup)", Status: Info, Value: "Włączone",
			Details: "Przy wyłączeniu komputer zapisuje stan jądra systemu (hibernacja), więc sterowniki nie ładują się od zera.",
			Advice:  "Przy problemach z uśpieniem, sterownikami, siecią lub aktualizacjami wyłącz szybkie uruchamianie (Opcje zasilania > Wybierz działanie przycisków zasilania) i sprawdź, czy problem ustąpi."})
	} else {
		c.Add(F{Title: "Szybkie uruchamianie (Fast Startup)", Status: OK, Value: "Wyłączone"})
	}
}

func checkIdentity(c *Ctx) {
	cs := csObj(c)
	bios := c.cim1("Win32_BIOS", "Manufacturer", "SMBIOSBIOSVersion", "ReleaseDate", "SerialNumber")
	bb := c.cim1("Win32_BaseBoard", "Manufacturer", "Product")
	csp := c.cim1("Win32_ComputerSystemProduct", "Version", "IdentifyingNumber")
	enc := c.cim1("Win32_SystemEnclosure", "ChassisTypes")
	_, biosReg, _ := win.RegValues(`HKLM\HARDWARE\DESCRIPTION\System\BIOS`)

	mfr, model := "", ""
	if cs != nil {
		mfr, model = cleanStr(cs.Str("Manufacturer"), ""), cleanStr(cs.Str("Model"), "")
	}
	family := ""
	if v, ok := biosReg["systemfamily"]; ok {
		family = cleanStr(win.AnyString(v), "")
	}
	if family == "" && csp != nil {
		family = cleanStr(csp.Str("Version"), "")
	}
	chassis := "Nieokreślony"
	if enc != nil && len(enc.Ints("ChassisTypes")) > 0 {
		chassis = chassisName(enc.Ints("ChassisTypes"))
	}
	vm := isVM(c)
	fam := ""
	if family != "" && family != model {
		fam = "(" + family + ")"
	}
	title := joinNonEmpty(" ", mfr, model, fam)
	c.SetMeta("Model", title)
	c.SetMeta("Chassis", chassis)
	lines := []string{"Typ obudowy: " + chassis}
	if cs != nil {
		if cs.Bool("PartOfDomain") {
			lines = append(lines, "Domena: "+cs.Str("Domain"))
		} else {
			lines = append(lines, "Grupa robocza: "+cs.Str("Domain"))
		}
	}
	val := title
	if val == "" {
		val = "Nie rozpoznano modelu"
	}
	c.Add(F{Title: "Komputer", Status: Info, Value: val, Details: strings.Join(lines, "\n")})

	serial := ""
	if bios != nil {
		serial = cleanStr(bios.Str("SerialNumber"), "")
	}
	if serial == "" && csp != nil {
		serial = cleanStr(csp.Str("IdentifyingNumber"), "")
	}
	if serial != "" {
		c.SetMeta("Serial", serial)
		c.Add(F{Title: "Numer seryjny", Status: Info, Value: serial, Private: true})
	} else {
		c.Add(F{Title: "Numer seryjny", Status: Info, Value: "Producent nie wpisał numeru seryjnego do BIOS.", Private: true})
	}
	if vm {
		c.Add(F{Title: "Maszyna wirtualna", Status: Info, Value: "Wykryto środowisko wirtualne",
			Details: "Część testów sprzętowych (SMART, bateria, temperatury) nie ma w maszynie wirtualnej sensu."})
	}
	if bb != nil {
		if t := joinNonEmpty(" ", cleanStr(bb.Str("Manufacturer"), ""), cleanStr(bb.Str("Product"), "")); t != "" {
			c.Add(F{Title: "Płyta główna", Status: Info, Value: t})
			c.SetMeta("Board", t)
		}
	}
	fw := firmwareType(c)
	if bios != nil {
		rel, hasRel := bios.Time("ReleaseDate")
		relTxt := ""
		if hasRel {
			relTxt = "z dnia " + fmtDate(rel)
		}
		biosTxt := joinNonEmpty(" ", bios.Str("Manufacturer"), bios.Str("SMBIOSBIOSVersion"), relTxt)
		c.SetMeta("Bios", biosTxt)
		adv := ""
		if hasRel && time.Since(rel).Hours()/24 > 365*6 && !vm {
			adv = "BIOS jest starszy niż 6 lat. Sprawdź na stronie producenta, czy są poprawki (stabilność, zgodność z Windows 11, luki w zabezpieczeniach). Aktualizuj tylko przy podłączonym zasilaniu i naładowanej baterii."
		}
		c.Add(F{Title: "BIOS / UEFI", Status: Info, Value: biosTxt + "; tryb: " + fw, Advice: adv})
	} else {
		c.Add(F{Title: "Tryb rozruchu", Status: Info, Value: fw})
	}
}

func checkWin11(c *Ctx) {
	w := winInfo(c)
	if w.Build >= 22000 {
		c.Add(F{Title: "Gotowość na Windows 11", Status: OK, Value: "Windows 11 jest już zainstalowany."})
		return
	}
	if w.IsServer {
		return
	}
	cpus := cpuObjs(c)
	cpuName := ""
	var logical int64
	if len(cpus) > 0 {
		cpuName = cpus[0].Str("Name")
		logical = cpus[0].Int("NumberOfLogicalProcessors")
	}
	ci := cpuInfoFor(cpuName)
	cs := csObj(c)

	// RAM: preferuj sumę zainstalowanych kości (TotalPhysicalMemory jest pomniejszone o pamięć zarezerwowaną)
	installed := 0.0
	for _, m := range c.cim("Win32_PhysicalMemory", "Capacity") {
		installed += m.Float("Capacity")
	}
	if installed <= 0 && cs != nil {
		installed = cs.Float("TotalPhysicalMemory")
	}
	ramGb := installed / (1 << 30)

	diskGb := 0.0
	drive := strings.ToUpper(sysDrive())
	if ld := c.Cim(Q{Class: "Win32_LogicalDisk", Props: []string{"DeviceID", "Size"}, Where: "DeviceID='" + drive + "'"}); len(ld) > 0 {
		diskGb = ld[0].Float("Size") / (1 << 30)
		parts := c.Cim(Q{Class: "Win32_LogicalDiskToPartition", WQL: "ASSOCIATORS OF {Win32_LogicalDisk.DeviceID='" + drive + "'} WHERE AssocClass=Win32_LogicalDiskToPartition", Props: []string{"DeviceID"}})
		if len(parts) > 0 {
			drives := c.Cim(Q{Class: "Win32_DiskDriveToDiskPartition", WQL: "ASSOCIATORS OF {Win32_DiskPartition.DeviceID='" + parts[0].Str("DeviceID") + "'} WHERE AssocClass=Win32_DiskDriveToDiskPartition", Props: []string{"Size"}})
			if len(drives) > 0 && drives[0].Float("Size") > 0 {
				diskGb = drives[0].Float("Size") / (1 << 30)
			}
		}
	}
	fw := firmwareType(c)
	sb := secureBoot(c)
	tpm := tpmInfo(c)

	var rows, hard, soft []string
	switch ci.Win11 {
	case Yes:
		rows = append(rows, "✔ Procesor: "+cpuName+" - na liście zgodnych (szacunek)")
	case No:
		rows = append(rows, "✖ Procesor: "+cpuName+" - "+ci.Gen+" nie jest obsługiwany")
		hard = append(hard, "procesor")
	default:
		rows = append(rows, "? Procesor: "+cpuName+" - sprawdź na liście procesorów Microsoft")
	}
	if ci.Note != "" {
		rows = append(rows, "   "+ci.Note)
	}
	if w.Arch != "x64" && w.Arch != "ARM64" {
		soft = append(soft, "64-bitowy system")
		rows = append(rows, "✖ System jest 32-bitowy (wymagany 64-bitowy)")
	}
	if len(cpus) > 0 && logical < 2 {
		hard = append(hard, "liczba rdzeni")
		rows = append(rows, "✖ Wymagane co najmniej 2 rdzenie logiczne")
	}
	if ramGb >= 3.7 {
		rows = append(rows, fmt.Sprintf("✔ RAM: %s GB (min. 4 GB)", fmtNum(ramGb, 1)))
	} else {
		rows = append(rows, fmt.Sprintf("✖ RAM: %s GB (min. 4 GB)", fmtNum(ramGb, 1)))
		soft = append(soft, "RAM")
	}
	if diskGb >= 63 {
		rows = append(rows, fmt.Sprintf("✔ Dysk systemowy: %s GB (min. 64 GB)", fmtNum(diskGb, 0)))
	} else {
		rows = append(rows, fmt.Sprintf("✖ Dysk systemowy: %s GB (min. 64 GB)", fmtNum(diskGb, 0)))
		soft = append(soft, "dysk")
	}
	switch fw {
	case "UEFI":
		rows = append(rows, "✔ Tryb UEFI")
		switch {
		case sb.Known && sb.Enabled:
			rows = append(rows, "✔ Secure Boot włączony")
		case sb.Known:
			rows = append(rows, "✖ Secure Boot jest wyłączony - włącz w BIOS/UEFI (wymaga dysku GPT)")
			soft = append(soft, "Secure Boot")
		default:
			rows = append(rows, "✖ Secure Boot nieobsługiwany przez firmware")
			hard = append(hard, "Secure Boot")
		}
	case "BIOS":
		rows = append(rows, "✖ Tryb Legacy BIOS - wymagany UEFI (konwersja MBR→GPT narzędziem mbr2gpt i przełączenie w BIOS)")
		soft = append(soft, "UEFI")
	default:
		rows = append(rows, "? Nie rozpoznano trybu rozruchu")
	}
	switch {
	case tpm.Present && strings.HasPrefix(tpm.Version, "2"):
		if tpm.Enabled == No {
			rows = append(rows, "✖ TPM 2.0 jest wyłączony - włącz w BIOS (fTPM / PTT)")
			soft = append(soft, "TPM")
		} else {
			rows = append(rows, "✔ TPM 2.0")
		}
	case tpm.Present && strings.HasPrefix(tpm.Version, "1"):
		rows = append(rows, "✖ Wykryto TPM 1.2 (wymagany 2.0)")
		hard = append(hard, "TPM 2.0")
	case tpm.Present:
		rows = append(rows, "? Wykryto TPM o nieustalonej wersji")
	default:
		rows = append(rows, "✖ Nie wykryto TPM - sprawdź w BIOS opcje fTPM / Intel PTT / AMD fTPM")
		soft = append(soft, "TPM")
	}
	rows = append(rows, "(DirectX 12 / WDDM 2.x oraz wyświetlacz HD nie są sprawdzane)")
	details := strings.Join(rows, "\n")
	cpuTxt := ""
	if ci.Gen != "" {
		y := ""
		if ci.Year > 0 {
			y = fmt.Sprintf(", %d", ci.Year)
		}
		cpuTxt = " (" + ci.Gen + y + ")"
	}
	switch {
	case len(hard) > 0:
		c.Add(F{Title: "Gotowość na Windows 11", Status: Warn, Value: "Niezgodny sprzęt: " + strings.Join(hard, ", "), Details: details,
			Advice: "Ten komputer nie spełnia sprzętowych wymagań Windows 11. Przy zakończonym wsparciu Windows 10 rozważ wymianę komputera lub program ESU; instalacja z pominięciem wymagań nie jest wspierana przez Microsoft."})
	case len(soft) > 0:
		c.Add(F{Title: "Gotowość na Windows 11", Status: Warn, Value: "Do poprawy przed aktualizacją: " + strings.Join(soft, ", "), Details: details,
			Advice: "Sprzęt rokuje dobrze, ale wymaga zmian w ustawieniach BIOS lub drobnej modernizacji (RAM/dysk). Po usunięciu blokad można zaktualizować do Windows 11."})
	case ci.Win11 == Unknown:
		c.Add(F{Title: "Gotowość na Windows 11", Status: Info, Value: "Prawdopodobnie zgodny - zweryfikuj procesor", Details: details,
			Advice: "Uruchom aplikację „Kontrola kondycji komputera” (PC Health Check) lub porównaj model procesora z listą Microsoft."})
	default:
		c.Add(F{Title: "Gotowość na Windows 11", Status: OK, Value: "Spełnia wymagania (szacunek)" + cpuTxt, Details: details,
			Advice: "Komputer można zaktualizować do Windows 11 przez Windows Update (przed aktualizacją wykonaj kopię danych)."})
	}
}

func checkRestore(c *Ctx) {
	pts := c.Cim(Q{NS: `root\default`, Class: "SystemRestore", Props: []string{"SequenceNumber", "CreationTime", "Description"}})
	if len(pts) == 0 {
		c.Add(F{Title: "Punkty przywracania systemu", Status: Info, Value: "Brak punktów przywracania (ochrona systemu może być wyłączona)",
			Advice: "Przed większymi zmianami (sterowniki, naprawa systemu) utwórz punkt przywracania lub obraz systemu."})
		return
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].Int("SequenceNumber") < pts[j].Int("SequenceNumber") })
	last := pts[len(pts)-1]
	var when time.Time
	switch v := last.Raw("CreationTime").(type) {
	case time.Time:
		when = v
	case string:
		when, _ = win.ParseCIMDateTime(v)
	}
	c.Add(F{Title: "Punkty przywracania systemu", Status: OK,
		Value: fmt.Sprintf("%s; ostatni: %s (%s)", fmtCount(len(pts), "punkt", "punkty", "punktów"), fmtDt(when), last.Str("Description"))})
}

// ---------------------------------------------------------------- pomocnicze

// regInt odpowiada Get-RegValue z liczbową wartością domyślną.
func regInt(path, name string, def int64) int64 {
	v, ok := win.RegValue(path, name)
	if !ok {
		return def
	}
	return win.AnyInt(v)
}

// regStr odpowiada Get-RegValue z tekstową wartością domyślną.
func regStr(path, name, def string) string {
	v, ok := win.RegValue(path, name)
	if !ok {
		return def
	}
	return win.AnyString(v)
}

func sysDrive() string {
	if d := strings.TrimSpace(getenv("SystemDrive")); d != "" {
		return d
	}
	return "C:"
}
