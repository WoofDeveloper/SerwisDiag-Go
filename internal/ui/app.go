package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// handler obsługuje komunikaty jednego okna; handled=false - domyślna obsługa Windows.
type handler interface {
	proc(h uintptr, m uint32, wp, lp uintptr) (r uintptr, handled bool)
}

var (
	handlers   = map[uintptr]handler{}
	pending    handler // okno w trakcie tworzenia (komunikaty WM_NCCREATE/WM_CREATE przed zwróceniem uchwytu)
	wndProcPtr = windows.NewCallback(wndProc)
	classes    = map[string]bool{}
	// dlgRoots: okna najwyższego poziomu z nawigacją klawiszem Tab między kontrolkami (IsDialogMessage)
	dlgRoots = map[uintptr]bool{}
)

func wndProc(h, m, wp, lp uintptr) uintptr {
	hd, ok := handlers[h]
	if !ok && pending != nil {
		hd, ok = pending, true
		handlers[h] = hd
	}
	if ok {
		var r uintptr
		handled := false
		func() {
			defer func() {
				if p := recover(); p != nil {
					logError(fmt.Sprintf("panic: %v\n%s", p, debug.Stack()))
				}
			}()
			r, handled = hd.proc(h, uint32(m), wp, lp)
		}()
		if uint32(m) == wmNCDestroy {
			delete(handlers, h)
			delete(dlgRoots, h)
		}
		if handled {
			return r
		}
	}
	return call(pDefWindowProcW, h, m, wp, lp)
}

// registerClass rejestruje klasę okna (raz); bg - pędzel tła (0: okno maluje samo).
func registerClass(name string, cursor int, style uint32) {
	if classes[name] {
		return
	}
	wc := wndClassEx{
		Size: uint32(unsafe.Sizeof(wndClassEx{})), Style: style, WndProc: wndProcPtr, Instance: hinstance(),
		Cursor: loadCursor(cursor), ClassName: u16(name),
		Icon: appIcon(metricForDpi(smCxIcon, dpiOf(0))), IconSm: appIcon(metricForDpi(smCxSmIcon, dpiOf(0))),
	}
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc)))
	classes[name] = true
}

// createWindow tworzy okno obsługiwane przez hd.
func createWindow(hd handler, class, title string, style, exStyle uint32, x, y, w, h int, parent uintptr, id int) uintptr {
	pending = hd
	hwnd := call(pCreateWindowExW, uintptr(exStyle), u16p(class), u16p(title), uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h), parent, uintptr(id), hinstance(), 0)
	pending = nil
	if hwnd != 0 {
		if _, ok := handlers[hwnd]; !ok {
			handlers[hwnd] = hd
		}
	}
	return hwnd
}

// pump obsługuje komunikaty do zakończenia pętli (WM_QUIT) albo do spełnienia warunku stop.
func pump(stop func() bool) bool {
	var m msgT
	for stop == nil || !stop() {
		r := int32(call(pGetMessageW, uintptr(unsafe.Pointer(&m)), 0, 0, 0))
		if r <= 0 {
			return false // WM_QUIT
		}
		if root := call(pGetAncestor, m.Hwnd, gaRoot); root != 0 && dlgRoots[root] {
			if call(pIsDialogMessageW, root, uintptr(unsafe.Pointer(&m))) != 0 {
				continue
			}
		}
		call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
		call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
	}
	return true
}

// runModal: okno dlg jako modalne względem owner (owner wyłączony do zamknięcia dlg).
func runModal(owner, dlg uintptr, done *bool) {
	if owner != 0 {
		enableWin(owner, false)
	}
	show := uintptr(swShow)
	if quietMessages {
		show = swShowNoActivate // autotest nie zabiera aktywnego okna użytkownikowi
	}
	call(pShowWindow, dlg, show)
	ok := pump(func() bool { return *done || call(pIsWindow, dlg) == 0 })
	if owner != 0 {
		enableWin(owner, true)
		if !quietMessages {
			call(pSetActiveWindow, owner)
		}
	}
	if call(pIsWindow, dlg) != 0 {
		call(pDestroyWindow, dlg)
	}
	if !ok {
		call(pPostQuitMessage, 0) // WM_QUIT w pętli okna modalnego - przekaż dalej
	}
}

// initUI: wątek okien (STA dla okien dialogowych powłoki) i biblioteka kontrolek w wersji 6.
func initUI() {
	runtime.LockOSThread()
	_ = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	icc := struct{ Size, ICC uint32 }{8, 0x0000FFFF}
	call(pInitCommonControlsEx, uintptr(unsafe.Pointer(&icc)))
}

// ---------------------------------------------------------------- dziennik błędów okien

func userDataDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil || base == "" {
		base = os.TempDir()
	}
	d := filepath.Join(base, "SerwisDiag")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// logError dopisuje błąd do %LOCALAPPDATA%\SerwisDiag\okno-go.log (najwyżej ok. 200 KB).
func logError(msg string) { appendLog("okno-go.log", msg) }

func appendLog(name, msg string) {
	p := filepath.Join(userDataDir(), name)
	if st, err := os.Stat(p); err == nil && st.Size() > 200*1024 {
		_ = os.Remove(p)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().Format("2006-01-02 15:04:05") + " " + msg + "\r\n")
}

// releaseMemory oddaje systemowi pamięć po zakończonej pracy (skan, sprawdzenie monitora) - Go robi to sam,
// ale stopniowo; okno i monitor działają długo, więc zwalniamy od razu. SERWISDIAG_MEMSTATS=1: zapis
// statystyk pamięci Go do dziennika (pomiary).
func releaseMemory(where string) {
	debug.FreeOSMemory()
	if os.Getenv("SERWISDIAG_MEMSTATS") != "" {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mb := func(v uint64) string { return fmt.Sprintf("%.1f", float64(v)/(1<<20)) }
		logError(fmt.Sprintf("pamięć (%s): sterta używana %s MB, sterta od systemu %s MB, oddana %s MB, stosy %s MB, razem od systemu %s MB, wątki Go %d",
			where, mb(ms.HeapInuse), mb(ms.HeapSys), mb(ms.HeapReleased), mb(ms.StackSys), mb(ms.Sys), runtime.NumGoroutine()))
	}
}

// ---------------------------------------------------------------- skalowanie DPI

// sc - skala ekranu okna: px(v) przelicza jednostki WPF (1/96 cala) na piksele.
type sc int

func (d sc) px(v int) int { return (v*int(d) + 48) / 96 }

func (d sc) pxf(v float64) int { return int(v*float64(d)/96 + 0.5) }
