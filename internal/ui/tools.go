package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Narzędzia serwisowe (Get-SdUiTools): okna systemu Windows, konsola z poleceniami naprawczymi, test ekranu
// i klawiatury, sterowniki (Windows Update, Katalog Microsoft, strona producenta), DISM z płyty/ISO, cofanie napraw
// i kopia przed reinstalacją Windows.

type tool struct {
	group, title, desc string
	admin              bool
	confirm            string
	run                func(mw *mainWin) error
}

func open(file string, args ...string) func(*mainWin) error {
	return func(*mainWin) error { return shellOpen(file, strings.Join(args, " ")) }
}

func console(cmd string) func(*mainWin) error {
	return func(*mainWin) error { return shellOpen("cmd.exe", "/k "+cmd) }
}

func serviceTools() []tool {
	const sys, dg, drv, fix, keep = "Narzędzia systemowe", "Diagnostyka", "Sterowniki", "Naprawa (uruchamia okno konsoli)", "Konserwacja"
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	return []tool{
		{group: sys, title: "Menedżer urządzeń", desc: "Sterowniki i urządzenia (devmgmt.msc)", run: open("devmgmt.msc")},
		{group: sys, title: "Zarządzanie dyskami", desc: "Partycje i woluminy (diskmgmt.msc)", run: open("diskmgmt.msc")},
		{group: sys, title: "Podgląd zdarzeń", desc: "Dzienniki systemu (eventvwr.msc)", run: open("eventvwr.msc")},
		{group: sys, title: "Monitor niezawodności", desc: "Historia awarii i indeks stabilności (perfmon /rel)", run: open("perfmon.exe", "/rel")},
		{group: sys, title: "Monitor zasobów", desc: "Procesor, pamięć, dysk, sieć w czasie rzeczywistym", run: open("resmon.exe")},
		{group: sys, title: "Menedżer zadań", desc: "Procesy i autostart", run: open("taskmgr.exe")},
		{group: sys, title: "Usługi", desc: "services.msc", run: open("services.msc")},
		{group: sys, title: "Konfiguracja systemu", desc: "msconfig - rozruch i usługi", run: open("msconfig.exe")},
		{group: sys, title: "Informacje o systemie", desc: "msinfo32", run: open("msinfo32.exe")},
		{group: sys, title: "DirectX / grafika", desc: "dxdiag", run: open("dxdiag.exe")},
		{group: sys, title: "Programy i funkcje", desc: "Odinstalowywanie programów (appwiz.cpl)", run: open("appwiz.cpl")},
		{group: sys, title: "Połączenia sieciowe", desc: "ncpa.cpl", run: open("ncpa.cpl")},
		{group: sys, title: "Windows Update", desc: "Ustawienia aktualizacji", run: func(*mainWin) error {
			if shellOpen("ms-settings:windowsupdate", "") != nil {
				return shellOpen("control.exe", "/name Microsoft.WindowsUpdate")
			}
			return nil
		}},
		{group: sys, title: "Oczyszczanie dysku", desc: "cleanmgr", run: open("cleanmgr.exe")},
		{group: sys, title: "Optymalizacja dysków", desc: "defrag / TRIM (dfrgui)", run: open("dfrgui.exe")},

		{group: dg, title: "Diagnostyka pamięci Windows", desc: "Test RAM po ponownym uruchomieniu komputera (mdsched). Wynik pojawi się przy następnej diagnostyce w kategorii Awarie i zdarzenia.",
			confirm: "Uruchomić Diagnostykę pamięci Windows? Komputer zostanie ponownie uruchomiony, jeśli wybierzesz restart.", run: open("mdsched.exe")},
		{group: dg, title: "Raport baterii", desc: "powercfg /batteryreport - historia i pojemność baterii", run: batteryReport},
		{group: dg, title: "Raport energetyczny (60 s)", desc: "powercfg /energy - problemy z zasilaniem i usypianiem", admin: true,
			confirm: "Raport energetyczny zbiera dane przez 60 sekund. Uruchomić?", run: func(mw *mainWin) error {
				p := filepath.Join(diag.ResolveReportDir(mw.opt.Root, mw.settings), "raport-energii.html")
				return shellOpen("cmd.exe", fmt.Sprintf(`/k powercfg /energy /output "%s" /duration 60 & start "" "%s"`, p, p))
			}},
		{group: dg, title: "Test ekranu (martwe piksele)", desc: "Pełnoekranowe kolory do wykrywania uszkodzonych pikseli i smug", run: func(mw *mainWin) error { showScreenTest(mw.h); return nil }},
		{group: dg, title: "Test klawiatury", desc: "Sprawdza działanie wszystkich klawiszy", run: func(mw *mainWin) error { showKeyboardTest(mw.h); return nil }},

		{group: drv, title: "Sterowniki z Windows Update", desc: "Wyszukuje oficjalne, podpisane sterowniki dla urządzeń tego komputera i instaluje zaznaczone (internet; instalacja wymaga administratora)",
			run: func(mw *mainWin) error { mw.showDrivers(); return nil }},
		{group: drv, title: "Brakujące sterowniki (Katalog Microsoft)", desc: "Urządzenia z błędami i wyszukiwanie ich sterowników w Katalogu Microsoft Update po identyfikatorze sprzętu",
			run: func(mw *mainWin) error { mw.showMissingDrivers(); return nil }},
		{group: drv, title: "Strona sterowników producenta", desc: "Otwiera stronę ze sterownikami producenta komputera (albo płyty głównej) i kopiuje do schowka numer seryjny lub model do wpisania",
			run: func(mw *mainWin) error { mw.vendorPage(mw.h); return nil }},

		{group: fix, title: "Kontrola plików systemowych (SFC)", desc: "sfc /scannow - sprawdza i naprawia uszkodzone pliki systemowe (10-20 min)", admin: true,
			confirm: "Uruchomić sfc /scannow? Operacja trwa 10-20 minut i może naprawiać pliki systemowe.", run: console("sfc /scannow")},
		{group: fix, title: "DISM RestoreHealth (z internetu)", desc: "Naprawa magazynu składników Windows plikami pobranymi z Windows Update - wymaga internetu", admin: true,
			confirm: "Uruchomić DISM /Online /Cleanup-Image /RestoreHealth? Pliki naprawcze zostaną pobrane z Windows Update; operacja może trwać kilkanaście minut.",
			run:     console("DISM /Online /Cleanup-Image /RestoreHealth")},
		{group: fix, title: "DISM RestoreHealth z płyty/ISO", desc: "Naprawa bez internetu: pliki z obrazu ISO albo nośnika instalacyjnego (install.wim / install.esd) tej samej wersji Windows", admin: true,
			run: dismFromMedia},
		{group: fix, title: "CHKDSK - skan online (tylko odczyt)", desc: "chkdsk /scan - sprawdza system plików bez blokowania dysku", admin: true, run: console("chkdsk " + drive + " /scan")},
		{group: fix, title: "Zaplanuj CHKDSK przy starcie", desc: "Oznacza wolumin systemowy jako wymagający sprawdzenia (fsutil dirty set); chkdsk uruchomi się po restarcie", admin: true,
			confirm: "Zaplanować sprawdzanie dysku systemowego przy następnym uruchomieniu? Na dysku z objawami awarii najpierw skopiuj dane.",
			run:     console("fsutil dirty set " + drive + " & echo. & echo Sprawdzanie dysku uruchomi sie po ponownym starcie komputera.")},
		{group: fix, title: "Reset komponentów Windows Update", desc: "Zatrzymuje usługi, zmienia nazwę SoftwareDistribution i catroot2, uruchamia usługi", admin: true,
			confirm: "Zresetować komponenty Windows Update? Foldery pamięci podręcznej aktualizacji zostaną zmienione na .old.",
			run:     console(`net stop wuauserv & net stop cryptSvc & net stop bits & net stop msiserver & rmdir /s /q %SystemRoot%\SoftwareDistribution.old 2>nul & rmdir /s /q %SystemRoot%\System32\catroot2.old 2>nul & ren %SystemRoot%\SoftwareDistribution SoftwareDistribution.old & ren %SystemRoot%\System32\catroot2 catroot2.old & net start wuauserv & net start cryptSvc & net start bits & net start msiserver & echo. & echo Gotowe - uruchom ponownie komputer.`)},
		{group: fix, title: "Wyczyść DNS", desc: "ipconfig /flushdns", run: console("ipconfig /flushdns")},
		{group: fix, title: "Reset Winsock i TCP/IP", desc: "netsh winsock reset oraz netsh int ip reset - naprawa problemów z siecią (wymaga restartu)", admin: true,
			confirm: "Zresetować Winsock i stos TCP/IP? Wymagane będzie ponowne uruchomienie komputera.",
			run:     console("netsh winsock reset & netsh int ip reset & ipconfig /flushdns & echo. & echo Gotowe - uruchom ponownie komputer.")},

		{group: keep, title: "Utwórz punkt przywracania", desc: "Punkt przywracania przed zmianami (system zezwala zwykle na jeden na 24 h)", admin: true, run: restorePoint},
		{group: keep, title: "Cofnij naprawy z dziennika", desc: "Przywraca ustawienia zmienione przez naprawy programu: rejestr, typ uruchamiania usług, zaporę, SMB1",
			run: func(mw *mainWin) error { mw.showUndo(""); return nil }},
		{group: keep, title: "Kopia przed reinstalacją Windows", desc: "Sterowniki, lista programów, klucz Windows i dane systemu (opcjonalnie profile Wi-Fi i klucze BitLocker) w folderze, np. na pendrive",
			run: func(mw *mainWin) error { mw.showBackup(); return nil }},
		{group: keep, title: "Wyczyść pliki tymczasowe", desc: "Usuwa zawartość folderów Temp użytkownika i systemu (poza plikami używanymi)",
			confirm: "Usunąć pliki tymczasowe (Temp użytkownika i Windows\\Temp)? Pliki używane przez programy zostaną pominięte.", run: cleanTemp},
	}
}

// runTool odpowiada Invoke-SdUiTool: uprawnienia, potwierdzenie, uruchomienie.
func (mw *mainWin) runTool(t tool) {
	if t.admin && !mw.isAdmin {
		msgBox(mw.h, "Ta operacja wymaga uprawnień administratora. Uruchom program jako administrator.", "Wymagane uprawnienia", mbIconWarning)
		return
	}
	if t.confirm != "" && msgBox(mw.h, t.confirm, "Potwierdzenie", mbIconQuestion|mbYesNo) != idYes {
		return
	}
	if err := t.run(mw); err != nil {
		msgBox(mw.h, "Nie udało się uruchomić: "+err.Error(), "Błąd", mbIconError)
	}
}

func batteryReport(mw *mainWin) error {
	p := filepath.Join(diag.ResolveReportDir(mw.opt.Root, mw.settings), "raport-baterii.html")
	withWaitCursor(func() {
		win.Run(60*time.Second, filepath.Join(os.Getenv("SystemRoot"), "System32", "powercfg.exe"), "/batteryreport", "/output", p)
	})
	if fileExists(p) {
		openPath(p)
	}
	return nil
}

// restorePoint: punkt przywracania przez SRSetRestorePointW (jak Checkpoint-Computer -RestorePointType MODIFY_SETTINGS).
func restorePoint(mw *mainWin) error {
	var err error
	withWaitCursor(func() { err = win.CreateRestorePoint("SerwisDiag - przed zmianami", win.RestoreModifySettings) })
	if err != nil {
		msgBox(mw.h, "Nie udało się utworzyć punktu przywracania:\n"+err.Error()+"\n\nSprawdź, czy ochrona systemu jest włączona dla dysku systemowego.", "SerwisDiag", mbIconWarning)
		return nil
	}
	msgBox(mw.h, "Punkt przywracania został utworzony.", "SerwisDiag", mbIconInfo)
	return nil
}

// cleanTemp: zawartość %TEMP% i Windows\Temp (pliki używane zostają) i ile miejsca zwolniono.
func cleanTemp(mw *mainWin) error {
	dirs := []string{os.TempDir(), filepath.Join(os.Getenv("SystemRoot"), "Temp")}
	var before, after int64
	withWaitCursor(func() {
		for _, d := range dirs {
			b, _ := win.DirSize(d)
			before += b
		}
		for _, d := range dirs {
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(d, e.Name()))
			}
		}
		for _, d := range dirs {
			b, _ := win.DirSize(d)
			after += b
		}
	})
	msgBox(mw.h, "Zwolniono: "+diag.FmtSize(float64(max(before-after, 0)), 1), "SerwisDiag", mbIconInfo)
	return nil
}

// ---------------------------------------------------------------- test ekranu (Show-SdScreenTest)

const screenClass = "SerwisDiagGoScreenTest"

type screenTest struct {
	h    uintptr
	i    int
	hint bool
	done bool
	dpi  int
	cv   *canvas
}

var screenColors = []col{hexc("#FFFFFF"), hexc("#000000"), hexc("#FF0000"), hexc("#00FF00"), hexc("#0000FF"), hexc("#808080"), hexc("#FFFF00"), hexc("#00FFFF"), hexc("#FF00FF")}
var screenNames = []string{"biały", "czarny", "czerwony", "zielony", "niebieski", "szary", "żółty", "cyjan", "magenta", "gradient (pasma)"}

func showScreenTest(owner uintptr) {
	registerClass(screenClass, idcArrow, 0)
	st := &screenTest{hint: true}
	mon := workAreaFull(owner)
	st.h = createWindow(st, screenClass, "Test ekranu", wsPopup, wsExTopmost|wsExToolWindow,
		int(mon.Left), int(mon.Top), mon.w(), mon.h(), owner, 0)
	st.dpi = dpiOf(st.h)
	runModal(owner, st.h, &st.done)
}

// workAreaFull: cały ekran monitora z oknem (z paskiem zadań).
func workAreaFull(h uintptr) rect {
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if mon := call(pMonitorFromWindow, h, monitorDefaultNear); mon != 0 && call(pGetMonitorInfoW, mon, uintptr(unsafe.Pointer(&mi))) != 0 {
		return mi.Monitor
	}
	return workArea(h)
}

func (st *screenTest) step(d int) {
	n := len(screenColors) + 1
	st.i = (st.i + d + n) % n
	st.hint = false
	invalidate(st.h)
}

func (st *screenTest) proc(h uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	switch m {
	case wmEraseBkgnd:
		return 1, true
	case wmSetCursor:
		call(pSetCursor, 0) // bez kursora (Cursor="None")
		return 1, true
	case wmPaint:
		var ps paintStruct
		hdc := call(pBeginPaint, h, uintptr(unsafe.Pointer(&ps)))
		cr := clientRect(h)
		st.cv = ensureCanvas(st.cv, cr.w(), cr.h())
		if cv := st.cv; cv != nil {
			if st.i < len(screenColors) {
				cv.fillRect(0, 0, cr.w(), cr.h(), screenColors[st.i])
			} else {
				cv.gradientH(0, 0, cr.w(), cr.h(), hexc("#000000"), hexc("#FFFFFF"))
			}
			if st.hint {
				s := sc(st.dpi)
				f := getFont(st.dpi, 160, fwNormal)
				t := fmt.Sprintf("%d/%d: %s   |   klik, spacja lub strzałka w prawo - następny kolor   |   strzałka w lewo - poprzedni   |   Esc - zakończ", st.i+1, len(screenColors)+1, screenNames[st.i])
				tw := strWidth(f, t)
				bw, bh := tw+s.px(40), f.height+s.px(20)
				x, y := (cr.w()-bw)/2, cr.h()-s.px(48)-bh
				cv.fillRoundA(x, y, x+bw, y+bh, float64(s.px(10)), hexc("#000000"), cornersAll, 0xC8/255.0)
				cv.text(f, t, x+s.px(20), y+s.px(10), cWhite)
			}
			cv.blit(hdc, cr.w(), cr.h())
		}
		call(pEndPaint, h, uintptr(unsafe.Pointer(&ps)))
		return 0, true
	case wmLButtonDown:
		st.step(1)
		return 0, true
	case wmKeyDown:
		switch wp {
		case vkEscape:
			st.done = true
		case vkRight, vkSpace, vkReturn:
			st.step(1)
		case vkLeft:
			st.step(-1)
		}
		return 0, true
	case wmClose:
		st.done = true
		return 0, true
	case wmDestroy:
		st.cv.free()
		st.cv = nil
		return 0, false
	}
	return 0, false
}

// ---------------------------------------------------------------- test klawiatury (Show-SdKeyboardTest)

type kbKey struct {
	label, name string
	w           float64
}

var kbRows = [][]kbKey{
	{{"Esc", "Escape", 1.3}, {"", "", 0.6}, {"F1", "F1", 1}, {"F2", "F2", 1}, {"F3", "F3", 1}, {"F4", "F4", 1}, {"", "", 0.3}, {"F5", "F5", 1}, {"F6", "F6", 1}, {"F7", "F7", 1}, {"F8", "F8", 1}, {"", "", 0.3}, {"F9", "F9", 1}, {"F10", "F10", 1}, {"F11", "F11", 1}, {"F12", "F12", 1}, {"", "", 0.3}, {"PrtSc", "Snapshot", 1.1}, {"ScrLk", "Scroll", 1.1}, {"Pause", "Pause", 1.1}},
	{{"`", "OemTilde", 1}, {"1", "D1", 1}, {"2", "D2", 1}, {"3", "D3", 1}, {"4", "D4", 1}, {"5", "D5", 1}, {"6", "D6", 1}, {"7", "D7", 1}, {"8", "D8", 1}, {"9", "D9", 1}, {"0", "D0", 1}, {"-", "OemMinus", 1}, {"=", "OemPlus", 1}, {"Backspace", "Back", 2.1}, {"", "", 0.3}, {"Ins", "Insert", 1.1}, {"Home", "Home", 1.1}, {"PgUp", "Prior", 1.1}},
	{{"Tab", "Tab", 1.5}, {"Q", "Q", 1}, {"W", "W", 1}, {"E", "E", 1}, {"R", "R", 1}, {"T", "T", 1}, {"Y", "Y", 1}, {"U", "U", 1}, {"I", "I", 1}, {"O", "O", 1}, {"P", "P", 1}, {"[", "OemOpenBrackets", 1}, {"]", "OemCloseBrackets", 1}, {"\\", "OemPipe", 1.6}, {"", "", 0.3}, {"Del", "Delete", 1.1}, {"End", "End", 1.1}, {"PgDn", "Next", 1.1}},
	{{"Caps", "Capital", 1.8}, {"A", "A", 1}, {"S", "S", 1}, {"D", "D", 1}, {"F", "F", 1}, {"G", "G", 1}, {"H", "H", 1}, {"J", "J", 1}, {"K", "K", 1}, {"L", "L", 1}, {";", "OemSemicolon", 1}, {"'", "OemQuotes", 1}, {"Enter", "Return", 2.3}},
	{{"Shift", "LeftShift", 2.3}, {"Z", "Z", 1}, {"X", "X", 1}, {"C", "C", 1}, {"V", "V", 1}, {"B", "B", 1}, {"N", "N", 1}, {"M", "M", 1}, {",", "OemComma", 1}, {".", "OemPeriod", 1}, {"/", "OemQuestion", 1}, {"Shift", "RightShift", 2.8}, {"", "", 1.4}, {"↑", "Up", 1.1}},
	{{"Ctrl", "LeftCtrl", 1.4}, {"Win", "LWin", 1.3}, {"Alt", "LeftAlt", 1.3}, {"Spacja", "Space", 6.1}, {"Alt", "RightAlt", 1.3}, {"Win", "RWin", 1.3}, {"Menu", "Apps", 1.3}, {"Ctrl", "RightCtrl", 1.4}, {"", "", 0.3}, {"←", "Left", 1.1}, {"↓", "Down", 1.1}, {"→", "Right", 1.1}},
	{{"Num", "NumLock", 1.1}, {"/", "Divide", 1}, {"*", "Multiply", 1}, {"-", "Subtract", 1}, {"", "", 0.4}, {"7", "NumPad7", 1}, {"8", "NumPad8", 1}, {"9", "NumPad9", 1}, {"+", "Add", 1}, {"", "", 0.4}, {"4", "NumPad4", 1}, {"5", "NumPad5", 1}, {"6", "NumPad6", 1}, {"", "", 0.4}, {"1", "NumPad1", 1}, {"2", "NumPad2", 1}, {"3", "NumPad3", 1}, {"", "", 0.4}, {"0", "NumPad0", 1.6}, {".", "Decimal", 1}},
}

// keyName: nazwa klawisza jak System.Windows.Input.Key w WPF (lewy/prawy Shift, Ctrl i Alt rozróżniane).
func keyName(vk int, lp uintptr) string {
	ext := lp&(1<<24) != 0
	scan := int(lp>>16) & 0xFF
	switch {
	case vk >= 'A' && vk <= 'Z':
		return string(rune(vk))
	case vk >= '0' && vk <= '9':
		return "D" + string(rune(vk))
	case vk >= 0x60 && vk <= 0x69:
		return fmt.Sprintf("NumPad%d", vk-0x60)
	case vk >= 0x70 && vk <= 0x87:
		return fmt.Sprintf("F%d", vk-0x6F)
	}
	switch vk {
	case vkShift:
		if scan == 0x36 {
			return "RightShift"
		}
		return "LeftShift"
	case vkControl:
		if ext {
			return "RightCtrl"
		}
		return "LeftCtrl"
	case vkMenu:
		if ext {
			return "RightAlt"
		}
		return "LeftAlt"
	}
	names := map[int]string{
		0x08: "Back", 0x09: "Tab", 0x0D: "Return", 0x13: "Pause", 0x14: "Capital", 0x1B: "Escape", 0x20: "Space",
		0x21: "Prior", 0x22: "Next", 0x23: "End", 0x24: "Home", 0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down",
		0x2C: "Snapshot", 0x2D: "Insert", 0x2E: "Delete", 0x5B: "LWin", 0x5C: "RWin", 0x5D: "Apps",
		0x6A: "Multiply", 0x6B: "Add", 0x6D: "Subtract", 0x6E: "Decimal", 0x6F: "Divide", 0x90: "NumLock", 0x91: "Scroll",
		0xBA: "OemSemicolon", 0xBB: "OemPlus", 0xBC: "OemComma", 0xBD: "OemMinus", 0xBE: "OemPeriod", 0xBF: "OemQuestion",
		0xC0: "OemTilde", 0xDB: "OemOpenBrackets", 0xDC: "OemPipe", 0xDD: "OemCloseBrackets", 0xDE: "OemQuotes", 0xE2: "OemBackslash",
	}
	if n, ok := names[vk]; ok {
		return n
	}
	return fmt.Sprintf("0x%02X", vk)
}

func showKeyboardTest(owner uintptr) {
	d := newDialog(owner, "Test klawiatury", 1180, 600, true)
	delete(dlgRoots, d.h) // klawisze (Tab, Enter, Esc, strzałki) trafiają do testu, a nie do nawigacji okna
	total := 0
	for _, row := range kbRows {
		for _, k := range row {
			if k.name != "" {
				total++
			}
		}
	}
	pressed := map[string]bool{}
	last := ""
	keys := newDoc(d.h, cWhite, func(dc *doc, width int) int {
		// szerokość klawisza dopasowana do okna (w WPF przewijanie w poziomie)
		maxUnits := 0.0
		for _, row := range kbRows {
			u := 0.0
			for _, k := range row {
				u += k.w
			}
			maxUnits = max(maxUnits, u+float64(len(row))*4/46)
		}
		unit := min(float64(dc.px(46)), float64(width-2*dc.px(18))/maxUnits)
		gap := max(int(unit*4/46), 2)
		kh := max(int(unit*42/46), dc.px(24))
		f := dc.font(125, fwNormal)
		y := dc.px(18)
		for _, row := range kbRows {
			x := dc.px(18)
			for _, k := range row {
				w := int(unit*k.w + (k.w-1)*float64(gap))
				if k.name != "" {
					fill, bd := cWhite, cBtnBorder
					if pressed[k.name] {
						fill, bd = hexc("#B7EBC6"), cGreen
					}
					dc.rect(x, y, w, kh, fill, bd, 1, float64(dc.px(6)))
					dc.box(f, k.label, x, y, w, kh, cText, dtCenter)
				}
				x += w + gap
			}
			y += kh + dc.px(5)
		}
		return y + dc.px(18)
	})
	btn := newButton(d.h, 501, "Wyczyść")
	setFont(btn, d.font(130, fwNormal))
	info := func() string {
		s := fmt.Sprintf("Naciśnięto %d z %d klawiszy. Wciskaj kolejne klawisze - zadziałający zmieni kolor na zielony.", len(pressed), total)
		if last != "" {
			s += "   Ostatni: " + last
		}
		return s
	}
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(18)
		f := d.font(140, fwNormal)
		cv.textBox(f, info(), p, p, w-p-d.px(120), p+d.px(32), cText, dtLeft)
		cv.card(p, p+d.px(32)+d.px(12), w-p, h-p, float64(d.px(12)), cWhite, cBorder, 1)
	}
	d.onSize = func(w, h int) {
		p := d.px(18)
		bw := buttonWidth(d.font(130, fwNormal), "Wyczyść", d.px(14))
		moveWin(btn, w-p-bw, p, bw, d.px(32))
		top := p + d.px(32) + d.px(12)
		moveWin(keys.h, p+d.px(1), top+d.px(1), w-2*p-d.px(2), h-p-top-d.px(2))
	}
	d.onCmd = func(id, code int) {
		if id == 501 {
			clear(pressed)
			last = ""
			keys.relayout()
			invalidate(d.h)
			call(pSetFocus, d.h)
		}
	}
	d.onKey = func(m uint32, wp, lp uintptr) bool {
		vk := int(wp)
		if m == wmSysKeyDown && vk == vkF4 {
			return false // Alt+F4 zamyka okno
		}
		if (m == wmKeyUp || m == wmSysKeyUp) && vk != vkSnapshot {
			return true // PrintScreen przychodzi tylko jako puszczenie klawisza
		}
		if m == wmSysChar {
			return true
		}
		name := keyName(vk, lp)
		last = name
		for _, row := range kbRows {
			for _, k := range row {
				if k.name == name && !pressed[name] {
					pressed[name] = true
					keys.relayout()
				}
			}
		}
		invalidate(d.h)
		return true
	}
	d.minW, d.minH = 700, 360
	d.show()
}
