package diag

import (
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Sprzęt (checks\20-hardware.ps1)

var hardwareChecks = []*Check{
	{ID: "hw.cpu", Category: "hardware", Title: "Procesor", Level: "Quick", Run: checkCPU},
	{ID: "hw.ram", Category: "hardware", Title: "Pamięć RAM", Level: "Quick", Run: checkRAM},
	{ID: "hw.gpu", Category: "hardware", Title: "Karta graficzna", Level: "Quick", Run: checkGPU},
	{ID: "hw.monitors", Category: "hardware", Title: "Wyświetlacze", Level: "Quick", Run: checkMonitors},
	{ID: "hw.thermal", Category: "hardware", Title: "Temperatury (czujniki ACPI)", Level: "Quick", NeedsAdmin: true, Run: checkThermal},
	{ID: "hw.printers", Category: "hardware", Title: "Drukarki", Level: "Quick", Run: checkPrinters},
}

var spacesRx = regexp.MustCompile(`\s+`)

func checkCPU(c *Ctx) {
	cpus := cpuObjs(c)
	if len(cpus) == 0 {
		c.Add(F{Title: "Procesor", Status: Err, Value: "Brak danych o procesorze (WMI)."})
		return
	}
	p := cpus[0]
	name := strings.TrimSpace(spacesRx.ReplaceAllString(p.Str("Name"), " "))
	var cores, threads int64
	for _, x := range cpus {
		cores += x.Int("NumberOfCores")
		threads += x.Int("NumberOfLogicalProcessors")
	}
	ghz := 0.0
	if p.Int("MaxClockSpeed") != 0 {
		ghz = psRoundN(p.Float("MaxClockSpeed")/1000, 2)
	}
	ci := cpuInfoFor(name)
	cs := csObj(c)
	c.SetMeta("Cpu", fmt.Sprintf("%s (%d rdzeni / %d wątków)", name, cores, threads))
	d := []string{
		"Producent: " + p.Str("Manufacturer"),
		fmt.Sprintf("Rdzenie / wątki: %d / %d", cores, threads),
		"Taktowanie maksymalne (znamionowe): " + fmtNum(ghz, 2) + " GHz",
		"Gniazdo: " + p.Str("SocketDesignation"),
		"Pamięć podręczna L2: " + fmtNum(p.Float("L2CacheSize")/1024, 1) + " MB, L3: " + fmtNum(p.Float("L3CacheSize")/1024, 1) + " MB",
	}
	if len(cpus) > 1 {
		d = append(d, fmt.Sprintf("Liczba procesorów fizycznych: %d", len(cpus)))
	}
	if ci.Gen != "" {
		y := ""
		if ci.Year > 0 {
			y = fmt.Sprintf(", rok premiery ok. %d", ci.Year)
		}
		d = append(d, "Rozpoznana generacja: "+ci.Gen+y)
	}
	c.Add(F{Title: "Procesor", Status: Info, Value: fmt.Sprintf("%s; %d rdzeni / %d wątków", name, cores, threads), Details: strings.Join(d, "\n")})

	if ci.Year > 0 && time.Now().Year()-ci.Year >= 9 && !isVM(c) {
		c.Add(F{Title: "Wiek procesora", Status: Info, Value: fmt.Sprintf("Procesor z ok. %d r. (%d lat)", ci.Year, time.Now().Year()-ci.Year),
			Details: "Starsza platforma: ograniczona wydajność, brak zgodności z Windows 11 (przy generacjach sprzed 8. Intel / sprzed Ryzen 2000).",
			Advice:  "Największy zysk w takim komputerze daje dysk SSD i minimum 8 GB RAM; przy ocenie opłacalności napraw weź pod uwagę wiek platformy."})
	}
	hv := cs != nil && cs.Bool("HypervisorPresent")
	switch {
	case hv:
		c.Add(F{Title: "Wirtualizacja sprzętowa", Status: OK, Value: "System korzysta z wirtualizacji (Hyper-V / zabezpieczenia oparte na wirtualizacji)"})
	case p.Has("VirtualizationFirmwareEnabled") && !p.Bool("VirtualizationFirmwareEnabled") && !isVM(c):
		c.Add(F{Title: "Wirtualizacja sprzętowa", Status: Info, Value: "Wyłączona w BIOS/UEFI (Intel VT-x / AMD-V)",
			Advice: "Włącz w BIOS, jeśli użytkownik potrzebuje WSL2, Hyper-V, Windows Sandbox lub emulatorów Androida."})
	case p.Has("VirtualizationFirmwareEnabled") && p.Bool("VirtualizationFirmwareEnabled"):
		c.Add(F{Title: "Wirtualizacja sprzętowa", Status: OK, Value: "Włączona w BIOS/UEFI"})
	}
}

var (
	memTypes     = map[int64]string{20: "DDR", 21: "DDR2", 22: "DDR2 FB-DIMM", 24: "DDR3", 26: "DDR4", 27: "LPDDR", 28: "LPDDR2", 29: "LPDDR3", 30: "LPDDR4", 34: "DDR5", 35: "LPDDR5"}
	memForms     = map[int64]string{8: "DIMM", 12: "SO-DIMM", 9: "RIMM", 13: "SRIMM", 11: "SIMM"}
	solderedRx   = rx(`Onboard|Memory Down|Soldered|OnBoard`)
	memFormSufRx = rx(`\s?(DIMM|SO-DIMM|lutowana)$`)
	dimmRx       = rx(`(^| )DIMM$`)
	soDimmRx     = rx(`SO-DIMM`)
)

func checkRAM(c *Ctx) {
	mods := c.cim("Win32_PhysicalMemory")
	arr := c.cim1("Win32_PhysicalMemoryArray")
	cs := csObj(c)
	w := winInfo(c)

	total := 0.0
	var rows []Row
	var caps, speeds []float64
	var types []string
	for _, m := range mods {
		capb := m.Float("Capacity")
		total += capb
		tn := m.Int("SMBIOSMemoryType")
		if tn <= 1 && m.Int("MemoryType") != 0 {
			tn = m.Int("MemoryType")
		}
		t := memTypes[tn]
		ff := memForms[m.Int("FormFactor")]
		if solderedRx.MatchString(m.Str("DeviceLocator")) {
			ff = "lutowana"
		}
		rated := m.Int("Speed")
		conf := m.Int("ConfiguredClockSpeed")
		caps = append(caps, capb)
		if conf != 0 {
			speeds = append(speeds, float64(conf))
		} else {
			speeds = append(speeds, float64(rated))
		}
		var confVal any = "-"
		if conf != 0 {
			confVal = conf
		}
		typ := joinNonEmpty(" ", t, ff)
		types = append(types, typ)
		rows = append(rows, row("Slot", joinNonEmpty(" / ", m.Str("DeviceLocator"), m.Str("BankLabel")), "Pojemność", fmtSize(capb, 0), "Typ", typ,
			"MHz (znam.)", rated, "MHz (ustaw.)", confVal, "Producent", cleanStr(m.Str("Manufacturer"), "-"), "Numer części", cleanStr(strings.TrimSpace(m.Str("PartNumber")), "-")))
	}
	if len(mods) == 0 && cs != nil {
		total = cs.Float("TotalPhysicalMemory")
	}
	totalGb := total / (1 << 30)
	slots := int64(0)
	if arr != nil {
		slots = arr.Int("MemoryDevices")
	}
	maxGb := 0.0
	if arr != nil {
		if arr.Int("MaxCapacityEx") != 0 {
			maxGb = arr.Float("MaxCapacityEx") / (1 << 20)
		} else if arr.Int("MaxCapacity") != 0 {
			maxGb = arr.Float("MaxCapacity") / (1 << 20)
		}
	}
	typeTxt := ""
	if len(types) > 0 {
		typeTxt = memFormSufRx.ReplaceAllString(types[0], "")
	}
	speedTxt := ""
	if len(speeds) > 0 && speeds[0] != 0 {
		speedTxt = fmt.Sprintf(" %d MHz", int64(speeds[0]))
	}
	typeSeg := ""
	if typeTxt != "" {
		typeSeg = " " + typeTxt
	}
	summary := fmtNum(totalGb, 1) + " GB"
	if len(mods) > 0 {
		summary = fmt.Sprintf("%s GB (%d × %s%s%s)", fmtNum(totalGb, 0), len(mods), fmtSize(caps[0], 0), typeSeg, speedTxt)
	}
	c.SetMeta("Ram", summary)

	slotTxt := ""
	if slots != 0 {
		slotTxt = fmt.Sprintf(" z %d gniazd", slots)
	}
	d := []string{fmt.Sprintf("Zainstalowane moduły: %d%s", len(mods), slotTxt)}
	if maxGb > 0 {
		d = append(d, "Maksymalna pojemność wg płyty: "+fmtNum(maxGb, 0)+" GB")
	}
	if cs != nil {
		d = append(d, "Dostępne dla systemu: "+fmtSize(cs.Float("TotalPhysicalMemory"), 1))
	}
	switch {
	case totalGb < 3.7:
		c.Add(F{Title: "Pamięć RAM", Status: Warn, Value: summary, Details: strings.Join(d, "\n"),
			Advice: "Bardzo mało pamięci RAM. Dołożenie modułu (docelowo min. 8 GB) znacząco poprawi komfort pracy."})
	case totalGb < 7.5 && w.Build >= 10240:
		c.Add(F{Title: "Pamięć RAM", Status: Warn, Value: summary, Details: strings.Join(d, "\n"),
			Advice: "Dla Windows 10/11 to niewiele - przy przeglądarce z wieloma kartami komputer będzie używał wolnego pliku wymiany. Rozważ dołożenie RAM do 8-16 GB."})
	default:
		c.Add(F{Title: "Pamięć RAM", Status: OK, Value: summary, Details: strings.Join(d, "\n")})
	}
	c.AddTable(T{Name: "Moduły pamięci RAM", Rows: rows})

	removable := 0
	for _, m := range mods {
		if !solderedRx.MatchString(m.Str("DeviceLocator")) {
			removable++
		}
	}
	if len(mods) == 1 && slots >= 2 && removable >= 1 && totalGb >= 3.7 {
		c.Add(F{Title: "Tryb pracy pamięci", Status: Info, Value: "Jeden moduł - tryb jednokanałowy",
			Details: fmt.Sprintf("Komputer ma %d gniazda pamięci, a zajęte jest tylko jedno.", slots),
			Advice:  "Drugi moduł o takiej samej pojemności i parametrach włączy tryb dwukanałowy - widocznie szybszą grafikę zintegrowaną i ogólną wydajność."})
	}
	uniq := func(v []float64, skipZero bool) int {
		seen := map[float64]bool{}
		for _, x := range v {
			if skipZero && x == 0 {
				continue
			}
			seen[x] = true
		}
		return len(seen)
	}
	if len(mods) > 1 && (uniq(caps, false) > 1 || uniq(speeds, true) > 1) {
		c.Add(F{Title: "Zgodność modułów pamięci", Status: Info, Value: "Moduły różnią się pojemnością lub szybkością",
			Details: "Pamięć pracuje z parametrami najwolniejszego modułu, a tryb dwukanałowy może obejmować tylko część pojemności.",
			Advice:  "Jeśli to możliwe, stosuj identyczne moduły (ten sam producent, pojemność i taktowanie)."})
	}
	desktopDimm := 0
	for _, t := range types {
		if dimmRx.MatchString(t) && !soDimmRx.MatchString(t) {
			desktopDimm++
		}
	}
	for _, m := range mods {
		rated, conf := m.Int("Speed"), m.Int("ConfiguredClockSpeed")
		if desktopDimm > 0 && conf > 0 && rated > 0 && float64(conf) < float64(rated)*0.8 {
			c.Add(F{Title: "Profil XMP / EXPO", Status: Info, Value: fmt.Sprintf("Pamięć pracuje %d MHz zamiast znamionowych %d MHz", conf, rated),
				Advice: "Włącz profil XMP (Intel) lub EXPO/DOCP (AMD) w BIOS/UEFI, aby pamięć osiągnęła deklarowaną szybkość."})
			break
		}
	}
	if w.Arch == "x86" && totalGb > 4.2 {
		c.Add(F{Title: "RAM a architektura systemu", Status: Warn, Value: fmt.Sprintf("Zainstalowano %s GB, a system jest 32-bitowy", fmtNum(totalGb, 0)),
			Advice: "System 32-bitowy wykorzysta najwyżej ok. 3-4 GB. Zainstaluj wersję 64-bitową, aby wykorzystać całą pamięć."})
	}
}

var fourDigitsRx = regexp.MustCompile(`^\d{4}$`)

// gpuVram odpowiada Get-SdGpuVram: prawdziwy rozmiar VRAM z rejestru (AdapterRAM jest ucinane do 4 GB).
func gpuVram(name string) float64 {
	base := `HKLM\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	for _, k := range win.RegSubKeys(base) {
		if !fourDigitsRx.MatchString(k) {
			continue
		}
		_, p, ok := win.RegValues(base + `\` + k)
		if !ok || !eqi(win.AnyString(p["driverdesc"]), name) {
			continue
		}
		if q := p["hardwareinformation.qwmemorysize"]; q != nil && win.AnyInt(q) != 0 {
			return float64(uint64(win.AnyInt(q)))
		}
		if m := p["hardwareinformation.memorysize"]; m != nil {
			if b, ok := m.([]byte); ok && len(b) >= 4 {
				if v := binary.LittleEndian.Uint32(b); v != 0 {
					return float64(v)
				}
				continue
			}
			if v := win.AnyInt(m); v != 0 {
				return float64(uint32(v))
			}
		}
	}
	return 0
}

var (
	basicGpuRx   = rx(`Basic Display|Basic Render|Standardowa karta graficzna|Standard VGA|Microsoft Basic`)
	virtualGpuRx = rx(`Basic|Remote|Mirror|Virtual`)
)

func checkGPU(c *Ctx) {
	gpus := c.cim("Win32_VideoController", "Name", "AdapterRAM", "CurrentHorizontalResolution", "CurrentVerticalResolution", "CurrentRefreshRate", "DriverDate", "DriverVersion", "PNPDeviceID", "ConfigManagerErrorCode")
	if len(gpus) == 0 {
		c.Add(F{Title: "Karta graficzna", Status: Warn, Value: "Nie wykryto żadnej karty graficznej."})
		return
	}
	var rows []Row
	var names []string
	real := 0
	for _, g := range gpus {
		name := g.Str("Name")
		vram := gpuVram(name)
		if vram <= 0 && g.Int("AdapterRAM") != 0 {
			vram = g.Float("AdapterRAM")
		}
		res := ""
		if g.Int("CurrentHorizontalResolution") != 0 {
			res = fmt.Sprintf("%d×%d @ %d Hz", g.Int("CurrentHorizontalResolution"), g.Int("CurrentVerticalResolution"), g.Int("CurrentRefreshRate"))
		}
		dd, hasDate := g.Time("DriverDate")
		age := math.NaN()
		if hasDate {
			age = time.Since(dd).Hours() / 24 / 365.25
		}
		basic := basicGpuRx.MatchString(name)
		names = append(names, name)
		if !virtualGpuRx.MatchString(name) {
			real++
		}
		vramTxt := "-"
		if vram > 0 {
			vramTxt = fmtSize(vram, 0)
		}
		rows = append(rows, row("Karta", name, "VRAM", vramTxt, "Wersja sterownika", g.Str("DriverVersion"), "Data sterownika", fmtDate(dd), "Rozdzielczość", res))
		d := []string{"Wersja sterownika: " + g.Str("DriverVersion") + " z dnia " + fmtDate(dd)}
		if vram > 0 {
			d = append(d, "Pamięć wideo: "+fmtSize(vram, 0))
		}
		if res != "" {
			d = append(d, "Aktualny tryb: "+res)
		}
		d = append(d, "Identyfikator: "+g.Str("PNPDeviceID"))
		vramVal := "brak danych o VRAM"
		if vram > 0 {
			vramVal = fmtSize(vram, 0)
		}
		code := g.Int("ConfigManagerErrorCode")
		switch {
		case basic:
			c.Add(F{Title: "Karta graficzna: " + name, Status: Warn, Value: "Działa na sterowniku podstawowym Microsoft", Details: strings.Join(d, "\n"),
				Advice: "Brak właściwego sterownika grafiki: obniżona wydajność, brak akceleracji i skalowania. Zainstaluj sterownik producenta komputera lub układu (Intel/NVIDIA/AMD)."})
		case code != 0:
			c.Add(F{Title: "Karta graficzna: " + name, Status: Crit, Value: fmt.Sprintf("Błąd urządzenia (kod %d)", code),
				Details: strings.Join(d, "\n") + "\n" + pnpErrorText(int(code)),
				Advice:  "Zainstaluj ponownie sterownik grafiki (najlepiej czysta instalacja z DDU); sprawdź, czy karta jest poprawnie osadzona i zasilona."})
		case !math.IsNaN(age) && age > 3:
			c.Add(F{Title: "Karta graficzna: " + name, Status: Info, Value: fmt.Sprintf("%s; sterownik sprzed %s lat", vramVal, fmtNum(age, 0)), Details: strings.Join(d, "\n"),
				Advice: "Sterownik grafiki jest stary - sprawdź, czy producent udostępnia nowszy (poprawki stabilności i wydajności)."})
		default:
			c.Add(F{Title: "Karta graficzna: " + name, Status: OK, Value: vramVal, Details: strings.Join(d, "\n")})
		}
	}
	c.SetMeta("Gpu", strings.Join(names, " + "))
	c.AddTable(T{Name: "Karty graficzne", Rows: rows})
	if real >= 2 {
		c.Add(F{Title: "Układy graficzne", Status: Info, Value: "Zintegrowana i dedykowana grafika (hybrydowa)",
			Advice: "W razie problemów z wydajnością sprawdź w Ustawieniach grafiki, który układ obsługuje daną aplikację."})
	}
}

var monitorVendors = map[string]string{
	"AUO": "AU Optronics", "BOE": "BOE", "LGD": "LG Display", "SDC": "Samsung Display", "CMN": "Chimei Innolux", "IVO": "InfoVision", "SHP": "Sharp",
	"LEN": "Lenovo", "DEL": "Dell", "SAM": "Samsung", "GSM": "LG Electronics", "ACI": "ASUS", "AUS": "ASUS", "AOC": "AOC", "PHL": "Philips",
	"HWP": "HP", "BNQ": "BenQ", "ACR": "Acer", "VSC": "ViewSonic", "MSI": "MSI", "GBT": "Gigabyte", "NEC": "NEC", "HKC": "HKC", "CSO": "CSOT", "TMX": "Tianma",
}

func edidText(v []int64) string {
	var sb strings.Builder
	for _, x := range v {
		if x != 0 {
			sb.WriteRune(rune(x))
		}
	}
	return strings.TrimSpace(sb.String())
}

func lookupFold(m map[string]string, k string) (string, bool) {
	for mk, v := range m {
		if strings.EqualFold(mk, k) {
			return v, true
		}
	}
	return "", false
}

func checkMonitors(c *Ctx) {
	ids := c.Cim(Q{NS: `root\wmi`, Class: "WmiMonitorID", Props: []string{"InstanceName", "ManufacturerName", "UserFriendlyName", "ProductCodeID", "YearOfManufacture"}})
	if len(ids) == 0 {
		return
	}
	params := c.Cim(Q{NS: `root\wmi`, Class: "WmiMonitorBasicDisplayParams", Props: []string{"InstanceName", "MaxHorizontalImageSize", "MaxVerticalImageSize"}})
	type mon struct {
		prod, model, code, diag string
		year                    int64
	}
	var list []mon
	for _, m := range ids {
		mf := edidText(m.Ints("ManufacturerName"))
		nm := edidText(m.Ints("UserFriendlyName"))
		pc := edidText(m.Ints("ProductCodeID"))
		diag := "-"
		for _, p := range params {
			if p.Str("InstanceName") == m.Str("InstanceName") {
				h, v := p.Float("MaxHorizontalImageSize"), p.Float("MaxVerticalImageSize")
				if h != 0 && v != 0 {
					diag = fmtNum(math.Sqrt(h*h+v*v)/2.54, 1) + `"`
				}
				break
			}
		}
		prod := mf
		if v, ok := lookupFold(monitorVendors, mf); ok {
			prod = v + " (" + mf + ")"
		}
		if nm == "" {
			nm = "(wbudowany panel)"
		}
		list = append(list, mon{prod, nm, pc, diag, m.Int("YearOfManufacture")})
	}
	// ten sam panel bywa raportowany raz na każdą kartę graficzną (Sort-Object ... -Unique)
	psSort(list, func(a, b mon) int {
		if r := cmpText(a.prod, b.prod); r != 0 {
			return r
		}
		if r := cmpText(a.model, b.model); r != 0 {
			return r
		}
		if r := cmpText(a.code, b.code); r != 0 {
			return r
		}
		return int(a.year - b.year)
	})
	var rows []Row
	var prev *mon
	for i := range list {
		m := list[i]
		if prev != nil && eqi(prev.prod, m.prod) && eqi(prev.model, m.model) && eqi(prev.code, m.code) && prev.year == m.year {
			continue
		}
		prev = &list[i]
		rows = append(rows, row("Producent", m.prod, "Model", m.model, "Kod panelu", m.code, "Rok", m.year, "Przekątna", m.diag))
	}
	first := rows[0]
	c.Add(F{Title: "Wyświetlacze", Status: Info, Value: fmt.Sprintf("%d szt.; %v %v %v", len(rows), first.Get("Producent"), first.Get("Kod panelu"), first.Get("Przekątna"))})
	c.AddTable(T{Name: "Wyświetlacze", Rows: rows})
}

func checkThermal(c *Ctx) {
	zones := c.Cim(Q{NS: `root\wmi`, Class: "MSAcpi_ThermalZoneTemperature", Props: []string{"InstanceName", "CurrentTemperature", "CriticalTripPoint"}})
	if len(zones) == 0 {
		c.Add(F{Title: "Temperatury (ACPI)", Status: Info, Value: "Brak czujnika dostępnego przez WMI",
			Details: "Wiele płyt nie udostępnia temperatur przez ACPI. Rzeczywiste temperatury CPU/GPU zmierzysz w HWiNFO lub HWMonitor."})
		return
	}
	maxT := -1000.0
	var d []string
	for _, z := range zones {
		t := z.Float("CurrentTemperature")/10 - 273.15
		critTxt := ""
		if z.Int("CriticalTripPoint") != 0 {
			critTxt = " (punkt krytyczny " + fmtNum(z.Float("CriticalTripPoint")/10-273.15, 0) + " °C)"
		}
		if t > maxT {
			maxT = t
		}
		d = append(d, fmt.Sprintf("%s: %s °C%s", z.Str("InstanceName"), fmtNum(t, 1), critTxt))
	}
	d = append(d, "Uwaga: to temperatura strefy ACPI, a nie rdzenia procesora.")
	val := fmtNum(maxT, 1) + " °C"
	switch {
	case maxT >= 90:
		c.Add(F{Title: "Temperatury (ACPI)", Status: Crit, Value: val, Details: strings.Join(d, "\n"),
			Advice: "Bardzo wysoka temperatura. Oczyść układ chłodzenia, wymień pastę termoprzewodzącą, sprawdź wentylatory."})
	case maxT >= 80:
		c.Add(F{Title: "Temperatury (ACPI)", Status: Warn, Value: val, Details: strings.Join(d, "\n"),
			Advice: "Wysoka temperatura już w spoczynku/lekkim obciążeniu. Sprawdź drożność wentylacji i wentylatory."})
	default:
		c.Add(F{Title: "Temperatury (ACPI)", Status: OK, Value: val, Details: strings.Join(d, "\n")})
	}
}

var virtualPrinterRx = rx(`OneNote|Microsoft Print to PDF|Microsoft XPS|Fax|Send To|Zapisz jako PDF|Adobe PDF`)

func checkPrinters(c *Ctx) {
	pr := c.cim("Win32_Printer", "Name", "Default", "PortName", "DriverName", "WorkOffline")
	if len(pr) == 0 {
		return
	}
	jobs := c.cim("Win32_PrintJob", "Name")
	var rows []Row
	var offline, stuck []string
	for _, p := range pr {
		name := p.Str("Name")
		if virtualPrinterRx.MatchString(name) {
			continue
		}
		n := 0
		for _, j := range jobs {
			if strings.HasPrefix(strings.ToLower(j.Str("Name")), strings.ToLower(name)+",") {
				n++
			}
		}
		def, off := "", ""
		if p.Bool("Default") {
			def = "tak"
		}
		if p.Bool("WorkOffline") {
			off = "TAK"
			offline = append(offline, name)
		}
		if n > 0 {
			stuck = append(stuck, name)
		}
		rows = append(rows, row("Nazwa", name, "Domyślna", def, "Port", p.Str("PortName"), "Sterownik", p.Str("DriverName"), "Offline", off, "Zadania w kolejce", n))
	}
	if len(rows) == 0 {
		return
	}
	var d []string
	if st, ok := win.ServiceState("Spooler"); ok {
		d = append(d, "Usługa Bufor wydruku: "+st)
	}
	cnt := fmtCount(len(rows), "drukarka", "drukarki", "drukarek")
	switch {
	case len(stuck) > 0:
		c.Add(F{Title: "Drukarki", Status: Warn, Value: fmt.Sprintf("%s; zadania w kolejce: %s", cnt, strings.Join(stuck, ", ")), Details: strings.Join(d, "\n"),
			Advice: `Zablokowane zadania wydruku: zatrzymaj usługę Bufor wydruku, wyczyść C:\Windows\System32\spool\PRINTERS i uruchom usługę ponownie.`})
	case len(offline) > 0:
		c.Add(F{Title: "Drukarki", Status: Info, Value: fmt.Sprintf("%s; offline: %s", cnt, strings.Join(offline, ", ")), Details: strings.Join(d, "\n"),
			Advice: "Drukarka jest w trybie offline - sprawdź zasilanie, kabel/sieć i opcję „Użyj drukarki w trybie offline”."})
	default:
		c.Add(F{Title: "Drukarki", Status: OK, Value: fmt.Sprintf("Zainstalowane: %d", len(rows)), Details: strings.Join(d, "\n")})
	}
	c.AddTable(T{Name: "Drukarki", Rows: rows})
}
