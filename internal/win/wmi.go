package win

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WMI przez natywne interfejsy IWbemLocator / IWbemServices (wbemprox.dll) - te same, z których korzysta
// Get-CimInstance, ale bez warstwy PowerShella i obiektów CimInstance.

var (
	clsidWbemLocator = windows.GUID{Data1: 0x4590f811, Data2: 0x1d3a, Data3: 0x11d0, Data4: [8]byte{0x89, 0x1f, 0x00, 0xaa, 0x00, 0x4b, 0x2e, 0x24}}
	iidIWbemLocator  = windows.GUID{Data1: 0xdc12a687, Data2: 0x737f, Data3: 0x11cf, Data4: [8]byte{0x88, 0x4d, 0x00, 0xaa, 0x00, 0x4b, 0x2e, 0x24}}
)

// ErrTimeout: zapytanie WMI przekroczyło limit czasu.
var ErrTimeout = errors.New("przekroczono limit czasu")

type wmiNamespace struct {
	once sync.Once
	svc  unsafe.Pointer
	err  error
}

var (
	wmiMu      sync.Mutex
	wmiSpaces  = map[string]*wmiNamespace{}
	wmiLocOnce sync.Once
	wmiLoc     unsafe.Pointer
	wmiLocErr  error
)

func wmiService(ns string) (unsafe.Pointer, error) {
	wmiMu.Lock()
	e := wmiSpaces[strings.ToLower(ns)]
	if e == nil {
		e = &wmiNamespace{}
		wmiSpaces[strings.ToLower(ns)] = e
	}
	wmiMu.Unlock()
	e.once.Do(func() {
		wmiLocOnce.Do(func() {
			hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidWbemLocator)), 0, clsctxInprocServer,
				uintptr(unsafe.Pointer(&iidIWbemLocator)), uintptr(unsafe.Pointer(&wmiLoc)))
			wmiLocErr = hrErr("WMI (WbemLocator)", hr)
		})
		if wmiLocErr != nil {
			e.err = wmiLocErr
			return
		}
		res := sysAllocString(ns)
		defer sysFreeString(res)
		var svc unsafe.Pointer
		// IWbemLocator::ConnectServer(zasób, użytkownik, hasło, ustawienia regionalne, flagi, urząd, kontekst, wynik)
		hr := comCall(wmiLoc, 3, res, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&svc)))
		if failed(hr) {
			e.err = hrErr("WMI "+ns, hr)
			return
		}
		setProxyBlanket(svc)
		e.svc = svc
	})
	return e.svc, e.err
}

// WmiObject to jeden obiekt WMI: właściwości dostępne bez względu na wielkość liter.
type WmiObject map[string]any

// Raw zwraca wartość właściwości (nil, gdy brak).
func (o WmiObject) Raw(name string) any { return o[strings.ToLower(name)] }

// Has: właściwość istnieje i nie jest pusta (null).
func (o WmiObject) Has(name string) bool { return o[strings.ToLower(name)] != nil }

func (o WmiObject) Str(name string) string { return AnyString(o.Raw(name)) }
func (o WmiObject) Int(name string) int64  { return AnyInt(o.Raw(name)) }
func (o WmiObject) Float(name string) float64 {
	return AnyFloat(o.Raw(name))
}
func (o WmiObject) Bool(name string) bool { return AnyBool(o.Raw(name)) }

// Time zwraca datę (czas lokalny) i informację, czy właściwość ją zawiera.
func (o WmiObject) Time(name string) (time.Time, bool) {
	t, ok := o.Raw(name).(time.Time)
	return t, ok && !t.IsZero()
}

func (o WmiObject) Strs(name string) []string {
	switch v := o.Raw(name).(type) {
	case []string:
		return v
	case string:
		return []string{v}
	}
	return nil
}

func (o WmiObject) Ints(name string) []int64 {
	switch v := o.Raw(name).(type) {
	case []int64:
		return v
	case []byte:
		out := make([]int64, len(v))
		for i, b := range v {
			out[i] = int64(b)
		}
		return out
	case int64:
		return []int64{v}
	}
	return nil
}

func (o WmiObject) Bytes(name string) []byte {
	switch v := o.Raw(name).(type) {
	case []byte:
		return v
	case []int64:
		b := make([]byte, len(v))
		for i, x := range v {
			b[i] = byte(x)
		}
		return b
	}
	return nil
}

// WmiQuery wykonuje zapytanie WQL. props - właściwości do odczytania (puste: wszystkie poza systemowymi).
// Wynik częściowy i ErrTimeout, gdy dostawca nie zdążył przed upływem limitu.
func WmiQuery(ns, wql string, props []string, timeout time.Duration) ([]WmiObject, error) {
	var out []WmiObject
	var err error
	WithCOM(func() { out, err = wmiQuery(ns, wql, props, timeout) })
	return out, err
}

func wmiQuery(ns, wql string, props []string, timeout time.Duration) ([]WmiObject, error) {
	svc, err := wmiService(ns)
	if err != nil {
		return nil, err
	}
	lang := sysAllocString("WQL")
	defer sysFreeString(lang)
	q := sysAllocString(wql)
	defer sysFreeString(q)
	var enum unsafe.Pointer
	// IWbemServices::ExecQuery, WBEM_FLAG_RETURN_IMMEDIATELY | WBEM_FLAG_FORWARD_ONLY
	hr := comCall(svc, 20, lang, q, 0x10|0x20, 0, uintptr(unsafe.Pointer(&enum)))
	if failed(hr) {
		return nil, hrErr("WMI "+wql, hr)
	}
	defer comRelease(enum)
	setProxyBlanket(enum)

	names := make([]*uint16, len(props))
	for i, p := range props {
		names[i], _ = windows.UTF16PtrFromString(p)
	}
	deadline := time.Now().Add(timeout)
	var out []WmiObject
	var objs [64]unsafe.Pointer
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return out, ErrTimeout
		}
		wait := min(left.Milliseconds(), 1000)
		var n uint32
		// IEnumWbemClassObject::Next(limit czasu ms, liczba, tablica, zwrócono)
		hr := comCall(enum, 4, uintptr(wait), uintptr(len(objs)), uintptr(unsafe.Pointer(&objs[0])), uintptr(unsafe.Pointer(&n)))
		for i := 0; i < int(n); i++ {
			out = append(out, readWmiObject(objs[i], props, names))
			comRelease(objs[i])
			objs[i] = nil
		}
		switch {
		case failed(hr):
			return out, hrErr("WMI "+wql, hr)
		case hr == 1: // WBEM_S_FALSE - koniec wyników
			return out, nil
		}
	}
}

func readWmiObject(obj unsafe.Pointer, props []string, names []*uint16) WmiObject {
	m := make(WmiObject, max(len(props), 8))
	if len(props) == 0 {
		comCall(obj, 8, 0x40) // BeginEnumeration(WBEM_FLAG_NONSYSTEM_ONLY)
		for {
			var name uintptr
			var v variant
			var ct int32
			hr := comCall(obj, 9, 0, uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&ct)), 0)
			if hr != 0 {
				break
			}
			m[strings.ToLower(bstrToString(name))] = cimValue(&v, ct)
			sysFreeString(name)
			v.clear()
		}
		comCall(obj, 10) // EndEnumeration
		return m
	}
	for i, p := range props {
		var v variant
		var ct int32
		// IWbemClassObject::Get(nazwa, flagi, wartość, typ CIM, pochodzenie)
		if hr := comCall(obj, 4, uintptr(unsafe.Pointer(names[i])), 0, uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&ct)), 0); hr == 0 {
			m[strings.ToLower(p)] = cimValue(&v, ct)
		}
		v.clear()
	}
	return m
}

const (
	cimSint8    = 16
	cimUint8    = 17
	cimSint16   = 2
	cimUint16   = 18
	cimUint32   = 19
	cimSint64   = 20
	cimUint64   = 21
	cimDatetime = 101
	cimFlagArr  = 0x2000
)

// cimValue: WMI zwraca uint16/uint32 jako VT_I4, a liczby 64-bitowe i daty jako tekst - tu zamieniamy je
// na właściwe typy (jak Get-CimInstance).
func cimValue(v *variant, ct int32) any {
	val := v.value()
	switch ct &^ cimFlagArr {
	case cimUint32:
		switch x := val.(type) {
		case int64:
			return int64(uint32(x))
		case []int64:
			for i := range x {
				x[i] = int64(uint32(x[i]))
			}
		}
	case cimUint16:
		switch x := val.(type) {
		case int64:
			return int64(uint16(x))
		case []int64:
			for i := range x {
				x[i] = int64(uint16(x[i]))
			}
		}
	case cimSint64:
		if s, ok := val.(string); ok {
			n, _ := strconv.ParseInt(s, 10, 64)
			return n
		}
	case cimUint64:
		if s, ok := val.(string); ok {
			n, _ := strconv.ParseUint(s, 10, 64)
			return n
		}
		if a, ok := val.([]string); ok {
			out := make([]int64, len(a))
			for i, s := range a {
				n, _ := strconv.ParseUint(s, 10, 64)
				out[i] = int64(n)
			}
			return out
		}
	case cimDatetime:
		if s, ok := val.(string); ok {
			if t, ok := ParseCIMDateTime(s); ok {
				return t
			}
		}
	}
	return val
}

// ParseCIMDateTime: "20261026083012.500000+060" -> czas lokalny. Przedziały czasu (":000") i daty z gwiazdkami
// (nieznane pola) nie są datami.
func ParseCIMDateTime(s string) (time.Time, bool) {
	if len(s) < 25 || strings.ContainsRune(s[:14], '*') || s[21] == ':' {
		return time.Time{}, false
	}
	num := func(a, b int) int { n, _ := strconv.Atoi(s[a:b]); return n }
	micro := 0
	if !strings.ContainsRune(s[15:21], '*') {
		micro = num(15, 21)
	}
	off := 0
	if !strings.ContainsRune(s[22:25], '*') {
		off = num(22, 25)
		if s[21] == '-' {
			off = -off
		}
	}
	t := time.Date(num(0, 4), time.Month(num(4, 6)), num(6, 8), num(8, 10), num(10, 12), num(12, 14), micro*1000, time.FixedZone("", off*60))
	return t.Local(), true
}

// ---------------------------------------------------------------- konwersje wartości (semantyka rzutowań PowerShella)

// AnyString: jak [string] w PowerShellu (null -> "").
func AnyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case []string:
		return strings.Join(x, " ")
	case time.Time:
		return x.Format("01/02/2006 15:04:05")
	}
	return ""
}

// AnyInt: jak [int64] w PowerShellu (tekst z liczbą też się zamienia; brak -> 0).
func AnyInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case bool:
		if x {
			return 1
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n
		}
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return int64(f)
	}
	return 0
}

func AnyFloat(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// AnyBool: jak [bool] w PowerShellu (niepusty tekst i liczba różna od zera -> true).
func AnyBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case uint64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []string:
		return len(x) > 0
	case []int64:
		return len(x) > 0
	}
	return false
}
