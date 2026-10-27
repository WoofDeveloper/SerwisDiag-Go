package win

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo odpowiada SHELLEXECUTEINFOW (112 bajtów w 64 bitach).
type shellExecuteInfo struct {
	cbSize     uint32
	fMask      uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

// RunVisibleWait uruchamia program z oknem przez powłokę (Start-Process -PassThru) - np. deinstalator, który
// technik przechodzi ręcznie - i czeka na jego zakończenie najwyżej timeout. finished = false: program nadal działa.
func RunVisibleWait(file, args string, timeout time.Duration) (exitCode int, finished bool, err error) {
	f16, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return -1, false, err
	}
	var a16 *uint16
	if args != "" {
		if a16, err = windows.UTF16PtrFromString(args); err != nil {
			return -1, false, err
		}
	}
	info := shellExecuteInfo{fMask: 0x40 | 0x400 /*SEE_MASK_NOCLOSEPROCESS | SEE_MASK_FLAG_NO_UI*/, file: f16, parameters: a16, show: windows.SW_SHOWNORMAL}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return -1, false, e
	}
	if info.process == 0 {
		return 0, true, nil // program przekazany do już działającej instancji
	}
	defer windows.CloseHandle(info.process)
	ev, err := windows.WaitForSingleObject(info.process, uint32(timeout.Milliseconds()))
	if err != nil {
		return -1, false, err
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return -1, false, nil
	}
	var code uint32
	_ = windows.GetExitCodeProcess(info.process, &code)
	return int(code), true, nil
}
