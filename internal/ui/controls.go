package ui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Kontrolki systemowe (comctl32 v6 - wygląd Windows 10/11): przyciski, pola tekstowe, listy rozwijane, pola wyboru.

func newControl(parent uintptr, class, text string, style, exStyle uint32, id int) uintptr {
	return call(pCreateWindowExW, uintptr(exStyle), u16p(class), u16p(text), uintptr(style|wsChild|wsVisible),
		0, 0, 10, 10, parent, uintptr(id), hinstance(), 0)
}

func setFont(h uintptr, f *font) {
	if h != 0 && f != nil {
		sendMsg(h, wmSetFont, f.h, 1)
	}
}

// primaryButtons: przyciski rysowane przez okno (niebieskie, jak PrimaryButton w XAML) i tło pod nimi.
var (
	primaryButtons = map[uintptr]col{}
	hotButtons     = map[uintptr]bool{}
	subclassPtr    = windows.NewCallback(buttonSubclass)
)

func newButton(parent uintptr, id int, text string) uintptr {
	return newControl(parent, "BUTTON", text, wsTabStop|bsPushButton, 0, id)
}

// newPrimaryButton: przycisk rysowany samodzielnie (WM_DRAWITEM w oknie nadrzędnym, drawPrimaryButton).
func newPrimaryButton(parent uintptr, id int, text string, bg col) uintptr {
	h := newControl(parent, "BUTTON", text, wsTabStop|bsOwnerDraw, 0, id)
	primaryButtons[h] = bg
	call(pSetWindowSubclass, h, subclassPtr, 1, 0)
	return h
}

// buttonSubclass śledzi najechanie myszą na przycisk rysowany samodzielnie (podświetlenie).
func buttonSubclass(h, m, wp, lp, id, ref uintptr) uintptr {
	switch uint32(m) {
	case wmMouseMove:
		if !hotButtons[h] {
			hotButtons[h] = true
			trackLeave(h)
			invalidate(h)
		}
	case wmMouseLeave:
		delete(hotButtons, h)
		invalidate(h)
	case wmNCDestroy:
		delete(hotButtons, h)
		delete(primaryButtons, h)
	}
	return call(pDefSubclassProc, h, m, wp, lp)
}

// drawPrimaryButton rysuje przycisk główny (WM_DRAWITEM).
func drawPrimaryButton(lp uintptr, f *font, dpi int) bool {
	di := (*drawItemStruct)(unsafe.Pointer(lp))
	bg, ok := primaryButtons[di.HwndItem]
	if !ok {
		return false
	}
	w, h := di.RcItem.w(), di.RcItem.h()
	cv := newCanvas(w, h)
	if cv == nil {
		return false
	}
	defer cv.free()
	cv.fillRect(0, 0, w, h, bg)
	fill := cBlue
	switch {
	case di.ItemState&odsSelected != 0:
		fill = cBluePress
	case hotButtons[di.HwndItem]:
		fill = cBlueHover
	}
	disabled := di.ItemState&odsDisabled != 0
	if disabled {
		fill = mix(bg, fill, 0.45)
	}
	cv.fillRound(0, 0, w, h, float64(sc(dpi).px(7)), fill, cornersAll)
	tc := cWhite
	if disabled {
		tc = mix(fill, cWhite, 0.7)
	}
	cv.textBox(f, getText(di.HwndItem), 0, 0, w, h, tc, dtCenter)
	if di.ItemState&odsFocus != 0 && di.ItemState&odsNoFocusRect == 0 {
		d := sc(dpi).px(3)
		cv.fillRoundA(d, d, w-d, h-d, float64(sc(dpi).px(5)), cWhite, cornersAll, 0.18)
	}
	cv.blit(di.Hdc, w, h)
	return true
}

func newEdit(parent uintptr, id int, text string, style uint32) uintptr {
	h := newControl(parent, "EDIT", text, wsTabStop|esAutoHScroll|style, wsExClientEdge, id)
	return h
}

// setEditMargins: wewnętrzny odstęp tekstu (Padding w XAML).
func setEditMargins(h uintptr, px int) {
	sendMsg(h, emSetMargins, 1|2, mkLParam(px, px))
}

func newCombo(parent uintptr, id int, items []string) uintptr {
	h := newControl(parent, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownList, 0, id)
	for _, it := range items {
		sendMsg(h, cbAddString, 0, u16p(it))
	}
	sendMsg(h, cbSetMinVisible, 12, 0)
	return h
}

func comboReset(h uintptr, items []string) {
	sendMsg(h, cbResetContent, 0, 0)
	for _, it := range items {
		sendMsg(h, cbAddString, 0, u16p(it))
	}
}

func comboSel(h uintptr) int { return int(int32(sendMsg(h, cbGetCurSel, 0, 0))) }

func setComboSel(h uintptr, i int) { sendMsg(h, cbSetCurSel, uintptr(i), 0) }

// setComboHeight: wysokość pola wyboru (bez listy) - jak pól tekstowych obok.
func setComboHeight(h uintptr, total int) {
	sendMsg(h, cbSetItemHeight, ^uintptr(0), uintptr(max(total-6, 10)))
}

func newCheck(parent uintptr, id int, text string) uintptr {
	return newControl(parent, "BUTTON", text, wsTabStop|bsAutoCheck, 0, id)
}

func isChecked(h uintptr) bool { return sendMsg(h, bmGetCheck, 0, 0) == bstChecked }

func setChecked(h uintptr, on bool) {
	v := uintptr(0)
	if on {
		v = bstChecked
	}
	sendMsg(h, bmSetCheck, v, 0)
}

// checkWidth: szerokość pola wyboru z opisem.
func checkWidth(f *font, text string, dpi int) int {
	return strWidth(f, text) + sc(dpi).px(22)
}

// buttonWidth: szerokość przycisku z marginesem padX po obu stronach tekstu.
func buttonWidth(f *font, text string, padX int) int { return strWidth(f, text) + 2*padX }

// brushCache: pędzle tła dla WM_CTLCOLORSTATIC (pola wyboru na białym pasku i w oknach dialogowych).
var brushCache = map[col]uintptr{}

func brush(c col) uintptr {
	if b, ok := brushCache[c]; ok {
		return b
	}
	b := call(pCreateSolidBrush, c.cref())
	brushCache[c] = b
	return b
}

// ctlColor: tło pola wyboru albo opisu (wp - kontekst urządzenia kontrolki).
func ctlColor(wp uintptr, bg col) uintptr {
	call(pSetBkMode, wp, transparentBk)
	call(pSetBkColor, wp, bg.cref())
	call(pSetTextColor, wp, cText.cref())
	return brush(bg)
}

// explorerTheme: wygląd list jak w Eksploratorze (podświetlenie wiersza).
func explorerTheme(h uintptr) { call(pSetWindowTheme, h, u16p("Explorer"), 0) }
