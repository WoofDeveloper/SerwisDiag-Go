package ui

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestMonitorTaskXML(t *testing.T) {
	cmd := `C:\Program Files\Serwis & Diag\serwisdiag.exe`
	s := monitorTaskXML(cmd, "-Monitor")
	var task struct {
		Desc    string `xml:"RegistrationInfo>Description"`
		Group   string `xml:"Principals>Principal>GroupId"`
		Level   string `xml:"Principals>Principal>RunLevel"`
		Delay   string `xml:"Triggers>LogonTrigger>Delay"`
		Command string `xml:"Actions>Exec>Command"`
		Args    string `xml:"Actions>Exec>Arguments"`
	}
	d := xml.NewDecoder(strings.NewReader(s))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Command != cmd || task.Args != "-Monitor" || task.Group != "S-1-5-32-545" || task.Level != "HighestAvailable" || task.Delay != "PT30S" ||
		!strings.HasPrefix(task.Desc, "SerwisDiag Go - ") {
		t.Errorf("zadanie: %+v", task)
	}
	if !strings.Contains(s, "Serwis &amp; Diag") || strings.Contains(s, "\n") && !strings.Contains(s, "\r\n") {
		t.Error("znaki specjalne i końce wierszy")
	}
	// schtasks /Query /XML: definicja z końcami wierszy \r\r\n
	if c := xmlCommand(strings.ReplaceAll(s, "\r\n", "\r\r\n")); c != cmd || xmlCommand("<Task></Task>") != "" {
		t.Errorf("odczyt programu zadania: %q", c)
	}
}

func TestSameExe(t *testing.T) {
	dir := t.TempDir()
	gui := filepath.Join(dir, "serwisdiag.exe")
	con := filepath.Join(dir, "serwisdiag-konsola.exe")
	for _, f := range []string{gui, con} {
		_ = os.WriteFile(f, nil, 0o644)
	}
	for cmd, want := range map[string]bool{
		`"` + gui + `" -Monitor`:                  true,
		`"` + strings.ToUpper(gui) + `" -Monitor`: true,
		gui + " -Monitor":                         true,
		gui:                                       true,
		`"C:\serwis-diag-go\serwisdiag.exe" -Monitor`: false,
		`"` + filepath.Join(dir, "inny.exe") + `"`:    false,
		"": false,
	} {
		if got := sameExe(cmd, con); got != want { // wersja konsolowa wskazuje plik okienkowy z tego samego folderu
			t.Errorf("sameExe(%q) = %v", cmd, got)
		}
	}
	if copyStopName(gui) != copyStopName(strings.ToUpper(gui)) || copyStopName(gui) != copyStopName(con) || copyStopName(gui) == copyStopName(filepath.Join(t.TempDir(), "serwisdiag.exe")) {
		t.Error("zdarzenie zatrzymania kopii")
	}
	for _, s := range []string{"run", "abc\r\n\r\nostatni wiersz\r\n  "} {
		_ = s
	}
	if lastLine("BŁĄD: pierwszy\r\r\nBŁĄD: drugi\r\n\r\n") != "BŁĄD: drugi" {
		t.Error("ostatni wiersz")
	}
}

// TestMonitorLock: drugi monitor czeka na blokadę poprzedniego (np. sprzed aktualizacji), a nie kończy się od razu -
// CreateMutex z x/sys zwraca wtedy ERROR_ALREADY_EXISTS (kod 3 przy uruchomieniu z zadania Harmonogramu).
func TestMonitorLock(t *testing.T) {
	runtime.LockOSThread() // blokada należy do wątku systemowego
	defer runtime.UnlockOSThread()
	name := fmt.Sprintf(`Local\SerwisDiagGo.Test.Lock.%d`, os.Getpid())
	held, release, done := make(chan bool), make(chan struct{}), make(chan struct{})
	go func() { // pierwszy monitor
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		h, ok, err := monitorLock(name, time.Second)
		held <- ok && err == nil
		if h != 0 {
			<-release
			_ = windows.ReleaseMutex(h)
			windows.CloseHandle(h)
		}
	}()
	if !<-held {
		t.Fatal("pierwsza blokada")
	}
	start := time.Now()
	if h, ok, err := monitorLock(name, 300*time.Millisecond); ok || err != nil || h != 0 || time.Since(start) < 250*time.Millisecond {
		t.Errorf("zajęta blokada: ok=%v err=%v po %v", ok, err, time.Since(start))
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()
	h, ok, err := monitorLock(name, 5*time.Second)
	if !ok || err != nil {
		t.Fatalf("blokada po zwolnieniu przez poprzedni monitor: %v", err)
	}
	_ = windows.ReleaseMutex(h)
	windows.CloseHandle(h)
	<-done
}

// TestStopMonitorCopy: -StopMonitor i -DisableMonitor zatrzymują tylko monitor tej kopii programu (nazwy testowe -
// prawdziwy monitor działający na tym komputerze nie jest dotykany).
func TestStopMonitorCopy(t *testing.T) {
	t.Setenv("SERWISDIAG_MONITOR_TEST", "test")
	a := filepath.Join(t.TempDir(), "serwisdiag.exe")
	b := filepath.Join(t.TempDir(), "serwisdiag.exe")
	if !strings.Contains(copyStopName(a), ".Test.") {
		t.Fatalf("nazwa testowa: %s", copyStopName(a))
	}
	h, err := windows.CreateEvent(nil, 0, 0, u16(copyStopName(a)))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if stopMonitorCopy(b, false) {
		t.Error("monitor innej kopii nie działa - nie ma czego zatrzymać")
	}
	if ev, _ := windows.WaitForSingleObject(h, 0); ev == windows.WAIT_OBJECT_0 {
		t.Fatal("zatrzymanie innej kopii dotarło do tej")
	}
	if !stopMonitorCopy(a, true) {
		t.Error("monitor tej kopii")
	}
	if ev, _ := windows.WaitForSingleObject(h, 0); ev != windows.WAIT_OBJECT_0 {
		t.Error("zdarzenie zatrzymania nie zostało ustawione")
	}
}
