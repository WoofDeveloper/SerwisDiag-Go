package ui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"serwisdiag/internal/diag"
)

// Diagnostyka z okna (Start-SdUiScan, Complete-SdUiScan): testy w gorutynach, okno co 300 ms odczytuje postęp
// i odświeża widoki, gdy przybyło wyników. Raporty i PDF zapisują się w tle.

type scanRun struct {
	id        int
	scan      *diag.Scan
	mu        sync.Mutex
	i, n      int
	title     string
	pdfPhase  bool
	done      *scanDone
	completed bool
	abandoned atomic.Bool // okno zakończyło skanowanie bez czekania na test, który nie reagował na przerwanie
}

type scanDone struct {
	res    *diag.Result
	dir    string
	paths  diag.ReportPaths
	err    error
	pdf    string
	pdfErr string
}

var scanSeq int

func (mw *mainWin) startScan() {
	if mw.scanning {
		return
	}
	level := "Quick"
	if comboSel(mw.cmbLevel) == 1 {
		level = "Full"
	}
	var tests []string
	for _, t := range []struct {
		h  uintptr
		id string
	}{{mw.chkDisk, "tt.disk"}, {mw.chkCpu, "tt.cpu"}, {mw.chkNet, "tt.net"}, {mw.chkVol, "tt.volscan"}, {mw.chkDism, "tt.dism"}} {
		if isChecked(t.h) {
			tests = append(tests, t.id)
		}
	}
	if slices.Contains(tests, "tt.cpu") && !mw.opt.SelfTest {
		if msgBox(mw.h, "Test obciążeniowy CPU będzie przez ok. 45 sekund obciążał wszystkie rdzenie - wentylatory mogą mocno przyspieszyć. Kontynuować?",
			"Test obciążeniowy", mbIconQuestion|mbYesNo) != idYes {
			return
		}
	}
	scanSeq++
	run := &scanRun{id: scanSeq}
	run.scan = diag.NewScan(diag.Options{Root: mw.opt.Root, Level: level, Tests: tests, Offline: isChecked(mw.chkOffline), Parallel: true,
		OnProgress: func(i, n int, ch *diag.Check) {
			run.mu.Lock()
			if i >= 0 {
				run.i, run.n = i, n
			}
			run.title = ch.Title
			run.mu.Unlock()
		}})
	s := run.scan
	s.SetMeta("Ticket", strings.TrimSpace(getText(mw.edTicket)))
	s.SetMeta("Customer", strings.TrimSpace(getText(mw.edCustomer)))
	s.SetMeta("DeviceNote", strings.TrimSpace(getText(mw.edNote)))
	s.SetMeta("Technician", mw.settings.Technician)
	if mw.opt.Integrity != nil {
		s.SetMeta("Integrity", mw.opt.Integrity())
	}
	mw.run = run
	mw.result, mw.paths, mw.pdfPath = nil, nil, ""
	mw.shownCount, mw.scanning, mw.cancelReq = -1, true, false
	enableWin(mw.btnStart, false)
	enableWin(mw.btnCancel, true)
	mw.setResultButtons(false)
	mw.progVisible, mw.progVal, mw.progMax, mw.progText = true, 0, 100, "Przygotowanie..."
	mw.footerText = ""
	mw.layout()
	mw.refreshAll()
	mw.redraw()

	settings, root, h, selfTest := mw.settings, mw.opt.Root, mw.h, mw.opt.SelfTest
	go func() {
		defer func() {
			if p := recover(); p != nil {
				logError(fmt.Sprintf("skanowanie: %v\n%s", p, debug.Stack()))
				run.mu.Lock()
				run.done = &scanDone{res: s.Result(), err: fmt.Errorf("%v", p)}
				run.mu.Unlock()
				postMsg(h, wmScanDone, uintptr(run.id), 0)
			}
		}()
		s.Run()
		if run.abandoned.Load() {
			return
		}
		d := saveScan(s.Result(), settings, root, run, selfTest)
		run.mu.Lock()
		run.done = d
		run.mu.Unlock()
		postMsg(h, wmScanDone, uintptr(run.id), 0)
	}()
	call(pSetTimer, mw.h, timerScan, 300, 0)
}

// saveScan zapisuje raporty (i PDF, gdy włączony w ustawieniach) - w gorutynie skanowania.
func saveScan(res *diag.Result, settings diag.Settings, root string, run *scanRun, selfTest bool) *scanDone {
	d := &scanDone{res: res, dir: diag.ResolveReportDir(root, settings)}
	d.paths, d.err = diag.SaveReports(res, settings, d.dir)
	if d.err == nil && settings.AutoPdf && !selfTest {
		run.mu.Lock()
		run.title, run.pdfPhase = "Zapisywanie PDF dla klienta...", true
		run.mu.Unlock()
		if p, err := diag.SavePDF(d.paths.Customer); err == nil {
			d.pdf = p
		} else {
			d.pdfErr = err.Error()
		}
	}
	return d
}

func (mw *mainWin) cancelScan() {
	if !mw.scanning || mw.run == nil {
		return
	}
	mw.cancelReq, mw.cancelAt = true, time.Now()
	mw.run.scan.Cancel()
	enableWin(mw.btnCancel, false)
	mw.progText = "Przerywanie po zakończeniu bieżącego testu..."
	mw.layout()
	mw.redraw()
}

// tick: postęp i wyniki na żywo (zegar okna co 300 ms).
func (mw *mainWin) tick() {
	if !mw.scanning || mw.run == nil {
		return
	}
	r := mw.run
	r.mu.Lock()
	i, n, t, pdf := r.i, r.n, r.title, r.pdfPhase
	r.mu.Unlock()
	switch {
	case pdf:
		mw.progVal, mw.progText = mw.progMax, t
	case n > 0:
		mw.progMax, mw.progVal = n, max(i-1, 0)
		if !mw.cancelReq {
			mw.progText = fmt.Sprintf("[%d/%d]  %s", i, n, t)
		}
	}
	if c := r.scan.FindingsCount(); c != mw.shownCount {
		mw.shownCount = c
		mw.refreshAll()
	}
	if mw.cancelReq && !mw.cancelAt.IsZero() && time.Since(mw.cancelAt) > 8*time.Second {
		// test nie reaguje na przerwanie (np. długie zapytanie) - kończymy z wynikami częściowymi;
		// gorutyna dokończy test w tle, a jej wynik zostanie pominięty
		mw.cancelAt = time.Time{}
		r.abandoned.Store(true)
		res := r.scan.Result()
		mw.complete(saveScan(res, mw.settings, mw.opt.Root, r, true))
		return
	}
	mw.layout()
	mw.redraw()
}

func (mw *mainWin) scanDone(id int) {
	r := mw.run
	if r == nil || r.id != id || r.completed {
		return
	}
	r.mu.Lock()
	d := r.done
	r.mu.Unlock()
	if d != nil {
		mw.complete(d)
	}
}

func (mw *mainWin) complete(d *scanDone) {
	call(pKillTimer, mw.h, timerScan)
	if mw.run != nil {
		mw.run.completed = true
	}
	mw.scanning = false
	mw.progVal = mw.progMax
	enableWin(mw.btnStart, true)
	enableWin(mw.btnCancel, false)
	if mw.cancelReq {
		mw.progText = "Diagnostykę przerwano - zapisano wyniki częściowe."
	} else {
		mw.progText = "Diagnostyka zakończona."
	}
	mw.result = d.res
	if d.err != nil {
		mw.footerText = "Nie udało się zapisać raportów: " + d.err.Error()
	} else {
		p := d.paths
		mw.paths = &p
		mw.setResultButtons(true)
		mw.footerText = "Raporty zapisano w: " + d.dir
		if d.pdf != "" {
			mw.pdfPath = d.pdf
		} else if d.pdfErr != "" {
			mw.footerText += " (PDF: " + d.pdfErr + ")"
		}
	}
	mw.layout()
	mw.refreshAll()
	mw.redraw()
	if d.res != nil && len(d.res.Findings) == 0 {
		tail := d.res.Log
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		msgBox(mw.h, "Skanowanie nie zwróciło żadnych wyników.\n\n"+strings.Join(tail, "\n"), "SerwisDiag", mbIconWarning)
	}
	open := mw.settings.OpenAfterScan
	if (open == "customer" || open == "technical") && mw.paths != nil && !mw.opt.SelfTest && !mw.cancelReq {
		if open == "technical" {
			openPath(mw.paths.Technical)
		} else {
			openPath(mw.paths.Customer)
		}
	}
	releaseMemory("okno po diagnostyce")
	if mw.self != nil {
		mw.self.scanDone = true
		call(pSetTimer, mw.h, timerSelfTest, 800, 0)
	}
}

// ---------------------------------------------------------------- autotest okna (-SelfTest -ScreenshotPath)

type selfTest struct {
	prefix   string
	steps    []string
	index    int
	shot     bool
	scanDone bool
	inDialog bool // otwarte okno dialogowe (pętla modalna) - kolejne kroki robią zrzut i je zamykają
	phase    int
}

// lastDialog: ostatnio otwarte okno dialogowe (autotest robi jego zrzut).
var lastDialog *dialog

// selfTestFixResults: przykładowe wyniki napraw do zrzutu okna napraw (autotest niczego nie naprawia).
var selfTestFixResults = []diag.FixResult{
	{Title: "Wyczyść pamięć podręczną DNS", FixID: "fx.dns", Result: "ok", Message: "Pamięć podręczna DNS wyczyszczona."},
	{Title: "Usuń pliki tymczasowe", FixID: "fx.cleanup", Result: "ok", Message: "Zwolniono 2,4 GB."},
	{Title: "Włącz ochronę Microsoft Defender", FixID: "fx.defender", Result: "warn", Message: "Ochrona włączona, ale definicje są nieaktualne."},
	{Title: "Napraw repozytorium WMI", FixID: "fx.wmi", Result: "error", Message: "winmgmt zakończył się kodem 5: Odmowa dostępu."},
	{Title: "Włącz zapisywanie zrzutów pamięci po awarii", FixID: "fx.dumps", Result: "skip", Message: "Pominięto: wymaga uprawnień administratora"},
}

func (mw *mainWin) startSelfTest() {
	prefix := mw.opt.Screenshot
	if prefix == "" {
		prefix = os.TempDir() + `\sd-go-selftest`
	}
	quietMessages = true
	steps := []string{"summary", "storage", "events", "security", "tools"}
	if os.Getenv("SERWISDIAG_SELFTEST_DIALOGS") != "" {
		// także okna dialogowe (poza pomiarami); okno sterowników naprawdę wyszukuje w Windows Update (bez instalacji)
		steps = append(steps, "about", "settings", "keyboard", "fixes", "undo", "compare", "drivers", "missing", "backup")
	}
	mw.self = &selfTest{prefix: prefix, steps: steps}
	if os.Getenv("SERWISDIAG_SELFTEST_UPDATE") != "" {
		// wygląd paska „Dostępna nowa wersja” (autotest nie łączy się z GitHubem)
		mw.showUpdateBanner(diag.Release{Ok: true, Newer: true, Latest: "9.9.9", Notes: "Przykładowa lista zmian.", URL: "https://github.com/"})
	}
	if ms, err := strconv.Atoi(os.Getenv("SERWISDIAG_SELFTEST_CANCEL")); err == nil && ms > 0 {
		// autotest przerwania: „Przerwij” po ms milisekundach od startu diagnostyki
		go func() {
			time.Sleep(time.Duration(ms) * time.Millisecond)
			postMsg(mw.h, wmCommand, ctlCancel, 0)
		}()
	}
	if getText(mw.edTicket) == "" {
		setText(mw.edTicket, "SELFTEST")
	}
	if getText(mw.edCustomer) == "" {
		setText(mw.edCustomer, "Test automatyczny")
	}
	postMsg(mw.h, wmCommand, ctlStart, 0)
}

// selfTestStep: na zmianę - wybór strony i (800 ms później) zrzut okna; po ostatniej stronie zamknięcie.
func (mw *mainWin) selfTestStep() {
	st := mw.self
	if st.index >= len(st.steps) {
		call(pKillTimer, mw.h, timerSelfTest)
		postMsg(mw.h, wmClose, 0, 0)
		return
	}
	key := st.steps[st.index]
	if st.inDialog {
		d := lastDialog
		if d == nil || call(pIsWindow, d.h) == 0 {
			return
		}
		if key == "keyboard" && st.phase == 0 {
			// naciśnięcia: A, lewy Shift, spacja, F5, strzałka w górę
			for _, k := range [][2]uintptr{{'A', 0x1E << 16}, {vkShift, 0x2A << 16}, {vkSpace, 0x39 << 16}, {0x74, 0x3F << 16}, {vkUp, 0x48<<16 | 1<<24}} {
				postMsg(d.h, wmKeyDown, k[0], k[1])
			}
			st.phase = 1
			return
		}
		if key == "fixes" {
			// okno napraw: zrzut po wykrywaniu (tylko odczyt), potem widok wyniku z wynikami przykładowymi - bez napraw
			fd := lastFixDialog
			if fd == nil || fd.busy {
				return
			}
			if st.phase == 0 {
				if err := saveWindowPNG(d.h, st.prefix+"-fixes.png"); err != nil {
					logError("zrzut fixes: " + err.Error())
				}
				fd.showResults(selfTestFixResults)
				st.phase = 1
				return
			}
			key = "fixes-wynik"
		}
		if key == "drivers" {
			if dd := lastDrvDialog; dd == nil || dd.busy {
				return // zrzut po zakończeniu wyszukiwania
			}
		}
		if (key == "undo" || key == "compare" || key == "drivers" || key == "missing" || key == "backup") && st.phase == 0 {
			st.phase = 1 // jeszcze jeden takt zegara przed zrzutem
			return
		}
		if err := saveWindowPNG(d.h, st.prefix+"-"+key+".png"); err != nil {
			logError("zrzut " + key + ": " + err.Error())
		}
		postMsg(d.h, wmClose, 0, 0)
		return
	}
	switch key {
	case "about", "settings", "keyboard", "fixes", "undo", "compare", "drivers", "missing", "backup":
		st.inDialog, st.phase = true, 0
		switch key {
		case "about":
			mw.showAbout()
		case "backup":
			mw.showBackup()
		case "compare":
			mw.showCompare()
		case "drivers":
			mw.showDrivers()
		case "missing":
			mw.showMissingDrivers()
		case "settings":
			mw.showSettings()
		case "fixes":
			mw.showFixes()
		case "undo":
			mw.showUndo(os.Getenv("SERWISDIAG_SELFTEST_UNDO")) // dziennik napraw do pokazania (np. testowy)
		default:
			showKeyboardTest(mw.h)
		}
		st.inDialog = false
		st.index++
		return
	}
	if !st.shot {
		mw.selectPage(key)
		if key == "events" && len(mw.catFind) > 0 {
			mw.lvFind.selectRow(0) // zrzut ze szczegółami wybranego wyniku
		}
		st.shot = true
		return
	}
	if err := saveWindowPNG(mw.h, st.prefix+"-"+key+".png"); err != nil {
		logError("zrzut " + key + ": " + err.Error())
	}
	st.index++
	st.shot = false
}

// saveWindowPNG: zrzut okna (także poza ekranem) do PNG.
func saveWindowPNG(h uintptr, path string) error {
	wr := windowRect(h)
	w, ht := wr.w(), wr.h()
	cv := newCanvas(w, ht)
	if cv == nil {
		return fmt.Errorf("brak pamięci na zrzut")
	}
	defer cv.free()
	call(pPrintWindow, h, cv.dc, pwRenderFullContent)
	cv.flush()
	img := image.NewNRGBA(image.Rect(0, 0, w, ht))
	for y := 0; y < ht; y++ {
		for x := 0; x < w; x++ {
			p := cv.pix[y*cv.w+x]
			i := (y*w + x) * 4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(p>>16), uint8(p>>8), uint8(p), 255
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
