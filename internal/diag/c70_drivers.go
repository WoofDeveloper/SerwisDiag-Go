package diag

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Sterowniki i urządzenia (checks\70-drivers.ps1, lib\Drivers.ps1)

var driverChecks = []*Check{
	{ID: "dr.problem", Category: "drivers", Title: "Urządzenia z błędami i brakujące sterowniki", Level: "Quick", Run: checkProblemDevices},
	{ID: "dr.wu", Category: "drivers", Title: "Nowsze sterowniki w Windows Update", Level: "Quick", NeedsInternet: true, Run: checkWuDrivers},
	{ID: "dr.signed", Category: "drivers", Title: "Sterowniki kluczowych urządzeń (wersje, daty, podpisy)", Level: "Quick", Run: checkSignedDrivers},
	{ID: "dr.devices", Category: "drivers", Title: "Lista urządzeń (Menedżer urządzeń)", Level: "Full", Run: checkDeviceList},
}

func checkProblemDevices(c *Ctx) {
	pnp := pnpObjs(c)
	if len(pnp) == 0 {
		c.Add(F{Title: "Urządzenia z błędami", Status: Err, Value: "Nie udało się odczytać listy urządzeń (WMI)."})
		return
	}
	var disabled, problem []win.WmiObject
	for _, d := range pnp {
		code := d.Int("ConfigManagerErrorCode")
		switch {
		case code == 0:
		case code == 22:
			disabled = append(disabled, d)
		case code != 45 && code != 47:
			problem = append(problem, d)
		}
	}
	if len(problem) == 0 {
		c.Add(F{Title: "Urządzenia z błędami", Status: OK, Value: fmt.Sprintf("Wszystkie wykryte urządzenia działają poprawnie (sprawdzono: %d)", len(pnp))})
	} else {
		var rows, missing []Row
		var lines []string
		worstSt, anyMissing := Warn, false
		for _, d := range problem {
			code := int(d.Int("ConfigManagerErrorCode"))
			hwid := ""
			if ids := d.Strs("HardwareID"); len(ids) > 0 {
				hwid = ids[0]
			} else {
				hwid = d.Str("PNPDeviceID")
			}
			vendor := vendorName(hwid)
			name := d.Str("Name")
			if name == "" {
				name = "Nieznane urządzenie"
			}
			text := pnpErrorText(code)
			cls := d.Str("PNPClass")
			if cls == "" {
				cls = "-"
			}
			rows = append(rows, row("Urządzenie", name, "Klasa", cls, "Kod", int64(code), "Opis", text, "Producent", vendor, "Identyfikator sprzętu", hwid))
			vt := ""
			if vendor != "" {
				vt = " (producent: " + vendor + ")"
			}
			lines = append(lines, fmt.Sprintf("%s [%s] - kod %d: %s%s", name, cls, code, text, vt))
			if code == 28 {
				anyMissing = true
				missing = append(missing, row("Urządzenie", name, "Klasa", cls, "Producent", vendor, "Identyfikator sprzętu", hwid, "Katalog Microsoft", catalogURL(hwid)))
			}
			if containsi([]string{"Display", "Net", "HDC", "SCSIAdapter", "DiskDrive"}, cls) {
				worstSt = Crit
			}
		}
		c.AddTable(T{Name: "Urządzenia z błędami (Menedżer urządzeń)", Rows: rows})
		if len(missing) > 0 {
			c.AddTable(T{Name: "Brakujące sterowniki - identyfikatory sprzętu", Rows: missing,
				Note: "Link „Katalog Microsoft” wyszukuje sterownik po identyfikatorze sprzętu (VEN_xxxx&DEV_xxxx) w Katalogu Microsoft Update."})
			vs := computerSupportInfo(c)
			copyTxt := ""
			if vs.CopyText != "" {
				copyTxt = " - wpisz tam " + vs.CopyLabel + " komputera"
			}
			lines = append(lines, "", fmt.Sprintf("Strona ze sterownikami producenta (%s): %s%s", vs.Vendor, vs.URL, copyTxt))
		}
		adv := "Zainstaluj brakujące sterowniki: Narzędzia serwisowe > „Sterowniki z Windows Update”, a gdy ich tam nie ma - „Brakujące sterowniki (Katalog Microsoft)” lub „Strona sterowników producenta”."
		if !anyMissing {
			adv = "Zaktualizuj lub zainstaluj ponownie sterownik urządzenia (Menedżer urządzeń); sprawdź, czy urządzenie jest sprawne i poprawnie podłączone."
		}
		c.Add(F{Title: "Urządzenia z błędami", Status: worstSt, Value: fmtCount(len(problem), "urządzenie z błędem", "urządzenia z błędami", "urządzeń z błędami"),
			Details: strings.Join(lines, "\n"), Advice: adv})
	}
	if len(disabled) > 0 {
		var n []string
		for i, d := range disabled {
			if i < 6 {
				n = append(n, d.Str("Name"))
			}
		}
		c.Add(F{Title: "Urządzenia wyłączone", Status: Info, Value: strings.Join(n, "; "), Details: "Urządzenia wyłączone ręcznie w Menedżerze urządzeń (kod 22)."})
	}
}

// WuDriver - sterownik dostępny w Windows Update.
type WuDriver struct {
	UpdateID, Title, Class, Manufacturer, Model, Provider, HardwareID string
	Date                                                              time.Time
	SizeBytes                                                         float64
	Optional                                                          bool
	DeviceProblem                                                     int64
}

type wuDriverSearch struct {
	Items    []WuDriver
	Error    string
	TimedOut bool
}

// findWuDrivers odpowiada Find-SdWuDrivers: wyszukiwanie z limitem czasu - zawieszona usługa Windows Update
// nie blokuje diagnostyki (w PowerShellu osobny proces, tu osobny wątek).
func findWuDrivers(timeout time.Duration) wuDriverSearch {
	done := make(chan wuDriverSearch, 1)
	go win.WithCOM(func() { done <- searchWuDrivers("IsInstalled=0 and Type='Driver' and IsHidden=0") })
	select {
	case r := <-done:
		return r
	case <-time.After(timeout):
		return wuDriverSearch{TimedOut: true}
	}
}

func searchWuDrivers(criteria string) wuDriverSearch {
	var r wuDriverSearch
	s, err := win.CreateObject("Microsoft.Update.Session")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer s.Release()
	_ = s.Put("ClientApplicationID", "SerwisDiag")
	srch, err := s.Obj("CreateUpdateSearcher")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer srch.Release()
	res, err := srch.Obj("Search", criteria)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer res.Release()
	ups, err := res.Obj("Updates")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer ups.Release()
	r.Items = wuDriverItems(ups)
	return r
}

// wuDriverItems: sterowniki z kolekcji aktualizacji Windows Update.
func wuDriverItems(ups *win.Dispatch) []WuDriver {
	var items []WuDriver
	_ = ups.Items(func(u *win.Dispatch) {
		d := WuDriver{Title: u.Str("Title"), Class: u.Str("DriverClass"), Manufacturer: u.Str("DriverManufacturer"), Model: u.Str("DriverModel"),
			Provider: u.Str("DriverProvider"), HardwareID: u.Str("DriverHardwareID"), SizeBytes: u.Float("MaxDownloadSize"),
			Optional: u.Bool("BrowseOnly"), DeviceProblem: u.Int("DeviceProblemNumber")}
		if t, ok := u.Value("DriverVerDate").(time.Time); ok {
			d.Date = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
		}
		if id, err := u.Obj("Identity"); err == nil {
			d.UpdateID = id.Str("UpdateID")
			id.Release()
		}
		if d.UpdateID != "" {
			items = append(items, d)
		}
	})
	return items
}

// driverUpdateStatus odpowiada Get-SdDriverUpdateStatus.
func driverUpdateStatus(items []WuDriver) Status {
	if len(items) == 0 {
		return OK
	}
	for _, i := range items {
		if i.DeviceProblem != 0 {
			return Warn
		}
	}
	return Info
}

// SortDrivers: kolejność z ConvertTo-SdDriverRows i okna sterowników - najpierw naprawiające urządzenia z błędem,
// potem według klasy i nazwy.
func SortDrivers(items []WuDriver) []WuDriver {
	s := slices.Clone(items)
	psSort(s, func(a, b WuDriver) int {
		pa, pb := 1, 1
		if a.DeviceProblem != 0 {
			pa = 0
		}
		if b.DeviceProblem != 0 {
			pb = 0
		}
		if pa != pb {
			return pa - pb
		}
		if r := cmpText(a.Class, b.Class); r != 0 {
			return r
		}
		return cmpText(a.Title, b.Title)
	})
	return s
}

// driverRows odpowiada ConvertTo-SdDriverRows.
func driverRows(items []WuDriver) []Row {
	var rows []Row
	for _, d := range SortDrivers(items) {
		prod := d.Manufacturer
		if prod == "" {
			prod = d.Provider
		}
		var uw []string
		if d.DeviceProblem != 0 {
			uw = append(uw, fmt.Sprintf("urządzenie z błędem (kod %d)", d.DeviceProblem))
		}
		if d.Optional {
			uw = append(uw, "opcjonalny")
		}
		rows = append(rows, row("Sterownik", d.Title, "Klasa", d.Class, "Producent", prod, "Data wersji", fmtDate(d.Date), "Rozmiar", fmtSize(d.SizeBytes, 1), "Uwagi", joinNonEmpty("; ", uw...)))
	}
	return rows
}

var displayClassRx = rx(`^Display`)

func checkWuDrivers(c *Ctx) {
	r := findWuDrivers(120 * time.Second)
	if r.TimedOut {
		c.Add(F{Title: "Sterowniki w Windows Update", Status: Info, Value: "Wyszukiwanie przerwano po 120 s (wolne łącze lub usługa Windows Update nie odpowiada)"})
		return
	}
	if r.Error != "" {
		c.Add(F{Title: "Sterowniki w Windows Update", Status: Info, Value: "Nie udało się wyszukać: " + limitText(r.Error, 120),
			Advice: "Brak połączenia z Windows Update, zasady firmowe (WSUS) albo uszkodzone składniki Windows Update (Narzędzia serwisowe > Reset komponentów Windows Update)."})
		return
	}
	if len(r.Items) == 0 {
		c.Add(F{Title: "Sterowniki w Windows Update", Status: OK, Value: "Brak nowszych sterowników dla urządzeń tego komputera"})
		return
	}
	c.AddTable(T{Name: "Sterowniki dostępne w Windows Update", Rows: driverRows(r.Items)})
	size, fix := 0.0, 0
	display := false
	var titles []string
	for i, d := range r.Items {
		size += d.SizeBytes
		if d.DeviceProblem != 0 {
			fix++
		}
		if displayClassRx.MatchString(d.Class) {
			display = true
		}
		if i < 12 {
			titles = append(titles, d.Title)
		}
	}
	val := fmt.Sprintf("%s do zainstalowania (%s)", fmtCount(len(r.Items), "sterownik", "sterowniki", "sterowników"), fmtSize(size, 1))
	if fix > 0 {
		val += fmt.Sprintf("; w tym dla urządzeń z błędami: %d", fix)
	}
	adv := "Zainstaluj je: Narzędzia serwisowe > „Sterowniki z Windows Update” (przed instalacją program tworzy punkt przywracania)."
	if display {
		adv += " Sterownik karty graficznej warto porównać z wersją ze strony NVIDIA / AMD / Intel - bywa tam nowszy."
	}
	c.Add(F{Title: "Sterowniki w Windows Update", Status: driverUpdateStatus(r.Items), Value: val, Details: strings.Join(titles, "\n"), Advice: adv})
}

var msRx = rx(`Microsoft`)

func checkSignedDrivers(c *Ctx) {
	drv := c.Cim(Q{Class: "Win32_PnPSignedDriver", Props: []string{"DeviceClass", "DriverProviderName", "Manufacturer", "DriverDate", "IsSigned", "DeviceName", "DriverVersion", "InfName"}, Timeout: 90 * time.Second})
	if len(drv) == 0 {
		return
	}
	keyClasses := []string{"DISPLAY", "NET", "MEDIA", "HDC", "SCSIADAPTER", "BLUETOOTH", "CAMERA", "IMAGE", "BIOMETRIC", "FIRMWARE", "SECURITYDEVICES"}
	now := time.Now()
	var rows, unsigned []Row
	var old []string
	for _, d := range drv {
		cls := strings.ToUpper(d.Str("DeviceClass"))
		isMs := msRx.MatchString(d.Str("DriverProviderName")) || msRx.MatchString(d.Str("Manufacturer"))
		dd, hasDate := d.Time("DriverDate")
		ageY := -1.0
		if hasDate {
			ageY = now.Sub(dd).Hours() / 24 / 365.25
		}
		if d.Has("IsSigned") && !d.Bool("IsSigned") && !isMs && d.Str("DeviceName") != "" {
			unsigned = append(unsigned, row("Urządzenie", d.Str("DeviceName"), "Klasa", cls, "Producent", d.Str("DriverProviderName"), "Wersja", d.Str("DriverVersion"), "Data", fmtDate(dd), "Plik", d.Str("InfName")))
		}
		if slices.Contains(keyClasses, cls) && d.Str("DeviceName") != "" && (!isMs || cls == "DISPLAY") {
			age := "-"
			if hasDate {
				age = fmtNum(ageY, 1)
			}
			signed := "nie"
			if d.Bool("IsSigned") {
				signed = "tak"
			}
			rows = append(rows, row("Urządzenie", d.Str("DeviceName"), "Klasa", cls, "Producent", d.Str("DriverProviderName"), "Wersja", d.Str("DriverVersion"),
				"Data", fmtDate(dd), "Wiek (lata)", age, "Podpisany", signed))
			if hasDate && !isMs {
				limit := 99.0
				switch cls {
				case "DISPLAY":
					limit = 2.5
				case "NET", "BLUETOOTH":
					limit = 4
				case "MEDIA":
					limit = 6
				}
				if ageY > limit {
					old = append(old, fmt.Sprintf("%s (%s, %s lat)", d.Str("DeviceName"), cls, fmtNum(ageY, 0)))
				}
			}
		}
	}
	if len(rows) > 0 {
		psSort(rows, func(a, b Row) int {
			if r := cmpText(fmt.Sprint(a.Get("Klasa")), fmt.Sprint(b.Get("Klasa"))); r != 0 {
				return r
			}
			return cmpText(fmt.Sprint(a.Get("Urządzenie")), fmt.Sprint(b.Get("Urządzenie")))
		})
		c.AddTable(T{Name: "Sterowniki kluczowych urządzeń", Rows: rows})
	}
	if len(unsigned) > 0 {
		c.AddTable(T{Name: "Sterowniki bez podpisu cyfrowego", Rows: unsigned})
		var det []string
		for i, u := range unsigned {
			if i < 8 {
				det = append(det, fmt.Sprintf("%v (%v)", u.Get("Urządzenie"), u.Get("Producent")))
			}
		}
		c.Add(F{Title: "Sterowniki bez podpisu cyfrowego", Status: Info, Value: fmtCount(len(unsigned), "sterownik", "sterowniki", "sterowników"), Details: strings.Join(det, "\n"),
			Advice: "Niepodpisane sterowniki mogą być blokowane przez Windows 11 (Izolacja rdzenia / Secure Boot) i obniżają stabilność. Poszukaj podpisanych wersji u producenta."})
	} else {
		c.Add(F{Title: "Podpisy sterowników", Status: OK, Value: "Wszystkie sterowniki spoza Microsoft są podpisane cyfrowo"})
	}
	if len(old) > 0 {
		c.Add(F{Title: "Wiek sterowników", Status: Info, Value: fmtCount(len(old), "stary sterownik", "stare sterowniki", "starych sterowników"), Details: strings.Join(old, "\n"),
			Advice: "Sprawdź u producenta, czy dostępne są nowsze wersje (szczególnie sterownik grafiki, sieci i Bluetooth). Nie instaluj sterowników z automatycznych „aktualizatorów”."})
	} else if len(rows) > 0 {
		c.Add(F{Title: "Wiek sterowników", Status: OK, Value: "Sterowniki kluczowych urządzeń nie są przestarzałe"})
	}
}

func checkDeviceList(c *Ctx) {
	pnp := pnpObjs(c)
	if len(pnp) == 0 {
		return
	}
	var list []win.WmiObject
	for _, d := range pnp {
		if d.Str("Name") != "" {
			list = append(list, d)
		}
	}
	psSort(list, func(a, b win.WmiObject) int {
		if r := cmpText(a.Str("PNPClass"), b.Str("PNPClass")); r != 0 {
			return r
		}
		return cmpText(a.Str("Name"), b.Str("Name"))
	})
	var rows []Row
	for _, d := range list {
		stan := "OK"
		if code := d.Int("ConfigManagerErrorCode"); code != 0 {
			stan = fmt.Sprintf("błąd %d", code)
		}
		rows = append(rows, row("Urządzenie", d.Str("Name"), "Klasa", d.Str("PNPClass"), "Producent", d.Str("Manufacturer"), "Usługa", d.Str("Service"), "Stan", stan))
	}
	c.AddTable(T{Name: "Urządzenia (Menedżer urządzeń)", Rows: rows})
}

// ---------------------------------------------------------------- Katalog Microsoft Update i strony producentów

// catalogQuery odpowiada Get-SdCatalogQuery: identyfikator skrócony do producenta i modelu układu.
func catalogQuery(hwid string) string {
	h := strings.TrimSpace(hwid)
	if h == "" {
		return ""
	}
	parts := strings.Split(h, "&")
	if len(parts) >= 2 && rx(`^(DEV|PID)_`).MatchString(parts[1]) {
		return parts[0] + "&" + parts[1]
	}
	return parts[0]
}

func catalogURL(hwid string) string {
	q := catalogQuery(hwid)
	if q == "" {
		return ""
	}
	return "https://www.catalog.update.microsoft.com/Search.aspx?q=" + escapeDataString(q)
}

// escapeDataString odpowiada [uri]::EscapeDataString (RFC 3986: zostają litery, cyfry i -_.~).
func escapeDataString(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

type vendorSupport struct {
	match, name, url, copy string
}

var vendorSupports = []vendorSupport{
	{`^LENOVO`, "Lenovo", "https://pcsupport.lenovo.com/", "Serial"},
	{`^Dell`, "Dell", "https://www.dell.com/support/home/", "Serial"},
	{`^(HP|Hewlett)`, "HP", "https://support.hp.com/drivers", "Serial"},
	{`^ASUS`, "ASUS", "https://www.asus.com/support/", "Model"},
	{`^Acer`, "Acer", "https://www.acer.com/us-en/support/drivers-and-manuals", "Serial"},
	{`^(Micro-Star|MSI)`, "MSI", "https://www.msi.com/support", "Model"},
	{`^(Gigabyte|GIGA-BYTE)`, "Gigabyte", "https://www.gigabyte.com/Support", "Model"},
	{`^ASRock`, "ASRock", "https://www.asrock.com/support/index.asp", "Model"},
	{`^(TOSHIBA|Dynabook)`, "Dynabook (Toshiba)", "https://support.dynabook.com/", "Model"},
	{`^SAMSUNG`, "Samsung", "https://www.samsung.com/support/", "Model"},
	{`^FUJITSU`, "Fujitsu", "https://support.ts.fujitsu.com/", "Serial"},
	{`^Microsoft`, "Microsoft Surface", "https://support.microsoft.com/surface", "Model"},
}

// SupportPage - strona ze sterownikami dla tego komputera (Get-SdVendorSupportInfo).
type SupportPage struct {
	Vendor, URL, CopyText, CopyLabel string
	IsSearch, Board                  bool
}

func vendorSupportInfo(manufacturer, model, serial, boardMfr, boardProduct string) SupportPage {
	find := func(m string) *vendorSupport {
		for i := range vendorSupports {
			if m != "" && rx(vendorSupports[i].match).MatchString(m) {
				return &vendorSupports[i]
			}
		}
		return nil
	}
	mfr, mdl, sn := cleanStr(manufacturer, ""), cleanStr(model, ""), cleanStr(serial, "")
	board := false
	v := find(mfr)
	if v == nil || mdl == "" {
		bm := cleanStr(boardMfr, "")
		if bv := find(bm); bv != nil && bv.name != "Microsoft Surface" {
			v, mfr, mdl, sn, board = bv, bm, cleanStr(boardProduct, ""), "", true
		}
	}
	if v != nil {
		useSerial := v.copy == "Serial" && sn != ""
		p := SupportPage{Vendor: v.name, URL: v.url, Board: board, CopyText: mdl, CopyLabel: "model"}
		switch {
		case useSerial:
			p.CopyText, p.CopyLabel = sn, "numer seryjny"
		case board:
			p.CopyLabel = "model płyty głównej"
		}
		return p
	}
	q := joinNonEmpty(" ", mfr, mdl, "sterowniki")
	vendor := mfr
	if vendor == "" {
		vendor = "nieznany producent"
	}
	return SupportPage{Vendor: vendor, URL: "https://www.bing.com/search?q=" + escapeDataString(q), IsSearch: true, Board: board, CopyText: mdl, CopyLabel: "model"}
}

// computerSupportInfo odpowiada Get-SdComputerSupportInfo (Lenovo: nazwa handlowa w Win32_ComputerSystemProduct.Version).
func computerSupportInfo(c *Ctx) SupportPage {
	cs := csObj(c)
	csp := c.cim1("Win32_ComputerSystemProduct", "Version", "IdentifyingNumber")
	bios := c.cim1("Win32_BIOS", "SerialNumber")
	bb := c.cim1("Win32_BaseBoard", "Manufacturer", "Product")
	mfr, model := "", ""
	if cs != nil {
		mfr, model = cs.Str("Manufacturer"), cs.Str("Model")
	}
	if rx(`^LENOVO`).MatchString(mfr) && csp != nil && cleanStr(csp.Str("Version"), "") != "" {
		model = csp.Str("Version")
	}
	serial := ""
	if bios != nil {
		serial = cleanStr(bios.Str("SerialNumber"), "")
	}
	if serial == "" && csp != nil {
		serial = cleanStr(csp.Str("IdentifyingNumber"), "")
	}
	bm, bp := "", ""
	if bb != nil {
		bm, bp = bb.Str("Manufacturer"), bb.Str("Product")
	}
	return vendorSupportInfo(mfr, model, serial, bm, bp)
}
