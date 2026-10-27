package diag

import (
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Wydajność (checks\50-perf.ps1)

var perfChecks = []*Check{
	{ID: "pf.sample", Category: "perf", Title: "Obciążenie procesora, pamięci i dysku (próbkowanie)", Level: "Quick", Run: checkSample},
	{ID: "pf.boot", Category: "perf", Title: "Czas uruchamiania systemu", Level: "Quick", NeedsAdmin: true, Run: checkBoot},
}

// events odpowiada Get-SdEvents: filtr XPath na każdy dziennik; z kilku dzienników - najnowsze max zdarzeń.
func (c *Ctx) events(f win.EventFilter, max int, withMsg bool) []*win.Event {
	var all []*win.Event
	xp := f.XPath()
	for _, log := range f.Logs {
		ev, err := win.QueryEvents(log, xp, max, withMsg)
		if err != nil {
			c.Log("WARN", fmt.Sprintf("Dziennik %s: %v", log, err))
		}
		all = append(all, ev...)
	}
	if len(f.Logs) > 1 {
		psSort(all, func(a, b *win.Event) int { return b.Time.Compare(a.Time) })
		if len(all) > max {
			all = all[:max]
		}
	}
	return all
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func maxOf(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = math.Max(m, x)
	}
	if len(v) == 0 {
		return 0
	}
	return m
}

type procRow struct {
	name         string
	pid          uint32
	cpu, ram, io float64
}

func checkSample(c *Ctx) {
	logical := 0
	for _, p := range cpuObjs(c) {
		logical += int(p.Int("NumberOfLogicalProcessors"))
	}
	if logical <= 0 {
		logical = runtime.NumCPU()
	}
	// Liczniki PDH (nazwy angielskie, niezależne od języka systemu); migawki procesów z jądra systemu.
	pdh, err := win.NewPdh()
	if err != nil {
		c.Log("WARN", "PDH: "+err.Error())
	}
	iCpu, iDisk, iQueue, iNp, iPaged, iCache := -1, -1, -1, -1, -1, -1
	if pdh != nil {
		defer pdh.Close()
		iCpu = pdh.Add(`\Processor(_Total)\% Processor Time`)
		iDisk = pdh.Add(`\PhysicalDisk(_Total)\% Disk Time`)
		iQueue = pdh.Add(`\PhysicalDisk(_Total)\Current Disk Queue Length`)
		iNp = pdh.Add(`\Memory\Pool Nonpaged Bytes`)
		iPaged = pdh.Add(`\Memory\Pool Paged Bytes`)
		iCache = pdh.Add(`\Memory\Cache Bytes`)
		pdh.Collect()
	}
	var cpuS, dskS, qS []float64
	clock := time.Now()
	sec := func() float64 { return time.Since(clock).Seconds() }
	a1 := sec()
	snap1, _ := win.Processes()
	b1 := sec()
	p1 := map[uint32]win.Process{}
	for _, p := range snap1 {
		p1[p.PID] = p
	}
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if pdh != nil {
			pdh.Collect()
			if v, ok := pdh.Get(iCpu); ok {
				cpuS = append(cpuS, v)
			}
			if v, ok := pdh.Get(iDisk); ok {
				dskS = append(dskS, math.Min(v, 100))
			}
			if v, ok := pdh.Get(iQueue); ok {
				qS = append(qS, v)
			}
		}
	}
	if len(cpuS) == 0 { // awaryjnie: WMI (wolniejsze)
		for i := 0; i < 3; i++ {
			var vals []float64
			for _, p := range c.cim("Win32_Processor", "LoadPercentage") {
				if p.Has("LoadPercentage") {
					vals = append(vals, p.Float("LoadPercentage"))
				}
			}
			if len(vals) > 0 {
				cpuS = append(cpuS, avg(vals))
			}
		}
	}
	a2 := sec()
	snap2, _ := win.Processes()
	b2 := sec()
	c.Log("DEBUG", fmt.Sprintf("pf.sample: p1 %s-%ss, p2 %s-%ss", fmtNum(a1, 1), fmtNum(b1, 1), fmtNum(a2, 1), fmtNum(b2, 1)))
	dt := math.Max((a2+b2)/2-(a1+b1)/2, 0.5)

	var rows []procRow
	for _, p := range snap2 {
		if p.PID == 0 {
			continue
		}
		r := procRow{name: p.Name, pid: p.PID, ram: float64(p.WorkingSet)}
		if prev, ok := p1[p.PID]; ok {
			cpuSec := float64((p.KernelTime+p.UserTime)-(prev.KernelTime+prev.UserTime)) / 1e7
			r.cpu = math.Max(0, 100*cpuSec/(dt*float64(logical)))
			rd := math.Max(0, float64(p.ReadBytes-prev.ReadBytes)/dt)
			wr := math.Max(0, float64(p.WriteBytes-prev.WriteBytes)/dt)
			r.io = rd + wr
		}
		rows = append(rows, r)
	}
	top := func(less func(a, b procRow) int, n int) []procRow {
		s := slices.Clone(rows)
		psSort(s, less)
		if len(s) > n {
			s = s[:n]
		}
		return s
	}

	// --- procesor
	cpuAvg, cpuMax := avg(cpuS), maxOf(cpuS)
	topCpu := top(func(a, b procRow) int { return cmpNum(b.cpu, a.cpu) }, 5)
	var topTxt, main []string
	for _, r := range topCpu {
		if r.cpu >= 1 {
			topTxt = append(topTxt, fmt.Sprintf("%s (%s%%)", r.name, fmtNum(r.cpu, 0)))
		}
		if r.cpu >= 5 && len(main) < 3 {
			main = append(main, r.name)
		}
	}
	cpuDetails := fmt.Sprintf("Próbkowanie: %d pomiarów w ciągu %s s; maksimum %s%%", len(cpuS), fmtNum(dt, 0), fmtNum(cpuMax, 0))
	if len(topTxt) > 0 {
		cpuDetails += "\nNajbardziej obciążające procesy: " + strings.Join(topTxt, ", ")
	}
	cpuVal := fmt.Sprintf("średnio %s%%", fmtNum(cpuAvg, 0))
	if len(topTxt) > 0 && cpuAvg >= 25 {
		cpuVal += "; głównie: " + strings.Join(main, ", ")
	}
	switch {
	case cpuAvg >= 90:
		c.Add(F{Title: "Obciążenie procesora", Status: Warn, Value: cpuVal, Details: cpuDetails,
			Advice: "Procesor jest niemal stale obciążony. Sprawdź wskazane procesy (Menedżer zadań > Szczegóły), skanowanie antywirusowe, aktualizacje w tle oraz możliwe złośliwe oprogramowanie (np. koparki kryptowalut)."})
	case cpuAvg >= 60:
		c.Add(F{Title: "Obciążenie procesora", Status: Info, Value: cpuVal, Details: cpuDetails})
	default:
		c.Add(F{Title: "Obciążenie procesora", Status: OK, Value: cpuVal, Details: cpuDetails})
	}

	// --- pamięć
	if m, ok := win.GetMemoryStatus(); ok && m.TotalPhys > 0 {
		totalKb, freeKb := float64(m.TotalPhys/1024), float64(m.AvailPhys/1024)
		usedPct := 100 * (totalKb - freeKb) / totalKb
		commitTotal, commitFree := float64(m.TotalPageFile/1024), float64(m.AvailPageFile/1024)
		commitPct := 0.0
		if commitTotal > 0 {
			commitPct = 100 * (commitTotal - commitFree) / commitTotal
		}
		var ramTxt []string
		for _, r := range top(func(a, b procRow) int { return cmpNum(b.ram, a.ram) }, 5) {
			ramTxt = append(ramTxt, fmt.Sprintf("%s (%s)", r.name, fmtSize(r.ram, 0)))
		}
		used, total := fmtSize((totalKb-freeKb)*1024, 1), fmtSize(totalKb*1024, 1)
		d := fmt.Sprintf("Zajęte: %s z %s; zatwierdzona pamięć wirtualna: %s%%\nNajwiększe procesy: %s", used, total, fmtNum(commitPct, 0), strings.Join(ramTxt, ", "))
		val := fmt.Sprintf("%s%% zajęte (%s z %s)", fmtNum(usedPct, 0), used, total)
		switch {
		case usedPct >= 95 || commitPct >= 95:
			c.Add(F{Title: "Zużycie pamięci RAM", Status: Crit, Value: val, Details: d,
				Advice: "Pamięć jest niemal całkowicie wykorzystana - system intensywnie korzysta z wolnego pliku wymiany. Zamknij zbędne programy, sprawdź wycieki pamięci w procesach i rozważ dołożenie RAM."})
		case usedPct >= 85 || commitPct >= 85:
			c.Add(F{Title: "Zużycie pamięci RAM", Status: Warn, Value: val, Details: d,
				Advice: "Wysokie zużycie pamięci. Sprawdź największe procesy i programy w autostarcie; rozważ dołożenie RAM."})
		default:
			c.Add(F{Title: "Zużycie pamięci RAM", Status: OK, Value: val, Details: d})
		}
	}

	// --- dysk
	if len(dskS) > 0 {
		dAvg, dMax, qAvg := avg(dskS), maxOf(dskS), avg(qS)
		var ioTxt, ioMain []string
		for _, r := range top(func(a, b procRow) int { return cmpNum(b.io, a.io) }, 5) {
			if r.io > 50*1024 {
				ioTxt = append(ioTxt, fmt.Sprintf("%s (%s/s)", r.name, fmtSize(r.io, 1)))
				if len(ioMain) < 3 {
					ioMain = append(ioMain, r.name)
				}
			}
		}
		d := fmt.Sprintf("Czas aktywności dysku: średnio %s%%, maks. %s%%; średnia kolejka: %s", fmtNum(dAvg, 0), fmtNum(dMax, 0), fmtNum(qAvg, 1))
		if len(ioTxt) > 0 {
			d += "\nNajwięcej operacji dyskowych: " + strings.Join(ioTxt, ", ")
		}
		val := "aktywność średnio " + fmtNum(dAvg, 0) + "%"
		if len(ioTxt) > 0 && dAvg >= 40 {
			val += "; głównie: " + strings.Join(ioMain, ", ")
		}
		switch {
		case dAvg >= 90:
			c.Add(F{Title: "Aktywność dysku", Status: Warn, Value: val, Details: d,
				Advice: "Dysk jest stale obciążony na ~100% - typowa przyczyna wolnego działania (szczególnie HDD). Sprawdź procesy z listy, indeksowanie, Windows Update, antywirus; zalecana wymiana HDD na SSD."})
		case dAvg >= 60:
			c.Add(F{Title: "Aktywność dysku", Status: Info, Value: val, Details: d})
		default:
			c.Add(F{Title: "Aktywność dysku", Status: OK, Value: val, Details: d})
		}
	}

	// --- pula nonpaged (wycieki w sterownikach)
	if pdh != nil {
		if np, ok := pdh.Get(iNp); ok && np > 0 {
			pg, _ := pdh.Get(iPaged)
			cb, _ := pdh.Get(iCache)
			d := fmt.Sprintf("Pula nonpaged: %s; pula paged: %s; pamięć podręczna: %s", fmtSize(np, 0), fmtSize(pg, 0), fmtSize(cb, 0))
			if np >= 1.5*(1<<30) {
				c.Add(F{Title: "Pula pamięci jądra (nonpaged)", Status: Warn, Value: fmtSize(np, 0), Details: d,
					Advice: "Bardzo duża pula nonpaged zwykle oznacza wyciek pamięci w sterowniku. Zaktualizuj sterowniki (sieć, dyski, antywirus, VPN); pomocny jest PoolMon."})
			} else {
				c.Add(F{Title: "Pula pamięci jądra (nonpaged)", Status: OK, Value: fmtSize(np, 0), Details: d})
			}
		}
	}

	// --- tabela procesów (ścieżki tylko dla pokazanych procesów)
	var tbl []Row
	for _, r := range top(func(a, b procRow) int { return cmpNum(b.cpu+b.ram/(1<<30), a.cpu+a.ram/(1<<30)) }, 20) {
		tbl = append(tbl, row("Proces", r.name, "PID", int64(r.pid), "CPU %", fmtNum(r.cpu, 1), "RAM", fmtSize(r.ram, 0), "Dysk /s", fmtSize(r.io, 1), "Ścieżka", win.ProcessPath(r.pid)))
	}
	c.AddTable(T{Name: "Najbardziej obciążające procesy", Rows: tbl})
	c.SetMeta("ProcessCount", int64(len(snap2)))
}

func checkBoot(c *Ctx) {
	days := 45
	if c.Full() {
		days = 90
	}
	since := time.Now().AddDate(0, 0, -days)
	const log = "Microsoft-Windows-Diagnostics-Performance/Operational"
	ev := c.events(win.EventFilter{Logs: []string{log}, IDs: []int{100}, Since: since}, 40, false)
	if len(ev) == 0 {
		c.Add(F{Title: "Czas uruchamiania systemu", Status: Skip, Value: "Brak danych o rozruchu w dzienniku Diagnostics-Performance (dziennik wyłączony lub wyczyszczony)."})
		return
	}
	type boot struct {
		t                 time.Time
		total, main, post float64
	}
	pf := func(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }
	var boots []boot
	for i, e := range ev {
		if i >= 12 {
			break
		}
		bt := pf(e.Data["BootTime"])
		if bt <= 0 {
			continue
		}
		boots = append(boots, boot{e.Time, bt / 1000, pf(e.Data["MainPathBootTime"]) / 1000, pf(e.Data["BootPostBootTime"]) / 1000})
	}
	if len(boots) == 0 {
		return
	}
	var mains, totals []float64
	var rows []Row
	for _, b := range boots {
		mains = append(mains, b.main)
		totals = append(totals, b.total)
		rows = append(rows, row("Data", fmtDt(b.t), "Do pulpitu (s)", fmtNum(b.main, 0), "Po starcie (s)", fmtNum(b.post, 0), "Razem (s)", fmtNum(b.total, 0)))
	}
	avgMain, avgTotal := avg(mains), avg(totals)
	c.AddTable(T{Name: "Czasy uruchamiania systemu", Rows: rows})
	val := fmt.Sprintf("średnio %s s do pulpitu (ostatnio %s s); %s s do pełnej responsywności", fmtNum(avgMain, 0), fmtNum(boots[0].main, 0), fmtNum(avgTotal, 0))
	d := fmt.Sprintf("Analizowane rozruchy: %d (ostatnie %d dni)", len(boots), days)
	switch {
	case avgMain >= 120:
		c.Add(F{Title: "Czas uruchamiania systemu", Status: Warn, Value: val, Details: d,
			Advice: "Bardzo wolny start. Zmniejsz liczbę programów w autostarcie, sprawdź elementy spowalniające start (poniżej), a przy dysku HDD wymień go na SSD."})
	case avgMain >= 60:
		c.Add(F{Title: "Czas uruchamiania systemu", Status: Info, Value: val, Details: d,
			Advice: "Start jest dość powolny - warto przejrzeć autostart; SSD skraca go zwykle kilkukrotnie."})
	default:
		c.Add(F{Title: "Czas uruchamiania systemu", Status: OK, Value: val, Details: d})
	}

	// elementy spowalniające uruchamianie (zdarzenia 101-110)
	deg := c.events(win.EventFilter{Logs: []string{log}, IDs: []int{101, 102, 103, 106, 109}, Since: since}, 300, false)
	if len(deg) == 0 {
		return
	}
	kind := map[int]string{101: "Aplikacja", 102: "Sterownik", 103: "Usługa", 106: "Urządzenie", 109: "Zadanie logowania"}
	type agg struct {
		typ, name   string
		count       int
		sum, maxTot float64
	}
	byKey := map[string]*agg{}
	var order []*agg
	for _, e := range deg {
		n := e.Data["FriendlyName"]
		if n == "" {
			n = e.Data["Name"]
		}
		if n == "" {
			n = e.Data["FileName"]
		}
		if n == "" {
			continue
		}
		k := strings.ToLower(fmt.Sprintf("%d|%s", e.ID, n))
		a := byKey[k]
		if a == nil {
			a = &agg{typ: kind[e.ID], name: n}
			byKey[k] = a
			order = append(order, a)
		}
		a.count++
		a.sum += pf(e.Data["DegradationTime"])
		if t := pf(e.Data["TotalTime"]); t > a.maxTot {
			a.maxTot = t
		}
	}
	psSort(order, func(a, b *agg) int { return cmpNum(b.sum, a.sum) })
	if len(order) > 12 {
		order = order[:12]
	}
	if len(order) == 0 {
		return
	}
	var trows []Row
	var names []string
	for i, a := range order {
		trows = append(trows, row("Typ", a.typ, "Nazwa", a.name, "Liczba rozruchów", int64(a.count), "Łączne opóźnienie (s)", fmtNum(a.sum/1000, 0), "Najdłuższy czas (s)", fmtNum(a.maxTot/1000, 0)))
		if i < 3 {
			names = append(names, a.name)
		}
	}
	c.AddTable(T{Name: "Elementy spowalniające uruchamianie", Rows: trows})
	c.Add(F{Title: "Elementy spowalniające start", Status: Info, Value: "Najbardziej opóźniają start: " + strings.Join(names, ", "),
		Details: "Pełna lista w tabeli „Elementy spowalniające uruchamianie”.",
		Advice:  "Rozważ wyłączenie niepotrzebnych programów z autostartu lub aktualizację/usunięcie wymienionych sterowników i usług."})
}
