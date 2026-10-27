package win

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Metody i zapis obiektów WMI - odpowiedniki Invoke-CimMethod i Set-CimInstance (naprawy: punkty przywracania,
// Defender, SMB1, plik wymiany).

// najczęstsze błędy WMI - FormatMessage systemu ich nie opisuje
var wbemErrors = map[uint32]string{
	0x80041001: "błąd ogólny WMI",
	0x80041002: "nie znaleziono obiektu",
	0x80041003: "odmowa dostępu",
	0x80041006: "brak pamięci",
	0x80041008: "nieprawidłowy parametr",
	0x8004100C: "operacja nieobsługiwana",
	0x80041010: "nieznana klasa WMI",
	0x80041013: "nie udało się załadować dostawcy WMI",
	0x80041017: "nieprawidłowe zapytanie",
	0x80041055: "nieznana metoda",
}

func wmiErr(op string, hr uintptr) error {
	if !failed(hr) {
		return nil
	}
	return &HResultError{Op: op, Code: uint32(hr), Desc: wbemErrors[uint32(hr)]}
}

func wmiGetObject(svc unsafe.Pointer, path string) (unsafe.Pointer, error) {
	b := sysAllocString(path)
	defer sysFreeString(b)
	var obj unsafe.Pointer
	// IWbemServices::GetObject(ścieżka, flagi, kontekst, obiekt, wynik wywołania)
	if hr := comCall(svc, 6, b, 0, 0, uintptr(unsafe.Pointer(&obj)), 0); failed(hr) {
		return nil, wmiErr("WMI "+path, hr)
	}
	return obj, nil
}

// wmiPut ustawia właściwość obiektu WMI (bool, string, int; typ CIM - jak w definicji klasy).
func wmiPut(obj unsafe.Pointer, name string, v any) error {
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var vr variant
	if err := setArg(&vr, v); err != nil {
		return err
	}
	defer vr.clear()
	// IWbemClassObject::Put(nazwa, flagi, wartość, typ CIM)
	if hr := comCall(obj, 5, uintptr(unsafe.Pointer(n16)), 0, uintptr(unsafe.Pointer(&vr)), 0); failed(hr) {
		return wmiErr(name, hr)
	}
	return nil
}

func wmiClassOf(path string) string {
	if i := strings.IndexAny(path, ".="); i > 0 {
		return path[:i]
	}
	return path
}

// wmiMethodSig zwraca sygnatury parametrów metody (wejściowe mogą być nil - metoda bez parametrów).
func wmiMethodSig(svc unsafe.Pointer, class, method string) (in, out unsafe.Pointer, err error) {
	cls, err := wmiGetObject(svc, class)
	if err != nil {
		return nil, nil, err
	}
	defer comRelease(cls)
	m16, _ := windows.UTF16PtrFromString(method)
	// IWbemClassObject::GetMethod(nazwa, flagi, parametry wejściowe, parametry wyjściowe)
	if hr := comCall(cls, 19, uintptr(unsafe.Pointer(m16)), 0, uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(&out))); failed(hr) {
		return nil, nil, wmiErr("WMI "+class+"."+method, hr)
	}
	return in, out, nil
}

// WmiExecMethod wywołuje metodę WMI (Invoke-CimMethod): path - nazwa klasy dla metody statycznej (np. "SystemRestore")
// albo ścieżka obiektu (np. `Win32_Service.Name="wuauserv"`); in - parametry wejściowe. Zwraca parametry
// wyjściowe (m.in. ReturnValue).
func WmiExecMethod(ns, path, method string, in map[string]any) (WmiObject, error) {
	var out WmiObject
	var err error
	WithCOM(func() { out, err = wmiExecMethod(ns, path, method, in) })
	return out, err
}

func wmiExecMethod(ns, path, method string, in map[string]any) (WmiObject, error) {
	svc, err := wmiService(ns)
	if err != nil {
		return nil, err
	}
	class := wmiClassOf(path)
	inSig, outSig, err := wmiMethodSig(svc, class, method)
	if err != nil {
		return nil, err
	}
	defer comRelease(inSig)
	defer comRelease(outSig)
	var inst unsafe.Pointer
	if inSig != nil {
		// IWbemClassObject::SpawnInstance(flagi, nowy obiekt)
		if hr := comCall(inSig, 15, 0, uintptr(unsafe.Pointer(&inst))); failed(hr) {
			return nil, wmiErr("WMI "+class+"."+method, hr)
		}
		defer comRelease(inst)
		for name, v := range in {
			if err := wmiPut(inst, name, v); err != nil {
				return nil, fmt.Errorf("WMI %s.%s, parametr %s: %w", class, method, name, err)
			}
		}
	} else if len(in) > 0 {
		return nil, fmt.Errorf("WMI %s.%s nie przyjmuje parametrów", class, method)
	}
	p := sysAllocString(path)
	defer sysFreeString(p)
	m := sysAllocString(method)
	defer sysFreeString(m)
	var outp unsafe.Pointer
	// IWbemServices::ExecMethod(ścieżka, metoda, flagi, kontekst, parametry, wynik, wynik wywołania)
	if hr := comCall(svc, 24, p, m, 0, 0, uintptr(inst), uintptr(unsafe.Pointer(&outp)), 0); failed(hr) {
		return nil, wmiErr("WMI "+class+"."+method, hr)
	}
	if outp == nil {
		return WmiObject{}, nil
	}
	defer comRelease(outp)
	return readWmiObject(outp, nil, nil), nil
}

// WmiMethodParams zwraca nazwy parametrów wejściowych metody (małymi literami) - bez jej wywoływania.
func WmiMethodParams(ns, class, method string) ([]string, error) {
	var names []string
	var err error
	WithCOM(func() {
		var svc unsafe.Pointer
		if svc, err = wmiService(ns); err != nil {
			return
		}
		var in, out unsafe.Pointer
		if in, out, err = wmiMethodSig(svc, class, method); err != nil {
			return
		}
		defer comRelease(in)
		defer comRelease(out)
		if in != nil {
			for k := range readWmiObject(in, nil, nil) {
				names = append(names, k)
			}
		}
	})
	return names, err
}

// WmiSetProperties zmienia właściwości obiektu WMI i zapisuje go (Set-CimInstance -Property), np.
// path `Win32_ComputerSystem.Name="PC"`.
func WmiSetProperties(ns, path string, props map[string]any) error {
	var err error
	WithCOM(func() {
		var svc unsafe.Pointer
		if svc, err = wmiService(ns); err != nil {
			return
		}
		var obj unsafe.Pointer
		if obj, err = wmiGetObject(svc, path); err != nil {
			return
		}
		defer comRelease(obj)
		for name, v := range props {
			if err = wmiPut(obj, name, v); err != nil {
				return
			}
		}
		// IWbemServices::PutInstance(obiekt, WBEM_FLAG_UPDATE_ONLY, kontekst, wynik wywołania)
		err = wmiErr("WMI "+path, comCall(svc, 14, uintptr(obj), 1, 0, 0))
	})
	return err
}

// EnablePrivilege włącza uprawnienie w żetonie procesu (np. SeCreatePagefilePrivilege przed zmianą pliku
// wymiany przez WMI - jak Security_.Privileges w skryptach). Uprawnienie musi należeć do konta.
func EnablePrivilege(name string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, n16, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}
