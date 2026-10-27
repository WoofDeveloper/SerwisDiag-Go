package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/diag"
)

// quietMessages: autotest - komunikaty nie pokazują się (jak Show-SdMessage przy SelfTest).
var quietMessages bool

// msgBox odpowiada Show-SdMessage; zwraca idOK, idYes albo idNo.
func msgBox(owner uintptr, text, title string, flags uint32) int {
	if quietMessages {
		return idOK
	}
	return int(call(pMessageBoxW, owner, u16p(text), u16p(title), uintptr(flags)))
}

// openPath otwiera plik albo adres skojarzonym programem (Start-Process).
func openPath(p string) {
	_ = windows.ShellExecute(0, u16("open"), u16(p), nil, nil, windows.SW_SHOWNORMAL)
}

// shellOpen uruchamia program z parametrami (Start-Process -ArgumentList); błąd - komunikat.
func shellOpen(file, args string) error {
	var a *uint16
	if args != "" {
		a = u16(args)
	}
	return windows.ShellExecute(0, u16("open"), u16(file), a, nil, windows.SW_SHOWNORMAL)
}

func setClipboard(owner uintptr, text string) bool {
	u, err := windows.UTF16FromString(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))
	if err != nil {
		return false
	}
	for i := 0; i < 5; i++ {
		if call(pOpenClipboard, owner) != 0 {
			break
		}
		if i == 4 {
			return false
		}
		windows.SleepEx(50, false)
	}
	defer call(pCloseClipboard)
	call(pEmptyClipboard)
	size := uintptr(len(u) * 2)
	hmem := call(pGlobalAlloc, gmemMoveable, size)
	if hmem == 0 {
		return false
	}
	p := call(pGlobalLock, hmem)
	if p == 0 {
		call(pGlobalFree, hmem)
		return false
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	call(pGlobalUnlock, hmem)
	if call(pSetClipboardData, cfUnicodeText, hmem) == 0 {
		call(pGlobalFree, hmem)
		return false
	}
	return true
}

// withWaitCursor: kursor oczekiwania na czas krótkiej operacji w wątku okien.
func withWaitCursor(fn func()) {
	old := call(pSetCursor, loadCursor(idcWait))
	defer call(pSetCursor, old)
	fn()
}

// ---------------------------------------------------------------- okno dialogowe (Window z ShowDialog)

const dlgClass = "SerwisDiagGoDialog"

type dialog struct {
	h      uintptr
	owner  uintptr
	dpi    int
	done   bool
	bg     col
	minW   int
	minH   int
	cv     *canvas
	paint  func(cv *canvas, w, h int)
	onCmd  func(id, code int)
	onSize func(w, h int)
	onKey  func(m uint32, wp, lp uintptr) bool // klawiatura (test klawiatury); true - obsłużone
	fonts  func()
	// modeless: okno niemodalne (okno wyników monitora) - zamknięcie niszczy okno, potem onClosed
	modeless bool
	onClosed func()
	// onMsg: pozostałe komunikaty (zegar, wyniki pracy w tle); true - obsłużone
	onMsg func(m uint32, wp, lp uintptr) (uintptr, bool)
	// canClose: false - okno zostaje otwarte (np. trwa naprawa)
	canClose func() bool
}

// newDialog tworzy okno dialogowe o rozmiarze w×h (jednostki WPF), wyśrodkowane nad owner.
func newDialog(owner uintptr, title string, w, h int, resizable bool) *dialog {
	registerClass(dlgClass, idcArrow, 0)
	d := &dialog{owner: owner, bg: cBg, dpi: dpiOf(owner)}
	style := uint32(wsCaption | wsSysMenu | wsClipChildren)
	if resizable {
		style |= wsThickFrame | wsMaximizeBox
	}
	s := sc(d.dpi)
	ww, hh := s.px(w), s.px(h)
	or := windowRect(owner)
	wa := workArea(owner)
	ww, hh = min(ww, wa.w()), min(hh, wa.h())
	x, y := int(or.Left)+(or.w()-ww)/2, int(or.Top)+(or.h()-hh)/2
	switch {
	case owner != 0 && or.Left < -2000:
		// okno główne autotestu poza ekranem - okno dialogowe też (bez migania na ekranie użytkownika)
	case owner == 0 || or.w() <= 0:
		x, y = int(wa.Left)+(wa.w()-ww)/2, int(wa.Top)+(wa.h()-hh)/2
		fallthrough
	default:
		x = min(max(x, int(wa.Left)), int(wa.Right)-ww)
		y = min(max(y, int(wa.Top)), int(wa.Bottom)-hh)
	}
	d.h = createWindow(d, dlgClass, title, style, wsExDlgModalFrame|wsExControlParent, x, y, ww, hh, owner, 0)
	dlgRoots[d.h] = true
	lastDialog = d
	return d
}

func (d *dialog) px(v int) int                  { return sc(d.dpi).px(v) }
func (d *dialog) font(size10, weight int) *font { return getFont(d.dpi, size10, weight) }

func (d *dialog) show() {
	if d.onSize != nil {
		cr := clientRect(d.h)
		d.onSize(cr.w(), cr.h())
	}
	runModal(d.owner, d.h, &d.done)
}

func (d *dialog) close() {
	d.done = true
	if d.h == 0 || call(pIsWindow, d.h) == 0 {
		return
	}
	if d.modeless {
		call(pDestroyWindow, d.h)
		return
	}
	call(pShowWindow, d.h, swHide)
}

func (d *dialog) render() {
	cr := clientRect(d.h)
	d.cv = ensureCanvas(d.cv, cr.w(), cr.h())
	if d.cv == nil {
		return
	}
	d.cv.fillRect(0, 0, cr.w(), cr.h(), d.bg)
	if d.paint != nil {
		d.paint(d.cv, cr.w(), cr.h())
	}
}

func (d *dialog) proc(h uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	if d.onMsg != nil {
		if r, ok := d.onMsg(m, wp, lp); ok {
			return r, true
		}
	}
	switch m {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		var ps paintStruct
		hdc := call(pBeginPaint, h, uintptr(unsafe.Pointer(&ps)))
		d.render()
		if d.cv != nil {
			cr := clientRect(h)
			d.cv.blit(hdc, cr.w(), cr.h())
		}
		call(pEndPaint, h, uintptr(unsafe.Pointer(&ps)))
		return 0, true
	case wmPrintClient:
		if d.cv == nil {
			d.render()
		}
		if d.cv != nil {
			cr := clientRect(h)
			call(pBitBlt, wp, 0, 0, uintptr(cr.w()), uintptr(cr.h()), d.cv.dc, 0, 0, srcCopy)
		}
		return 0, true
	case wmSize:
		if d.onSize != nil {
			d.onSize(lowWord(lp)&0xFFFF, highWord(lp)&0xFFFF)
		}
		d.render()
		invalidate(h)
		return 0, true
	case wmGetMinMaxInfo:
		if d.minW > 0 {
			mm := (*minMaxInfo)(unsafe.Pointer(lp))
			mm.MinTrackSize = point{int32(d.px(d.minW)), int32(d.px(d.minH))}
			return 0, true
		}
		return 0, false
	case wmDpiChanged:
		d.dpi = int(wp & 0xFFFF)
		if d.fonts != nil {
			d.fonts()
		}
		r := (*rect)(unsafe.Pointer(lp))
		call(pSetWindowPos, h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.w()), uintptr(r.h()), swpNoZOrder|swpNoActivate)
		return 0, true
	case wmCtlColorStatic, wmCtlColorBtn:
		return ctlColor(wp, d.bg), true
	case wmDrawItem:
		if drawPrimaryButton(lp, d.font(130, fwSemiBold), d.dpi) {
			return 1, true
		}
		return 0, false
	case wmCommand:
		id := int(wp & 0xFFFF)
		if id == idCancel {
			if d.canClose == nil || d.canClose() {
				d.close()
			}
			return 0, true
		}
		if d.onCmd != nil {
			d.onCmd(id, int(wp>>16&0xFFFF))
		}
		return 0, true
	case wmKeyDown, wmKeyUp, wmSysKeyDown, wmSysKeyUp, wmSysChar:
		if d.onKey != nil && d.onKey(m, wp, lp) {
			return 0, true
		}
		return 0, false
	case wmNextDlgCtl:
		// Tab w wielowierszowym polu tekstowym: pole prosi okno nadrzędne o następną kontrolkę (wp - wstecz)
		if lp&0xFFFF != 0 {
			call(pSetFocus, wp)
		} else if next := call(pGetNextDlgTabItem, h, call(pGetFocus), wp); next != 0 {
			call(pSetFocus, next)
		}
		return 0, true
	case wmClose:
		if d.canClose == nil || d.canClose() {
			d.close()
		}
		return 0, true
	case wmDestroy:
		d.cv.free()
		d.cv = nil
		if d.onClosed != nil {
			d.onClosed()
		}
		return 0, false
	}
	return 0, false
}

// ---------------------------------------------------------------- O programie (Show-SdUiAbout)

const licenseText = `MIT License

Copyright (c) 2026 Kacper Suliński - INDUSIP.PL

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.`

func (mw *mainWin) showAbout() {
	d := newDialog(mw.h, "O programie", 620, 600, true)
	d.minW, d.minH = 480, 420
	integrity := "nie sprawdzono"
	if mw.opt.Integrity != nil {
		integrity = mw.opt.Integrity()
	}
	regOn := diag.RegistrationEnabled()
	project := "Wersja Go nie jest jeszcze opublikowana. Wersja PowerShell: https://github.com/WoofDeveloper/SerwisDiag"
	if diag.ProjectURL != "" {
		project = diag.ProjectURL
	}
	privacy := "Program pracuje lokalnie i nie wysyła danych o komputerze. Z internetem łączy się tylko w testach łączności, przy wyszukiwaniu sterowników w Windows Update i przy sprawdzaniu nowej wersji na GitHubie (na żądanie, a w wersji zainstalowanej także raz na dobę - można to wyłączyć w ustawieniach)."
	if regOn {
		privacy += " Dobrowolna rejestracja wysyła autorowi programu tylko dane wpisane w jej oknie (imię i nazwisko, e-mail, telefon) - szczegóły w oknie „Rejestracja”."
	}
	sections := [][2]string{
		{"Do czego służy", "Diagnostyka i naprawa komputerów z Windows 10 i 11: sprzęt, dyski, bateria, awarie, sterowniki, zabezpieczenia, aktualizacje, sieć i autostart. Wersja Go to jeden plik .exe bez PowerShella - działa bez instalacji, także z pendrive'a. Monitor przy zegarze sprawdza system w tle, a wersja zainstalowana sama się aktualizuje."},
		{"Autor", diag.Author},
		{"Licencja", "MIT - program można bezpłatnie używać (także w firmie), kopiować, zmieniać i rozpowszechniać, pod warunkiem zachowania informacji o autorze i licencji."},
		{"Strona projektu", project},
		{"Baza wiedzy (okresy wsparcia Windows i ESU)", mw.kbText},
		{"Plik programu", integrity},
		{"Bez gwarancji", "Program jest udostępniany „tak jak jest”, bez jakiejkolwiek gwarancji. Naprawy zmieniają ustawienia systemu: program tworzy przed nimi punkt przywracania i zapisuje dziennik zmian (zmiany ustawień można cofnąć), ale za ich użycie odpowiada osoba, która je uruchamia. Przed poważnymi zmianami zrób kopię danych."},
		{"Prywatność", privacy},
	}
	panel := newDoc(d.h, cWhite, func(dc *doc, width int) int {
		y := dc.px(12)
		w := width - 2*dc.px(16)
		for _, s := range sections {
			t := layoutText([]span{{text: s[0], font: dc.font(130, fwSemiBold), color: cBlue}, {text: s[1], font: dc.font(130, fwNormal), color: cGray3, br: true}}, w, 0)
			dc.block(t, dc.px(16), y)
			y += t.h + dc.px(12)
		}
		return y
	})
	panel.dpi = d.dpi
	btnLic := newButton(d.h, 301, "Tekst licencji")
	btnClose := newButton(d.h, 302, "Zamknij")
	btnUpd := newButton(d.h, 303, "Sprawdź nową wersję")
	btnReg := newButton(d.h, 304, "Rejestracja")
	// bez adresu strony projektu na GitHubie nie ma czego sprawdzać; rejestracja - tylko z adresem formularza
	enableWin(btnUpd, diag.GitHubRepo(diag.ProjectURL) != "")
	showWin(btnReg, regOn)
	fonts := func() {
		for _, b := range []uintptr{btnLic, btnClose, btnUpd, btnReg} {
			setFont(b, d.font(130, fwNormal))
		}
		panel.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(22)
		tf := d.font(220, fwSemiBold)
		cv.text(tf, "SerwisDiag", p, p, cText)
		cv.text(d.font(130, fwNormal), "wersja "+diag.Version+" (Go) · licencja "+diag.License, p, p+tf.height+d.px(2), cGray)
		top := p + tf.height + d.px(2) + d.font(130, fwNormal).height + d.px(12)
		bottom := h - p - d.px(32) - d.px(14)
		cv.card(p, top, w-p, bottom, float64(d.px(8)), cWhite, cBorder, 1)
	}
	d.onSize = func(w, h int) {
		p := d.px(22)
		tf := d.font(220, fwSemiBold)
		top := p + tf.height + d.px(2) + d.font(130, fwNormal).height + d.px(12)
		bottom := h - p - d.px(32) - d.px(14)
		moveWin(panel.h, p+d.px(1), top+d.px(1), w-2*p-d.px(2), bottom-top-d.px(2))
		x := p
		for _, b := range []uintptr{btnLic, btnUpd, btnReg} {
			if b == btnReg && !regOn {
				continue
			}
			bw := buttonWidth(d.font(130, fwNormal), getText(b), d.px(12))
			moveWin(b, x, h-p-d.px(32), bw, d.px(32))
			x += bw + d.px(8)
		}
		moveWin(btnClose, w-p-d.px(100), h-p-d.px(32), d.px(100), d.px(32))
	}
	d.onCmd = func(id, code int) {
		switch id {
		case 301:
			msgBox(d.h, licenseText, "Licencja", mbIconInfo)
		case 302, idOK:
			d.close()
		case 303:
			mw.checkUpdateNow(d.h, d.close)
		case 304:
			showRegistration(d.h, mw.opt.Root)
		}
	}
	d.show()
}

// ---------------------------------------------------------------- ustawienia serwisu (Show-SdUiSettings)

func (mw *mainWin) showSettings() {
	d := newDialog(mw.h, "Ustawienia serwisu", 600, 680, false)
	s := mw.settings
	type field struct {
		label string
		val   string
		edit  uintptr
		pick  uintptr
	}
	fields := []*field{
		{label: "Nazwa serwisu", val: s.ShopName}, {label: "Telefon", val: s.ShopPhone}, {label: "E-mail / WWW", val: s.ShopEmail},
		{label: "Adres", val: s.ShopAddress}, {label: "Technik", val: s.Technician}, {label: "Logo (PNG/JPG)", val: s.LogoPath},
		{label: "Folder raportów", val: s.ReportDir},
	}
	for i, f := range fields {
		f.edit = newEdit(d.h, 400+i, f.val, 0)
	}
	fields[5].pick = newButton(d.h, 420, "Wybierz...")
	fields[6].pick = newButton(d.h, 421, "Wybierz...")
	cmbOpen := newCombo(d.h, 430, []string{"nie otwieraj raportu", "otwórz raport dla klienta", "otwórz raport techniczny"})
	openIdx := map[string]int{"none": 0, "customer": 1, "technical": 2}[s.OpenAfterScan]
	setComboSel(cmbOpen, openIdx)
	chkPdf := newCheck(d.h, 431, "zapisuj też raport dla klienta jako PDF (Microsoft Edge)")
	setChecked(chkPdf, s.AutoPdf)
	chkUpd := newCheck(d.h, 434, "sprawdzaj raz na dobę, czy jest nowa wersja (GitHub)")
	setChecked(chkUpd, s.AutoUpdateCheck)
	// aktualizacje tylko w wersji zainstalowanej (instalator podmienia pliki); monitor - także w przenośnej
	enableWin(chkUpd, diag.IsInstalledCopy(mw.opt.Root))
	chkMon := newCheck(d.h, 432, "monitoruj system w tle")
	monWas := monitorAutostart() != ""
	setChecked(chkMon, monWas)
	cmbInt := newCombo(d.h, 433, nil)
	var items []string
	for _, iv := range monitorIntervals {
		items = append(items, intervalText(iv))
	}
	comboReset(cmbInt, items)
	cur := 60
	if st := readMonitorState(monitorStatePath()); st != nil {
		cur = st.Interval
	}
	setComboSel(cmbInt, max(indexOf(monitorIntervals, cur), 0))
	btnSave := newButton(d.h, 440, "Zapisz")
	btnCancel := newButton(d.h, 441, "Anuluj")
	all := []uintptr{cmbOpen, chkPdf, chkUpd, chkMon, cmbInt, btnSave, btnCancel, fields[5].pick, fields[6].pick}
	fonts := func() {
		f := d.font(130, fwNormal)
		for _, fl := range fields {
			setFont(fl.edit, f)
			setEditMargins(fl.edit, d.px(4))
		}
		for _, c := range all {
			setFont(c, f)
		}
		setComboHeight(cmbOpen, d.px(30))
		setComboHeight(cmbInt, d.px(30))
	}
	fonts()
	d.fonts = fonts
	rowLabels := []string{"Nazwa serwisu", "Telefon", "E-mail / WWW", "Adres", "Technik", "Logo (PNG/JPG)", "Folder raportów", "Po diagnostyce", "PDF", "Aktualizacje", "Monitorowanie"}
	rowY := func(i int) int {
		y := d.px(22) + d.font(160, fwSemiBold).height + d.px(12)
		return y + i*d.px(40)
	}
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(22)
		cv.text(d.font(160, fwSemiBold), "Dane serwisu (widoczne w nagłówku raportów)", p, p, cText)
		f := d.font(130, fwNormal)
		for i, l := range rowLabels {
			cv.text(f, l, p, rowY(i)+(d.px(30)-f.height)/2, cText)
		}
		note := layoutText([]span{{text: "Monitor przy zegarze uruchamia się przy logowaniu (w instalacji dla wszystkich użytkowników - zadanie Harmonogramu, w pozostałych - wpis Autostartu bieżącego użytkownika) i co jakiś czas sprawdza dyski, awarie, urządzenia, antywirusa, zaporę i Windows Update.", font: d.font(120, fwNormal), color: cGray}}, w-2*p, 0)
		cv.block(note, p, rowY(len(rowLabels))+d.px(2))
	}
	d.onSize = func(w, h int) {
		p := d.px(22)
		x := p + d.px(130)
		for i, f := range fields {
			ew := w - x - p
			if f.pick != 0 {
				pw := buttonWidth(d.font(130, fwNormal), "Wybierz...", d.px(10))
				ew -= pw + d.px(8)
				moveWin(f.pick, w-p-pw, rowY(i), pw, d.px(30))
			}
			moveWin(f.edit, x, rowY(i), ew, d.px(30))
		}
		moveWin(cmbOpen, x, rowY(7), w-x-p, d.px(200))
		moveWin(chkPdf, x, rowY(8)+d.px(5), w-x-p, d.px(20))
		moveWin(chkUpd, x, rowY(9)+d.px(5), w-x-p, d.px(20))
		cw := checkWidth(d.font(130, fwNormal), "monitoruj system w tle", d.dpi)
		moveWin(chkMon, x, rowY(10)+d.px(5), cw, d.px(20))
		moveWin(cmbInt, w-p-d.px(140), rowY(10), d.px(140), d.px(200))
		moveWin(btnSave, w-p-d.px(208), h-p-d.px(32), d.px(100), d.px(32))
		moveWin(btnCancel, w-p-d.px(100), h-p-d.px(32), d.px(100), d.px(32))
	}
	save := func() {
		s2 := mw.settings
		s2.ShopName = strings.TrimSpace(getText(fields[0].edit))
		s2.ShopPhone = strings.TrimSpace(getText(fields[1].edit))
		s2.ShopEmail = strings.TrimSpace(getText(fields[2].edit))
		s2.ShopAddress = strings.TrimSpace(getText(fields[3].edit))
		s2.Technician = strings.TrimSpace(getText(fields[4].edit))
		s2.LogoPath = strings.TrimSpace(getText(fields[5].edit))
		s2.ReportDir = strings.TrimSpace(getText(fields[6].edit))
		s2.OpenAfterScan = []string{"none", "customer", "technical"}[max(comboSel(cmbOpen), 0)]
		s2.AutoPdf = isChecked(chkPdf)
		s2.AutoUpdateCheck = isChecked(chkUpd)
		if err := diag.SaveSettings(mw.opt.Root, s2); err != nil {
			msgBox(d.h, "Nie udało się zapisać ustawień: "+err.Error(), "Błąd", mbIconError)
		}
		mw.settings = s2
		if iv := comboSel(cmbInt); iv >= 0 {
			_ = setMonitorInterval(monitorStatePath(), monitorIntervals[iv])
		}
		if on := isChecked(chkMon); on != monWas {
			var err error
			if on {
				if _, err = registerAutostart(mw.opt.Exe, mw.opt.Root); err == nil {
					stopMonitor(false)
					err = startMonitorProcess(mw.opt.Exe)
				}
			} else {
				stopMonitor(false)
				if slices.Contains(unregisterAutostart(mw.opt.Exe, false), "task: brak uprawnień") {
					err = errors.New("monitorowanie dla wszystkich użytkowników (zadanie Harmonogramu) wyłączy tylko administrator - uruchom program jako administrator")
				}
			}
			if err != nil {
				msgBox(d.h, "Nie udało się zmienić monitorowania: "+err.Error(), "Błąd", mbIconError)
			}
		}
		d.close()
	}
	d.onCmd = func(id, code int) {
		switch id {
		case 440, idOK:
			save()
		case 441:
			d.close()
		case 420:
			if p := pickPath(d.h, "Wybierz logo serwisu", "Obrazy (*.png;*.jpg;*.jpeg;*.svg;*.gif)", "*.png;*.jpg;*.jpeg;*.svg;*.gif", false, getText(fields[5].edit)); p != "" {
				setText(fields[5].edit, p)
			}
		case 421:
			if p := pickPath(d.h, "Wybierz folder raportów", "", "", true, getText(fields[6].edit)); p != "" {
				setText(fields[6].edit, p)
			}
		}
	}
	d.show()
	mw.layout()
	mw.redraw()
}

func indexOf(list []int, v int) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------- wybór pliku albo folderu (IFileOpenDialog)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}

	pSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
)

//go:uintptrescapes
func comCall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*int(unsafe.Sizeof(uintptr(0)))))
	all := append([]uintptr{uintptr(obj)}, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

// pickPath: okno wyboru pliku (filtr) albo folderu; pusty napis - anulowano.
func pickPath(owner uintptr, title, filterName, filterSpec string, folder bool, initial string) string {
	var dlg unsafe.Pointer
	if hr := call(pCoCreateInstance, uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1 /*CLSCTX_INPROC_SERVER*/, uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg))); int32(hr) < 0 || dlg == nil {
		return ""
	}
	defer comCall(dlg, 2)
	opts := uint32(0x40 | 0x800) // FOS_FORCEFILESYSTEM | FOS_PATHMUSTEXIST
	if folder {
		opts |= 0x20 // FOS_PICKFOLDERS
	} else {
		opts |= 0x1000 // FOS_FILEMUSTEXIST
		spec := [2]*uint16{u16(filterName), u16(filterSpec)}
		comCall(dlg, 4, 1, uintptr(unsafe.Pointer(&spec[0])))
	}
	comCall(dlg, 9, uintptr(opts))
	comCall(dlg, 17, u16p(title))
	if initial = strings.TrimSpace(initial); initial != "" {
		dir := initial
		if st, err := os.Stat(initial); err != nil || !st.IsDir() {
			dir = filepath.Dir(initial)
		}
		var item unsafe.Pointer
		if hr := call(pSHCreateItemFromParsingName, u16p(dir), 0, uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item))); int32(hr) >= 0 && item != nil {
			comCall(dlg, 12, uintptr(item))
			comCall(item, 2)
		}
	}
	if hr := comCall(dlg, 3, owner); int32(hr) < 0 {
		return "" // anulowano
	}
	var res unsafe.Pointer
	if hr := comCall(dlg, 20, uintptr(unsafe.Pointer(&res))); int32(hr) < 0 || res == nil {
		return ""
	}
	defer comCall(res, 2)
	var name *uint16
	if hr := comCall(res, 5, 0x80058000, uintptr(unsafe.Pointer(&name))); int32(hr) < 0 || name == nil {
		return ""
	}
	defer call(pCoTaskMemFree, uintptr(unsafe.Pointer(name)))
	return windows.UTF16PtrToString(name)
}
