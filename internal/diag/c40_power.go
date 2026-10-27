package diag

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Zasilanie i bateria (checks\40-power.ps1)

var powerChecks = []*Check{
	{ID: "pw.battery", Category: "power", Title: "Bateria", Level: "Quick", Run: checkBattery},
	{ID: "pw.plan", Category: "power", Title: "Plan zasilania", Level: "Quick", Run: checkPowerPlan},
}

type batteryReportEntry struct {
	ID, Manufacturer, Chemistry        string
	DesignCapacity, FullCharge, Cycles int
}

// batteryReport odpowiada Get-SdBatteryReport: powercfg /batteryreport /xml (Windows 10 1607+).
func batteryReport(c *Ctx) []batteryReportEntry {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("sd-batt-%d.xml", time.Now().UnixNano()))
	defer os.Remove(tmp)
	win.Run(30*time.Second, filepath.Join(getenv("SystemRoot"), `System32\powercfg.exe`), "/batteryreport", "/xml", "/output", tmp)
	b, err := os.ReadFile(tmp)
	if err != nil {
		return nil
	}
	b = decodeText(b)
	var doc struct {
		Batteries struct {
			Battery []struct {
				Id                 string
				Manufacturer       string
				Chemistry          string
				DesignCapacity     string
				FullChargeCapacity string
				CycleCount         string
			}
		}
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		c.Log("WARN", "Raport baterii: "+err.Error())
		return nil
	}
	var out []batteryReportEntry
	for _, x := range doc.Batteries.Battery {
		d, _ := strconv.Atoi(strings.TrimSpace(x.DesignCapacity))
		f, _ := strconv.Atoi(strings.TrimSpace(x.FullChargeCapacity))
		cy, _ := strconv.Atoi(strings.TrimSpace(x.CycleCount))
		out = append(out, batteryReportEntry{ID: x.Id, Manufacturer: x.Manufacturer, Chemistry: x.Chemistry, DesignCapacity: d, FullCharge: f, Cycles: cy})
	}
	return out
}

var instSuffixRx = regexp.MustCompile(`_\d+$`)

// selectBatteryData odpowiada Select-SdBatteryData: dopasowanie po InstanceName (PNPDeviceID + "_0"),
// potem po nazwie urządzenia; przy jednej baterii i jednym wpisie - jedyny wpis.
func selectBatteryData(bat win.WmiObject, items, static []win.WmiObject, count int) win.WmiObject {
	if len(items) == 0 {
		return nil
	}
	if pnp := strings.ToUpper(bat.Str("PNPDeviceID")); pnp != "" {
		for _, x := range items {
			if strings.ToUpper(instSuffixRx.ReplaceAllString(x.Str("InstanceName"), "")) == pnp {
				return x
			}
		}
	}
	if nm := strings.TrimSpace(bat.Str("Name")); nm != "" {
		for _, s := range static {
			if strings.TrimSpace(s.Str("DeviceName")) == nm {
				for _, x := range items {
					if x.Str("InstanceName") == s.Str("InstanceName") {
						return x
					}
				}
			}
		}
	}
	if count == 1 && len(items) == 1 {
		return items[0]
	}
	return nil
}

func checkBattery(c *Ctx) {
	laptop, vm := isLaptop(c), isVM(c)
	bats := c.cim("Win32_Battery", "Name", "PNPDeviceID", "EstimatedChargeRemaining", "BatteryStatus", "EstimatedRunTime", "Status")
	if len(bats) == 0 {
		if laptop && !vm {
			c.Add(F{Title: "Bateria", Status: Warn, Value: "Nie wykryto baterii w laptopie",
				Details: "System nie widzi baterii: może być odłączona, całkowicie zużyta/uszkodzona lub mieć problem ze sterownikiem ACPI.",
				Advice:  "Sprawdź połączenie baterii, uruchom laptop bez baterii z samym zasilaczem (jeśli to możliwe) i sprawdź w Menedżerze urządzeń „Bateria z metodą sterowania zgodna z ACPI”."})
		} else {
			c.Add(F{Title: "Bateria", Status: Skip, Value: "Nie dotyczy (komputer bez baterii)"})
		}
		return
	}
	const ns = `root\wmi`
	full := c.Cim(Q{NS: ns, Class: "BatteryFullChargedCapacity", Props: []string{"InstanceName", "FullChargedCapacity"}})
	stat := c.Cim(Q{NS: ns, Class: "BatteryStaticData", Props: []string{"InstanceName", "DesignedCapacity", "ManufactureName", "DeviceName"}})
	cyc := c.Cim(Q{NS: ns, Class: "BatteryCycleCount", Props: []string{"InstanceName", "CycleCount"}})
	live := c.Cim(Q{NS: ns, Class: "BatteryStatus", Props: []string{"InstanceName", "Charging", "Discharging", "PowerOnline", "Voltage", "Critical"}})
	var report []batteryReportEntry
	if len(stat) == 0 || stat[0].Int("DesignedCapacity") == 0 {
		report = batteryReport(c)
	}
	n := len(bats)
	for idx, b := range bats {
		st := selectBatteryData(b, stat, stat, n)
		fc := selectBatteryData(b, full, stat, n)
		cy := selectBatteryData(b, cyc, stat, n)
		lv := selectBatteryData(b, live, stat, n)
		var rep *batteryReportEntry
		for i := range report {
			if b.Str("Name") != "" && strings.TrimSpace(report[i].ID) == strings.TrimSpace(b.Str("Name")) {
				rep = &report[i]
				break
			}
		}
		if rep == nil && n == 1 && len(report) == 1 {
			rep = &report[0]
		}
		design, fullCap := 0.0, 0.0
		cycles := int64(-1)
		if st != nil && st.Int("DesignedCapacity") != 0 {
			design = st.Float("DesignedCapacity")
		} else if rep != nil && rep.DesignCapacity != 0 {
			design = float64(rep.DesignCapacity)
		}
		if fc != nil && fc.Int("FullChargedCapacity") != 0 {
			fullCap = fc.Float("FullChargedCapacity")
		} else if rep != nil && rep.FullCharge != 0 {
			fullCap = float64(rep.FullCharge)
		}
		if cy != nil && cy.Has("CycleCount") {
			cycles = cy.Int("CycleCount")
		} else if rep != nil {
			cycles = int64(rep.Cycles)
		}
		chem, mfr := "", ""
		if rep != nil {
			chem = rep.Chemistry
		}
		if st != nil && st.Str("ManufactureName") != "" {
			mfr = st.Str("ManufactureName")
		} else if rep != nil {
			mfr = rep.Manufacturer
		}
		name := b.Str("Name")
		if name == "" {
			name = fmt.Sprintf("Bateria %d", idx+1)
		}
		d := []string{"Model: " + name}
		if mfr != "" {
			d = append(d, "Producent: "+mfr)
		}
		if chem != "" {
			d = append(d, "Technologia: "+chem)
		}
		rows := []Row{row("Parametr", "Model", "Wartość", name)}
		health := math.NaN()
		if design > 0 && fullCap > 0 {
			health = 100 * fullCap / design
			d = append(d, "Pojemność fabryczna: "+fmtNum(design, 0)+" mWh", "Pojemność przy pełnym naładowaniu: "+fmtNum(fullCap, 0)+" mWh")
			rows = append(rows, row("Parametr", "Pojemność fabryczna", "Wartość", fmtNum(design, 0)+" mWh"),
				row("Parametr", "Pojemność aktualna (pełne naładowanie)", "Wartość", fmtNum(fullCap, 0)+" mWh"),
				row("Parametr", "Kondycja baterii", "Wartość", fmtNum(health, 1)+"%"))
		}
		if cycles > 0 {
			d = append(d, fmt.Sprintf("Liczba cykli ładowania: %d", cycles))
			rows = append(rows, row("Parametr", "Cykle ładowania", "Wartość", cycles))
		}
		charge := b.Int("EstimatedChargeRemaining")
		state := ""
		if lv != nil {
			switch {
			case lv.Bool("Charging"):
				state = "ładuje się"
			case lv.Bool("Discharging"):
				state = "rozładowuje się"
			case lv.Bool("PowerOnline"):
				state = "zasilanie sieciowe"
			}
		}
		if state == "" {
			switch b.Int("BatteryStatus") {
			case 1:
				state = "rozładowuje się"
			case 2:
				state = "zasilanie sieciowe"
			case 3:
				state = "naładowana"
			case 6:
				state = "ładuje się"
			}
		}
		d = append(d, fmt.Sprintf("Poziom naładowania: %d%% (%s)", charge, state))
		rows = append(rows, row("Parametr", "Poziom naładowania", "Wartość", fmt.Sprintf("%d%% (%s)", charge, state)))
		if rt := b.Int("EstimatedRunTime"); rt != 0 && rt < 1000000 && lv != nil && lv.Bool("Discharging") {
			d = append(d, "Szacowany czas pracy: "+fmtSpan(time.Duration(rt)*time.Minute))
		}
		if lv != nil && lv.Int("Voltage") != 0 {
			d = append(d, "Napięcie: "+fmtNum(lv.Float("Voltage")/1000, 2)+" V")
		}
		status, advice := OK, ""
		var parts []string
		if !math.IsNaN(health) {
			if health > 130 {
				status = Info
				parts = append(parts, "Dane o pojemności niewiarygodne (producent podaje nietypowe wartości)")
			} else {
				h := math.Min(health, 100)
				parts = append(parts, fmt.Sprintf("kondycja %s%% (pojemność %s z %s mWh)", fmtNum(h, 0), fmtNum(fullCap, 0), fmtNum(design, 0)))
				switch {
				case h < 50:
					status, advice = Crit, "Bateria jest mocno zużyta - czas pracy będzie bardzo krótki, a przy dalszym spadku pojemności mogą wystąpić nagłe wyłączenia. Zalecana wymiana baterii."
				case h < 65:
					status, advice = Warn, "Bateria jest wyraźnie zużyta (poniżej 65% pojemności). Rozważ wymianę, jeśli czas pracy na baterii jest dla użytkownika zbyt krótki."
				case h < 80:
					status = Info
				}
			}
		} else {
			parts = append(parts, "brak danych o pojemności fabrycznej")
			status = Info
		}
		if cycles > 0 {
			parts = append(parts, fmtCount(int(cycles), "cykl", "cykle", "cykli"))
		}
		parts = append(parts, fmt.Sprintf("%d%% (%s)", charge, state))
		if s := b.Str("Status"); s != "" && !eqi(s, "OK") {
			status = worst(status, Warn)
			parts = append(parts, "stan: "+s)
			if advice == "" {
				advice = "System zgłasza nieprawidłowy stan baterii - sprawdź baterię i sterownik ACPI."
			}
		}
		if lv != nil && lv.Bool("Critical") {
			status = Crit
			parts = append(parts, "krytycznie niski poziom")
		}
		title := "Bateria"
		if n > 1 {
			title = fmt.Sprintf("Bateria %d", idx+1)
		}
		c.Add(F{Title: title, Status: status, Value: strings.Join(parts, "; "), Details: strings.Join(d, "\n"), Advice: advice})
		c.AddTable(T{Name: title, Rows: rows})
		if !math.IsNaN(health) && health <= 130 {
			c.SetMeta("BatteryHealth", psRound(math.Min(health, 100)))
		}
	}
}

var knownPlans = map[string]string{
	"381b4222-f694-41f0-9685-ff5bb260df2e": "Zrównoważony",
	"8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c": "Wysoka wydajność",
	"a1841308-3541-4fab-bc81-f71556f20b4a": "Oszczędzanie energii",
	"e9a42b02-d5df-448d-aa00-03f14749eb61": "Najwyższa wydajność",
}

func checkPowerPlan(c *Ctx) {
	guid, friendly, ok := win.ActivePowerScheme()
	if !ok {
		return
	}
	name := guid
	if n, ok := knownPlans[guid]; ok {
		name = n
	} else if friendly != "" {
		name = friendly
	}
	if guid == "a1841308-3541-4fab-bc81-f71556f20b4a" {
		c.Add(F{Title: "Plan zasilania", Status: Info, Value: name,
			Details: "Plan „Oszczędzanie energii” ogranicza taktowanie procesora i jasność ekranu.",
			Advice:  "Jeśli komputer wydaje się wolny, zmień plan na „Zrównoważony” lub „Wysoka wydajność” (zwłaszcza przy zasilaniu sieciowym)."})
	} else {
		c.Add(F{Title: "Plan zasilania", Status: OK, Value: name})
	}
}
