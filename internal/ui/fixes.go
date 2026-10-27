package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Naprawa problemów po diagnostyce (Show-SdUiFixes) i cofanie napraw z dziennika (Show-SdUiUndo). Wykrywanie,
// punkt przywracania i naprawy działają w tle; okno co 300 ms pokazuje postęp, a po zakończeniu - wynik.

const (
	wmFixBg  = wmApp + 20 // wynik pracy w tle okna napraw
	timerFix = 7

	ctlFixRestore = 701 + iota
	ctlFixRec
	ctlFixNone
	ctlFixRun
	ctlFixRescan
	ctlFixClose
)

type fixDialog struct {
	mw   *mainWin
	d    *dialog
	list *doc
	chk  uintptr
	btns map[int]uintptr

	phase    string // detect | restore | fix | done
	busy     bool
	anim     int
	status   string
	progress float64 // 0..1; < 0 - pasek bez wartości (wykrywanie, punkt przywracania)

	cands   []diag.FixCandidate
	unfixed []diag.Finding
	checked map[string]bool
	keys    []string
	done    []string // wykonane wcześniej (włączenie ochrony systemu przed punktem przywracania)
	pre     []diag.FixResult
	results []diag.FixResult
	rescan  bool

	mu     sync.Mutex
	bgI    int
	bgN    int
	bgText string
	bgDone func() // wynik pracy w tle - wykonywany w wątku okien
}

// lastFixDialog: otwarte okno napraw (autotest czeka na koniec wykrywania).
var lastFixDialog *fixDialog

var fixLabels = map[string][2]any{"ok": {"OK", hexc("#067647")}, "warn": {"UWAGA", hexc("#B54708")}, "error": {"BŁĄD", hexc("#B42318")}, "skip": {"POMINIĘTO", cGray}}

func (mw *mainWin) showFixes() {
	if mw.result == nil {
		msgBox(mw.h, "Najpierw wykonaj diagnostykę - naprawy dobierane są do jej wyników.", "Naprawa", mbIconInfo)
		return
	}
	fd := &fixDialog{mw: mw, checked: map[string]bool{}, btns: map[int]uintptr{}, progress: -1}
	lastFixDialog = fd
	defer func() { lastFixDialog = nil }()
	d := newDialog(mw.h, "Naprawa problemów", 880, 680, true)
	d.minW, d.minH = 680, 480
	fd.d = d
	fd.list = newDoc(d.h, cWhite, fd.layoutList)
	fd.list.dpi = d.dpi
	fd.list.onClick = fd.toggle
	fd.chk = newCheck(d.h, ctlFixRestore, "Przed naprawą utwórz punkt przywracania systemu (zalecane)")
	setChecked(fd.chk, true)
	for id, t := range map[int]string{ctlFixRec: "Zaznacz zalecane", ctlFixNone: "Odznacz wszystkie", ctlFixRun: "Napraw zaznaczone", ctlFixRescan: "Diagnostyka ponownie", ctlFixClose: "Zamknij"} {
		fd.btns[id] = newButton(d.h, id, t)
	}
	showWin(fd.btns[ctlFixRescan], false)
	fd.enableChoice(false)
	fonts := func() {
		f := d.font(130, fwNormal)
		setFont(fd.chk, f)
		for _, b := range fd.btns {
			setFont(b, f)
		}
		fd.list.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = fd.paint
	d.onSize = func(w, h int) { fd.place(w, h) }
	d.onCmd = fd.command
	d.canClose = func() bool {
		if fd.busy && fd.phase != "detect" {
			msgBox(d.h, "Trwa naprawa - poczekaj na jej zakończenie.", "Naprawa", mbIconInfo)
			return false
		}
		return true
	}
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		switch m {
		case wmTimer:
			if wp == timerFix {
				fd.tick()
				return 0, true
			}
		case wmFixBg:
			fd.mu.Lock()
			f := fd.bgDone
			fd.bgDone = nil
			fd.mu.Unlock()
			if f != nil {
				f()
			}
			return 0, true
		}
		return 0, false
	}
	fd.startDetect()
	call(pSetTimer, d.h, timerFix, 300, 0)
	d.show()
	call(pKillTimer, d.h, timerFix)
	if fd.rescan {
		mw.startScan()
	}
}

// ---------------------------------------------------------------- praca w tle

// background uruchamia pracę w gorutynie; wynik (finish) wykonuje się w wątku okien.
func (fd *fixDialog) background(phase string, work func() func()) {
	fd.phase, fd.busy = phase, true
	fd.enableChoice(false)
	h := fd.d.h
	go func() {
		var finish func()
		func() {
			defer func() {
				if p := recover(); p != nil {
					logError(fmt.Sprintf("okno napraw (%s): %v", phase, p))
					msg := fmt.Sprint(p)
					finish = func() { fd.fail(msg) }
				}
			}()
			finish = work()
		}()
		fd.mu.Lock()
		fd.bgDone = finish
		fd.mu.Unlock()
		postMsg(h, wmFixBg, 0, 0)
	}()
}

func (fd *fixDialog) fail(msg string) {
	fd.busy = false
	fd.setStatus("Błąd programu: " + msg)
	fd.enableChoice(len(fd.cands) > 0 && fd.phase != "done")
}

func (fd *fixDialog) startDetect() {
	res, isAdmin, offline := fd.mw.result, win.IsAdmin(), isChecked(fd.mw.chkOffline)
	fd.setStatusP("Sprawdzanie, co można naprawić...", -1)
	fd.background("detect", func() func() {
		fc := diag.NewFixContext(res, isAdmin, offline)
		cands := diag.FixCandidates(fc)
		unfixed := diag.UnfixedProblems(res, cands)
		for _, l := range fc.Log {
			logError(l)
		}
		return func() { fd.detected(cands, unfixed) }
	})
}

func (fd *fixDialog) detected(cands []diag.FixCandidate, unfixed []diag.Finding) {
	fd.busy = false
	fd.cands, fd.unfixed = cands, unfixed
	enabled := 0
	for _, c := range cands {
		fd.checked[c.Key] = c.Default
		if c.Blocked == "" {
			enabled++
		}
	}
	txt := fmt.Sprintf("Program może naprawić: %s", diag.FmtCount(len(cands), "problem", "problemy", "problemów"))
	if enabled < len(cands) {
		txt += " (część wymaga uprawnień administratora lub internetu - użyj „Uruchom jako administrator”)"
	}
	fd.setStatusP(txt+".", 0)
	fd.enableChoice(enabled > 0)
	fd.list.relayout()
}

// run: wybrane i potwierdzone naprawy - najpierw (opcjonalnie) punkt przywracania, potem naprawy.
func (fd *fixDialog) run() {
	var keys []string
	var sel []diag.FixCandidate
	for _, c := range fd.cands {
		if fd.checked[c.Key] && c.Blocked == "" {
			keys = append(keys, c.Key)
			sel = append(sel, c)
		}
	}
	if len(keys) == 0 {
		msgBox(fd.d.h, "Zaznacz naprawy do wykonania.", "Naprawa", mbIconInfo)
		return
	}
	restore := isChecked(fd.chk)
	q := fmt.Sprintf("Wykonać %s?", diag.FmtCount(len(keys), "naprawę", "naprawy", "napraw"))
	if restore {
		q += "\n\nNajpierw zostanie utworzony punkt przywracania systemu."
	}
	has := func(f func(diag.FixCandidate) bool) bool { return slices.ContainsFunc(sel, f) }
	if has(func(c diag.FixCandidate) bool { return c.Long }) {
		q += "\nCzęść napraw (DISM, SFC, sterowniki) potrwa kilkanaście minut - nie wyłączaj komputera."
	}
	if has(func(c diag.FixCandidate) bool { return c.Interactive }) {
		q += "\nDeinstalatory programów otworzą własne okna - przejdź je ręcznie."
	}
	if has(func(c diag.FixCandidate) bool { return c.Reboot }) {
		q += "\nCzęść zmian zadziała po ponownym uruchomieniu komputera."
	}
	q += "\n\nProgram jest udostępniany bez gwarancji - naprawy wykonujesz na własną odpowiedzialność."
	if msgBox(fd.d.h, q, "Naprawa", mbIconQuestion|mbYesNo) != idYes {
		return
	}
	fd.keys, fd.done, fd.pre = keys, nil, nil
	if !restore {
		fd.startFix()
		return
	}
	var pre []diag.FixCandidate
	for _, c := range sel {
		if c.FixID == "fx.restoreon" {
			pre = append(pre, c)
		}
	}
	fd.setStatusP("Przygotowanie punktu przywracania...", -1)
	fd.background("restore", func() func() {
		var preRes []diag.FixResult
		if len(pre) > 0 {
			preRes = diag.RunFixes(pre, fd.progress3)
		}
		fd.progress3(0, 0, "Tworzenie punktu przywracania (może potrwać minutę)...")
		err := diag.CreateRepairRestorePoint()
		return func() { fd.restored(pre, preRes, err) }
	})
}

func (fd *fixDialog) restored(pre []diag.FixCandidate, preRes []diag.FixResult, err error) {
	fd.busy = false
	fd.pre = preRes
	for _, c := range pre {
		fd.done = append(fd.done, c.Key)
	}
	if err == nil {
		fd.startFix()
		return
	}
	msg := "Nie udało się utworzyć punktu przywracania:\n" + err.Error() + "\n\nWykonać naprawy bez punktu przywracania? (Zmiany i tak zostaną zapisane w dzienniku napraw.)"
	if msgBox(fd.d.h, msg, "Punkt przywracania", mbIconWarning|mbYesNo) == idYes {
		fd.startFix()
		return
	}
	fd.setStatusP("Naprawy anulowano.", 0)
	fd.enableChoice(true)
}

func (fd *fixDialog) startFix() {
	var sel []diag.FixCandidate
	for _, c := range fd.cands {
		if slices.Contains(fd.keys, c.Key) && !slices.Contains(fd.done, c.Key) {
			sel = append(sel, c)
		}
	}
	fd.setStatusP("Naprawianie... nie zamykaj okna i nie wyłączaj komputera.", 0)
	fd.background("fix", func() func() {
		res := diag.RunFixes(sel, fd.progress3)
		return func() { fd.fixed(res) }
	})
}

// progress3: postęp z gorutyny (numer, liczba, opis) - okno odczytuje go co 300 ms.
func (fd *fixDialog) progress3(i, n int, text string) {
	fd.mu.Lock()
	if n > 0 {
		fd.bgI, fd.bgN = i, n
	}
	fd.bgText = text
	fd.mu.Unlock()
}

func (fd *fixDialog) fixed(res []diag.FixResult) {
	fd.busy = false
	all := append(slices.Clone(fd.pre), res...)
	fd.showResults(all)
}

// showResults: wynik napraw na liście, dziennik napraw obok raportów (także autotest - z wynikami przykładowymi).
func (fd *fixDialog) showResults(all []diag.FixResult) {
	mw := fd.mw
	fd.results, fd.phase = all, "done"
	path := ""
	if !mw.opt.SelfTest {
		p, err := diag.SaveRepairLog(all, mw.result.Meta, diag.ResolveReportDir(mw.opt.Root, mw.settings))
		if err != nil {
			logError("dziennik napraw: " + err.Error())
		} else {
			path = p
		}
	}
	mw.lastRepair = all
	okN, errN, reboot := 0, 0, false
	for _, r := range all {
		switch r.Result {
		case "ok", "warn":
			okN++
		case "error":
			errN++
		}
		if r.Reboot && r.Result != "error" && r.Result != "skip" {
			reboot = true
		}
	}
	head := fmt.Sprintf("Zakończono: wykonano %d z %d", okN, len(all))
	if errN > 0 {
		head += fmt.Sprintf(", nieudane: %d", errN)
	}
	head += "."
	if reboot {
		head += " Część napraw zadziała po ponownym uruchomieniu komputera."
	}
	if path != "" {
		head += "\nDziennik napraw: " + path
		mw.setFooter("Dziennik napraw: " + path)
	}
	head += "\nSprawdź efekt: diagnostyka ponownie, a potem „Porównaj przed/po” (wykonane naprawy wpiszą się same)."
	for _, id := range []int{ctlFixRun, ctlFixRec, ctlFixNone} {
		showWin(fd.btns[id], false)
	}
	showWin(fd.chk, false)
	showWin(fd.btns[ctlFixRescan], true)
	fd.setStatusP(head, 0)
	fd.list.scroll = 0
	fd.list.relayout()
	if reboot {
		msgBox(fd.d.h, "Część napraw zadziała dopiero po ponownym uruchomieniu komputera. Uruchom go ponownie, a potem wykonaj diagnostykę i porównanie przed/po.", "Naprawa", mbIconInfo)
	}
}

func (fd *fixDialog) tick() {
	if !fd.busy {
		return
	}
	fd.anim++
	fd.mu.Lock()
	i, n, t := fd.bgI, fd.bgN, fd.bgText
	fd.mu.Unlock()
	switch {
	case fd.phase == "fix" && n > 0:
		fd.progress = float64(max(i-1, 0)) / float64(n)
		if t != "" {
			fd.status = fmt.Sprintf("[%d/%d] %s... nie zamykaj okna.", i, n, strings.TrimSuffix(t, "..."))
		}
	case fd.phase == "restore" && t != "":
		fd.status = t
	}
	cr := clientRect(fd.d.h)
	fd.place(cr.w(), cr.h())
}

// ---------------------------------------------------------------- przyciski i lista

func (fd *fixDialog) enableChoice(on bool) {
	for _, id := range []int{ctlFixRun, ctlFixRec, ctlFixNone} {
		enableWin(fd.btns[id], on)
	}
	enableWin(fd.chk, on)
}

func (fd *fixDialog) command(id, code int) {
	switch id {
	case ctlFixRec:
		for _, c := range fd.cands {
			if c.Blocked == "" {
				fd.checked[c.Key] = c.Default
			}
		}
		fd.list.relayout()
	case ctlFixNone:
		for k := range fd.checked {
			fd.checked[k] = false
		}
		fd.list.relayout()
	case ctlFixRun:
		if !fd.busy {
			fd.run()
		}
	case ctlFixRescan:
		fd.rescan = true
		fd.d.close()
	case ctlFixClose, idOK:
		if fd.d.canClose() {
			fd.d.close()
		}
	}
}

func (fd *fixDialog) toggle(id int) {
	i := id - 1000
	if fd.busy || fd.phase == "done" || i < 0 || i >= len(fd.cands) || fd.cands[i].Blocked != "" {
		return
	}
	k := fd.cands[i].Key
	fd.checked[k] = !fd.checked[k]
	fd.list.relayout()
}

func (fd *fixDialog) setStatusP(s string, p float64) {
	fd.status, fd.progress = s, p
	cr := clientRect(fd.d.h)
	fd.place(cr.w(), cr.h())
}

func (fd *fixDialog) setStatus(s string) { fd.setStatusP(s, fd.progress) }

// place: położenie kontrolek; wysokość komunikatu zależy od jego treści.
func (fd *fixDialog) place(w, h int) {
	d := fd.d
	if w <= 0 || h <= 0 {
		return
	}
	p := d.px(20)
	f := d.font(130, fwNormal)
	bh := d.px(32)
	by := h - p - bh
	// przyciski: z lewej zaznaczanie, z prawej Zamknij, Napraw zaznaczone / Diagnostyka ponownie
	x := p
	for _, id := range []int{ctlFixRec, ctlFixNone} {
		bw := buttonWidth(f, getText(fd.btns[id]), d.px(12))
		moveWin(fd.btns[id], x, by, bw, bh)
		x += bw + d.px(8)
	}
	rx := w - p - d.px(100)
	moveWin(fd.btns[ctlFixClose], rx, by, d.px(100), bh)
	for _, id := range []int{ctlFixRun, ctlFixRescan} {
		bw := buttonWidth(f, getText(fd.btns[id]), d.px(14))
		moveWin(fd.btns[id], rx-d.px(8)-bw, by, bw, bh)
	}
	st := fd.statusBlock(w - 2*p)
	progH := 0
	if fd.busy {
		progH = d.px(6) + d.px(6)
	}
	y := by - d.px(12) - progH - st.h
	if call(pIsWindowVisible, fd.chk) != 0 {
		y -= d.px(8) + d.px(20)
		moveWin(fd.chk, p, y+d.px(8), checkWidth(f, getText(fd.chk), d.dpi), d.px(20))
	}
	top := fd.listTop()
	moveWin(fd.list.h, p+1, top+1, w-2*p-2, max(y-d.px(10)-top-2, d.px(40)))
	invalidate(d.h)
}

func (fd *fixDialog) header() (title, desc textBlock) {
	d := fd.d
	cr := clientRect(d.h)
	w := cr.w() - 2*d.px(20)
	title = layoutText([]span{{text: "Naprawa problemów wykrytych w diagnostyce", font: d.font(160, fwSemiBold), color: cText}}, w, 0)
	desc = layoutText([]span{{text: "Zaznacz, co program ma naprawić. Zalecane naprawy są zaznaczone, ryzykowniejsze zaznaczasz sam. Przed zmianami program tworzy punkt przywracania, a każdą zmianę zapisuje w dzienniku napraw z poprzednią wartością - zmienione ustawienia można potem cofnąć. Program jest udostępniany bez gwarancji: naprawy wykonujesz na własną odpowiedzialność.", font: d.font(130, fwNormal), color: cGray}}, w, 0)
	return
}

func (fd *fixDialog) listTop() int {
	t, ds := fd.header()
	return fd.d.px(20) + t.h + fd.d.px(4) + ds.h + fd.d.px(10)
}

func (fd *fixDialog) statusBlock(w int) textBlock {
	if fd.status == "" {
		return textBlock{}
	}
	var spans []span
	for i, l := range strings.Split(fd.status, "\n") {
		spans = append(spans, span{text: l, font: fd.d.font(130, fwNormal), color: cGray2, br: i > 0})
	}
	return layoutText(spans, w, 0)
}

func (fd *fixDialog) paint(cv *canvas, w, h int) {
	d := fd.d
	p := d.px(20)
	t, ds := fd.header()
	cv.block(t, p, p)
	cv.block(ds, p, p+t.h+d.px(4))
	lr := windowRectIn(fd.list.h, d.h)
	cv.card(int(lr.Left)-1, int(lr.Top)-1, int(lr.Right)+1, int(lr.Bottom)+1, float64(d.px(8)), cWhite, cBorder, 1)
	bh := d.px(32)
	by := h - p - bh
	st := fd.statusBlock(w - 2*p)
	progH := 0
	if fd.busy {
		progH = d.px(12)
	}
	sy := by - d.px(12) - progH - st.h
	cv.block(st, p, sy)
	if fd.busy {
		drawProgress(cv, p, sy+st.h+d.px(6), w-2*p, d.px(6), fd.progress, fd.anim)
	}
}

// drawProgress: pasek postępu; v < 0 - bez wartości (przesuwający się odcinek, jak IsIndeterminate w WPF).
func drawProgress(cv *canvas, x, y, w, h int, v float64, anim int) {
	cv.fillRound(x, y, x+w, y+h, float64(h)/2, hexc("#DCE3EC"), cornersAll)
	if v >= 0 {
		if fw := int(float64(w) * min(v, 1)); fw > 0 {
			cv.fillRound(x, y, x+max(fw, h), y+h, float64(h)/2, cBlue, cornersAll)
		}
		return
	}
	seg := w / 4
	pos := (anim*w/12)%(w+seg) - seg
	a, b := max(x+pos, x), min(x+pos+seg, x+w)
	if b > a {
		cv.fillRound(a, y, b, y+h, float64(h)/2, cBlue, cornersAll)
	}
}

func (fd *fixDialog) layoutList(dc *doc, width int) int {
	x := dc.px(14)
	w := width - 2*x
	y := dc.px(10)
	f, fb := dc.font(130, fwNormal), dc.font(130, fwSemiBold)
	small, head := dc.font(120, fwNormal), dc.font(140, fwSemiBold)
	section := func(text string, c col, gap int) {
		if y > dc.px(10) {
			y += gap
		}
		tb := layoutText([]span{{text: text, font: head, color: c}}, w, 0)
		dc.block(tb, x, y)
		y += tb.h + dc.px(2)
	}
	if fd.phase == "done" {
		section("Wynik napraw", cBlue, 0)
		for _, r := range fd.results {
			lb, ok := fixLabels[r.Result]
			if !ok {
				lb = [2]any{r.Result, cGray2}
			}
			spans := []span{{text: "[" + lb[0].(string) + "] ", font: fb, color: lb[1].(col)}, {text: r.Title, font: fb, color: cText}}
			if r.Message != "" {
				spans = append(spans, span{text: r.Message, font: f, color: cGray2, br: true})
			}
			tb := layoutText(spans, w, 0)
			y += dc.px(4)
			dc.block(tb, x, y)
			y += tb.h + dc.px(4)
		}
		return y + dc.px(10)
	}
	if fd.phase == "detect" && fd.busy {
		return y
	}
	if len(fd.cands) == 0 {
		tb := layoutText([]span{{text: "Program nie znalazł problemów, które może naprawić sam.", font: fb, color: cText}}, w, 0)
		dc.block(tb, x, y)
		y += tb.h
	}
	cb := dc.px(16)
	group := ""
	for i, c := range fd.cands {
		if c.Group != group {
			group = c.Group
			section(group, cBlue, dc.px(10))
		}
		var badges []string
		if c.Reboot {
			badges = append(badges, "wymaga ponownego uruchomienia")
		}
		if c.Long {
			badges = append(badges, "potrwa kilkanaście minut")
		}
		if c.Interactive {
			badges = append(badges, "otworzy okno deinstalatora")
		}
		switch c.Risk {
		case "medium":
			badges = append(badges, "ryzyko: średnie")
		case "high":
			badges = append(badges, "ryzyko: wysokie")
		}
		if c.NeedsInternet {
			badges = append(badges, "wymaga internetu")
		}
		what := "Co zrobi: " + c.Description
		if len(badges) > 0 {
			what += " · " + strings.Join(badges, " · ")
		}
		spans := []span{{text: c.Title, font: fb, color: cText}}
		if c.Problem != "" {
			spans = append(spans, span{text: c.Problem, font: f, color: cGray2, br: true})
		}
		spans = append(spans, span{text: what, font: small, color: cGray, br: true})
		if c.Blocked != "" {
			spans = append(spans, span{text: "Niedostępne: " + c.Blocked, font: small, color: hexc("#B42318"), br: true})
		}
		tb := layoutText(spans, w-cb-dc.px(10)-dc.px(8), 0)
		rowH := tb.h + dc.px(10)
		y += dc.px(2)
		row := dc.rect(x-dc.px(6), y, w+dc.px(12), rowH, none, none, 0, float64(dc.px(6)))
		if c.Blocked == "" && !fd.busy {
			row.id, row.hoverFill = 1000+i, cHover
		}
		dc.check(x, y+dc.px(5)+(f.height-cb)/2, cb, fd.checked[c.Key], c.Blocked != "" || fd.busy)
		dc.block(tb, x+cb+dc.px(10), y+dc.px(5))
		y += rowH + dc.px(2)
	}
	if len(fd.unfixed) > 0 {
		section("Tego program nie naprawi - wymaga pracy serwisanta", hexc("#B54708"), dc.px(14))
		for i, u := range fd.unfixed {
			if i >= 25 {
				break
			}
			tb := layoutText([]span{{text: "• " + u.Title + ": ", font: fb, color: cGray3}, {text: u.Value, font: f, color: cGray2}}, w, 0)
			y += dc.px(2)
			dc.block(tb, x, y)
			y += tb.h + dc.px(2)
		}
	}
	return y + dc.px(10)
}

// windowRectIn: prostokąt okna potomnego we współrzędnych klienta rodzica.
func windowRectIn(child, parent uintptr) rect {
	r := windowRect(child)
	tl := screenToClient(parent, point{r.Left, r.Top})
	return rect{tl.X, tl.Y, tl.X + int32(r.w()), tl.Y + int32(r.h())}
}

// ---------------------------------------------------------------- cofanie napraw (Show-SdUiUndo)

const (
	ctlUndoPick = 721 + iota
	ctlUndoRestore
	ctlUndoRun
	ctlUndoClose
	ctlUndoFile
)

type undoDialog struct {
	mw      *mainWin
	d       *dialog
	list    *doc
	edit    uintptr
	btns    map[int]uintptr
	path    string
	rl      *diag.RepairLog
	items   []diag.UndoItem
	checked []bool
	results []diag.UndoResult
	status  string
	note    string // nagłówek listy albo komunikat zamiast listy
}

// showUndo otwiera okno cofania napraw; path - dziennik do pokazania (pusty: najnowszy dziennik tego komputera).
func (mw *mainWin) showUndo(path string) {
	ud := &undoDialog{mw: mw, btns: map[int]uintptr{}}
	d := newDialog(mw.h, "Cofanie napraw", 860, 620, true)
	d.minW, d.minH = 640, 440
	ud.d = d
	ud.list = newDoc(d.h, cWhite, ud.layoutList)
	ud.list.dpi = d.dpi
	ud.list.onClick = ud.toggle
	ud.edit = newEdit(d.h, ctlUndoFile, "", esReadOnly)
	for id, t := range map[int]string{ctlUndoPick: "Wybierz dziennik...", ctlUndoRestore: "Przywracanie systemu...", ctlUndoRun: "Cofnij zaznaczone", ctlUndoClose: "Zamknij"} {
		ud.btns[id] = newButton(d.h, id, t)
	}
	fonts := func() {
		f := d.font(130, fwNormal)
		setFont(ud.edit, f)
		setEditMargins(ud.edit, d.px(4))
		for _, b := range ud.btns {
			setFont(b, f)
		}
		ud.list.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = ud.paint
	d.onSize = func(w, h int) { ud.place(w, h) }
	d.onCmd = ud.command
	if path == "" {
		if logs := diag.FindRepairLogs(diag.ResolveReportDir(mw.opt.Root, mw.settings), os.Getenv("COMPUTERNAME")); len(logs) > 0 {
			path = logs[0]
		}
	}
	ud.load(path)
	d.show()
}

// load odpowiada Update-SdUiUndoList.
func (ud *undoDialog) load(path string) {
	ud.path, ud.rl, ud.items, ud.checked, ud.results, ud.note = "", nil, nil, nil, nil, ""
	setText(ud.edit, path)
	defer func() {
		enableWin(ud.btns[ctlUndoRun], len(ud.items) > 0)
		ud.list.scroll = 0
		ud.list.relayout()
		cr := clientRect(ud.d.h)
		ud.place(cr.w(), cr.h())
	}()
	if path == "" {
		ud.note = "Nie znaleziono dziennika napraw tego komputera w folderze raportów. Wskaż plik *_naprawy.json przyciskiem „Wybierz dziennik...”."
		ud.status = ""
		return
	}
	rl, err := diag.ReadRepairLog(path)
	if err != nil {
		ud.status = "Nie udało się odczytać dziennika: " + err.Error()
		return
	}
	if !strings.EqualFold(rl.Computer, os.Getenv("COMPUTERNAME")) {
		ud.status = fmt.Sprintf("Ten dziennik pochodzi z innego komputera (%s) - jego zmian nie można cofnąć tutaj.", rl.Computer)
		return
	}
	ud.path, ud.rl = path, rl
	ud.items = diag.UndoItems(rl)
	ud.checked = make([]bool, len(ud.items))
	for i := range ud.checked {
		ud.checked[i] = true
	}
	when := rl.Created
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", rl.Created, time.Local); err == nil {
		when = diag.FmtDt(t)
	}
	ud.note = "Naprawy z " + when
	if rl.Ticket != "" {
		ud.note += ", zlecenie " + rl.Ticket
	}
	ud.status = fmt.Sprintf("Zmian do cofnięcia: %d.", len(ud.items))
	if !win.IsAdmin() {
		ud.status += " Cofanie wymaga uprawnień administratora - użyj „Uruchom jako administrator” w oknie programu."
	}
}

func (ud *undoDialog) command(id, code int) {
	d := ud.d
	switch id {
	case ctlUndoPick:
		if p := pickPath(d.h, "Wybierz dziennik napraw", "Dziennik napraw SerwisDiag (*_naprawy.json)", "*_naprawy.json", false,
			diag.ResolveReportDir(ud.mw.opt.Root, ud.mw.settings)+`\`); p != "" {
			ud.load(p)
		}
	case ctlUndoRestore:
		if err := shellOpen("rstrui.exe", ""); err != nil {
			msgBox(d.h, "Nie udało się uruchomić przywracania systemu: "+err.Error(), "Cofanie napraw", mbIconError)
		}
	case ctlUndoRun:
		ud.run()
	case ctlUndoClose, idOK:
		d.close()
	}
}

func (ud *undoDialog) toggle(id int) {
	i := id - 1000
	if ud.results != nil || i < 0 || i >= len(ud.checked) {
		return
	}
	ud.checked[i] = !ud.checked[i]
	ud.list.relayout()
}

func (ud *undoDialog) run() {
	d := ud.d
	if !win.IsAdmin() {
		msgBox(d.h, "Cofanie zmian wymaga uprawnień administratora. Uruchom program jako administrator.", "Cofanie napraw", mbIconWarning)
		return
	}
	var sel []diag.UndoItem
	for i, it := range ud.items {
		if ud.checked[i] {
			sel = append(sel, it)
		}
	}
	if len(sel) == 0 {
		msgBox(d.h, "Zaznacz zmiany do cofnięcia.", "Cofanie napraw", mbIconInfo)
		return
	}
	q := fmt.Sprintf("Cofnąć %s?", diag.FmtCount(len(sel), "zmianę", "zmiany", "zmian")) +
		"\n\nPrzywrócone zostaną ustawienia sprzed naprawy - także te niebezpieczne (np. wyłączona zapora albo przekierowanie programu dodane przez złośliwe oprogramowanie)."
	if msgBox(d.h, q, "Cofanie napraw", mbIconQuestion|mbYesNo) != idYes {
		return
	}
	var res []diag.UndoResult
	withWaitCursor(func() { res = diag.RunUndo(sel) })
	ud.results = res
	logPath := ""
	if p, err := diag.SaveUndoLog(res, ud.path); err != nil {
		logError("dziennik cofnięcia: " + err.Error())
	} else {
		logPath = p
	}
	okN := 0
	for _, r := range res {
		if r.Result == "ok" {
			okN++
		}
	}
	ud.status = fmt.Sprintf("Cofnięto %d z %d. Część zmian (usługi, plik wymiany, TRIM) zadziała po ponownym uruchomieniu komputera.", okN, len(res))
	if logPath != "" {
		ud.status += "\nDziennik: " + logPath
		ud.mw.setFooter("Dziennik cofnięcia napraw: " + logPath)
	}
	enableWin(ud.btns[ctlUndoRun], false)
	ud.list.scroll = 0
	ud.list.relayout()
	cr := clientRect(d.h)
	ud.place(cr.w(), cr.h())
}

func (ud *undoDialog) header() (title, desc textBlock) {
	d := ud.d
	cr := clientRect(d.h)
	w := cr.w() - 2*d.px(20)
	title = layoutText([]span{{text: "Cofanie zmian wprowadzonych przez naprawy", font: d.font(160, fwSemiBold), color: cText}}, w, 0)
	desc = layoutText([]span{{text: "Program przywraca poprzednie ustawienia zapisane w dzienniku napraw: wartości rejestru, typ uruchamiania usług, zaporę i SMB1. Nie cofa DISM, SFC, czyszczenia plików ani instalacji sterowników - do tego służy punkt przywracania systemu. Ustawienia zmienione ponownie po naprawie są pomijane.", font: d.font(130, fwNormal), color: cGray}}, w, 0)
	return
}

func (ud *undoDialog) statusBlock(w int) textBlock {
	if ud.status == "" {
		return textBlock{}
	}
	var spans []span
	for i, l := range strings.Split(ud.status, "\n") {
		spans = append(spans, span{text: l, font: ud.d.font(130, fwNormal), color: cGray2, br: i > 0})
	}
	return layoutText(spans, w, 0)
}

func (ud *undoDialog) place(w, h int) {
	d := ud.d
	if w <= 0 || h <= 0 {
		return
	}
	p := d.px(20)
	f := d.font(130, fwNormal)
	t, ds := ud.header()
	top := p + t.h + d.px(4) + ds.h + d.px(10)
	pw := buttonWidth(f, getText(ud.btns[ctlUndoPick]), d.px(10))
	moveWin(ud.btns[ctlUndoPick], w-p-pw, top, pw, d.px(30))
	moveWin(ud.edit, p, top, w-2*p-pw-d.px(8), d.px(30))
	listTop := top + d.px(30) + d.px(8)
	bh := d.px(32)
	by := h - p - bh
	rw := buttonWidth(f, getText(ud.btns[ctlUndoRestore]), d.px(12))
	moveWin(ud.btns[ctlUndoRestore], p, by, rw, bh)
	moveWin(ud.btns[ctlUndoClose], w-p-d.px(100), by, d.px(100), bh)
	cw := buttonWidth(f, getText(ud.btns[ctlUndoRun]), d.px(14))
	moveWin(ud.btns[ctlUndoRun], w-p-d.px(100)-d.px(8)-cw, by, cw, bh)
	st := ud.statusBlock(w - 2*p)
	listBottom := by - d.px(12) - st.h - d.px(8)
	moveWin(ud.list.h, p+1, listTop+1, w-2*p-2, max(listBottom-listTop-2, d.px(40)))
	invalidate(d.h)
}

func (ud *undoDialog) paint(cv *canvas, w, h int) {
	d := ud.d
	p := d.px(20)
	t, ds := ud.header()
	cv.block(t, p, p)
	cv.block(ds, p, p+t.h+d.px(4))
	lr := windowRectIn(ud.list.h, d.h)
	cv.card(int(lr.Left)-1, int(lr.Top)-1, int(lr.Right)+1, int(lr.Bottom)+1, float64(d.px(8)), cWhite, cBorder, 1)
	st := ud.statusBlock(w - 2*p)
	cv.block(st, p, h-p-d.px(32)-d.px(12)-st.h)
}

func (ud *undoDialog) layoutList(dc *doc, width int) int {
	x := dc.px(14)
	w := width - 2*x
	y := dc.px(10)
	f, fb := dc.font(130, fwNormal), dc.font(130, fwSemiBold)
	small, head := dc.font(120, fwNormal), dc.font(140, fwSemiBold)
	section := func(text string, c col, gap int) {
		if y > dc.px(10) {
			y += gap
		}
		tb := layoutText([]span{{text: text, font: head, color: c}}, w, 0)
		dc.block(tb, x, y)
		y += tb.h + dc.px(4)
	}
	if ud.results != nil {
		section("Wynik cofania", cBlue, 0)
		labels := map[string][2]any{"ok": {"OK", hexc("#067647")}, "skip": {"POMINIĘTO", cGray}, "error": {"BŁĄD", hexc("#B42318")}}
		for _, r := range ud.results {
			lb, ok := labels[r.Result]
			if !ok {
				lb = [2]any{r.Result, cGray2}
			}
			tb := layoutText([]span{{text: "[" + lb[0].(string) + "] ", font: fb, color: lb[1].(col)}, {text: r.Text, font: fb, color: cText},
				{text: r.Message, font: f, color: cGray2, br: true}}, w, 0)
			y += dc.px(4)
			dc.block(tb, x, y)
			y += tb.h + dc.px(4)
		}
		return y + dc.px(10)
	}
	if ud.rl == nil {
		if ud.note != "" {
			tb := layoutText([]span{{text: ud.note, font: f, color: cGray2}}, w, 0)
			dc.block(tb, x, y)
			y += tb.h
		}
		return y + dc.px(10)
	}
	section(ud.note, cBlue, 0)
	if len(ud.items) == 0 {
		tb := layoutText([]span{{text: "W tym dzienniku nie ma zmian ustawień do cofnięcia.", font: f, color: cGray2}}, w, 0)
		dc.block(tb, x, y)
		y += tb.h
	}
	cb := dc.px(16)
	for i, it := range ud.items {
		tb := layoutText([]span{{text: it.FixTitle, font: fb, color: cText}, {text: it.Text, font: small, color: cGray2, br: true}}, w-cb-dc.px(18), 0)
		rowH := tb.h + dc.px(8)
		y += dc.px(2)
		row := dc.rect(x-dc.px(6), y, w+dc.px(12), rowH, none, none, 0, float64(dc.px(6)))
		row.id, row.hoverFill = 1000+i, cHover
		dc.check(x, y+dc.px(4)+(f.height-cb)/2, cb, ud.checked[i], false)
		dc.block(tb, x+cb+dc.px(10), y+dc.px(4))
		y += rowH + dc.px(2)
	}
	if nu := diag.NotUndoable(ud.rl); len(nu) > 0 {
		section("Bez cofania przez program (użyj punktu przywracania, jeśli trzeba)", hexc("#B54708"), dc.px(14))
		for _, t := range nu {
			tb := layoutText([]span{{text: "• " + t, font: f, color: cGray2}}, w, 0)
			dc.block(tb, x, y)
			y += tb.h + dc.px(2)
		}
	}
	return y + dc.px(10)
}
