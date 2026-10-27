package win

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Usługi przez menedżer usług (SCM) - jak Get-Service, a dla listy usług zamiast Win32_Service (WMI),
// które jest kilkanaście razy wolniejsze.

// stany jak ServiceControllerStatus (Get-Service) i jak Win32_Service.State
var (
	svcStatusNames = map[uint32]string{1: "Stopped", 2: "StartPending", 3: "StopPending", 4: "Running", 5: "ContinuePending", 6: "PausePending", 7: "Paused"}
	svcStateNames  = map[uint32]string{1: "Stopped", 2: "Start Pending", 3: "Stop Pending", 4: "Running", 5: "Continue Pending", 6: "Pause Pending", 7: "Paused"}
	svcStartNames  = map[uint32]string{0: "Boot", 1: "System", 2: "Auto", 3: "Manual", 4: "Disabled"}
)

// ServiceState zwraca stan usługi jak (Get-Service).Status, np. "Running"; ok = false, gdy usługi nie ma.
func ServiceState(name string) (string, bool) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", false
	}
	defer windows.CloseServiceHandle(m)
	n16, _ := windows.UTF16PtrFromString(name)
	s, err := windows.OpenService(m, n16, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "", false
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) != nil {
		return "", false
	}
	return svcStatusNames[st.CurrentState], true
}

// Service - usługa z konfiguracją (pola jak w Win32_Service).
type Service struct {
	Name        string
	DisplayName string
	State       string // "Running", "Stopped", "Start Pending"...
	StartMode   string // "Auto", "Manual", "Disabled", "Boot", "System"
	StartName   string // konto
	PathName    string
}

// Services zwraca usługi Win32 (bez sterowników) z ich konfiguracją.
func Services() ([]Service, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(m)
	var need, count, resume uint32
	buf := make([]byte, 256*1024)
	for {
		err = windows.EnumServicesStatusEx(m, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL,
			&buf[0], uint32(len(buf)), &need, &count, &resume, nil)
		if err == windows.ERROR_MORE_DATA && need > uint32(len(buf)) {
			buf = make([]byte, need+4096)
			resume = 0
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	entries := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), count)
	out := make([]Service, 0, count)
	for _, e := range entries {
		s := Service{
			Name:        windows.UTF16PtrToString(e.ServiceName),
			DisplayName: windows.UTF16PtrToString(e.DisplayName),
			State:       svcStateNames[e.ServiceStatusProcess.CurrentState],
		}
		if h, err := windows.OpenService(m, e.ServiceName, windows.SERVICE_QUERY_CONFIG); err == nil {
			if qc := queryServiceConfig(h); qc != nil {
				s.PathName = windows.UTF16PtrToString(qc.BinaryPathName)
				s.StartName = windows.UTF16PtrToString(qc.ServiceStartName)
				s.StartMode = svcStartNames[qc.StartType]
			}
			windows.CloseServiceHandle(h)
		}
		out = append(out, s)
	}
	return out, nil
}

// ---------------------------------------------------------------- zmiany (naprawy; wymagają administratora)

func openService(name string, access uint32) (windows.Handle, func(), error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, nil, err
	}
	n16, _ := windows.UTF16PtrFromString(name)
	s, err := windows.OpenService(m, n16, access)
	if err != nil {
		windows.CloseServiceHandle(m)
		return 0, nil, fmt.Errorf("usługa %s: %w", name, err)
	}
	return s, func() { windows.CloseServiceHandle(s); windows.CloseServiceHandle(m) }, nil
}

// SetServiceStartMode ustawia typ uruchamiania jak Set-Service -StartupType: "Automatic", "Manual" albo "Disabled".
func SetServiceStartMode(name, mode string) error {
	types := map[string]uint32{"Automatic": windows.SERVICE_AUTO_START, "Manual": windows.SERVICE_DEMAND_START, "Disabled": windows.SERVICE_DISABLED}
	t, ok := types[mode]
	if !ok {
		return fmt.Errorf("nieznany typ uruchamiania: %s", mode)
	}
	s, done, err := openService(name, windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return err
	}
	defer done()
	return windows.ChangeServiceConfig(s, windows.SERVICE_NO_CHANGE, t, windows.SERVICE_NO_CHANGE, nil, nil, nil, nil, nil, nil, nil)
}

func waitServiceState(s windows.Handle, want uint32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var st windows.SERVICE_STATUS
		if err := windows.QueryServiceStatus(s, &st); err != nil {
			return err
		}
		if st.CurrentState == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("usługa nie zmieniła stanu w ciągu %d s (stan: %s)", int(timeout.Seconds()), svcStatusNames[st.CurrentState])
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// StopService zatrzymuje usługę i czeka na jej zatrzymanie; force - najpierw działające usługi zależne
// (Stop-Service -Force). Zatrzymana usługa nie jest błędem.
func StopService(name string, force bool, timeout time.Duration) error {
	s, done, err := openService(name, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS|windows.SERVICE_ENUMERATE_DEPENDENTS)
	if err != nil {
		return err
	}
	defer done()
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) == nil && st.CurrentState == windows.SERVICE_STOPPED {
		return nil
	}
	if force {
		var need, count uint32
		_ = windows.EnumDependentServices(s, windows.SERVICE_ACTIVE, nil, 0, &need, &count)
		if need > 0 {
			buf := make([]byte, need)
			if windows.EnumDependentServices(s, windows.SERVICE_ACTIVE, (*windows.ENUM_SERVICE_STATUS)(unsafe.Pointer(&buf[0])), need, &need, &count) == nil {
				for _, d := range unsafe.Slice((*windows.ENUM_SERVICE_STATUS)(unsafe.Pointer(&buf[0])), count) {
					_ = StopService(windows.UTF16PtrToString(d.ServiceName), false, timeout)
				}
			}
		}
	}
	if err := windows.ControlService(s, windows.SERVICE_CONTROL_STOP, &st); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
		return fmt.Errorf("zatrzymanie usługi %s: %w", name, err)
	}
	return waitServiceState(s, windows.SERVICE_STOPPED, timeout)
}

// StartService uruchamia usługę i czeka, aż zacznie działać. Działająca usługa nie jest błędem.
func StartService(name string, timeout time.Duration) error {
	s, done, err := openService(name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer done()
	if err := windows.StartService(s, 0, nil); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return fmt.Errorf("uruchomienie usługi %s: %w", name, err)
	}
	return waitServiceState(s, windows.SERVICE_RUNNING, timeout)
}
