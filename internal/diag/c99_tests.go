package diag

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"serwisdiag/internal/win"
)

// Testy obciążeniowe (poziom "Test", checks\99-tests.ps1): uruchamiane wyłącznie na życzenie technika.

var testChecks = []*Check{
	{ID: "tt.disk", Category: "tests", Title: "Test szybkości dysku systemowego", Level: "Test", Run: checkDiskBench},
	{ID: "tt.cpu", Category: "tests", Title: "Test obciążeniowy procesora (dławienie, temperatura)", Level: "Test", Run: checkCPUBurn},
	{ID: "tt.net", Category: "tests", Title: "Test prędkości pobierania", Level: "Test", NeedsInternet: true, Run: checkDownload},
	{ID: "tt.volscan", Category: "tests", Title: "Skan systemu plików NTFS (online, tylko odczyt)", Level: "Test", NeedsAdmin: true, Run: checkVolScan},
	{ID: "tt.dism", Category: "tests", Title: "Pełna weryfikacja magazynu składników (DISM ScanHealth)", Level: "Test", NeedsAdmin: true, Run: checkDismScan},
}

var diskFailRx = rx(`Predictive|Failed|IO Error|Lost Communication`)

// systemDiskRisk odpowiada Get-SdSystemDiskRisk: opis zagrożenia, gdy dysk systemowy ma objawy awarii.
func systemDiskRisk(c *Ctx) string {
	disks := diskInventory(c)
	idx := -1
	if d, ok := volumeMapsFor(c).letterToDisk[strings.ToUpper(sysDrive())]; ok {
		idx = d
	}
	if idx < 0 {
		for _, d := range disks {
			if d.IsSystem {
				idx = int(d.Index)
				break
			}
		}
	}
	if idx < 0 {
		return ""
	}
	c.s.mu.Lock()
	st, ok := c.s.diskStatus[idx]
	c.s.mu.Unlock()
	if ok && st == Crit {
		return fmt.Sprintf("dysk systemowy (dysk %d) ma objawy awarii w ocenie SMART - zob. kategoria Dyski", idx)
	}
	for _, d := range disks {
		if int(d.Index) == idx && (eqi(d.Health, "Unhealthy") || eqi(d.WmiStatus, "Pred Fail") || diskFailRx.MatchString(d.Operational)) {
			return fmt.Sprintf("Windows zgłasza awarię dysku systemowego (dysk %d: %s)", idx, joinNonEmpty(", ", d.Health, d.WmiStatus, d.Operational))
		}
	}
	return ""
}

func checkDiskBench(c *Ctx) {
	if risk := systemDiskRisk(c); risk != "" {
		c.Add(F{Title: "Test szybkości dysku", Status: Skip, Value: "Pominięto: " + risk,
			Advice: "Najpierw zabezpiecz dane (kopia lub klon dysku), dopiero potem testuj wydajność - intensywny zapis i odczyt może przyspieszyć awarię."})
		return
	}
	drive := sysDrive()
	if v := c.Cim(Q{Class: "Win32_LogicalDisk", Props: []string{"FreeSpace"}, Where: "DeviceID='" + drive + "'"}); len(v) > 0 && v[0].Float("FreeSpace") < 4*(1<<30) {
		c.Add(F{Title: "Test szybkości dysku", Status: Skip, Value: "Za mało wolnego miejsca na teście (potrzeba min. 4 GB)."})
		return
	}
	const sizeMB = 512
	path := filepath.Join(os.TempDir(), fmt.Sprintf("sd-bench-%d.tmp", time.Now().UnixNano()))
	c.Progress("Test dysku: zapis i odczyt sekwencyjny, losowe 4K (ok. 25 s)...")
	r := win.DiskBench(path, sizeMB, 4)
	os.Remove(path)
	if r.Err != "" {
		c.Add(F{Title: "Test szybkości dysku", Status: Err, Value: "Błąd testu: " + r.Err})
		return
	}
	disks := diskInventory(c)
	typ := "dysk"
	var sys *Disk
	for _, d := range disks {
		if d.IsSystem {
			sys = d
			break
		}
	}
	if sys == nil && len(disks) > 0 {
		sys = disks[0]
	}
	if sys != nil {
		typ = diskTypeName(sys)
	}
	seqThr, iopsThr := 60.0, 60.0
	switch {
	case strings.Contains(typ, "NVMe"):
		seqThr, iopsThr = 800, 6000
	case strings.Contains(strings.ToUpper(typ), "SSD"):
		seqThr, iopsThr = 300, 4000
	}
	val := fmt.Sprintf("zapis sekw. %s MB/s; odczyt sekw. %s MB/s; losowy odczyt 4K: %s IOPS; losowy zapis 4K: %s IOPS", fmtNum(r.SeqWriteMBs, 0), fmtNum(r.SeqReadMBs, 0), fmtNum(r.Rand4kReadIops, 0), fmtNum(r.Rand4kWriteIops, 0))
	d := []string{fmt.Sprintf("Typ dysku: %s; plik testowy: %d MB na %s (usunięty po teście)", typ, sizeMB, drive), "Test bez pamięci podręcznej systemu, wątek pojedynczy (głębokość kolejki 1)."}
	c.AddTable(T{Name: "Wyniki testu dysku", Rows: []Row{
		row("Test", "Odczyt sekwencyjny", "Wynik", fmtNum(r.SeqReadMBs, 0)+" MB/s"),
		row("Test", "Zapis sekwencyjny", "Wynik", fmtNum(r.SeqWriteMBs, 0)+" MB/s"),
		row("Test", "Odczyt losowy 4 KB (QD1)", "Wynik", fmt.Sprintf("%s IOPS (~%s MB/s)", fmtNum(r.Rand4kReadIops, 0), fmtNum(r.Rand4kReadIops*4096/(1<<20), 1))),
		row("Test", "Zapis losowy 4 KB (QD1)", "Wynik", fmt.Sprintf("%s IOPS (~%s MB/s)", fmtNum(r.Rand4kWriteIops, 0), fmtNum(r.Rand4kWriteIops*4096/(1<<20), 1))),
	}})
	if r.SeqReadMBs < seqThr || r.Rand4kReadIops < iopsThr {
		c.Add(F{Title: "Test szybkości dysku", Status: Warn, Value: val, Details: strings.Join(d, "\n"),
			Advice: "Wydajność dysku jest niższa niż typowa dla tego typu nośnika. Sprawdź tryb kontrolera (AHCI), stan SMART, kabel SATA i sterowniki; zapełniony lub zużyty dysk bywa wolniejszy. Przy HDD zalecany SSD."})
	} else {
		c.Add(F{Title: "Test szybkości dysku", Status: OK, Value: val, Details: strings.Join(d, "\n")})
	}
}

type thermalReading struct {
	temp, passive float64
	hasT, hasP    bool
	reasons       int64
}

func readThermal(c *Ctx) thermalReading {
	var r thermalReading
	for _, z := range c.cim("Win32_PerfFormattedData_Counters_ThermalZoneInformation", "HighPrecisionTemperature", "Temperature", "PercentPassiveLimit", "ThrottleReasons") {
		t, ok := 0.0, false
		if z.Int("HighPrecisionTemperature") != 0 {
			t, ok = z.Float("HighPrecisionTemperature")/10-273.15, true
		} else if z.Int("Temperature") != 0 {
			t, ok = z.Float("Temperature")-273.15, true
		}
		if ok && (!r.hasT || t > r.temp) {
			r.temp, r.hasT = t, true
		}
		if z.Has("PercentPassiveLimit") && (!r.hasP || z.Float("PercentPassiveLimit") < r.passive) {
			r.passive, r.hasP = z.Float("PercentPassiveLimit"), true
		}
		r.reasons |= z.Int("ThrottleReasons")
	}
	return r
}

func checkCPUBurn(c *Ctx) {
	logical := runtime.NumCPU()
	const burnSec = 45
	pdh, _ := win.NewPdh()
	iFreq, iLoad := -1, -1
	if pdh != nil {
		defer pdh.Close()
		iFreq = pdh.Add(`\Processor Information(_Total)\% of Maximum Frequency`)
		iLoad = pdh.Add(`\Processor(_Total)\% Processor Time`)
		pdh.Collect()
	}
	var freq, load, temps, passive []float64
	var reasons int64
	bat := false
	for _, b := range c.cim("Win32_Battery", "BatteryStatus") {
		if b.Int("BatteryStatus") == 1 {
			bat = true
		}
	}
	c.Progress("Test CPU: pomiar w spoczynku...")
	idle := readThermal(c)
	time.Sleep(800 * time.Millisecond)
	var stop atomic.Bool
	for i := 0; i < logical; i++ {
		go func() {
			x := 1.2345
			for !stop.Load() {
				for k := 0; k < 2000000; k++ {
					x = math.Sqrt(x*1.000001+0.5) + math.Sin(x)
				}
				if math.IsNaN(x) || math.IsInf(x, 0) {
					x = 1
				}
			}
		}()
	}
	start := time.Now()
	for tick := 1; time.Since(start).Seconds() < burnSec; tick++ {
		time.Sleep(time.Second)
		c.Progress(fmt.Sprintf("Test CPU: obciążenie wszystkich wątków... %d/%d s", int(time.Since(start).Seconds()), burnSec))
		if pdh != nil {
			pdh.Collect()
			if v, ok := pdh.Get(iFreq); ok {
				freq = append(freq, v)
			}
			if v, ok := pdh.Get(iLoad); ok {
				load = append(load, v)
			}
		}
		if tick%3 == 0 {
			th := readThermal(c)
			if th.hasT {
				temps = append(temps, th.temp)
			}
			if th.hasP {
				passive = append(passive, th.passive)
			}
			reasons |= th.reasons
			if len(freq) == 0 {
				if pi := c.Cim(Q{Class: "Win32_PerfFormattedData_Counters_ProcessorInformation", Props: []string{"PercentOfMaximumFrequency"}, Where: "Name='_Total'"}); len(pi) > 0 {
					freq = append(freq, pi[0].Float("PercentOfMaximumFrequency"))
				}
			}
		}
		if c.s.cancel.Load() {
			break
		}
	}
	stop.Store(true)
	if len(freq) < 8 || len(load) < 8 {
		c.Add(F{Title: "Test obciążeniowy procesora", Status: Info, Value: "Za mało próbek, aby ocenić dławienie (liczniki wydajności niedostępne)."})
		return
	}
	startF := avg(freq[3:min(9, len(freq))])
	endF := avg(freq[max(0, len(freq)-12):])
	minF := slicesMin(freq)
	avgLoad := avg(load[3:])
	maxTemp, hasTemp, tempNote := 0.0, false, ""
	if len(temps) > 0 {
		maxTemp, hasTemp = maxOf(temps), true
		// stały odczyt = czujnik ACPI nie odzwierciedla temperatury rdzeni (częste w laptopach) - nie oceniamy po nim
		if len(temps) >= 3 && maxTemp-slicesMin(temps) < 0.6 {
			tempNote = "Odczyt temperatury ACPI był stały podczas całego testu - czujnik nie odzwierciedla temperatury procesora (użyj HWiNFO / HWMonitor)."
			hasTemp = false
		}
	}
	minPassive := 100.0
	if len(passive) > 0 {
		minPassive = slicesMin(passive)
	}
	drop := startF - endF
	tempVal := "brak odczytu"
	if hasTemp {
		tempVal = fmt.Sprintf("%s / %s °C", fmtNum(idle.temp, 0), fmtNum(maxTemp, 0))
	}
	c.AddTable(T{Name: "Wyniki testu obciążeniowego CPU", Rows: []Row{
		row("Parametr", "Średnie obciążenie w trakcie testu", "Wartość", fmtNum(avgLoad, 0)+"%"),
		row("Parametr", "Taktowanie na początku (% wartości znamionowej)", "Wartość", fmtNum(startF, 0)+"%"),
		row("Parametr", "Taktowanie pod koniec testu", "Wartość", fmtNum(endF, 0)+"%"),
		row("Parametr", "Najniższe taktowanie", "Wartość", fmtNum(minF, 0)+"%"),
		row("Parametr", "Temperatura strefy ACPI w spoczynku / maks.", "Wartość", tempVal),
		row("Parametr", "Najniższy limit pasywny (dławienie termiczne)", "Wartość", fmtNum(minPassive, 0)+"%"),
	}})
	d := []string{fmt.Sprintf("Czas obciążenia: %d s, wątków: %d", burnSec, logical)}
	if bat {
		d = append(d, "Uwaga: test wykonano przy zasilaniu z baterii - spadek taktowania jest wtedy normalny (oszczędzanie energii).")
	}
	if tempNote != "" {
		d = append(d, tempNote)
	} else {
		d = append(d, "Temperatura pochodzi ze strefy ACPI i nie zawsze odpowiada temperaturze rdzeni procesora.")
	}
	val := fmt.Sprintf("obciążenie %s%%, taktowanie %s%% → %s%% (spadek %s pkt), limit pasywny %s%%", fmtNum(avgLoad, 0), fmtNum(startF, 0), fmtNum(endF, 0), fmtNum(drop, 0), fmtNum(minPassive, 0))
	if hasTemp {
		val += ", maks. temperatura " + fmtNum(maxTemp, 0) + " °C"
	}
	det := strings.Join(d, "\n")
	throttle := minPassive < 100 || reasons != 0 || drop > 25 || endF < 60
	switch {
	case avgLoad < 80:
		c.Add(F{Title: "Test obciążeniowy procesora", Status: Info, Value: val, Details: det, Advice: "Nie udało się w pełni obciążyć procesora (inne procesy lub ograniczenia mocy) - wynik jest orientacyjny."})
	case hasTemp && maxTemp >= 95:
		c.Add(F{Title: "Test obciążeniowy procesora", Status: Crit, Value: val, Details: det, Advice: "Krytycznie wysoka temperatura pod obciążeniem. Oczyść chłodzenie, wymień pastę termoprzewodzącą, sprawdź wentylatory i przepływ powietrza."})
	case throttle && !bat:
		c.Add(F{Title: "Test obciążeniowy procesora", Status: Warn, Value: val, Details: det,
			Advice: "Procesor zwalnia pod obciążeniem (dławienie termiczne lub limit mocy). Oczyść układ chłodzenia, wymień pastę termoprzewodzącą, sprawdź zasilacz (moc) i plan zasilania."})
	case throttle:
		c.Add(F{Title: "Test obciążeniowy procesora", Status: Info, Value: val, Details: det, Advice: "Powtórz test przy podłączonym zasilaniu sieciowym, aby ocenić prawdziwe dławienie termiczne."})
	default:
		c.Add(F{Title: "Test obciążeniowy procesora", Status: OK, Value: val, Details: det})
	}
}

func slicesMin(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		m = math.Min(m, x)
	}
	return m
}

func checkDownload(c *Ctx) {
	servers := []struct{ name, url string }{
		{"Cloudflare", "https://speed.cloudflare.com/__down?bytes=30000000"},
		{"Cloudflare (alt.)", "https://speed.cloudflare.com/__down?bytes=15000000"},
		{"ThinkBroadband", "http://ipv4.download.thinkbroadband.com/20MB.zip"},
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, s := range servers {
		c.Progress("Test prędkości: pobieranie z " + s.name + "...")
		t0 := time.Now()
		resp, err := client.Get(s.url)
		if err != nil {
			continue
		}
		ttfb := time.Since(t0).Milliseconds()
		var total int64
		buf := make([]byte, 65536)
		start := time.Now()
		for time.Since(start).Seconds() < 10 {
			n, err := resp.Body.Read(buf)
			total += int64(n)
			if err == io.EOF || err != nil {
				break
			}
		}
		el := time.Since(start).Seconds()
		resp.Body.Close()
		if total > 500000 && el > 0.5 {
			mbps := float64(total) * 8 / 1e6 / el
			st, adv := OK, ""
			if mbps < 5 {
				st, adv = Warn, "Bardzo niska prędkość pobierania - porównaj z pakietem u operatora, sprawdź Wi-Fi/kabel, inne urządzenia w sieci i router."
			} else if mbps < 25 {
				st = Info
			}
			c.Add(F{Title: "Test prędkości pobierania", Status: st, Value: fmt.Sprintf("%s Mb/s (pobrano %s w %s s; serwer: %s; opóźnienie odpowiedzi %d ms)", fmtNum(mbps, 0), fmtSize(float64(total), 1), fmtNum(el, 1), s.name, ttfb), Advice: adv})
			return
		}
	}
	c.Add(F{Title: "Test prędkości pobierania", Status: Warn, Value: "Nie udało się pobrać danych testowych z żadnego serwera.",
		Advice: "Brak internetu, blokada przez zaporę/proxy lub filtr. Wynik testów łączności znajdziesz w kategorii Sieć."})
}

func checkVolScan(c *Ctx) {
	drive := sysDrive()
	c.Progress(fmt.Sprintf("Skan NTFS wolumenu %s: może potrwać kilka minut...", drive))
	r := win.Run(30*time.Minute, filepath.Join(getenv("SystemRoot"), `System32\chkdsk.exe`), drive, "/scan")
	if r.Err != nil {
		c.Add(F{Title: "Skan NTFS", Status: Err, Value: "Błąd: " + limitText(r.Err.Error(), 140)})
		return
	}
	// kody zakończenia chkdsk: 0 - brak błędów, 2 - tylko porządkowanie, 1 - naprawiono, 3 - błędy nienaprawione
	switch r.ExitCode {
	case 0, 2:
		c.Add(F{Title: "Skan NTFS " + drive, Status: OK, Value: "Nie wykryto błędów systemu plików"})
	default:
		res := map[int]string{1: "ErrorsFixed", 3: "ErrorsFound"}[r.ExitCode]
		if res == "" {
			res = fmt.Sprintf("kod %d", r.ExitCode)
		}
		c.Add(F{Title: "Skan NTFS " + drive, Status: Warn, Value: "Wynik: " + res, Advice: "Zaplanuj naprawę (chkdsk /f) po wykonaniu kopii danych i sprawdzeniu stanu SMART dysku."})
	}
}

func checkDismScan(c *Ctx) {
	c.Progress("DISM ScanHealth: może potrwać kilka minut...")
	s, err := win.ImageHealth(true)
	if err != nil {
		c.Add(F{Title: "DISM ScanHealth", Status: Err, Value: "Błąd: " + limitText(err.Error(), 140)})
		return
	}
	switch s {
	case "Healthy":
		c.Add(F{Title: "DISM ScanHealth", Status: OK, Value: "Magazyn składników jest zdrowy"})
	case "Repairable":
		c.Add(F{Title: "DISM ScanHealth", Status: Warn, Value: "Wykryto uszkodzenia możliwe do naprawy", Advice: "Uruchom DISM /Online /Cleanup-Image /RestoreHealth, następnie sfc /scannow i uruchom ponownie komputer."})
	default:
		c.Add(F{Title: "DISM ScanHealth", Status: Crit, Value: "Stan: " + s, Advice: "Uszkodzeń nie da się naprawić online - użyj nośnika instalacyjnego jako źródła naprawy lub zainstaluj system z zachowaniem plików."})
	}
}
