// Package win to warstwa systemowa SerwisDiag w Go: COM, WMI, OLE Automation, dzienniki zdarzeń, rejestr
// i funkcje API Windows wywoływane bezpośrednio - bez PowerShella, C# i bibliotek w C.
package win

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")

	procCoCreateInstance      = ole32.NewProc("CoCreateInstance")
	procCoInitializeSecurity  = ole32.NewProc("CoInitializeSecurity")
	procCoSetProxyBlanket     = ole32.NewProc("CoSetProxyBlanket")
	procCLSIDFromProgID       = ole32.NewProc("CLSIDFromProgID")
	procSysAllocString        = oleaut32.NewProc("SysAllocString")
	procSysFreeString         = oleaut32.NewProc("SysFreeString")
	procSysStringLen          = oleaut32.NewProc("SysStringLen")
	procVariantClear          = oleaut32.NewProc("VariantClear")
	procSafeArrayGetLBound    = oleaut32.NewProc("SafeArrayGetLBound")
	procSafeArrayGetUBound    = oleaut32.NewProc("SafeArrayGetUBound")
	procSafeArrayAccessData   = oleaut32.NewProc("SafeArrayAccessData")
	procSafeArrayUnaccessData = oleaut32.NewProc("SafeArrayUnaccessData")
)

const (
	clsctxInprocServer = 0x1
	clsctxLocalServer  = 0x4
)

// HResultError to błąd zwrócony przez metodę COM.
type HResultError struct {
	Op   string
	Code uint32
	Desc string
}

func (e *HResultError) Error() string {
	msg := e.Desc
	if msg == "" {
		msg = SystemMessage(e.Code)
	}
	if msg != "" {
		return fmt.Sprintf("%s: %s (0x%08X)", e.Op, msg, e.Code)
	}
	return fmt.Sprintf("%s: błąd 0x%08X", e.Op, e.Code)
}

// SystemMessage zwraca opis kodu błędu Windows w języku systemu (pusty, gdy Windows go nie zna).
func SystemMessage(code uint32) string {
	buf := make([]uint16, 1024)
	n, err := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS, 0, code, 0, buf, nil)
	if err != nil || n == 0 {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buf[:n]))
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func hrErr(op string, hr uintptr) error {
	if !failed(hr) {
		return nil
	}
	return &HResultError{Op: op, Code: uint32(hr)}
}

// ---------------------------------------------------------------- wątki COM

var mtaOnce sync.Once

// ensureMTA uruchamia wątek-kotwicę z COM w trybie wielowątkowym (MTA). Dopóki działa, połączenia WMI
// utworzone na jednym wątku MTA można używać z każdego innego - także po zakończeniu wątku, który je utworzył.
func ensureMTA() {
	mtaOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			_ = windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
			// Poziom personifikacji "impersonate" wymagają niektórzy dostawcy WMI (np. root\SecurityCenter2).
			procCoInitializeSecurity.Call(0, ^uintptr(0), 0, 0, 0, 3, 0, 0, 0)
			close(ready)
			select {}
		}()
		<-ready
	})
}

// WithCOM wykonuje f na wątku systemowym z zainicjowanym COM (MTA). Wywołania COM w Go muszą odbywać się
// na wątku z COM, a goroutine może zmieniać wątki - stąd przypięcie do wątku na czas f.
func WithCOM(f func()) {
	ensureMTA()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	if err == nil || err == syscall.Errno(windows.S_FALSE) {
		defer windows.CoUninitialize()
	}
	f()
}

// comCall wywołuje metodę nr idx z tablicy metod (vtable) obiektu COM; 0-2 to metody IUnknown.
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*int(unsafe.Sizeof(uintptr(0)))))
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(obj))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

func comAddRef(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 1)
	}
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

func setProxyBlanket(obj unsafe.Pointer) {
	// RPC_C_AUTHN_WINNT, RPC_C_AUTHZ_NONE, RPC_C_AUTHN_LEVEL_CALL, RPC_C_IMP_LEVEL_IMPERSONATE, EOAC_NONE
	procCoSetProxyBlanket.Call(uintptr(obj), 10, 0, 0, 3, 3, 0, 0)
}

// ---------------------------------------------------------------- BSTR

func sysAllocString(s string) uintptr {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	r, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(p)))
	return r
}

func sysFreeString(b uintptr) {
	if b != 0 {
		procSysFreeString.Call(b)
	}
}

func bstrToString(b uintptr) string {
	if b == 0 {
		return ""
	}
	n, _, _ := procSysStringLen.Call(b)
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(b)), n))
}

// ---------------------------------------------------------------- VARIANT

const (
	vtEmpty    = 0
	vtNull     = 1
	vtI2       = 2
	vtI4       = 3
	vtR4       = 4
	vtR8       = 5
	vtCY       = 6
	vtDate     = 7
	vtBSTR     = 8
	vtDispatch = 9
	vtError    = 10
	vtBool     = 11
	vtVariant  = 12
	vtUnknown  = 13
	vtDecimal  = 14
	vtI1       = 16
	vtUI1      = 17
	vtUI2      = 18
	vtUI4      = 19
	vtI8       = 20
	vtUI8      = 21
	vtInt      = 22
	vtUInt     = 23
	vtArray    = 0x2000
)

// variant odpowiada strukturze VARIANT (16 bajtów w 32 bitach, 24 w 64 bitach).
type variant struct {
	vt  uint16
	_   [3]uint16
	val [2]uintptr
}

func (v *variant) clear() { procVariantClear.Call(uintptr(unsafe.Pointer(v))) }

func (v *variant) ptr() unsafe.Pointer { return unsafe.Pointer(&v.val[0]) }

// value zamienia VARIANT na wartość Go: liczby całkowite -> int64 (UI8 -> uint64), rzeczywiste -> float64,
// BSTR -> string, DATE -> time.Time (czas "ścienny" oznaczony jako UTC), tablice -> wycinki, IDispatch -> *Dispatch.
func (v *variant) value() any {
	if v.vt&vtArray != 0 {
		return safeArrayValue(*(*uintptr)(v.ptr()), v.vt&^vtArray)
	}
	switch v.vt {
	case vtI1:
		return int64(*(*int8)(v.ptr()))
	case vtI2:
		return int64(*(*int16)(v.ptr()))
	case vtI4, vtInt, vtError:
		return int64(*(*int32)(v.ptr()))
	case vtI8:
		return *(*int64)(v.ptr())
	case vtUI1:
		return int64(*(*uint8)(v.ptr()))
	case vtUI2:
		return int64(*(*uint16)(v.ptr()))
	case vtUI4, vtUInt:
		return int64(*(*uint32)(v.ptr()))
	case vtUI8:
		return *(*uint64)(v.ptr())
	case vtR4:
		return float64(*(*float32)(v.ptr()))
	case vtR8:
		return *(*float64)(v.ptr())
	case vtCY:
		return float64(*(*int64)(v.ptr())) / 10000
	case vtBool:
		return *(*int16)(v.ptr()) != 0
	case vtBSTR:
		return bstrToString(*(*uintptr)(v.ptr()))
	case vtDate:
		return oleDate(*(*float64)(v.ptr()))
	case vtDecimal:
		return decimalValue(v)
	case vtDispatch:
		p := *(*unsafe.Pointer)(v.ptr())
		if p == nil {
			return nil
		}
		comAddRef(p)
		return &Dispatch{p: p}
	}
	return nil
}

// oleDate: data OLE Automation (dni od 30.12.1899) -> time.Time.
func oleDate(d float64) time.Time {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(math.Round(d*86400000)) * time.Millisecond)
}

// decimalValue: DECIMAL zajmuje cały VARIANT (pole vt pokrywa się z wReserved).
func decimalValue(v *variant) float64 {
	b := (*[16]byte)(unsafe.Pointer(v))
	scale, sign := b[2], b[3]
	hi := *(*uint32)(unsafe.Pointer(&b[4]))
	lo := *(*uint64)(unsafe.Pointer(&b[8]))
	f := (float64(hi)*18446744073709551616.0 + float64(lo)) / math.Pow10(int(scale))
	if sign&0x80 != 0 {
		f = -f
	}
	return f
}

func safeArrayValue(psa uintptr, vt uint16) any {
	if psa == 0 {
		return nil
	}
	var lb, ub int32
	procSafeArrayGetLBound.Call(psa, 1, uintptr(unsafe.Pointer(&lb)))
	procSafeArrayGetUBound.Call(psa, 1, uintptr(unsafe.Pointer(&ub)))
	n := int(ub) - int(lb) + 1
	if n < 0 {
		n = 0
	}
	var data unsafe.Pointer
	if hr, _, _ := procSafeArrayAccessData.Call(psa, uintptr(unsafe.Pointer(&data))); failed(hr) {
		return nil
	}
	defer procSafeArrayUnaccessData.Call(psa)
	if n > 0 && data == nil {
		return nil
	}
	switch vt {
	case vtUI1:
		b := make([]byte, n)
		if n > 0 {
			copy(b, unsafe.Slice((*byte)(data), n))
		}
		return b
	case vtBSTR:
		s := make([]string, n)
		if n > 0 {
			for i, p := range unsafe.Slice((*uintptr)(data), n) {
				s[i] = bstrToString(p)
			}
		}
		return s
	case vtI1, vtI2, vtI4, vtInt, vtUI2, vtUI4, vtUInt, vtI8, vtBool:
		out := make([]int64, n)
		for i := 0; i < n; i++ {
			switch vt {
			case vtI1:
				out[i] = int64(*(*int8)(unsafe.Add(data, i)))
			case vtI2, vtBool:
				out[i] = int64(*(*int16)(unsafe.Add(data, i*2)))
			case vtUI2:
				out[i] = int64(*(*uint16)(unsafe.Add(data, i*2)))
			case vtI4, vtInt:
				out[i] = int64(*(*int32)(unsafe.Add(data, i*4)))
			case vtUI4, vtUInt:
				out[i] = int64(*(*uint32)(unsafe.Add(data, i*4)))
			case vtI8:
				out[i] = *(*int64)(unsafe.Add(data, i*8))
			}
		}
		return out
	case vtR8:
		f := make([]float64, n)
		if n > 0 {
			copy(f, unsafe.Slice((*float64)(data), n))
		}
		return f
	case vtR4:
		f := make([]float64, n)
		for i, x := range unsafe.Slice((*float32)(data), n) {
			f[i] = float64(x)
		}
		return f
	case vtVariant:
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = (*variant)(unsafe.Add(data, i*int(unsafe.Sizeof(variant{})))).value()
		}
		return out
	}
	return nil
}
