package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Options - parametry okna programu (z wiersza poleceń, jak Start-SdGui).
type Options struct {
	Root       string // folder programu (ustawienia config\settings.json, raporty)
	Exe        string // ścieżka pliku programu
	Ticket     string
	Customer   string
	Note       string
	Offline    bool
	SelfTest   bool   // autotest: okno poza ekranem, szybka diagnostyka, zrzuty stron, zamknięcie
	Screenshot string // prefiks plików zrzutów autotestu
	Integrity  func() string
}

const (
	mainClass = "SerwisDiagGoMain"

	ctlAbout = 101 + iota
	ctlSettings
	ctlElevate
	ctlKbAbout
	ctlTicket
	ctlCustomer
	ctlNote
	ctlLevel
	ctlStart
	ctlCancel
	ctlChkDisk
	ctlChkCpu
	ctlChkNet
	ctlChkVol
	ctlChkDism
	ctlChkOffline
	ctlTables
	ctlLvFind
	ctlLvTable
	ctlTech
	ctlCust
	ctlPdf
	ctlFolder
	ctlMore
	ctlFix
	ctlCompare
	ctlUpdate
	ctlUpdNotes

	ctlMenuCopy   = 201
	ctlMenuAnon   = 202
	ctlMenuUndo   = 203
	ctlMenuBackup = 204

	timerScan     = 1
	timerSelfTest = 2
	wmScanDone    = wmApp + 1
	wmMwBg        = wmApp + 3 // wynik pracy w tle okna głównego (aktualizacje, rejestracja)
	wmPrintClient = 0x0318
)

type navItem struct {
	key, label string
	dot        col
	badge      string
}

// mwLayout - położenie elementów rysowanych przez okno (liczone w layout).
type mwLayout struct {
	hdrH, ctlTop, ctlH          int
	chips                       [2]rect
	banners                     []bannerLayout
	labels                      []labelPos
	progTop, progH              int
	contentTop, contentBottom   int
	navCard, page, cardA, cardC rect
	aHeaderH, detTop            int
	footerTop, footerTextY      int
	narrow                      bool
}

type bannerLayout struct {
	top, h int
	tb     textBlock
	fg, bg col
	bd     col
}

type labelPos struct {
	text string
	x, y int
	f    *font
	c    col
	page bool // napis na stronie kategorii (rysowany tylko, gdy jest widoczna)
}

type mainWin struct {
	opt      Options
	h        uintptr
	dpi      int
	settings diag.Settings
	isAdmin  bool
	osText   string
	osCh     chan string
	kbText   string
	kbStale  bool

	btnAbout, btnSettings, btnElevate, btnKb             uintptr
	btnUpdate, btnUpdNotes                               uintptr
	edTicket, edCustomer, edNote, cmbLevel               uintptr
	btnStart, btnCancel                                  uintptr
	chkDisk, chkCpu, chkNet, chkVol, chkDism, chkOffline uintptr
	cmbTables                                            uintptr
	btnFix, btnTech, btnCust, btnPdf, btnFolder, btnMore uintptr
	btnCompare                                           uintptr
	lvFind, lvTable                                      *listView
	nav, sumDoc, toolsDoc, detDoc                        *doc
	navItems                                             []navItem
	selected                                             string

	run         *scanRun
	scanning    bool
	cancelReq   bool
	cancelAt    time.Time
	shownCount  int
	result      *diag.Result
	paths       *diag.ReportPaths
	pdfPath     string
	progText    string
	progVal     int
	progMax     int
	progVisible bool
	footerText  string
	catFind     []diag.Finding
	catTables   []diag.Table
	detail      int
	detailKey   string // wybrany wynik (test|tytuł) - zostaje wybrany po odświeżeniu listy
	tableKey    string // pokazana tabela danych szczegółowych (kolumny ustawiane tylko po zmianie tabeli)

	cv    *canvas
	L     mwLayout
	self  *selfTest
	tools []tool
	// lastRepair: wynik ostatnich napraw (do opisu prac w porównaniu przed/po)
	lastRepair []diag.FixResult

	// updRel: nowa wersja z GitHuba (pasek „Dostępna nowa wersja”), updText - tekst paska
	updRel  *diag.Release
	updText string
	// bgQueue: dokończenia pracy w tle do wykonania w wątku okna (wmMwBg)
	bgMu    sync.Mutex
	bgQueue []func()
}

// background: praca w tle bez blokowania okna; zwrócona funkcja wykonuje się potem w wątku okna.
func (mw *mainWin) background(work func() func()) {
	go func() {
		done := work()
		mw.bgMu.Lock()
		mw.bgQueue = append(mw.bgQueue, done)
		mw.bgMu.Unlock()
		postMsg(mw.h, wmMwBg, 0, 0)
	}()
}

func (mw *mainWin) s() sc                         { return sc(mw.dpi) }
func (mw *mainWin) px(v int) int                  { return sc(mw.dpi).px(v) }
func (mw *mainWin) font(size10, weight int) *font { return getFont(mw.dpi, size10, weight) }
func (mw *mainWin) ui() *font                     { return mw.font(130, fwNormal) }

// RunGUI otwiera okno programu i działa do jego zamknięcia (Start-SdGui).
func RunGUI(opt Options) int {
	initUI()
	mw := &mainWin{opt: opt, settings: diag.LoadSettings(opt.Root), isAdmin: win.IsAdmin(), selected: "summary", detail: -1, shownCount: -1}
	mw.kbText, mw.kbStale = diag.KnowledgeInfo(time.Now())
	mw.tools = serviceTools()
	registerClass(mainClass, idcArrow, 0)
	title := fmt.Sprintf("SerwisDiag %s (Go) - diagnostyka systemu Windows", diag.Version)
	mw.h = createWindow(mw, mainClass, title, wsOverlappedWindow|wsClipChildren, 0,
		cwUseDefault, cwUseDefault, 1260, 840, 0, 0)
	if mw.h == 0 {
		return 3
	}
	dlgRoots[mw.h] = true
	mw.dpi = dpiOf(mw.h)
	mw.createControls()
	mw.placeWindow()
	// nazwa systemu z WMI - w tle, żeby nie opóźniać pokazania okna (przekazana komunikatem do wątku okien)
	osCh := make(chan string, 1)
	mw.osCh = osCh
	go func() {
		c, a := diag.SystemCaption()
		osCh <- fmt.Sprintf("%s (%s)", c, a)
		postMsg(mw.h, wmApp+2, 0, 0)
	}()
	mw.startupBackground()
	if opt.SelfTest {
		mw.startSelfTest()
	}
	pump(nil)
	return 0
}

// placeWindow: rozmiar okna w granicach obszaru roboczego ekranu (Set-SdUiWindowFit); okno większe niż ekran
// otwiera się zmaksymalizowane.
func (mw *mainWin) placeWindow() {
	wa := workArea(mw.h)
	w, h := mw.px(1260), mw.px(840)
	maximize := w > wa.w() || h > wa.h()
	w, h = min(w, wa.w()), min(h, wa.h())
	x, y := int(wa.Left)+(wa.w()-w)/2, int(wa.Top)+(wa.h()-h)/2
	if mw.opt.SelfTest {
		x, y, maximize = -3000, 40, false
		w, h = mw.px(1260), mw.px(840)
	}
	call(pSetWindowPos, mw.h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	switch {
	case mw.opt.SelfTest:
		call(pShowWindow, mw.h, swShowNoActivate)
	case maximize:
		call(pShowWindow, mw.h, swShowMaximized)
	default:
		call(pShowWindow, mw.h, swShowNormal)
	}
	call(pUpdateWindow, mw.h)
}

func (mw *mainWin) createControls() {
	h := mw.h
	mw.btnAbout = newButton(h, ctlAbout, "O programie")
	mw.btnSettings = newButton(h, ctlSettings, "Ustawienia")
	mw.btnElevate = newButton(h, ctlElevate, "Uruchom jako administrator")
	mw.btnKb = newButton(h, ctlKbAbout, "Sprawdź nową wersję")
	mw.btnUpdate = newButton(h, ctlUpdate, "Zaktualizuj")
	mw.btnUpdNotes = newButton(h, ctlUpdNotes, "Co nowego")
	mw.edTicket = newEdit(h, ctlTicket, mw.opt.Ticket, 0)
	mw.edCustomer = newEdit(h, ctlCustomer, mw.opt.Customer, 0)
	mw.edNote = newEdit(h, ctlNote, mw.opt.Note, 0)
	mw.cmbLevel = newCombo(h, ctlLevel, []string{"Szybka diagnostyka (ok. 1 min)", "Pełna diagnostyka (kilka min)"})
	setComboSel(mw.cmbLevel, 0)
	mw.btnStart = newPrimaryButton(h, ctlStart, "▶  Rozpocznij diagnostykę", cWhite)
	mw.btnCancel = newButton(h, ctlCancel, "Przerwij")
	enableWin(mw.btnCancel, false)
	mw.chkDisk = newCheck(h, ctlChkDisk, "szybkość dysku")
	mw.chkCpu = newCheck(h, ctlChkCpu, "obciążenie CPU (45 s)")
	mw.chkNet = newCheck(h, ctlChkNet, "prędkość internetu")
	mw.chkVol = newCheck(h, ctlChkVol, "skan NTFS (admin)")
	mw.chkDism = newCheck(h, ctlChkDism, "DISM ScanHealth (admin)")
	mw.chkOffline = newCheck(h, ctlChkOffline, "tryb bez internetu")
	setChecked(mw.chkOffline, mw.opt.Offline)
	mw.syncOffline()

	mw.nav = newDoc(h, cWhite, mw.layoutNav)
	mw.nav.onClick = mw.navClick
	mw.nav.onKey = mw.navKey
	mw.nav.focusable = true
	mw.sumDoc = newDoc(h, cBg, mw.layoutSummary)
	mw.toolsDoc = newDoc(h, cBg, mw.layoutTools)
	mw.toolsDoc.onClick = mw.toolClick
	mw.detDoc = newDoc(h, cWhite, mw.layoutDetail)

	mw.lvFind = newListView(h, ctlLvFind, mw.dpi)
	mw.lvFind.setColumns([]string{"Status", "Test", "Wynik"}, []int{mw.px(112), mw.px(250), mw.px(400)})
	mw.lvFind.onSelect = func(i int) {
		mw.detail = i
		if i < 0 {
			mw.detailKey = ""
		}
		// najpierw szerokość panelu szczegółów, potem jego treść, na końcu wysokość według treści
		mw.layout()
		mw.detDoc.scroll = 0
		mw.detDoc.relayout()
		mw.layout()
		mw.redraw()
		if i >= 0 {
			sendMsg(mw.lvFind.h, lvmEnsureVisible, uintptr(i), 0)
		}
	}
	mw.cmbTables = newCombo(h, ctlTables, nil)
	mw.lvTable = newListView(h, ctlLvTable, mw.dpi)

	mw.btnFix = newButton(h, ctlFix, "Napraw problemy...")
	mw.btnTech = newButton(h, ctlTech, "Raport techniczny")
	mw.btnCust = newButton(h, ctlCust, "Raport dla klienta")
	mw.btnPdf = newButton(h, ctlPdf, "Zapisz PDF")
	mw.btnCompare = newButton(h, ctlCompare, "Porównaj przed/po")
	mw.btnFolder = newButton(h, ctlFolder, "Folder raportów")
	mw.btnMore = newButton(h, ctlMore, "Więcej ▾")
	mw.setResultButtons(false)
	mw.initNav()
	mw.applyFonts()
	mw.showPage("summary")
}

// applyFonts: czcionki kontrolek dla bieżącego DPI (także po przeniesieniu okna na inny ekran).
func (mw *mainWin) applyFonts() {
	f := mw.ui()
	for _, c := range []uintptr{mw.btnAbout, mw.btnSettings, mw.btnElevate, mw.btnKb, mw.btnUpdate, mw.btnUpdNotes, mw.edTicket, mw.edCustomer, mw.edNote,
		mw.cmbLevel, mw.btnStart, mw.btnCancel, mw.chkDisk, mw.chkCpu, mw.chkNet, mw.chkVol, mw.chkDism,
		mw.cmbTables, mw.lvFind.h, mw.lvTable.h, mw.btnFix, mw.btnTech, mw.btnCust, mw.btnPdf, mw.btnCompare, mw.btnFolder, mw.btnMore} {
		setFont(c, f)
	}
	setFont(mw.chkOffline, mw.font(130, fwSemiBold))
	setFont(mw.btnStart, mw.font(130, fwSemiBold))
	for _, e := range []uintptr{mw.edTicket, mw.edCustomer, mw.edNote} {
		setEditMargins(e, mw.px(4))
	}
	setComboHeight(mw.cmbLevel, mw.px(30))
	setComboHeight(mw.cmbTables, mw.px(30))
	for _, d := range []*doc{mw.nav, mw.sumDoc, mw.toolsDoc, mw.detDoc} {
		d.dpi = mw.dpi
	}
	mw.lvFind.dpi, mw.lvTable.dpi = mw.dpi, mw.dpi
	mw.lvFind.setRowHeight()
	mw.lvTable.setRowHeight()
}

func (mw *mainWin) syncOffline() {
	off := isChecked(mw.chkOffline)
	if off {
		setChecked(mw.chkNet, false)
	}
	enableWin(mw.chkNet, !off)
}

func (mw *mainWin) setResultButtons(on bool) {
	for _, b := range []uintptr{mw.btnFix, mw.btnTech, mw.btnCust, mw.btnPdf} {
		enableWin(b, on)
	}
}

func (mw *mainWin) redraw() { invalidate(mw.h) }

// ---------------------------------------------------------------- układ

func (mw *mainWin) layout() {
	cr := clientRect(mw.h)
	W, H := cr.w(), cr.h()
	if W <= 0 || H <= 0 {
		return
	}
	L := mwLayout{}
	f := mw.ui()
	gray := cGray
	L.narrow = W*96/mw.dpi < 1200
	compact := H*96/mw.dpi < 700

	// nagłówek
	padY := mw.px(14)
	if compact {
		padY = mw.px(8)
	}
	titleF, shopF := mw.font(230, fwSemiBold), mw.font(125, fwNormal)
	L.hdrH = padY + titleF.height + mw.px(1) + shopF.height + padY
	btnH := f.height + mw.px(10)
	bx := W - mw.px(22)
	for _, b := range []uintptr{mw.btnSettings, mw.btnAbout} {
		bw := buttonWidth(f, getText(b), mw.px(12))
		bx -= bw
		moveWin(b, bx, (L.hdrH-btnH)/2, bw, btnH)
		bx -= mw.px(8)
	}
	chipF := mw.font(120, fwNormal)
	chipH := chipF.height + mw.px(8)
	adminText := mw.adminChipText()
	bx -= mw.px(4)
	aw := strWidth(mw.font(120, fwSemiBold), adminText) + mw.px(22)
	L.chips[1] = rect{int32(bx - aw), int32((L.hdrH - chipH) / 2), int32(bx), int32((L.hdrH + chipH) / 2)}
	bx -= aw + mw.px(8)
	ow := strWidth(chipF, mw.osText) + mw.px(22)
	if mw.osText == "" {
		ow = 0
	}
	L.chips[0] = rect{int32(bx - ow), L.chips[1].Top, int32(bx), L.chips[1].Bottom}

	// banery
	y := L.hdrH
	type bannerDef struct {
		show bool
		text string
		btn  uintptr
		btn2 uintptr // drugi przycisk (na lewo od pierwszego) albo 0
		bg   col
		bd   col
		fg   col
	}
	notes := uintptr(0)
	if mw.updRel != nil && mw.updRel.Notes != "" {
		notes = mw.btnUpdNotes
	}
	banners := []bannerDef{
		{!mw.isAdmin, "Program działa bez uprawnień administratora - zostaną pominięte m.in. odczyt SMART dysków SATA/HDD, BitLocker, dzienniki zabezpieczeń, DISM i część testów.", mw.btnElevate, 0, cBannerBg, cBannerBd, cBannerFg},
		{mw.kbStale, "Baza wiedzy programu (okresy wsparcia Windows i ESU): " + mw.kbText + ". Daty wsparcia w raportach mogą być nieaktualne.", mw.btnKb, 0, cBannerBg, cBannerBd, cBannerFg},
		{mw.updRel != nil, mw.updText, mw.btnUpdate, notes, cSelected, hexc("#B2CCFF"), hexc("#1849A9")},
	}
	showWin(mw.btnUpdNotes, false)
	for _, b := range banners {
		if !b.show {
			showWin(b.btn, false)
			continue
		}
		bh0 := btnH - mw.px(4)
		bw := buttonWidth(f, getText(b.btn), mw.px(12))
		bw2 := 0
		if b.btn2 != 0 {
			bw2 = buttonWidth(f, getText(b.btn2), mw.px(12)) + mw.px(8)
		}
		tb := layoutText([]span{{text: b.text, font: f, color: b.fg}}, W-mw.px(44)-bw-bw2-mw.px(14), 0)
		bh := mw.px(8) + max(tb.h, bh0) + mw.px(8)
		L.banners = append(L.banners, bannerLayout{top: y, h: bh, tb: tb, fg: b.fg, bg: b.bg, bd: b.bd})
		moveWin(b.btn, W-mw.px(22)-bw, y+(bh-bh0)/2, bw, bh0)
		showWin(b.btn, true)
		if b.btn2 != 0 {
			moveWin(b.btn2, W-mw.px(22)-bw-bw2, y+(bh-bh0)/2, bw2-mw.px(8), bh0)
			showWin(b.btn2, true)
		}
		y += bh + 1
	}

	// dane zlecenia i sterowanie
	L.ctlTop = y
	row1 := y + mw.px(12)
	ch := mw.px(30)
	ly := row1 + (ch-f.height)/2
	x := mw.px(22)
	label := func(t string) {
		L.labels = append(L.labels, labelPos{text: t, x: x, y: ly, f: f, c: gray})
		x += strWidth(f, t) + mw.px(8)
	}
	ticketW, custW, levelW := mw.px(130), mw.px(190), mw.px(236)
	startText := "▶  Rozpocznij diagnostykę"
	if L.narrow {
		ticketW, custW, levelW = mw.px(100), mw.px(150), mw.px(214)
		startText = "▶  Rozpocznij"
	}
	if getText(mw.btnStart) != startText {
		setText(mw.btnStart, startText)
	}
	semi := mw.font(130, fwSemiBold)
	startW := buttonWidth(semi, startText, mw.px(18))
	cancelW := buttonWidth(f, "Przerwij", mw.px(14))
	label("Zlecenie")
	moveWin(mw.edTicket, x, row1, ticketW, ch)
	x += ticketW + mw.px(16)
	label("Klient")
	moveWin(mw.edCustomer, x, row1, custW, ch)
	x += custW + mw.px(16)
	label("Zgłoszenie")
	right := W - mw.px(22) - cancelW - mw.px(8) - startW - mw.px(10) - levelW - mw.px(16)
	noteW := max(right-x, mw.px(60))
	moveWin(mw.edNote, x, row1, noteW, ch)
	x += noteW + mw.px(16)
	moveWin(mw.cmbLevel, x, row1, levelW, mw.px(300))
	x += levelW + mw.px(10)
	moveWin(mw.btnStart, x, row1, startW, ch)
	x += startW + mw.px(8)
	moveWin(mw.btnCancel, x, row1, cancelW, ch)

	// testy dodatkowe (zawijane jak WrapPanel)
	row2 := row1 + ch + mw.px(10)
	cbH := max(f.height+mw.px(2), mw.px(20))
	x = mw.px(22)
	L.labels = append(L.labels, labelPos{text: "Testy dodatkowe:", x: x, y: row2 + (cbH-f.height)/2, f: f, c: gray})
	x += strWidth(f, "Testy dodatkowe:") + mw.px(12)
	rowY := row2
	for i, c := range []uintptr{mw.chkDisk, mw.chkCpu, mw.chkNet, mw.chkVol, mw.chkDism, mw.chkOffline} {
		cf := f
		gapBefore := 0
		if i == 5 {
			cf, gapBefore = semi, mw.px(12)
		}
		w := checkWidth(cf, getText(c), mw.dpi)
		if x+gapBefore+w > W-mw.px(22) && x > mw.px(22) {
			x = mw.px(22)
			rowY += cbH + mw.px(6)
			gapBefore = 0
		}
		x += gapBefore
		moveWin(c, x, rowY, w, cbH)
		x += w + mw.px(16)
	}
	L.ctlH = rowY + cbH + mw.px(12) - L.ctlTop
	y = L.ctlTop + L.ctlH + 1

	// pasek postępu
	if mw.progVisible {
		L.progTop, L.progH = y, mw.px(8)+max(f.height, mw.px(8))+mw.px(2)
		y += L.progH
	}

	// stopka: przyciski (zawijane) i komunikat
	fb := []uintptr{mw.btnFix, mw.btnTech, mw.btnCust, mw.btnPdf, mw.btnCompare, mw.btnFolder, mw.btnMore}
	widths := make([]int, len(fb))
	rows, rx := 1, mw.px(22)
	for i, b := range fb {
		widths[i] = buttonWidth(f, getText(b), mw.px(14))
		if rx+widths[i] > W-mw.px(22) && rx > mw.px(22) {
			rows++
			rx = mw.px(22)
		}
		rx += widths[i] + mw.px(8)
	}
	rowH := ch + mw.px(4)
	footerH := 1 + mw.px(8) + rows*rowH + mw.px(8)
	if mw.footerText != "" {
		footerH += mw.px(5) + f.height
	}
	L.footerTop = H - footerH
	rx, ry := mw.px(22), L.footerTop+1+mw.px(8)+mw.px(2)
	for i, b := range fb {
		if rx+widths[i] > W-mw.px(22) && rx > mw.px(22) {
			rx = mw.px(22)
			ry += rowH
		}
		moveWin(b, rx, ry, widths[i], ch)
		rx += widths[i] + mw.px(8)
	}
	L.footerTextY = ry + ch + mw.px(2) + mw.px(5)

	// treść: menu boczne i strona
	L.contentTop = y + mw.px(12)
	L.contentBottom = L.footerTop - mw.px(6)
	ch2 := max(L.contentBottom-L.contentTop, mw.px(60))
	L.navCard = rect{int32(mw.px(14)), int32(L.contentTop), int32(mw.px(14) + mw.px(236)), int32(L.contentTop + ch2)}
	pad := mw.px(6) + 1
	moveWin(mw.nav.h, int(L.navCard.Left)+pad, int(L.navCard.Top)+pad, L.navCard.w()-2*pad, L.navCard.h()-2*pad)
	px0 := int(L.navCard.Right) + mw.px(12)
	L.page = rect{int32(px0), int32(L.contentTop), int32(W - mw.px(14)), int32(L.contentTop + ch2)}
	moveWin(mw.sumDoc.h, px0, L.contentTop, L.page.w(), ch2)
	moveWin(mw.toolsDoc.h, px0, L.contentTop, L.page.w(), ch2)

	// strona kategorii: karta wyników (z listą i szczegółami) i karta danych szczegółowych
	pw, ph := L.page.w(), L.page.h()
	aH := ph
	hasTables := len(mw.catTables) > 0
	if hasTables {
		aH = max((ph-mw.px(8))*3/5, mw.px(150))
	}
	L.cardA = rect{L.page.Left, L.page.Top, L.page.Right, L.page.Top + int32(aH)}
	L.aHeaderH = mw.px(12) + mw.font(170, fwSemiBold).height + mw.px(6)
	inner := aH - L.aHeaderH - mw.px(4)
	// szczegóły wybranego wyniku pod listą (jak rozwinięcie wiersza w DataGrid) - wysokość według treści,
	// najwyżej połowa karty; bez zaznaczenia cała karta to lista
	detH := 0
	if mw.detail >= 0 && mw.detail < len(mw.catFind) {
		detH = min(max(mw.detDoc.height, mw.px(60)), inner/2)
	}
	listH := max(inner-detH-1, mw.px(40))
	if detH == 0 {
		listH = inner
	}
	moveWin(mw.lvFind.h, px0+mw.px(4), int(L.cardA.Top)+L.aHeaderH, pw-mw.px(8), listH)
	L.detTop = int(L.cardA.Top) + L.aHeaderH + listH
	if detH > 0 {
		moveWin(mw.detDoc.h, px0+1, L.detTop+1, pw-2, detH)
	} else {
		L.detTop = -1
	}
	// kolumna „Wynik” do prawej krawędzi listy (bez poziomego paska przewijania)
	if lw := pw - mw.px(8); lw > 0 {
		sbW := metricForDpi(2 /*SM_CXVSCROLL*/, mw.dpi)
		mw.lvFind.setColWidth(2, max(lw-mw.lvFind.colWidth(0)-mw.lvFind.colWidth(1)-sbW-mw.px(2), mw.px(200)))
	}
	if hasTables {
		L.cardC = rect{L.page.Left, L.cardA.Bottom + int32(mw.px(8)), L.page.Right, L.page.Bottom}
		lbl := "Dane szczegółowe:"
		lw := strWidth(semi, lbl)
		cy := int(L.cardC.Top) + mw.px(8)
		L.labels = append(L.labels, labelPos{text: lbl, x: px0 + mw.px(16), y: cy + (ch-f.height)/2, f: semi, c: cText, page: true})
		moveWin(mw.cmbTables, px0+mw.px(16)+lw+mw.px(10), cy, min(max(mw.px(360), pw/2), pw-lw-mw.px(42)), mw.px(300))
		top := cy + ch + mw.px(6)
		moveWin(mw.lvTable.h, px0+mw.px(4), top, pw-mw.px(8), max(int(L.cardC.Bottom)-mw.px(4)-top, mw.px(30)))
	}
	mw.L = L
	mw.updatePageVisibility()
}

func (mw *mainWin) adminChipText() string {
	if mw.isAdmin {
		return "Administrator"
	}
	return "Bez uprawnień administratora"
}

// ---------------------------------------------------------------- rysowanie

func (mw *mainWin) render() {
	cr := clientRect(mw.h)
	W, H := cr.w(), cr.h()
	mw.cv = ensureCanvas(mw.cv, W, H)
	cv := mw.cv
	if cv == nil {
		return
	}
	L := &mw.L
	f := mw.ui()
	cv.fillRect(0, 0, W, H, cBg)

	// nagłówek
	cv.gradientH(0, 0, W, L.hdrH, cBlueDark, cBlue)
	titleF, shopF := mw.font(230, fwSemiBold), mw.font(125, fwNormal)
	ty := (L.hdrH - titleF.height - mw.px(1) - shopF.height) / 2
	cv.text(titleF, "SerwisDiag", mw.px(22), ty, cWhite)
	cv.text(shopF, mw.shopText(), mw.px(22), ty+titleF.height+mw.px(1), hexc("#CFE3F7"))
	if mw.osText != "" {
		c := L.chips[0]
		cv.fillRoundA(int(c.Left), int(c.Top), int(c.Right), int(c.Bottom), float64(c.h())/2, cWhite, cornersAll, 0x26/255.0)
		cv.textBox(mw.font(120, fwNormal), mw.osText, int(c.Left), int(c.Top), int(c.Right), int(c.Bottom), cWhite, dtCenter)
	}
	c := L.chips[1]
	if mw.isAdmin {
		cv.fillRoundA(int(c.Left), int(c.Top), int(c.Right), int(c.Bottom), float64(c.h())/2, cWhite, cornersAll, 0x26/255.0)
	} else {
		cv.fillRound(int(c.Left), int(c.Top), int(c.Right), int(c.Bottom), float64(c.h())/2, cBadgeNav, cornersAll)
	}
	cv.textBox(mw.font(120, fwSemiBold), mw.adminChipText(), int(c.Left), int(c.Top), int(c.Right), int(c.Bottom), cWhite, dtCenter)

	// banery
	for _, b := range L.banners {
		cv.fillRect(0, b.top, W, b.top+b.h, b.bg)
		cv.fillRect(0, b.top+b.h, W, b.top+b.h+1, b.bd)
		cv.block(b.tb, mw.px(22), b.top+(b.h-b.tb.h)/2)
	}

	// pasek danych zlecenia
	cv.fillRect(0, L.ctlTop, W, L.ctlTop+L.ctlH, cWhite)
	cv.fillRect(0, L.ctlTop+L.ctlH, W, L.ctlTop+L.ctlH+1, cBorder)
	for _, l := range L.labels {
		if !l.page {
			cv.text(l.f, l.text, l.x, l.y, l.c)
		}
	}

	// postęp
	if mw.progVisible {
		top := L.progTop + mw.px(8)
		tw := 0
		if mw.progText != "" {
			tw = min(strWidth(f, mw.progText), mw.px(560))
		}
		barR := W - mw.px(22) - tw - mw.px(14)
		bh := mw.px(8)
		by := top + (f.height-bh)/2 - mw.px(2)
		cv.fillRound(mw.px(22), by, barR, by+bh, float64(bh)/2, hexc("#DCE3EC"), cornersAll)
		if mw.progMax > 0 && mw.progVal > 0 {
			fw := (barR - mw.px(22)) * min(mw.progVal, mw.progMax) / mw.progMax
			cv.fillRound(mw.px(22), by, mw.px(22)+max(fw, bh), by+bh, float64(bh)/2, cBlue, cornersAll)
		}
		if tw > 0 {
			cv.textBox(f, mw.progText, W-mw.px(22)-tw, top-mw.px(4), W-mw.px(22), top-mw.px(4)+f.height, cGray2, dtLeft)
		}
	}

	// menu boczne
	n := L.navCard
	cv.card(int(n.Left), int(n.Top), int(n.Right), int(n.Bottom), float64(mw.px(12)), cWhite, cBorder, 1)

	// strona kategorii
	if mw.selected != "summary" && mw.selected != "tools" {
		a := L.cardA
		cv.card(int(a.Left), int(a.Top), int(a.Right), int(a.Bottom), float64(mw.px(12)), cWhite, cBorder, 1)
		hf := mw.font(170, fwSemiBold)
		cv.text(hf, diag.CategoryLabel(mw.selected), int(a.Left)+mw.px(16), int(a.Top)+mw.px(12), cText)
		cv.textBox(f, mw.emptyText(), int(a.Left)+mw.px(16)+strWidth(hf, diag.CategoryLabel(mw.selected))+mw.px(20),
			int(a.Top)+mw.px(12), int(a.Right)-mw.px(16), int(a.Top)+mw.px(12)+hf.height, cGray, dtRight)
		if L.detTop >= 0 {
			cv.fillRect(int(a.Left)+1, L.detTop, int(a.Right)-1, L.detTop+1, cBorder)
		}
		if len(mw.catTables) > 0 {
			t := L.cardC
			cv.card(int(t.Left), int(t.Top), int(t.Right), int(t.Bottom), float64(mw.px(12)), cWhite, cBorder, 1)
			for _, l := range L.labels {
				if l.page {
					cv.text(l.f, l.text, l.x, l.y, l.c)
				}
			}
		}
	}

	// stopka
	cv.fillRect(0, L.footerTop, W, H, cWhite)
	cv.fillRect(0, L.footerTop, W, L.footerTop+1, cBorder)
	if mw.footerText != "" {
		cv.textBox(f, mw.footerText, mw.px(24), L.footerTextY, W-mw.px(22), L.footerTextY+f.height, cGray, dtLeft)
	}
}

func (mw *mainWin) paint() {
	var ps paintStruct
	hdc := call(pBeginPaint, mw.h, uintptr(unsafe.Pointer(&ps)))
	mw.render()
	if mw.cv != nil {
		cr := clientRect(mw.h)
		mw.cv.blit(hdc, cr.w(), cr.h())
	}
	call(pEndPaint, mw.h, uintptr(unsafe.Pointer(&ps)))
}

func (mw *mainWin) shopText() string {
	s := mw.settings
	contact := joinNonEmpty(" · ", s.ShopPhone, s.ShopEmail)
	if contact != "" {
		return s.ShopName + " · " + contact
	}
	return s.ShopName
}

func joinNonEmpty(sep string, items ...string) string {
	var out []string
	for _, s := range items {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, sep)
}

// ---------------------------------------------------------------- komunikaty

func (mw *mainWin) proc(h uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	switch m {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		mw.paint()
		return 0, true
	case wmPrintClient:
		// tło pod zaokrąglonymi przyciskami i polami wyboru (DrawThemeParentBackground)
		if mw.cv == nil {
			mw.render()
		}
		if mw.cv != nil {
			cr := clientRect(h)
			call(pBitBlt, wp, 0, 0, uintptr(cr.w()), uintptr(cr.h()), mw.cv.dc, 0, 0, srcCopy)
		}
		return 0, true
	case wmSize:
		mw.layout()
		mw.redraw()
		return 0, true
	case wmGetMinMaxInfo:
		mm := (*minMaxInfo)(unsafe.Pointer(lp))
		wa := workArea(h)
		mm.MinTrackSize = point{int32(min(mw.px(1040), wa.w())), int32(min(mw.px(680), wa.h()))}
		return 0, true
	case wmDpiChanged:
		mw.dpi = int(wp & 0xFFFF)
		mw.applyFonts()
		r := (*rect)(unsafe.Pointer(lp))
		call(pSetWindowPos, h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.w()), uintptr(r.h()), swpNoZOrder|swpNoActivate)
		mw.layout()
		mw.refreshAll()
		return 0, true
	case wmCtlColorStatic, wmCtlColorBtn:
		return ctlColor(wp, cWhite), true
	case wmDrawItem:
		di := (*drawItemStruct)(unsafe.Pointer(lp))
		f := mw.font(130, fwSemiBold)
		if drawPrimaryButton(lp, f, mw.dpi) {
			_ = di
			return 1, true
		}
		return 0, false
	case wmNotify:
		bf := mw.font(110, fwBold)
		if r, ok := mw.lvFind.notify(lp, bf); ok {
			return r, true
		}
		if r, ok := mw.lvTable.notify(lp, bf); ok {
			return r, true
		}
		return 0, false
	case wmCommand:
		mw.command(int(wp&0xFFFF), int(wp>>16&0xFFFF))
		return 0, true
	case wmTimer:
		switch wp {
		case timerScan:
			mw.tick()
		case timerSelfTest:
			mw.selfTestStep()
		}
		return 0, true
	case wmScanDone:
		mw.scanDone(int(wp))
		return 0, true
	case wmMwBg:
		mw.bgMu.Lock()
		q := mw.bgQueue
		mw.bgQueue = nil
		mw.bgMu.Unlock()
		for _, f := range q {
			if f != nil {
				f()
			}
		}
		return 0, true
	case wmApp + 2:
		select {
		case s := <-mw.osCh:
			mw.osText = s
		default:
		}
		mw.layout()
		mw.redraw()
		releaseMemory("okno po starcie")
		return 0, true
	case wmClose:
		if mw.scanning && mw.run != nil {
			mw.run.scan.Cancel()
		}
		call(pDestroyWindow, h)
		return 0, true
	case wmDestroy:
		mw.cv.free()
		mw.lvFind.destroy()
		mw.lvTable.destroy()
		call(pPostQuitMessage, 0)
		return 0, true
	}
	return 0, false
}

func (mw *mainWin) command(id, code int) {
	switch id {
	case ctlStart:
		mw.startScan()
	case ctlCancel:
		mw.cancelScan()
	case ctlChkOffline:
		if code == bnClicked {
			mw.syncOffline()
		}
	case ctlTables:
		if code == cbnSelChange {
			mw.updateTable()
		}
	case ctlAbout, ctlKbAbout:
		mw.showAbout()
	case ctlSettings:
		mw.showSettings()
	case ctlElevate:
		mw.elevate()
	case ctlTech:
		if mw.paths != nil {
			openPath(mw.paths.Technical)
		}
	case ctlCust:
		if mw.paths != nil {
			openPath(mw.paths.Customer)
		}
	case ctlPdf:
		mw.savePdf()
	case ctlFolder:
		dir := diag.ResolveReportDir(mw.opt.Root, mw.settings)
		shellOpen("explorer.exe", `"`+dir+`"`)
	case ctlMore:
		mw.moreMenu()
	case ctlFix:
		mw.showFixes()
	case ctlCompare:
		mw.showCompare()
	case ctlUpdate:
		mw.update(false)
	case ctlUpdNotes:
		if r := mw.updRel; r != nil {
			msgBox(mw.h, "Co nowego w wersji "+r.Latest+":\n\n"+r.Notes, "Nowa wersja", mbIconInfo)
		}
	}
}

// elevate: nowa instancja z uprawnieniami administratora przejmuje wpisane dane zlecenia.
func (mw *mainWin) elevate() {
	var args []string
	for _, p := range [][2]string{{"Ticket", getText(mw.edTicket)}, {"Customer", getText(mw.edCustomer)}, {"Note", getText(mw.edNote)}} {
		if v := strings.TrimSpace(p[1]); v != "" {
			args = append(args, "-"+p[0], diag.FormatCmdArg(v))
		}
	}
	if isChecked(mw.chkOffline) {
		args = append(args, "-Offline")
	}
	err := windows.ShellExecute(windows.Handle(mw.h), u16("runas"), u16(mw.opt.Exe), u16(strings.Join(args, " ")), u16(filepath.Dir(mw.opt.Exe)), windows.SW_SHOWNORMAL)
	if err != nil {
		msgBox(mw.h, "Nie uruchomiono nowej instancji z uprawnieniami administratora (anulowano UAC).", "SerwisDiag", mbIconInfo)
		return
	}
	call(pPostMessageW, mw.h, wmClose, 0, 0)
}

func (mw *mainWin) moreMenu() {
	m := call(pCreatePopupMenu)
	flags := uintptr(mfString)
	if mw.result == nil {
		flags |= mfGrayed
	}
	call(pAppendMenuW, m, flags, ctlMenuCopy, u16p("Kopiuj podsumowanie do schowka"))
	call(pAppendMenuW, m, flags, ctlMenuAnon, u16p("Raport anonimowy (do pokazania na forum)"))
	call(pAppendMenuW, m, mfSeparator, 0, 0)
	call(pAppendMenuW, m, mfString, ctlMenuUndo, u16p("Cofnij naprawy z dziennika..."))
	call(pAppendMenuW, m, mfString, ctlMenuBackup, u16p("Kopia przed reinstalacją Windows..."))
	r := windowRect(mw.btnMore)
	cmd := call(pTrackPopupMenu, m, tpmLeftAlign|tpmBottomAlign|tpmReturnCmd|tpmNoNotify, uintptr(r.Left), uintptr(r.Top), 0, mw.h, 0)
	call(pDestroyMenu, m)
	switch cmd {
	case ctlMenuCopy:
		if mw.result != nil {
			if setClipboard(mw.h, diag.TextReport(mw.result, mw.settings, false)) {
				mw.setFooter("Skopiowano podsumowanie do schowka.")
			} else {
				mw.setFooter("Nie udało się skopiować do schowka.")
			}
		}
	case ctlMenuAnon:
		mw.anonymous()
	case ctlMenuUndo:
		mw.showUndo("")
	case ctlMenuBackup:
		mw.showBackup()
	}
}

func (mw *mainWin) setFooter(s string) {
	mw.footerText = s
	mw.layout()
	mw.redraw()
}

// savePdf: raport dla klienta jako PDF (obok raportu HTML) i otwarcie pliku (Invoke-SdUiPdf).
func (mw *mainWin) savePdf() {
	if mw.paths == nil {
		return
	}
	if mw.pdfPath == "" || !fileExists(mw.pdfPath) {
		mw.setFooter("Zapisywanie PDF...")
		var p string
		var err error
		withWaitCursor(func() { p, err = diag.SavePDF(mw.paths.Customer) })
		if err != nil {
			mw.setFooter("Nie udało się zapisać PDF.")
			msgBox(mw.h, "Nie udało się zapisać raportu jako PDF:\n"+err.Error(), "Zapisz PDF", mbIconWarning)
			return
		}
		mw.pdfPath = p
	}
	mw.setFooter("PDF dla klienta: " + mw.pdfPath)
	openPath(mw.pdfPath)
}

// anonymous: raport anonimowy; podsumowanie tekstowe do schowka (Invoke-SdUiAnonymous).
func (mw *mainWin) anonymous() {
	if mw.result == nil {
		return
	}
	dir := diag.ResolveReportDir(mw.opt.Root, mw.settings)
	var html, txt string
	var err error
	withWaitCursor(func() { html, txt, err = diag.SaveAnonymousReports(mw.result, dir) })
	if err != nil {
		msgBox(mw.h, "Nie udało się zapisać raportu anonimowego: "+err.Error(), "Raport anonimowy", mbIconError)
		return
	}
	copied := false
	if b, e := os.ReadFile(txt); e == nil {
		copied = setClipboard(mw.h, strings.TrimPrefix(string(b), "\ufeff"))
	}
	mw.setFooter("Raport anonimowy: " + html)
	msg := "Zapisano raport anonimowy - bez nazwy komputera i kont, numerów seryjnych, kluczy, adresów IP/MAC, nazw sieci Wi-Fi i danych klienta:\n" + html
	if copied {
		msg += "\n\nKrótkie podsumowanie skopiowano do schowka - możesz je wkleić w poście na forum."
	}
	msg += "\n\nPrzed publikacją przejrzyj raport: program usuwa znane dane identyfikujące, ale nie gwarantuje, że wykryje wszystkie."
	msgBox(mw.h, msg, "Raport anonimowy", mbIconInfo)
	openPath(html)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
