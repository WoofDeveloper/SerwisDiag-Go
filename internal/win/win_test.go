package win

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// Testy na żywym systemie (tylko odczyt). go test -short je pomija.

func TestWmiLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	start := time.Now()
	os, err := WmiQuery(`root\cimv2`, "SELECT Caption,BuildNumber,LastBootUpTime,TotalVisibleMemorySize FROM Win32_OperatingSystem", []string{"Caption", "BuildNumber", "LastBootUpTime", "TotalVisibleMemorySize"}, 20*time.Second)
	if err != nil || len(os) != 1 {
		t.Fatalf("Win32_OperatingSystem: %v (%d)", err, len(os))
	}
	boot, ok := os[0].Time("LastBootUpTime")
	t.Logf("%s build %s, start %v, RAM %d KB (%v)", os[0].Str("Caption"), os[0].Str("BuildNumber"), boot, os[0].Int("TotalVisibleMemorySize"), time.Since(start))
	if !ok || os[0].Int("TotalVisibleMemorySize") <= 0 {
		t.Fatal("brak daty startu lub pamięci")
	}
	all, err := WmiQuery(`root\cimv2`, "SELECT * FROM Win32_Processor", nil, 20*time.Second)
	if err != nil || len(all) == 0 {
		t.Fatalf("Win32_Processor: %v", err)
	}
	t.Logf("CPU: %s, rdzenie %d, właściwości: %d", all[0].Str("Name"), all[0].Int("NumberOfCores"), len(all[0]))
	pd, err := WmiQuery(`root\Microsoft\Windows\Storage`, "SELECT DeviceId,BusType,MediaType,HealthStatus,OperationalStatus FROM MSFT_PhysicalDisk", []string{"DeviceId", "BusType", "MediaType", "HealthStatus", "OperationalStatus"}, 30*time.Second)
	if err != nil {
		t.Fatalf("MSFT_PhysicalDisk: %v", err)
	}
	for _, d := range pd {
		t.Logf("dysk %s: bus %d media %d health %d op %v", d.Str("DeviceId"), d.Int("BusType"), d.Int("MediaType"), d.Int("HealthStatus"), d.Ints("OperationalStatus"))
	}
}

// TestWmiMethodsLive: metody WMI, których używają naprawy, istnieją i mają oczekiwane parametry (bez ich wywoływania).
func TestWmiMethodsLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	for _, m := range []struct{ ns, class, method, param string }{
		{`root\default`, "SystemRestore", "Enable", "drive"},
		{`root\Microsoft\Windows\Defender`, "MSFT_MpPreference", "Set", "disablerealtimemonitoring"},
		{`root\Microsoft\Windows\Defender`, "MSFT_MpSignature", "Update", ""},
		{`root\Microsoft\Windows\SMB`, "MSFT_SmbServerConfiguration", "SetConfiguration", "enablesmb1protocol"},
	} {
		names, err := WmiMethodParams(m.ns, m.class, m.method)
		if err != nil {
			t.Errorf("%s.%s: %v", m.class, m.method, err)
			continue
		}
		t.Logf("%s.%s: %v", m.class, m.method, names)
		if m.param != "" && !slices.Contains(names, m.param) {
			t.Errorf("%s.%s: brak parametru %s", m.class, m.method, m.param)
		}
	}
}

func TestEventsLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	start := time.Now()
	f := EventFilter{Levels: []int{1, 2}, Since: time.Now().AddDate(0, 0, -14)}
	ev, err := QueryEvents("System", f.XPath(), 4000, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("System, błędy z 14 dni: %d (%v)", len(ev), time.Since(start))
	if len(ev) > 0 {
		e := ev[0]
		t.Logf("najnowsze: %s/%d poziom %d %v %s props=%v", e.Provider, e.ID, e.Level, e.Time, e.Log, e.Props)
		t.Logf("opis: %.150s", e.Message())
	}
	kp, err := QueryEvents("System", EventFilter{Providers: []string{"Microsoft-Windows-Kernel-Power"}, IDs: []int{41}}.XPath(), 5, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range kp {
		t.Logf("Kernel-Power 41: %v BugcheckCode=%q", e.Time, e.Data["BugcheckCode"])
	}
	if _, err := QueryEvents("Nie-Ma-Takiego/Dziennika", "*", 1, false); err == nil {
		t.Error("oczekiwano błędu dla nieistniejącego dziennika")
	}
}

func TestRegistryLive(t *testing.T) {
	v, ok := RegValue(`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "CurrentBuild")
	if !ok || AnyString(v) == "" {
		t.Fatal("brak CurrentBuild")
	}
	ubr, ok := RegValue(`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "UBR")
	t.Logf("build %v.%v (%T)", v, ubr, ubr)
	if _, ok := RegValue(`HKLM\SOFTWARE\NieIstnieje`, "x"); ok {
		t.Fatal("nieistniejący klucz")
	}
}

func TestWindowsUpdateHistoryLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	WithCOM(func() {
		s, err := CreateObject("Microsoft.Update.Session")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Release()
		srch, err := s.Obj("CreateUpdateSearcher")
		if err != nil {
			t.Fatal(err)
		}
		defer srch.Release()
		n := srch.Int("GetTotalHistoryCount")
		t.Logf("historia WU: %d wpisów", n)
		if n > 0 {
			h, err := srch.Obj("QueryHistory", 0, int(min(n, 3)))
			if err != nil {
				t.Fatal(err)
			}
			defer h.Release()
			h.Items(func(e *Dispatch) {
				d, _ := e.Value("Date").(time.Time)
				t.Logf("%v | %s | wynik %d", d, e.Str("Title"), e.Int("ResultCode"))
			})
		}
	})
}

func TestServicesLive(t *testing.T) {
	st, ok := ServiceState("wuauserv")
	t.Logf("wuauserv: %q %v", st, ok)
	m, ok := ServiceStartMode("wuauserv")
	t.Logf("start mode: %q %v", m, ok)
	svcs, err := Services()
	t.Logf("usług: %d, błąd: %v", len(svcs), err)
	for i, s := range svcs {
		if i < 3 {
			t.Logf("%+v", s)
		}
	}
}

// TestMountISOLive: podłączenie i odłączenie obrazu ISO z plikiem sources\install.wim
// (SERWISDIAG_TEST_ISO=<plik .iso>, obraz nie może być wcześniej podłączony).
func TestMountISOLive(t *testing.T) {
	iso := os.Getenv("SERWISDIAG_TEST_ISO")
	if iso == "" {
		t.Skip("ustaw SERWISDIAG_TEST_ISO=<plik .iso>")
	}
	letter, mounted, err := MountISO(iso)
	if err != nil {
		t.Fatalf("MountISO: %v (administrator: %v)", err, IsAdmin())
	}
	t.Logf("litera %q, podłączony przez test: %v, administrator: %v", letter, mounted, IsAdmin())
	if mounted {
		defer func() {
			if err := DismountISO(iso); err != nil {
				t.Errorf("DismountISO: %v", err)
			}
			if _, err := os.Stat(letter + `:\sources\install.wim`); err == nil {
				t.Errorf("po odłączeniu napęd %s: nadal jest", letter)
			}
		}()
	} else {
		t.Error("obraz był już podłączony - test nie wie, czy go odłączyć")
	}
	if letter == "" {
		t.Fatal("brak litery dysku")
	}
	if _, err := os.Stat(letter + `:\sources\install.wim`); err != nil {
		t.Errorf(`brak sources\install.wim na %s: %v`, letter, err)
	}
	// ponownie: obraz już podłączony - ta sama litera, bez ponownego podłączania
	if l2, m2, err := MountISO(iso); err != nil || l2 != letter || m2 {
		t.Errorf("ponowne MountISO: %q %v %v", l2, m2, err)
	}
	if l := cdromLetter(`\\.\CDROM99`); l != "" {
		t.Errorf("cdromLetter nieistniejącego napędu: %q", l)
	}
}

// TestDeleteEmptyTaskFolderLive tworzy w Harmonogramie zadań folder testowy (z podfolderem i bez) i sprawdza, że
// usuwany jest tylko pusty. Zmienia system (na chwilę) - go test -short go pomija.
func TestDeleteEmptyTaskFolderLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	WithCOM(func() {
		name := fmt.Sprintf("SerwisDiagGoTest%d", os.Getpid())
		svc, err := CreateObject("Schedule.Service")
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Release()
		if _, err := svc.Get("Connect"); err != nil {
			t.Fatal(err)
		}
		root, _ := svc.Obj("GetFolder", `\`)
		defer root.Release()
		f, err := root.Obj("CreateFolder", name)
		if err != nil {
			t.Skip("zwykły użytkownik nie może tworzyć folderów Harmonogramu: " + err.Error())
		}
		sub, err := f.Obj("CreateFolder", "pod")
		if err != nil {
			f.Release()
			_, _ = root.Get("DeleteFolder", name, 0)
			t.Fatal(err)
		}
		sub.Release()
		if ok, err := DeleteEmptyTaskFolder(`\` + name); ok || err != nil {
			t.Errorf("folder z podfolderem usunięty: %v %v", ok, err)
		}
		_, _ = f.Get("DeleteFolder", "pod", 0)
		f.Release()
		if ok, err := DeleteEmptyTaskFolder(`\` + name); !ok || err != nil {
			t.Errorf("pusty folder: %v %v", ok, err)
			_, _ = root.Get("DeleteFolder", name, 0)
		}
		if ok, err := DeleteEmptyTaskFolder(`\` + name); ok || err != nil {
			t.Errorf("folder, którego już nie ma: %v %v", ok, err)
		}
	})
}
