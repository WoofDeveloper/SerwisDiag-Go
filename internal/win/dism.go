package win

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DISM API (dismapi.dll) - to samo, co wywołuje Repair-WindowsImage -CheckHealth / -ScanHealth.

var (
	dismapi                  = windows.NewLazySystemDLL("dismapi.dll")
	procDismInitialize       = dismapi.NewProc("DismInitialize")
	procDismOpenSession      = dismapi.NewProc("DismOpenSession")
	procDismCheckImageHealth = dismapi.NewProc("DismCheckImageHealth")
	procDismCloseSession     = dismapi.NewProc("DismCloseSession")
	procDismShutdown         = dismapi.NewProc("DismShutdown")
	dismMu                   sync.Mutex
)

// ImageHealth sprawdza magazyn składników działającego systemu. scan = true: pełne skanowanie (ScanHealth,
// kilka minut), false: szybki odczyt stanu (CheckHealth). Wynik: Healthy, Repairable albo NonRepairable.
// Wymaga uprawnień administratora.
func ImageHealth(scan bool) (string, error) {
	dismMu.Lock()
	defer dismMu.Unlock()
	if err := dismapi.Load(); err != nil {
		return "", err
	}
	if r, _, _ := procDismInitialize.Call(0, 0, 0); failed(r) {
		return "", hrErr("DismInitialize", r)
	}
	defer procDismShutdown.Call()
	image, _ := windows.UTF16PtrFromString("DISM_{53BFAE52-B167-4E2F-A258-0A37B57FF845}")
	var session uint32
	if r, _, _ := procDismOpenSession.Call(uintptr(unsafe.Pointer(image)), 0, 0, uintptr(unsafe.Pointer(&session))); failed(r) {
		return "", hrErr("DismOpenSession", r)
	}
	defer procDismCloseSession.Call(uintptr(session))
	var state int32
	scanFlag := uintptr(0)
	if scan {
		scanFlag = 1
	}
	if r, _, _ := procDismCheckImageHealth.Call(uintptr(session), scanFlag, 0, 0, 0, uintptr(unsafe.Pointer(&state))); failed(r) {
		return "", hrErr("DismCheckImageHealth", r)
	}
	switch state {
	case 0:
		return "Healthy", nil
	case 1:
		return "Repairable", nil
	}
	return "NonRepairable", nil
}

// ServiceStartMode zwraca tryb uruchamiania usługi jak Win32_Service.StartMode ("Auto", "Manual", "Disabled"...).
func ServiceStartMode(name string) (string, bool) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", false
	}
	defer windows.CloseServiceHandle(m)
	n16, _ := windows.UTF16PtrFromString(name)
	s, err := windows.OpenService(m, n16, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "", false
	}
	defer windows.CloseServiceHandle(s)
	qc := queryServiceConfig(s)
	if qc == nil {
		return "", false
	}
	return svcStartNames[qc.StartType], true
}

// queryServiceConfig: bufor o rozmiarze podanym przez system, zawsze na stercie - z buforem na stosie gorutyny
// QueryServiceConfigW kończy się naruszeniem ochrony pamięci.
func queryServiceConfig(s windows.Handle) *windows.QUERY_SERVICE_CONFIG {
	var need uint32
	_ = windows.QueryServiceConfig(s, nil, 0, &need)
	if need == 0 {
		return nil
	}
	buf := make([]byte, need)
	qc := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if windows.QueryServiceConfig(s, qc, need, &need) != nil {
		return nil
	}
	return qc
}
