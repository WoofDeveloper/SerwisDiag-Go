package ui

import (
	"runtime"
	"testing"
	"unsafe"
)

// TestDialogTabFromMultilineEdit: Tab w wielowierszowym polu tekstowym (opis prac w oknie porównania) przechodzi
// do następnej kontrolki - pole wysyła WM_NEXTDLGCTL do okna nadrzędnego, które nie jest oknem dialogowym systemu.
func TestDialogTabFromMultilineEdit(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := newDialog(0, "test", 300, 200, false)
	defer call(pDestroyWindow, d.h)
	ed := newControl(d.h, "EDIT", "", wsTabStop|esMultiline|esWantReturn, wsExClientEdge, 901)
	btn := newButton(d.h, 902, "OK")
	moveWin(ed, 10, 10, 200, 60)
	moveWin(btn, 10, 80, 80, 30)
	x := -30000 // poza ekranem
	call(pSetWindowPos, d.h, 0, uintptr(x), uintptr(x), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
	call(pShowWindow, d.h, swShowNoActivate)
	call(pSetFocus, ed)
	if call(pGetFocus) != ed {
		t.Skip("nie można ustawić fokusu w tym środowisku")
	}
	// naciśnięcie Tab tak, jak obsługuje je pętla komunikatów okien (pump)
	m := msgT{Hwnd: ed, Message: wmKeyDown, WParam: vkTab}
	if call(pIsDialogMessageW, d.h, uintptr(unsafe.Pointer(&m))) == 0 {
		call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
		call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
	}
	if f := call(pGetFocus); f != btn {
		t.Fatalf("Tab w polu wielowierszowym: fokus %#x, oczekiwano przycisku %#x", f, btn)
	}
	if getText(ed) != "" {
		t.Fatalf("Tab wstawił tekst do pola: %q", getText(ed))
	}
}
