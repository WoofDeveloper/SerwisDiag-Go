// Package ui to okno programu SerwisDiag i monitor przy zegarze w czystym Go: natywne okna Windows (user32, gdi32,
// comctl32 v6) bez WPF, .NET i cgo. Wygląd odwzorowuje okna WPF wersji PowerShell (lib\Gui.ps1, lib\Monitor.ps1).
package ui

import (
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------- biblioteki i funkcje

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	uxtheme  = windows.NewLazySystemDLL("uxtheme.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pUpdateWindow             = user32.NewProc("UpdateWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW         = user32.NewProc("IsDialogMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pMoveWindow               = user32.NewProc("MoveWindow")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pInvalidateRect           = user32.NewProc("InvalidateRect")
	pBeginPaint               = user32.NewProc("BeginPaint")
	pEndPaint                 = user32.NewProc("EndPaint")
	pGetDC                    = user32.NewProc("GetDC")
	pReleaseDC                = user32.NewProc("ReleaseDC")
	pSetTimer                 = user32.NewProc("SetTimer")
	pKillTimer                = user32.NewProc("KillTimer")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pSetCursor                = user32.NewProc("SetCursor")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	pAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow             = user32.NewProc("EnableWindow")
	pIsWindowEnabled          = user32.NewProc("IsWindowEnabled")
	pSetFocus                 = user32.NewProc("SetFocus")
	pGetFocus                 = user32.NewProc("GetFocus")
	pGetNextDlgTabItem        = user32.NewProc("GetNextDlgTabItem")
	pSetScrollInfo            = user32.NewProc("SetScrollInfo")
	pGetScrollInfo            = user32.NewProc("GetScrollInfo")
	pTrackMouseEvent          = user32.NewProc("TrackMouseEvent")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pScreenToClient           = user32.NewProc("ScreenToClient")
	pClientToScreen           = user32.NewProc("ClientToScreen")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pCreateIconIndirect       = user32.NewProc("CreateIconIndirect")
	pDestroyIcon              = user32.NewProc("DestroyIcon")
	pOpenClipboard            = user32.NewProc("OpenClipboard")
	pEmptyClipboard           = user32.NewProc("EmptyClipboard")
	pSetClipboardData         = user32.NewProc("SetClipboardData")
	pCloseClipboard           = user32.NewProc("CloseClipboard")
	pMonitorFromWindow        = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pIsWindow                 = user32.NewProc("IsWindow")
	pIsIconic                 = user32.NewProc("IsIconic")
	pGetKeyState              = user32.NewProc("GetKeyState")
	pMapVirtualKeyW           = user32.NewProc("MapVirtualKeyW")
	pFillRect                 = user32.NewProc("FillRect")
	pDrawTextW                = user32.NewProc("DrawTextW")
	pSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")
	pGetAncestor              = user32.NewProc("GetAncestor")
	pPrintWindow              = user32.NewProc("PrintWindow")
	pSetCapture               = user32.NewProc("SetCapture")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pGetCapture               = user32.NewProc("GetCapture")
	pGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	pBringWindowToTop         = user32.NewProc("BringWindowToTop")
	pSetActiveWindow          = user32.NewProc("SetActiveWindow")
	pMessageBeep              = user32.NewProc("MessageBeep")

	pInitCommonControlsEx  = comctl32.NewProc("InitCommonControlsEx")
	pImageListCreate       = comctl32.NewProc("ImageList_Create")
	pImageListDestroy      = comctl32.NewProc("ImageList_Destroy")
	pSetWindowSubclass     = comctl32.NewProc("SetWindowSubclass")
	pDefSubclassProc       = comctl32.NewProc("DefSubclassProc")
	pTaskDialogIndirect    = comctl32.NewProc("TaskDialogIndirect")
	pCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	pSelectObject          = gdi32.NewProc("SelectObject")
	pDeleteObject          = gdi32.NewProc("DeleteObject")
	pDeleteDC              = gdi32.NewProc("DeleteDC")
	pBitBlt                = gdi32.NewProc("BitBlt")
	pCreateFontW           = gdi32.NewProc("CreateFontW")
	pSetTextColor          = gdi32.NewProc("SetTextColor")
	pSetBkMode             = gdi32.NewProc("SetBkMode")
	pSetBkColor            = gdi32.NewProc("SetBkColor")
	pGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
	pExtTextOutW           = gdi32.NewProc("ExtTextOutW")
	pCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	pGdiFlush              = gdi32.NewProc("GdiFlush")
	pGetTextMetricsW       = gdi32.NewProc("GetTextMetricsW")
	pCreateBitmap          = gdi32.NewProc("CreateBitmap")

	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	pSetWindowTheme   = uxtheme.NewProc("SetWindowTheme")

	pGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	pGlobalLock   = kernel32.NewProc("GlobalLock")
	pGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	pGlobalFree   = kernel32.NewProc("GlobalFree")

	pCoCreateInstance = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// call wywołuje funkcję API bez odczytu GetLastError (szybciej niż LazyProc.Call w malowaniu).
// uintptrescapes: wskaźniki zamienione na uintptr w argumentach (uintptr(unsafe.Pointer(&x))) trafiają na stertę
// i żyją do końca wywołania - bez tego stos gorutyny mógłby się przenieść, a bufor zostać zwolniony.
//
//go:uintptrescapes
func call(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(p.Addr(), a...)
	return r
}

// u16 zamienia napis na UTF-16 zakończony zerem.
func u16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		p, _ = windows.UTF16PtrFromString("?")
	}
	return p
}

// keepStr: ostatnie bufory z u16p - bufor podany jako uintptr nie jest widoczny dla odśmiecania pamięci,
// więc trzymamy go jeszcze przez kolejne 256 wywołań (wywołania API kończą się dużo wcześniej).
var (
	keepMu  sync.Mutex
	keepStr [256]*uint16
	keepIdx int
)

func u16p(s string) uintptr {
	p := u16(s)
	keepMu.Lock()
	keepStr[keepIdx] = p
	keepIdx = (keepIdx + 1) % len(keepStr)
	keepMu.Unlock()
	return uintptr(unsafe.Pointer(p))
}

// ---------------------------------------------------------------- struktury

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) w() int { return int(r.Right - r.Left) }
func (r rect) h() int { return int(r.Bottom - r.Top) }

type point struct{ X, Y int32 }

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	RcPaint   rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type textMetric struct {
	Height, Ascent, Descent, InternalLeading, ExternalLeading int32
	AveCharWidth, MaxCharWidth, Weight, Overhang              int32
	DigitizedAspectX, DigitizedAspectY                        int32
	FirstChar, LastChar, DefaultChar, BreakChar               uint16
	Italic, Underlined, StruckOut, PitchAndFamily, CharSet    byte
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type scrollInfo struct {
	Size     uint32
	Mask     uint32
	Min      int32
	Max      int32
	Page     uint32
	Pos      int32
	TrackPos int32
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	Track     uintptr
	HoverTime uint32
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize point
}

type nmhdr struct {
	HwndFrom uintptr
	IDFrom   uintptr
	Code     int32
}

type lvColumn struct {
	Mask      uint32
	Fmt       int32
	Cx        int32
	Text      *uint16
	TextMax   int32
	SubItem   int32
	Image     int32
	Order     int32
	CxMin     int32
	CxDefault int32
	CxIdeal   int32
}

type lvItem struct {
	Mask      uint32
	Item      int32
	SubItem   int32
	State     uint32
	StateMask uint32
	Text      uintptr
	TextMax   int32
	Image     int32
	LParam    uintptr
	Indent    int32
	GroupID   int32
	Columns   uint32
	PuColumns uintptr
	PiColFmt  uintptr
	Group     int32
}

type nmLVDispInfo struct {
	Hdr  nmhdr
	Item lvItem
}

type nmListView struct {
	Hdr      nmhdr
	Item     int32
	SubItem  int32
	NewState uint32
	OldState uint32
	Changed  uint32
	Action   point
	LParam   uintptr
}

type nmCustomDraw struct {
	Hdr        nmhdr
	DrawStage  uint32
	Hdc        uintptr
	Rc         rect
	ItemSpec   uintptr
	ItemState  uint32
	ItemlParam uintptr
}

type nmLVCustomDraw struct {
	Nmcd       nmCustomDraw
	ClrText    uint32
	ClrTextBk  uint32
	SubItem    int32
	ItemType   uint32
	ClrFace    uint32
	IconEffect int32
	IconPhase  int32
	PartID     int32
	StateID    int32
	RcText     rect
	Align      uint32
}

type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	HwndItem   uintptr
	Hdc        uintptr
	RcItem     rect
	ItemData   uintptr
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUIDItem         windows.GUID
	BalloonIcon      uintptr
}

// ---------------------------------------------------------------- stałe

const (
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsDisabled         = 0x08000000
	wsClipSiblings     = 0x04000000
	wsClipChildren     = 0x02000000
	wsCaption          = 0x00C00000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	wsHScroll          = 0x00100000
	wsSysMenu          = 0x00080000
	wsThickFrame       = 0x00040000
	wsGroup            = 0x00020000
	wsTabStop          = 0x00010000
	wsMinimizeBox      = 0x00020000
	wsMaximizeBox      = 0x00010000

	wsExDlgModalFrame   = 0x00000001
	wsExTopmost         = 0x00000008
	wsExToolWindow      = 0x00000080
	wsExClientEdge      = 0x00000200
	wsExControlParent   = 0x00010000
	wsExAppWindow       = 0x00040000
	wsExNoActivate      = 0x08000000
	wsExComposited      = 0x02000000
	cwUseDefault        = 0x80000000
	csVRedraw           = 0x0001
	csHRedraw           = 0x0002
	csDblClks           = 0x0008
	gwlpUserData        = -21
	gwlStyle            = -16
	gwlExStyle          = -20
	gaRoot              = 2
	swHide              = 0
	swShowNormal        = 1
	swShowMaximized     = 3
	swShowNoActivate    = 4
	swShow              = 5
	swMinimize          = 6
	swRestore           = 9
	swpNoSize           = 0x0001
	swpNoMove           = 0x0002
	swpNoZOrder         = 0x0004
	swpNoActivate       = 0x0010
	swpFrameChanged     = 0x0020
	swpShowWindow       = 0x0040
	swpHideWindow       = 0x0080
	hwndTopmost         = ^uintptr(0) // -1
	idcArrow            = 32512
	idcIBeam            = 32513
	idcWait             = 32514
	idcHand             = 32649
	imageIcon           = 1
	lrDefaultColor      = 0
	iconSmall           = 0
	iconBig             = 1
	pwRenderFullContent = 2

	wmNull            = 0x0000
	wmEndSession      = 0x0016
	wmCreate          = 0x0001
	wmDestroy         = 0x0002
	wmMove            = 0x0003
	wmSize            = 0x0005
	wmActivate        = 0x0006
	wmSetFocus        = 0x0007
	wmKillFocus       = 0x0008
	wmEnable          = 0x000A
	wmSetRedraw       = 0x000B
	wmSetText         = 0x000C
	wmPaint           = 0x000F
	wmClose           = 0x0010
	wmQuit            = 0x0012
	wmEraseBkgnd      = 0x0014
	wmSettingChange   = 0x001A
	wmSetCursor       = 0x0020
	wmGetMinMaxInfo   = 0x0024
	wmNextDlgCtl      = 0x0028
	wmDrawItem        = 0x002B
	wmSetFont         = 0x0030
	wmGetFont         = 0x0031
	wmSetIcon         = 0x0080
	wmNotify          = 0x004E
	wmContextMenu     = 0x007B
	wmNCDestroy       = 0x0082
	wmGetDlgCode      = 0x0087
	wmKeyDown         = 0x0100
	wmKeyUp           = 0x0101
	wmChar            = 0x0102
	wmSysKeyDown      = 0x0104
	wmSysKeyUp        = 0x0105
	wmSysChar         = 0x0106
	wmCommand         = 0x0111
	wmSysCommand      = 0x0112
	wmTimer           = 0x0113
	wmHScroll         = 0x0114
	wmVScroll         = 0x0115
	wmCtlColorEdit    = 0x0133
	wmCtlColorListBox = 0x0134
	wmCtlColorBtn     = 0x0135
	wmCtlColorDlg     = 0x0136
	wmCtlColorStatic  = 0x0138
	wmMouseMove       = 0x0200
	wmLButtonDown     = 0x0201
	wmLButtonUp       = 0x0202
	wmLButtonDblClk   = 0x0203
	wmRButtonDown     = 0x0204
	wmRButtonUp       = 0x0205
	wmMouseWheel      = 0x020A
	wmMouseLeave      = 0x02A3
	wmDpiChanged      = 0x02E0
	wmThemeChanged    = 0x031A
	wmUser            = 0x0400
	wmApp             = 0x8000

	bnClicked       = 0
	cbnSelChange    = 1
	enChange        = 0x0300
	bsPushButton    = 0x0
	bsDefPush       = 0x1
	bsAutoCheck     = 0x3
	bsOwnerDraw     = 0xB
	bmGetCheck      = 0x00F0
	bmSetCheck      = 0x00F1
	bstChecked      = 1
	esMultiline     = 0x0004
	esAutoVScroll   = 0x0040
	esAutoHScroll   = 0x0080
	esReadOnly      = 0x0800
	esWantReturn    = 0x1000
	emSetSel        = 0x00B1
	emLimitText     = 0x00C5
	emSetMargins    = 0x00D3
	emSetCueBanner  = 0x1501
	cbsDropDownList = 0x0003
	cbAddString     = 0x0143
	cbGetCount      = 0x0146
	cbGetCurSel     = 0x0147
	cbResetContent  = 0x014B
	cbSetCurSel     = 0x014E
	cbSetItemHeight = 0x0153
	cbShowDropDown  = 0x014F
	cbSetMinVisible = 0x1701

	dlgcWantArrows  = 0x0001
	dlgcWantTab     = 0x0002
	dlgcWantAllKeys = 0x0004
	dlgcWantChars   = 0x0080
	dlgcButton      = 0x2000
	idOK            = 1
	idCancel        = 2
	idYes           = 6
	idNo            = 7
	mbOK            = 0x00
	mbYesNo         = 0x04
	mbIconError     = 0x10
	mbIconQuestion  = 0x20
	mbIconWarning   = 0x30
	mbIconInfo      = 0x40
	mbSetForeground = 0x10000
	mbTopmost       = 0x40000

	sbVert             = 1
	sifRange           = 0x1
	sifPage            = 0x2
	sifPos             = 0x4
	sifTrackPos        = 0x10
	sifDisableNoScroll = 0x8
	sbLineUp           = 0
	sbLineDown         = 1
	sbPageUp           = 2
	sbPageDown         = 3
	sbThumbPosition    = 4
	sbThumbTrack       = 5
	sbTop              = 6
	sbBottom           = 7

	tmeLeave           = 0x2
	spiGetWorkArea     = 0x0030
	spiGetWheelScroll  = 0x0068
	monitorDefaultNear = 2
	smCxIcon           = 11
	smCxSmIcon         = 49
	smCySmIcon         = 50

	mfString       = 0x0000
	mfGrayed       = 0x0001
	mfChecked      = 0x0008
	mfPopup        = 0x0010
	mfSeparator    = 0x0800
	mftRadio       = 0x0200
	tpmLeftAlign   = 0x0000
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNoNotify    = 0x0080

	nimAdd              = 0
	nimModify           = 1
	nimDelete           = 2
	nimSetVersion       = 4
	nifMessage          = 0x01
	nifIcon             = 0x02
	nifTip              = 0x04
	nifInfo             = 0x10
	niifInfo            = 1
	niifWarning         = 2
	niifError           = 3
	ninBalloonUserClick = wmUser + 5

	lvsReport         = 0x0001
	lvsSingleSel      = 0x0004
	lvsShowSelAlways  = 0x0008
	lvsOwnerData      = 0x1000
	lvsNoSortHeader   = 0x8000
	lvmFirst          = 0x1000
	lvmSetImageList   = lvmFirst + 3
	lvmGetItemCount   = lvmFirst + 4
	lvmGetNextItem    = lvmFirst + 12
	lvmEnsureVisible  = lvmFirst + 19
	lvmRedrawItems    = lvmFirst + 21
	lvmDeleteColumn   = lvmFirst + 28
	lvmGetColumnWidth = lvmFirst + 29
	lvmSetColumnWidth = lvmFirst + 30
	lvmGetHeader      = lvmFirst + 31
	lvmSetItemState   = lvmFirst + 43
	lvmSetItemCount   = lvmFirst + 47
	lvmSetExStyle     = lvmFirst + 54
	lvmGetSubItemRect = lvmFirst + 56
	lvmInsertColumnW  = lvmFirst + 97
	lvsExGridLines    = 0x00000001
	lvsExFullRowSel   = 0x00000020
	lvsExLabelTip     = 0x00004000
	lvsExDoubleBuffer = 0x00010000
	lvcfFmt           = 0x1
	lvcfWidth         = 0x2
	lvcfText          = 0x4
	lvcfSubItem       = 0x8
	lvifText          = 0x1
	lvifState         = 0x8
	lvisFocused       = 0x1
	lvisSelected      = 0x2
	lvniSelected      = 0x2
	lvsilSmall        = 1
	lvirBounds        = 0
	ilcColor32        = 0x20
	hdmGetItemCount   = 0x1200

	nmClick         = -2
	nmDblClk        = -3
	nmCustomDrawN   = -12
	lvnItemChanged  = -101
	lvnGetDispInfoW = -177

	cddsPrePaint          = 0x00001
	cddsPostPaint         = 0x00002
	cddsItem              = 0x10000
	cddsSubItem           = 0x20000
	cddsItemPrePaint      = cddsItem | cddsPrePaint
	cddsItemPostPaint     = cddsItem | cddsPostPaint
	cdrfDoDefault         = 0x00
	cdrfNewFont           = 0x02
	cdrfSkipDefault       = 0x04
	cdrfNotifyPostPaint   = 0x10
	cdrfNotifyItemDraw    = 0x20
	cdrfNotifySubItemDraw = 0x20
	cdisSelected          = 0x0001
	cdisHot               = 0x0040

	odsSelected    = 0x0001
	odsDisabled    = 0x0004
	odsFocus       = 0x0010
	odsNoFocusRect = 0x0200

	dtLeft        = 0x0000
	dtCenter      = 0x0001
	dtRight       = 0x0002
	dtVCenter     = 0x0004
	dtWordBreak   = 0x0010
	dtSingleLine  = 0x0020
	dtNoClip      = 0x0100
	dtCalcRect    = 0x0400
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000

	transparentBk = 1
	srcCopy       = 0x00CC0020
	etoOpaque     = 0x0002
	etoClipped    = 0x0004
	fwNormal      = 400
	fwSemiBold    = 600
	fwBold        = 700
	cleartypeQ    = 5
	cfUnicodeText = 13
	gmemMoveable  = 0x0002

	vkBack     = 0x08
	vkTab      = 0x09
	vkReturn   = 0x0D
	vkShift    = 0x10
	vkControl  = 0x11
	vkMenu     = 0x12
	vkEscape   = 0x1B
	vkSpace    = 0x20
	vkPrior    = 0x21
	vkNext     = 0x22
	vkEnd      = 0x23
	vkHome     = 0x24
	vkLeft     = 0x25
	vkUp       = 0x26
	vkRight    = 0x27
	vkDown     = 0x28
	vkSnapshot = 0x2C
	vkF4       = 0x73
)

// ---------------------------------------------------------------- krótkie opakowania

//go:uintptrescapes
func sendMsg(h uintptr, m uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(pSendMessageW.Addr(), h, uintptr(m), wp, lp)
	return r
}

func postMsg(h uintptr, m uint32, wp, lp uintptr) { call(pPostMessageW, h, uintptr(m), wp, lp) }

func clientRect(h uintptr) rect {
	var r rect
	call(pGetClientRect, h, uintptr(unsafe.Pointer(&r)))
	return r
}

func windowRect(h uintptr) rect {
	var r rect
	call(pGetWindowRect, h, uintptr(unsafe.Pointer(&r)))
	return r
}

func invalidate(h uintptr) {
	if h != 0 {
		call(pInvalidateRect, h, 0, 0)
	}
}

func moveWin(h uintptr, x, y, w, ht int) {
	call(pMoveWindow, h, uintptr(x), uintptr(y), uintptr(max(w, 0)), uintptr(max(ht, 0)), 1)
}

func showWin(h uintptr, show bool) {
	cmd := uintptr(swHide)
	if show {
		cmd = swShowNoActivate
	}
	call(pShowWindow, h, cmd)
}

func isVisible(h uintptr) bool { return call(pIsWindowVisible, h) != 0 }

func enableWin(h uintptr, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	call(pEnableWindow, h, v)
}

func setText(h uintptr, s string) { call(pSetWindowTextW, h, u16p(s)) }

func getText(h uintptr) string {
	n := int(call(pGetWindowTextLengthW, h))
	if n <= 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	call(pGetWindowTextW, h, uintptr(unsafe.Pointer(&buf[0])), uintptr(n+1))
	return windows.UTF16ToString(buf)
}

func dpiOf(h uintptr) int {
	if pGetDpiForWindow.Find() == nil && h != 0 {
		if d := int(call(pGetDpiForWindow, h)); d > 0 {
			return d
		}
	}
	if pGetDpiForSystem.Find() == nil {
		if d := int(call(pGetDpiForSystem)); d > 0 {
			return d
		}
	}
	return 96
}

func metricForDpi(idx, dpi int) int {
	if pGetSystemMetricsForDpi.Find() == nil {
		return int(call(pGetSystemMetricsForDpi, uintptr(idx), uintptr(dpi)))
	}
	return 16 * dpi / 96
}

func loadCursor(id int) uintptr { return call(pLoadCursorW, 0, uintptr(id)) }

func hinstance() uintptr {
	var h windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &h)
	return uintptr(h)
}

func lowWord(v uintptr) int  { return int(int16(uint16(v))) }
func highWord(v uintptr) int { return int(int16(uint16(v >> 16))) }
func mkLParam(lo, hi int) uintptr {
	return uintptr(uint32(uint16(int16(lo))) | uint32(uint16(int16(hi)))<<16)
}

// workArea zwraca obszar roboczy monitora, na którym jest okno (bez paska zadań).
func workArea(h uintptr) rect {
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	mon := call(pMonitorFromWindow, h, monitorDefaultNear)
	if mon != 0 && call(pGetMonitorInfoW, mon, uintptr(unsafe.Pointer(&mi))) != 0 {
		return mi.Work
	}
	var r rect
	call(pSystemParametersInfoW, spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	return r
}

func cursorPos() point {
	var p point
	call(pGetCursorPos, uintptr(unsafe.Pointer(&p)))
	return p
}

func screenToClient(h uintptr, p point) point {
	call(pScreenToClient, h, uintptr(unsafe.Pointer(&p)))
	return p
}

func clientToScreen(h uintptr, p point) point {
	call(pClientToScreen, h, uintptr(unsafe.Pointer(&p)))
	return p
}

func trackLeave(h uintptr) {
	t := trackMouseEvent{Size: uint32(unsafe.Sizeof(trackMouseEvent{})), Flags: tmeLeave, Track: h}
	call(pTrackMouseEvent, uintptr(unsafe.Pointer(&t)))
}

// keyDown: klawisz wciśnięty (GetKeyState).
func keyDown(vk int) bool { return int16(call(pGetKeyState, uintptr(vk))) < 0 }
