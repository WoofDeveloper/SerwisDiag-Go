package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Sterowniki (Show-SdUiDrivers, Show-SdUiMissingDrivers, Invoke-SdUiVendorPage) i DISM RestoreHealth z płyty
// albo obrazu ISO (Invoke-SdUiDismFromMedia). Wyszukiwanie, punkt przywracania i instalacja sterowników działają
// w tle; okno co 300 ms pokazuje stan, a wynik wyszukiwania (sesja Windows Update) służy potem do instalacji.

const (
	wmDrvBg  = wmApp + 21 // wynik pracy w tle okna sterowników
	timerDrv = 8

	ctlDrvRestore = 741 + iota
	ctlDrvSearch
	ctlDrvInstall
	ctlDrvClose
	ctlMissList
	ctlMissCatalog
	ctlMissCopy
	ctlMissVendor
	ctlMissClose
)

type driversDialog struct {
	mw   *mainWin
	d    *dialog
	list *doc
	chk  uintptr
	btns map[int]uintptr

	phase   string // search | restore | install
	busy    bool
	anim    int
	status  string
	ws      *diag.WuSearch
	items   []diag.WuDriver
	checked map[string]bool
	ids     []string // sterowniki wybrane do instalacji

	mu     sync.Mutex
	closed bool
	bgText string
	bgDone func() // wynik pracy w tle - wykonywany w wątku okien
}

// lastDrvDialog: otwarte okno sterowników (autotest czeka na koniec wyszukiwania).
var lastDrvDialog *driversDialog

func (mw *mainWin) showDrivers() {
	if isChecked(mw.chkOffline) {
		msgBox(mw.h, "Włączony jest tryb bez internetu. Wyszukiwanie sterowników wymaga internetu - odznacz „tryb bez internetu”.", "Sterowniki", mbIconInfo)
		return
	}
	dd := &driversDialog{mw: mw, checked: map[string]bool{}, btns: map[int]uintptr{}}
	lastDrvDialog = dd
	defer func() { lastDrvDialog = nil }()
	d := newDialog(mw.h, "Sterowniki z Windows Update", 780, 560, true)
	d.minW, d.minH = 600, 420
	dd.d = d
	dd.list = newDoc(d.h, cWhite, dd.layoutList)
	dd.list.dpi = d.dpi
	dd.list.onClick = dd.toggle
	dd.chk = newCheck(d.h, ctlDrvRestore, "Przed instalacją utwórz punkt przywracania systemu (zalecane)")
	setChecked(dd.chk, true)
	for id, t := range map[int]string{ctlDrvSearch: "Wyszukaj ponownie", ctlDrvInstall: "Zainstaluj zaznaczone", ctlDrvClose: "Zamknij"} {
		dd.btns[id] = newButton(d.h, id, t)
	}
	enableWin(dd.btns[ctlDrvSearch], false)
	enableWin(dd.btns[ctlDrvInstall], false)
	fonts := func() {
		f := d.font(130, fwNormal)
		setFont(dd.chk, f)
		for _, b := range dd.btns {
			setFont(b, f)
		}
		dd.list.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = dd.paint
	d.onSize = func(w, h int) { dd.place(w, h) }
	d.onCmd = dd.command
	d.canClose = func() bool {
		if dd.busy && dd.phase != "search" {
			msgBox(d.h, "Trwa tworzenie punktu przywracania lub instalacja sterowników - poczekaj na zakończenie.", "Sterowniki", mbIconInfo)
			return false
		}
		return true
	}
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		switch m {
		case wmTimer:
			if wp == timerDrv {
				dd.tick()
				return 0, true
			}
		case wmDrvBg:
			dd.mu.Lock()
			f := dd.bgDone
			dd.bgDone = nil
			dd.mu.Unlock()
			if f != nil {
				f()
			}
			return 0, true
		}
		return 0, false
	}
	dd.search()
	call(pSetTimer, d.h, timerDrv, 300, 0)
	d.show()
	call(pKillTimer, d.h, timerDrv)
	// trwające wyszukiwanie kończy się w tle i samo zwalnia swój wynik (okno jest już zamknięte)
	dd.mu.Lock()
	dd.closed = true
	pending := dd.bgDone
	dd.bgDone = nil
	dd.mu.Unlock()
	if pending != nil {
		pending()
	}
	if ws := dd.ws; ws != nil {
		go ws.Release()
	}
}

func (dd *driversDialog) isClosed() bool {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	return dd.closed
}

// background uruchamia pracę w gorutynie; wynik (finish) wykonuje się w wątku okien, a po zamknięciu okna -
// od razu (finish sprawdza wtedy isClosed i tylko zwalnia zasoby).
func (dd *driversDialog) background(phase string, work func() func()) {
	dd.phase, dd.busy = phase, true
	enableWin(dd.btns[ctlDrvInstall], false)
	enableWin(dd.btns[ctlDrvSearch], false)
	h := dd.d.h
	go func() {
		var finish func()
		func() {
			defer func() {
				if p := recover(); p != nil {
					logError(fmt.Sprintf("okno sterowników (%s): %v", phase, p))
					msg := fmt.Sprint(p)
					finish = func() {
						if !dd.isClosed() {
							dd.done("Błąd programu: " + msg)
						}
					}
				}
			}()
			finish = work()
		}()
		dd.mu.Lock()
		if dd.closed {
			dd.mu.Unlock()
			if finish != nil {
				finish()
			}
			return
		}
		dd.bgDone = finish
		dd.mu.Unlock()
		postMsg(h, wmDrvBg, 0, 0)
	}()
}

// done: koniec pracy w tle - pasek postępu znika, „Wyszukaj ponownie” jest znowu dostępne.
func (dd *driversDialog) done(status string) {
	dd.busy = false
	enableWin(dd.btns[ctlDrvSearch], true)
	dd.setStatus(status)
}

func (dd *driversDialog) search() {
	dd.items = nil
	dd.list.relayout()
	dd.setStatus("Wyszukiwanie sterowników w Windows Update (zwykle kilkanaście sekund)...")
	dd.background("search", func() func() {
		ws := diag.WuDriverSearch()
		return func() {
			if dd.isClosed() {
				ws.Release()
				return
			}
			dd.searched(ws)
		}
	})
}

func (dd *driversDialog) searched(ws *diag.WuSearch) {
	if old := dd.ws; old != nil {
		go old.Release()
	}
	dd.ws = ws
	clear(dd.checked)
	if ws.Error != "" {
		dd.done("Nie udało się wyszukać sterowników: " + ws.Error + "\nSprawdź połączenie z internetem i usługę Windows Update.")
		return
	}
	if len(ws.Items) == 0 {
		dd.done("Windows Update nie ma nowszych sterowników dla urządzeń tego komputera.")
		return
	}
	for _, it := range diag.SelectDriversToInstall(ws.Items) {
		dd.checked[it.UpdateID] = true
	}
	dd.items = diag.SortDrivers(ws.Items)
	st := fmt.Sprintf("Znaleziono: %s.", diag.FmtCount(len(dd.items), "sterownik", "sterowniki", "sterowników"))
	admin := win.IsAdmin()
	if !admin {
		st += " Instalacja wymaga uprawnień administratora - użyj „Uruchom jako administrator” w oknie programu."
	}
	dd.list.relayout()
	dd.done(st)
	enableWin(dd.btns[ctlDrvInstall], admin)
}

// install: potwierdzenie, (opcjonalnie) punkt przywracania, potem pobranie i instalacja zaznaczonych.
func (dd *driversDialog) install() {
	var ids []string
	size := 0.0
	for _, it := range dd.items {
		if dd.checked[it.UpdateID] {
			ids = append(ids, it.UpdateID)
			size += it.SizeBytes
		}
	}
	if len(ids) == 0 {
		msgBox(dd.d.h, "Zaznacz sterowniki do zainstalowania.", "Sterowniki", mbIconInfo)
		return
	}
	restore := isChecked(dd.chk)
	q := fmt.Sprintf("Zainstalować %s (do pobrania ok. %s)?", diag.FmtCount(len(ids), "sterownik", "sterowniki", "sterowników"), diag.FmtSize(size, 1))
	if restore {
		q += "\n\nNajpierw zostanie utworzony punkt przywracania systemu."
	}
	q += "\n\nPodczas instalacji ekran może na chwilę zgasnąć, a połączenie sieciowe zostać przerwane."
	if msgBox(dd.d.h, q, "Instalacja sterowników", mbIconQuestion|mbYesNo) != idYes {
		return
	}
	dd.ids = ids
	if !restore {
		dd.startInstall()
		return
	}
	dd.setStatus("Tworzenie punktu przywracania (może potrwać minutę)...")
	dd.background("restore", func() func() {
		err := win.CreateRestorePoint("SerwisDiag - przed instalacją sterowników", win.RestoreDeviceDriverInstall)
		return func() { dd.restored(err) }
	})
}

func (dd *driversDialog) restored(err error) {
	if err == nil {
		dd.startInstall()
		return
	}
	dd.busy = false
	msg := "Nie udało się utworzyć punktu przywracania:\n" + err.Error() + "\n\nZainstalować sterowniki bez punktu przywracania?"
	if msgBox(dd.d.h, msg, "Punkt przywracania", mbIconWarning|mbYesNo) == idYes {
		dd.startInstall()
		return
	}
	dd.done("Instalację anulowano.")
	enableWin(dd.btns[ctlDrvInstall], true)
}

func (dd *driversDialog) startInstall() {
	dd.setStatus("Pobieranie i instalowanie sterowników... nie zamykaj okna.")
	ws, ids := dd.ws, slices.Clone(dd.ids)
	dd.background("install", func() func() {
		out := ws.Install(ids, func(t string) {
			dd.mu.Lock()
			dd.bgText = t
			dd.mu.Unlock()
		})
		return func() { dd.installed(out) }
	})
}

func (dd *driversDialog) installed(out diag.WuInstall) {
	var lines []string
	if out.Error != "" {
		lines = append(lines, "Błąd: "+out.Error)
	}
	ok := 0
	for _, x := range out.Results {
		lines = append(lines, diag.WuResultText(x.ResultCode)+" - "+x.Title)
		if x.ResultCode == 2 {
			ok++
		}
	}
	head := fmt.Sprintf("Instalacja zakończona: %d z %d.", ok, len(out.Results))
	if out.RebootRequired {
		head += " Wymagane ponowne uruchomienie komputera."
	}
	dd.items = nil
	dd.list.relayout()
	dd.done(strings.TrimRight(head+"\n"+strings.Join(lines, "\n"), "\n"))
	dd.mw.setFooter("Sterowniki z Windows Update: " + head)
	if out.RebootRequired {
		msgBox(dd.d.h, head+"\n\nUruchom komputer ponownie, a potem wykonaj diagnostykę jeszcze raz.", "Sterowniki", mbIconInfo)
	}
}

func (dd *driversDialog) tick() {
	if !dd.busy {
		return
	}
	dd.anim++
	if dd.phase == "install" {
		dd.mu.Lock()
		t := dd.bgText
		dd.mu.Unlock()
		if t != "" {
			dd.status = t + " Nie zamykaj okna."
		}
	}
	cr := clientRect(dd.d.h)
	dd.place(cr.w(), cr.h())
}

func (dd *driversDialog) command(id, code int) {
	switch id {
	case ctlDrvSearch:
		if !dd.busy {
			dd.search()
		}
	case ctlDrvInstall:
		if !dd.busy {
			dd.install()
		}
	case ctlDrvClose:
		if dd.d.canClose() {
			dd.d.close()
		}
	}
}

func (dd *driversDialog) toggle(id int) {
	i := id - 1000
	if dd.busy || i < 0 || i >= len(dd.items) {
		return
	}
	k := dd.items[i].UpdateID
	dd.checked[k] = !dd.checked[k]
	dd.list.relayout()
}

func (dd *driversDialog) setStatus(s string) {
	dd.status = s
	cr := clientRect(dd.d.h)
	dd.place(cr.w(), cr.h())
}

func (dd *driversDialog) header() (title, desc textBlock) {
	d := dd.d
	w := clientRect(d.h).w() - 2*d.px(20)
	title = layoutText([]span{{text: "Sterowniki z Windows Update", font: d.font(160, fwSemiBold), color: cText}}, w, 0)
	desc = layoutText([]span{{text: "Oficjalne, podpisane sterowniki dopasowane do urządzeń tego komputera. Zaznaczone są sterowniki zalecane i te, które naprawiają urządzenia z błędami; opcjonalne zaznacz sam.", font: d.font(130, fwNormal), color: cGray}}, w, 0)
	return
}

func (dd *driversDialog) statusBlock(w int) textBlock {
	if dd.status == "" {
		return textBlock{}
	}
	var spans []span
	for i, l := range strings.Split(dd.status, "\n") {
		spans = append(spans, span{text: l, font: dd.d.font(130, fwNormal), color: cGray2, br: i > 0})
	}
	return layoutText(spans, w, 0)
}

// place: układ jak w XAML - lista od opisu do pola wyboru, pod nim komunikat i pasek postępu, przyciski na dole.
func (dd *driversDialog) place(w, h int) {
	d := dd.d
	if w <= 0 || h <= 0 {
		return
	}
	p := d.px(20)
	f := d.font(130, fwNormal)
	bh := d.px(32)
	by := h - p - bh
	rx := w - p - d.px(100)
	moveWin(dd.btns[ctlDrvClose], rx, by, d.px(100), bh)
	for _, id := range []int{ctlDrvInstall, ctlDrvSearch} {
		bw := buttonWidth(f, getText(dd.btns[id]), d.px(14))
		rx -= d.px(8) + bw
		moveWin(dd.btns[id], rx, by, bw, bh)
	}
	st := dd.statusBlock(w - 2*p)
	progH := 0
	if dd.busy {
		progH = d.px(12)
	}
	y := by - d.px(12) - progH - st.h - d.px(8) - d.px(20)
	moveWin(dd.chk, p, y, checkWidth(f, getText(dd.chk), d.dpi), d.px(20))
	t, ds := dd.header()
	top := p + t.h + d.px(4) + ds.h + d.px(10)
	moveWin(dd.list.h, p+1, top+1, w-2*p-2, max(y-d.px(10)-top-2, d.px(40)))
	invalidate(d.h)
}

func (dd *driversDialog) paint(cv *canvas, w, h int) {
	d := dd.d
	p := d.px(20)
	t, ds := dd.header()
	cv.block(t, p, p)
	cv.block(ds, p, p+t.h+d.px(4))
	lr := windowRectIn(dd.list.h, d.h)
	cv.card(int(lr.Left)-1, int(lr.Top)-1, int(lr.Right)+1, int(lr.Bottom)+1, float64(d.px(8)), cWhite, cBorder, 1)
	by := h - p - d.px(32)
	st := dd.statusBlock(w - 2*p)
	progH := 0
	if dd.busy {
		progH = d.px(12)
	}
	sy := by - d.px(12) - progH - st.h
	cv.block(st, p, sy)
	if dd.busy {
		drawProgress(cv, p, sy+st.h+d.px(6), w-2*p, d.px(6), -1, dd.anim)
	}
}

func (dd *driversDialog) layoutList(dc *doc, width int) int {
	x := dc.px(12)
	w := width - 2*x
	y := dc.px(10)
	fb, small := dc.font(130, fwSemiBold), dc.font(120, fwNormal)
	cb := dc.px(16)
	for i, it := range dd.items {
		tb := layoutText([]span{{text: it.Title, font: fb, color: cText}, {text: diag.DriverInfo(it), font: small, color: cGray, br: true}}, w-cb-dc.px(10)-dc.px(8), 0)
		y += dc.px(4)
		row := dc.rect(x-dc.px(6), y-dc.px(2), w+dc.px(12), tb.h+dc.px(4), none, none, 0, float64(dc.px(6)))
		if !dd.busy {
			row.id, row.hoverFill = 1000+i, cHover
		}
		dc.check(x, y+(fb.height-cb)/2, cb, dd.checked[it.UpdateID], dd.busy)
		dc.block(tb, x+cb+dc.px(10), y)
		y += tb.h + dc.px(6)
	}
	return y + dc.px(10)
}

// ---------------------------------------------------------------- brakujące sterowniki (Show-SdUiMissingDrivers)

// selfTestMissing: przykładowe urządzenia do zrzutu okna, gdy komputer nie ma urządzeń z błędami.
var selfTestMissing = []diag.MissingDevice{
	{Name: "Kontroler sieci Ethernet", Code: 28, Problem: "Sterowniki tego urządzenia nie są zainstalowane.", Vendor: "Realtek", HardwareID: `PCI\VEN_10EC&DEV_8168&SUBSYS_86771043&REV_15`},
	{Name: "Nieznane urządzenie", Code: 28, Problem: "Sterowniki tego urządzenia nie są zainstalowane.", Vendor: "", HardwareID: `ACPI\VEN_INT&DEV_33A0`},
	{Name: "Kamera USB", Code: 10, Problem: "Nie można uruchomić tego urządzenia.", Vendor: "Chicony", HardwareID: `USB\VID_04F2&PID_B6DD&REV_0006`},
}

func (mw *mainWin) showMissingDrivers() {
	var devs []diag.MissingDevice
	withWaitCursor(func() { devs = diag.MissingDevices() })
	if len(devs) == 0 && mw.opt.SelfTest {
		devs = selfTestMissing
	}
	if len(devs) == 0 {
		msgBox(mw.h, "Wszystkie urządzenia mają sterowniki i działają poprawnie (Menedżer urządzeń nie zgłasza błędów).", "Brakujące sterowniki", mbIconInfo)
		return
	}
	d := newDialog(mw.h, "Brakujące sterowniki", 880, 480, true)
	d.minW, d.minH = 640, 360
	lv := newListView(d.h, ctlMissList, d.dpi)
	defer lv.destroy()
	lv.setColumns([]string{"Urządzenie", "Kod", "Problem", "Producent", "Identyfikator"}, nil)
	rows := make([][]string, len(devs))
	for i, dv := range devs {
		rows[i] = []string{dv.Name, strconv.Itoa(dv.Code), dv.Problem, dv.Vendor, dv.HardwareID}
	}
	lv.setRows(rows, nil)
	btns := map[int]uintptr{}
	for id, t := range map[int]string{ctlMissCatalog: "Szukaj w Katalogu Microsoft", ctlMissCopy: "Kopiuj identyfikator", ctlMissVendor: "Strona producenta", ctlMissClose: "Zamknij"} {
		btns[id] = newButton(d.h, id, t)
	}
	fonts := func() {
		f := d.font(130, fwNormal)
		setFont(lv.h, f)
		for _, b := range btns {
			setFont(b, f)
		}
		lv.dpi = d.dpi
		lv.setRowHeight()
		for i, cw := range lv.autoWidths(f, d.font(130, fwSemiBold), d.px(420)) {
			lv.setColWidth(i, cw)
		}
	}
	fonts()
	d.fonts = fonts
	lv.selectRow(0)
	const title = "Urządzenia bez sterownika lub z błędem"
	const info = "Zaznacz urządzenie i wyszukaj sterownik w Katalogu Microsoft Update po identyfikatorze sprzętu. Pobrany plik .cab rozpakuj i wskaż w Menedżerze urządzeń (Aktualizuj sterownik > Przeglądaj mój komputer)."
	header := func(w int) (textBlock, textBlock) {
		return layoutText([]span{{text: title, font: d.font(160, fwSemiBold), color: cText}}, w-2*d.px(20), 0),
			layoutText([]span{{text: info, font: d.font(130, fwNormal), color: cGray}}, w-2*d.px(20), 0)
	}
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(20)
		t, ds := header(w)
		cv.block(t, p, p)
		cv.block(ds, p, p+t.h+d.px(4))
		lr := windowRectIn(lv.h, d.h)
		cv.card(int(lr.Left)-1, int(lr.Top)-1, int(lr.Right)+1, int(lr.Bottom)+1, 0, cWhite, cBorder, 1)
	}
	d.onSize = func(w, h int) {
		p := d.px(20)
		f := d.font(130, fwNormal)
		bh := d.px(32)
		by := h - p - bh
		rx := w - p - d.px(100)
		moveWin(btns[ctlMissClose], rx, by, d.px(100), bh)
		for _, id := range []int{ctlMissVendor, ctlMissCopy, ctlMissCatalog} {
			bw := buttonWidth(f, getText(btns[id]), d.px(14))
			rx -= d.px(8) + bw
			moveWin(btns[id], rx, by, bw, bh)
		}
		t, ds := header(w)
		top := p + t.h + d.px(4) + ds.h + d.px(10)
		moveWin(lv.h, p+1, top+1, w-2*p-2, max(by-d.px(12)-top-2, d.px(60)))
		invalidate(d.h)
	}
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		if m == wmNotify {
			return lv.notify(lp, nil)
		}
		return 0, false
	}
	selectedID := func() string {
		if i := lv.selected(); i >= 0 && i < len(devs) {
			return devs[i].HardwareID
		}
		return ""
	}
	d.onCmd = func(id, code int) {
		switch id {
		case ctlMissCatalog, idOK: // „Szukaj w Katalogu Microsoft” to przycisk domyślny (Enter)
			hw := selectedID()
			if hw == "" {
				return
			}
			if u := diag.CatalogURL(hw); u != "" {
				if err := shellOpen(u, ""); err != nil {
					msgBox(d.h, "Nie udało się otworzyć przeglądarki: "+err.Error(), "Błąd", mbIconError)
				}
			}
		case ctlMissCopy:
			if hw := selectedID(); hw != "" {
				setClipboard(d.h, hw)
			}
		case ctlMissVendor:
			mw.vendorPage(d.h)
		case ctlMissClose:
			d.close()
		}
	}
	d.show()
}

// vendorPage odpowiada Invoke-SdUiVendorPage: strona ze sterownikami producenta komputera (albo płyty głównej);
// do schowka trafia numer seryjny albo model do wpisania na stronie.
func (mw *mainWin) vendorPage(owner uintptr) {
	var vm bool
	var info diag.SupportPage
	withWaitCursor(func() {
		if vm = diag.IsVirtualMachine(); !vm {
			info = diag.ComputerSupportPage()
		}
	})
	if vm {
		msgBox(owner, "To maszyna wirtualna - sterowniki dostarcza platforma wirtualizacji (np. Hyper-V, VMware Tools, VirtualBox Guest Additions).", "Strona producenta", mbIconInfo)
		return
	}
	if info.CopyText != "" {
		setClipboard(owner, info.CopyText)
	}
	var msg string
	if info.IsSearch {
		msg = "Nie rozpoznano producenta komputera - otwieram wyszukiwarkę sterowników dla: " + info.Vendor + "."
	} else {
		msg = "Otwieram stronę ze sterownikami: " + info.Vendor
		if info.Board {
			msg += " (producent płyty głównej - komputer składany)"
		}
		msg += "."
	}
	if info.CopyText != "" {
		msg += "\n\nDo schowka skopiowano " + info.CopyLabel + ": " + info.CopyText + "\nWklej go (Ctrl+V) w polu wyszukiwania na stronie producenta."
	}
	msgBox(owner, msg, "Strona producenta", mbIconInfo)
	if mw.opt.SelfTest {
		return // autotest nie otwiera przeglądarki
	}
	if err := shellOpen(info.URL, ""); err != nil {
		msgBox(owner, "Nie udało się otworzyć przeglądarki: "+err.Error(), "Błąd", mbIconError)
	}
}

// ---------------------------------------------------------------- DISM z płyty / ISO (Invoke-SdUiDismFromMedia)

// dismFromMedia: DISM RestoreHealth bez internetu - pliki naprawcze z obrazu ISO albo nośnika instalacyjnego tej samej
// wersji Windows. Obraz ISO podłączony przez program zostaje odłączony, gdy naprawa się nie odbędzie.
func dismFromMedia(mw *mainWin) error {
	path := pickPath(mw.h, "Wskaż obraz ISO Windows albo plik install.wim / install.esd (folder sources na nośniku instalacyjnym)",
		"Obraz Windows (*.iso;*.wim;*.esd)", "*.iso;*.wim;*.esd", false, "")
	if path == "" {
		return nil
	}
	var img diag.InstallImage
	var sel diag.ImageSelection
	withWaitCursor(func() {
		img = diag.ResolveInstallImage(path)
		if img.Error != "" {
			return
		}
		eds, err := diag.ImageEditions(img.ImagePath)
		if err != nil {
			img.Error = "Nie udało się odczytać obrazu: " + err.Error()
			return
		}
		ed, build, arch, lang := diag.SystemForImage()
		sel = diag.SelectImageIndex(eds, ed, build, arch, lang)
	})
	dismount := func() {
		if img.MountedISO != "" {
			if err := win.DismountISO(img.MountedISO); err != nil {
				logError("odłączenie obrazu ISO: " + err.Error())
			}
		}
	}
	if img.Error != "" {
		dismount()
		msgBox(mw.h, img.Error, "DISM z płyty/ISO", mbIconWarning)
		return nil
	}
	if sel.Index <= 0 {
		dismount()
		why := strings.Join(sel.Problems, "\n")
		if why == "" {
			why = "brak danych o obrazie"
		}
		msgBox(mw.h, "Ten obraz nie nadaje się do naprawy tego systemu:\n"+why, "DISM z płyty/ISO", mbIconWarning)
		return nil
	}
	msg := fmt.Sprintf("Uruchomić naprawę magazynu składników z obrazu?\n\nObraz: %s\nEdycja: %s (indeks %d, kompilacja %d)", img.ImagePath, sel.Image.Name, sel.Index, sel.Image.Build)
	if len(sel.Problems) > 0 {
		msg += "\n\nUwagi:\n" + strings.Join(sel.Problems, "\n")
	}
	msg += "\n\nNaprawa trwa zwykle kilkanaście minut i nie korzysta z internetu."
	if msgBox(mw.h, msg, "DISM z płyty/ISO", mbIconQuestion|mbYesNo) != idYes {
		dismount()
		return nil
	}
	tail := ""
	if img.MountedISO != "" {
		tail = " & echo. & echo Po zakonczeniu mozna odlaczyc obraz ISO: Eksplorator, prawy przycisk na napedzie - Wysun."
	}
	return shellOpen("cmd.exe", "/k DISM /Online /Cleanup-Image /RestoreHealth "+diag.DismSourceArg(img.ImagePath, sel.Index)+" /LimitAccess"+tail)
}
