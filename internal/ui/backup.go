package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Kopia przed reinstalacją Windows (Show-SdUiBackup): wybór elementów i folderu, kopia w tle z postępem, potem
// wynik każdego elementu i „Otwórz folder kopii”.

const (
	wmBkBg  = wmApp + 22 // wynik kopii w tle
	timerBk = 9

	ctlBkPick = 761 + iota
	ctlBkDest
	ctlBkOpen
	ctlBkRun
	ctlBkClose
)

type backupDialog struct {
	mw      *mainWin
	d       *dialog
	list    *doc
	edit    uintptr
	btns    map[int]uintptr
	isAdmin bool
	checked map[string]bool

	busy   bool
	anim   int
	status string
	result *diag.BackupResult

	mu     sync.Mutex
	bgText string
	bgDone func()
}

var bkLabels = map[string][2]any{"ok": {"OK", hexc("#067647")}, "warn": {"UWAGA", hexc("#B54708")}, "skip": {"POMINIĘTO", cGray}, "error": {"BŁĄD", hexc("#B42318")}}

func (mw *mainWin) showBackup() {
	bd := &backupDialog{mw: mw, btns: map[int]uintptr{}, checked: map[string]bool{}, isAdmin: win.IsAdmin()}
	for _, p := range diag.BackupParts {
		bd.checked[p.Key] = p.Default && (!p.Admin || bd.isAdmin)
	}
	d := newDialog(mw.h, "Kopia przed reinstalacją Windows", 780, 650, true)
	d.minW, d.minH = 600, 460
	bd.d = d
	bd.list = newDoc(d.h, cWhite, bd.layoutList)
	bd.list.dpi = d.dpi
	bd.list.onClick = bd.toggle
	bd.edit = newEdit(d.h, ctlBkDest, diag.ResolveReportDir(mw.opt.Root, mw.settings), 0)
	for id, t := range map[int]string{ctlBkPick: "Wybierz...", ctlBkOpen: "Otwórz folder kopii", ctlBkRun: "Utwórz kopię", ctlBkClose: "Zamknij"} {
		bd.btns[id] = newButton(d.h, id, t)
	}
	showWin(bd.btns[ctlBkOpen], false)
	fonts := func() {
		f := d.font(130, fwNormal)
		setFont(bd.edit, f)
		setEditMargins(bd.edit, d.px(4))
		for _, b := range bd.btns {
			setFont(b, f)
		}
		bd.list.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = bd.paint
	d.onSize = func(w, h int) { bd.place(w, h) }
	d.onCmd = bd.command
	d.canClose = func() bool {
		if bd.busy {
			msgBox(d.h, "Trwa tworzenie kopii - poczekaj na zakończenie.", "Kopia", mbIconInfo)
			return false
		}
		return true
	}
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		switch m {
		case wmTimer:
			if wp == timerBk {
				bd.tick()
				return 0, true
			}
		case wmBkBg:
			bd.mu.Lock()
			f := bd.bgDone
			bd.bgDone = nil
			bd.mu.Unlock()
			if f != nil {
				f()
			}
			return 0, true
		}
		return 0, false
	}
	call(pSetTimer, d.h, timerBk, 300, 0)
	d.show()
	call(pKillTimer, d.h, timerBk)
}

func (bd *backupDialog) command(id, code int) {
	switch id {
	case ctlBkPick:
		if p := pickPath(bd.d.h, "Folder na kopię (najlepiej pendrive albo dysk inny niż systemowy)", "", "", true, getText(bd.edit)); p != "" {
			setText(bd.edit, p)
		}
	case ctlBkOpen:
		if bd.result != nil && bd.result.Dir != "" {
			shellOpen("explorer.exe", `"`+bd.result.Dir+`"`)
		}
	case ctlBkRun:
		if !bd.busy {
			bd.run()
		}
	case ctlBkClose:
		if bd.d.canClose() {
			bd.d.close()
		}
	}
}

func (bd *backupDialog) toggle(id int) {
	i := id - 1000
	if bd.busy || bd.result != nil || i < 0 || i >= len(diag.BackupParts) {
		return
	}
	p := diag.BackupParts[i]
	if p.Admin && !bd.isAdmin {
		return
	}
	bd.checked[p.Key] = !bd.checked[p.Key]
	bd.list.relayout()
}

// run: sprawdzenia i pytania jak w wersji PowerShell, potem kopia w tle.
func (bd *backupDialog) run() {
	var inc []string
	for _, p := range diag.BackupParts {
		if bd.checked[p.Key] && (!p.Admin || bd.isAdmin) {
			inc = append(inc, p.Key)
		}
	}
	dest := strings.TrimSpace(getText(bd.edit))
	if len(inc) == 0 {
		msgBox(bd.d.h, "Zaznacz, co ma znaleźć się w kopii.", "Kopia", mbIconInfo)
		return
	}
	if dest == "" {
		msgBox(bd.d.h, "Wskaż folder docelowy kopii.", "Kopia", mbIconInfo)
		return
	}
	var q []string
	if root, sd := filepath.VolumeName(dest), os.Getenv("SystemDrive"); root != "" && strings.EqualFold(root, sd) {
		q = append(q, "Folder jest na dysku systemowym - przy reinstalacji zostanie usunięty. Po utworzeniu kopii przenieś ją na pendrive.")
	}
	wifi, bl := slices.Contains(inc, "wifi"), slices.Contains(inc, "bitlocker")
	if wifi || bl {
		s := "Kopia będzie zawierać "
		if wifi {
			s += "hasła Wi-Fi"
		}
		if wifi && bl {
			s += " i "
		}
		if bl {
			s += "klucze odzyskiwania BitLocker"
		}
		q = append(q, s+" zapisane jawnym tekstem.")
	}
	if slices.Contains(inc, "drivers") {
		// za długa ścieżka przerwałaby eksport sterowników w połowie - lepiej od razu wybrać krótszy folder
		var lc diag.DriverExportCheck
		withWaitCursor(func() {
			lc = diag.TestDriverExport(filepath.Join(dest, diag.BackupDirName(time.Now()), "sterowniki"), nil, -1)
		})
		if !lc.Ok {
			msgBox(bd.d.h, "Sterowniki nie zmieszczą się w tym folderze - "+lc.Message, "Kopia", mbIconWarning)
			return
		}
	}
	if len(q) > 0 && msgBox(bd.d.h, strings.Join(q, "\n\n")+"\n\nKontynuować?", "Kopia", mbIconQuestion|mbYesNo) != idYes {
		return
	}
	bd.busy = true
	enableWin(bd.btns[ctlBkRun], false)
	enableWin(bd.btns[ctlBkClose], false)
	bd.setStatus("Tworzenie kopii...")
	bd.list.relayout()
	h := bd.d.h
	go func() {
		var res diag.BackupResult
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					logError(fmt.Sprintf("kopia: %v", p))
					err = fmt.Errorf("%v", p)
				}
			}()
			res, err = diag.RunBackup(dest, inc, func(t string) {
				bd.mu.Lock()
				bd.bgText = t
				bd.mu.Unlock()
			})
		}()
		bd.mu.Lock()
		bd.bgDone = func() { bd.finished(res, err) }
		bd.mu.Unlock()
		postMsg(h, wmBkBg, 0, 0)
	}()
}

func (bd *backupDialog) finished(res diag.BackupResult, err error) {
	bd.busy = false
	enableWin(bd.btns[ctlBkClose], true)
	if err != nil || res.Dir == "" {
		msg := "brak wyniku"
		if err != nil {
			msg = err.Error()
		}
		enableWin(bd.btns[ctlBkRun], true)
		bd.setStatus("Nie udało się utworzyć kopii: " + msg)
		bd.list.relayout()
		return
	}
	bd.result = &res
	bd.list.scroll = 0
	bd.list.relayout()
	showWin(bd.btns[ctlBkOpen], true)
	bd.setStatus("Kopię zapisano w: " + res.Dir + "\nInstrukcja przywracania jest w pliku CZYTAJ.txt w tym folderze.")
	bd.mw.setFooter("Kopia przed reinstalacją: " + res.Dir)
}

func (bd *backupDialog) tick() {
	if !bd.busy {
		return
	}
	bd.anim++
	bd.mu.Lock()
	t := bd.bgText
	bd.mu.Unlock()
	if t != "" {
		bd.status = t + " Nie zamykaj okna."
	}
	cr := clientRect(bd.d.h)
	bd.place(cr.w(), cr.h())
}

func (bd *backupDialog) setStatus(s string) {
	bd.status = s
	cr := clientRect(bd.d.h)
	bd.place(cr.w(), cr.h())
}

func (bd *backupDialog) header() (title, desc textBlock) {
	d := bd.d
	w := clientRect(d.h).w() - 2*d.px(20)
	title = layoutText([]span{{text: "Kopia przed reinstalacją Windows", font: d.font(160, fwSemiBold), color: cText}}, w, 0)
	desc = layoutText([]span{{text: "Zapisuje w jednym folderze to, co zwykle ginie przy reinstalacji systemu. Program niczego nie zmienia na komputerze. Folder wybierz na pendrive albo dysku innym niż systemowy. Plików użytkownika (dokumenty, zdjęcia) program nie kopiuje.", font: d.font(130, fwNormal), color: cGray}}, w, 0)
	return
}

func (bd *backupDialog) statusBlock(w int) textBlock {
	if bd.status == "" {
		return textBlock{}
	}
	var spans []span
	for i, l := range strings.Split(bd.status, "\n") {
		spans = append(spans, span{text: l, font: bd.d.font(130, fwNormal), color: cGray2, br: i > 0})
	}
	return layoutText(spans, w, 0)
}

// place: układ jak w XAML - lista elementów, pod nią folder docelowy, komunikat i pasek postępu, przyciski na dole.
func (bd *backupDialog) place(w, h int) {
	d := bd.d
	if w <= 0 || h <= 0 {
		return
	}
	p := d.px(20)
	f := d.font(130, fwNormal)
	bh := d.px(32)
	by := h - p - bh
	moveWin(bd.btns[ctlBkOpen], p, by, buttonWidth(f, getText(bd.btns[ctlBkOpen]), d.px(12)), bh)
	rx := w - p - d.px(100)
	moveWin(bd.btns[ctlBkClose], rx, by, d.px(100), bh)
	rw := buttonWidth(f, getText(bd.btns[ctlBkRun]), d.px(14))
	moveWin(bd.btns[ctlBkRun], rx-d.px(8)-rw, by, rw, bh)
	st := bd.statusBlock(w - 2*p)
	progH := 0
	if bd.busy {
		progH = d.px(12)
	}
	// folder docelowy: opis, pole, „Wybierz...”
	destY := by - d.px(12) - progH - st.h - d.px(8) - d.px(30)
	if st.h == 0 {
		destY += d.px(8)
	}
	lw := strWidth(f, "Folder docelowy") + d.px(10)
	pw := buttonWidth(f, "Wybierz...", d.px(10))
	moveWin(bd.edit, p+lw, destY, w-2*p-lw-pw-d.px(8), d.px(30))
	moveWin(bd.btns[ctlBkPick], w-p-pw, destY, pw, d.px(30))
	t, ds := bd.header()
	top := p + t.h + d.px(4) + ds.h + d.px(10)
	moveWin(bd.list.h, p+1, top+1, w-2*p-2, max(destY-d.px(10)-top-2, d.px(40)))
	invalidate(d.h)
}

func (bd *backupDialog) paint(cv *canvas, w, h int) {
	d := bd.d
	p := d.px(20)
	f := d.font(130, fwNormal)
	t, ds := bd.header()
	cv.block(t, p, p)
	cv.block(ds, p, p+t.h+d.px(4))
	lr := windowRectIn(bd.list.h, d.h)
	cv.card(int(lr.Left)-1, int(lr.Top)-1, int(lr.Right)+1, int(lr.Bottom)+1, float64(d.px(8)), cWhite, cBorder, 1)
	er := windowRectIn(bd.edit, d.h)
	cv.text(f, "Folder docelowy", p, int(er.Top)+(er.h()-f.height)/2, cText)
	st := bd.statusBlock(w - 2*p)
	sy := int(er.Bottom) + d.px(8)
	cv.block(st, p, sy)
	if bd.busy {
		drawProgress(cv, p, sy+st.h+d.px(6), w-2*p, d.px(6), -1, bd.anim)
	}
}

func (bd *backupDialog) layoutList(dc *doc, width int) int {
	x := dc.px(14)
	w := width - 2*x
	y := dc.px(10)
	f, fb, small := dc.font(130, fwNormal), dc.font(130, fwSemiBold), dc.font(120, fwNormal)
	if r := bd.result; r != nil {
		tb := layoutText([]span{{text: "Wynik kopii", font: dc.font(140, fwSemiBold), color: cBlue}}, w, 0)
		dc.block(tb, x, y)
		y += tb.h + dc.px(4)
		for _, p := range r.Parts {
			lb, ok := bkLabels[p.Result]
			if !ok {
				lb = [2]any{p.Result, cGray2}
			}
			tb := layoutText([]span{{text: "[" + lb[0].(string) + "] ", font: fb, color: lb[1].(col)}, {text: p.Label, font: fb, color: cText},
				{text: p.Message, font: f, color: cGray2, br: true}}, w, 0)
			y += dc.px(4)
			dc.block(tb, x, y)
			y += tb.h + dc.px(4)
		}
		for _, wn := range r.Warnings {
			tb := layoutText([]span{{text: "Uwaga: ", font: fb, color: hexc("#B54708")}, {text: wn, font: f, color: hexc("#7A2E0E")}}, w, 0)
			y += dc.px(8)
			dc.block(tb, x, y)
			y += tb.h + dc.px(2)
		}
		return y + dc.px(10)
	}
	cb := dc.px(16)
	for i, p := range diag.BackupParts {
		blocked := p.Admin && !bd.isAdmin
		spans := []span{{text: p.Label, font: fb, color: cText}, {text: p.Description, font: small, color: cGray, br: true}}
		if blocked {
			spans = append(spans, span{text: "Wymaga uprawnień administratora - użyj „Uruchom jako administrator” w oknie programu.", font: small, color: hexc("#B42318"), br: true})
		}
		tb := layoutText(spans, w-cb-dc.px(10)-dc.px(8), 0)
		y += dc.px(4)
		row := dc.rect(x-dc.px(6), y-dc.px(2), w+dc.px(12), tb.h+dc.px(4), none, none, 0, float64(dc.px(6)))
		if !blocked && !bd.busy {
			row.id, row.hoverFill = 1000+i, cHover
		}
		dc.check(x, y+(fb.height-cb)/2, cb, bd.checked[p.Key], blocked || bd.busy)
		dc.block(tb, x+cb+dc.px(10), y)
		y += tb.h + dc.px(8)
	}
	return y + dc.px(10)
}
