package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/win"
)

// Monitor przy zegarze (lib\Monitor.ps1): ikona w obszarze powiadomień, co jakiś czas lekki zestaw testów
// i powiadomienie o nowych problemach - nowych, pogorszonych (ostrzeżenie -> krytyczny) albo z historii zdarzeń
// z nowym zdarzeniem. Stan: %LOCALAPPDATA%\SerwisDiag\monitor-go.json. Wersja Go ma własne nazwy blokady,
// zdarzenia zatrzymania, pliku stanu i wpisu autostartu, więc działa obok monitora wersji PowerShell.

var (
	monitorChecks    = []string{"st.disks", "st.volumes", "ev.crash", "ev.hardware", "dr.problem", "se.av", "se.firewall", "up.history"}
	monitorIntervals = []int{60, 240, 1440} // minuty: co godzinę, co 4 godziny, raz na dobę
)

const (
	monitorFirstDelay = 120 * time.Second // pierwsze sprawdzenie chwilę po logowaniu, żeby nie spowalniać startu
	monitorRunValue   = "SerwisDiag Go Monitor"
	runKey            = `Software\Microsoft\Windows\CurrentVersion\Run`
	monClass          = "SerwisDiagGoMonitor"
	monProduct        = "SerwisDiag Go"
	wmTray            = wmApp + 20
	wmCheckDone       = wmApp + 21
	idTray            = 1
	timerMonitor      = 1
)

func monitorNames() (mutex, stop string) {
	mutex, stop = `Local\SerwisDiagGo.Monitor`, `Local\SerwisDiagGo.Monitor.Stop`
	if os.Getenv("SERWISDIAG_MONITOR_TEST") != "" {
		// test automatyczny działa obok prawdziwego monitora (nie czeka na jego blokadę i go nie zatrzymuje)
		mutex += ".Test"
		stop += ".Test"
	}
	return
}

// ---------------------------------------------------------------- stan i porównanie z poprzednim sprawdzeniem

type monProblem struct {
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Value     string  `json:"value"`
	Advice    string  `json:"advice"`
	LastEvent *string `json:"lastEvent"`
}

type monStateFile struct {
	LastCheck *string      `json:"lastCheck"`
	Interval  int          `json:"interval"`
	Problems  []monProblem `json:"problems"`
}

type monitorState struct {
	LastCheck time.Time
	Interval  int
	Problems  []monProblem
}

const sFormat = "2006-01-02T15:04:05" // ToString('s'), czas lokalny

func monitorStatePath() string {
	if p := os.Getenv("SERWISDIAG_MONITOR_STATE"); p != "" {
		return p // testy: osobny plik stanu
	}
	return filepath.Join(userDataDir(), "monitor-go.json")
}

// readMonitorState: stan albo nil przed pierwszym sprawdzeniem (Read-SdMonitorState).
func readMonitorState(path string) *monitorState {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f monStateFile
	if json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &f) != nil {
		return nil
	}
	st := &monitorState{Interval: 60, Problems: f.Problems}
	if f.Interval > 0 {
		st.Interval = f.Interval
	}
	if f.LastCheck != nil {
		st.LastCheck, _ = time.ParseInLocation(sFormat, *f.LastCheck, time.Local)
	}
	return st
}

// saveMonitorState: zapis stanu; interval 0 - bez zmiany częstotliwości, when zero - bez zmiany daty sprawdzenia.
func saveMonitorState(path string, problems []monProblem, when time.Time, interval int) error {
	old := readMonitorState(path)
	if interval <= 0 {
		interval = 60
		if old != nil {
			interval = old.Interval
		}
	}
	if when.IsZero() && old != nil {
		when = old.LastCheck
	}
	f := monStateFile{Interval: interval, Problems: problems}
	if f.Problems == nil {
		f.Problems = []monProblem{}
	}
	if !when.IsZero() {
		s := when.Format(sFormat)
		f.LastCheck = &s
	}
	b, err := json.MarshalIndent(f, "", "    ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, b, 0o644)
}

// setMonitorInterval: zmiana częstotliwości (menu ikony, ustawienia programu) bez utraty stanu.
func setMonitorInterval(path string, minutes int) error {
	var pr []monProblem
	if st := readMonitorState(path); st != nil {
		pr = st.Problems
	}
	return saveMonitorState(path, pr, time.Time{}, minutes)
}

func intervalText(m int) string {
	switch {
	case m >= 1440:
		return "raz na dobę"
	case m == 60:
		return "co godzinę"
	case m >= 60:
		return "co " + diag.FmtCount(m/60, "godzinę", "godziny", "godzin")
	}
	return "co " + diag.FmtCount(m, "minutę", "minuty", "minut")
}

func monitorKey(f diag.Finding) string { return strings.ToLower(f.CheckID + "|" + f.Title) }

func toProblem(f diag.Finding) monProblem {
	p := monProblem{Key: monitorKey(f), Title: f.Title, Status: string(f.Status), Value: f.Value, Advice: f.Advice}
	if !f.LastEvent.IsZero() {
		s := f.LastEvent.Format(sFormat)
		p.LastEvent = &s
	}
	return p
}

// monitorChanges odpowiada Get-SdMonitorChanges: nowe problemy (nieobecne wcześniej, pogorszone albo z nowym
// zdarzeniem po poprzednim sprawdzeniu), bieżące problemy i tytuły ustąpionych.
func monitorChanges(prev []monProblem, findings []diag.Finding, lastCheck time.Time) (newOnes, problems []diag.Finding, resolved []string) {
	byKey := map[string]monProblem{}
	for _, p := range prev {
		byKey[p.Key] = p
	}
	now := map[string]bool{}
	for _, f := range findings {
		if f.Status != diag.Warn && f.Status != diag.Crit {
			continue
		}
		problems = append(problems, f)
		k := monitorKey(f)
		now[k] = true
		p, ok := byKey[k]
		switch {
		case !ok:
			newOnes = append(newOnes, f)
		case p.Status == "warn" && f.Status == diag.Crit:
			newOnes = append(newOnes, f)
		case !f.LastEvent.IsZero() && !lastCheck.IsZero() && f.LastEvent.After(lastCheck):
			newOnes = append(newOnes, f)
		}
	}
	for _, p := range prev {
		if !now[p.Key] {
			resolved = append(resolved, p.Title)
		}
	}
	return
}

// monitorLevel: kolor ikony - crit przy problemie krytycznym, warn przy samych ostrzeżeniach, ok bez problemów.
func monitorLevel(pr []monProblem) string {
	lvl := "ok"
	for _, p := range pr {
		if p.Status == "crit" {
			return "crit"
		}
		lvl = "warn"
	}
	return lvl
}

// ---------------------------------------------------------------- autostart i zatrzymywanie

// guiExe: plik okna programu (serwisdiag.exe), także gdy wywołano wersję konsolową.
func guiExe(exe string) string {
	if strings.Contains(strings.ToLower(filepath.Base(exe)), "konsola") {
		if g := filepath.Join(filepath.Dir(exe), "serwisdiag.exe"); fileExists(g) {
			return g
		}
	}
	return exe
}

// Autostart przy logowaniu (Register-SdMonitorAutostart): w instalacji dla wszystkich użytkowników (z uprawnieniami
// administratora) zadanie Harmonogramu, w pozostałych przypadkach wpis Run bieżącego użytkownika. Na jednym komputerze
// może być kilka kopii programu (np. zainstalowana i przenośna), więc instalator i odinstalowanie (-DisableMonitor,
// -StopMonitor) usuwają tylko wpisy wskazujące tę kopię i zatrzymują tylko jej monitor.

const monitorTaskName = `SerwisDiag\Monitor Go`

func schtasksExe() string { return filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe") }

// monitorTaskXML odpowiada New-SdMonitorTaskXml: start przy logowaniu każdego użytkownika, w jego sesji, z najwyższymi
// dostępnymi uprawnieniami (administrator: pełne testy bez okna UAC; zwykły użytkownik: bez uprawnień).
func monitorTaskXML(command, args string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace
	a := ""
	if args != "" {
		a = "<Arguments>" + esc(args) + "</Arguments>"
	}
	return strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`,
		`  <RegistrationInfo><Author>SerwisDiag</Author><Description>SerwisDiag Go - monitorowanie systemu w tle (ikona przy zegarze).</Description></RegistrationInfo>`,
		`  <Triggers><LogonTrigger><Enabled>true</Enabled><Delay>PT30S</Delay></LogonTrigger></Triggers>`,
		`  <Principals><Principal id="Author"><GroupId>S-1-5-32-545</GroupId><RunLevel>HighestAvailable</RunLevel></Principal></Principals>`,
		`  <Settings>`,
		`    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`    <AllowHardTerminate>true</AllowHardTerminate>`,
		`    <StartWhenAvailable>false</StartWhenAvailable>`,
		`    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>`,
		`    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>`,
		`    <AllowStartOnDemand>true</AllowStartOnDemand>`,
		`    <Enabled>true</Enabled>`,
		`    <Hidden>false</Hidden>`,
		`    <RunOnlyIfIdle>false</RunOnlyIfIdle>`,
		`    <WakeToRun>false</WakeToRun>`,
		`    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`    <Priority>7</Priority>`,
		`  </Settings>`,
		`  <Actions Context="Author"><Exec><Command>` + esc(command) + `</Command>` + a + `</Exec></Actions>`,
		`</Task>`,
	}, "\r\n")
}

// sameExe: czy polecenie (wpis Run albo zadania) uruchamia tę kopię programu.
func sameExe(command, exe string) bool {
	c := strings.TrimSpace(command)
	if strings.HasPrefix(c, `"`) {
		if i := strings.Index(c[1:], `"`); i >= 0 {
			c = c[1 : i+1]
		}
	} else if i := strings.Index(strings.ToLower(c), ".exe "); i >= 0 {
		c = c[:i+4]
	}
	return c != "" && strings.EqualFold(filepath.Clean(c), filepath.Clean(guiExe(exe)))
}

// monitorTask: czy jest zadanie Harmonogramu monitora i jaki program uruchamia ("" - nie udało się odczytać).
func monitorTask() (exists bool, command string) {
	r := win.Run(30*time.Second, schtasksExe(), "/Query", "/TN", monitorTaskName, "/XML")
	if r.ExitCode != 0 {
		return false, ""
	}
	return true, xmlCommand(win.OEMString(r.Stdout))
}

// xmlCommand: program akcji z definicji zadania (schtasks /Query /XML).
func xmlCommand(xml string) string {
	if i := strings.Index(xml, "<Command>"); i >= 0 {
		if j := strings.Index(xml[i:], "</Command>"); j > 0 {
			return strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(xml[i+9 : i+j])
		}
	}
	return ""
}

func runValue() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(monitorRunValue)
	return v, err == nil
}

func deleteRunValue() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	if err := k.DeleteValue(monitorRunValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// monitorAutostart odpowiada Get-SdMonitorAutostart: "task", "run" albo "".
func monitorAutostart() string {
	if ok, _ := monitorTask(); ok {
		return "task"
	}
	if _, ok := runValue(); ok {
		return "run"
	}
	return ""
}

// registerAutostart odpowiada Register-SdMonitorAutostart; wynik "task" albo "run".
func registerAutostart(exe, root string) (string, error) {
	cmd := guiExe(exe)
	if diag.InstallMode(root) == "allusers" && win.IsAdmin() {
		xml := filepath.Join(os.TempDir(), fmt.Sprintf("SerwisDiag-monitor-%d-%d.xml", os.Getpid(), time.Now().UnixNano()))
		u := windows.StringToUTF16(monitorTaskXML(cmd, "-Monitor"))
		b := make([]byte, 0, 2+2*len(u))
		b = append(b, 0xFF, 0xFE) // UTF-16 LE z BOM (jak [Text.Encoding]::Unicode)
		for _, c := range u[:len(u)-1] {
			b = append(b, byte(c), byte(c>>8))
		}
		if err := os.WriteFile(xml, b, 0o600); err != nil {
			return "", err
		}
		defer os.Remove(xml)
		r := win.Run(60*time.Second, schtasksExe(), "/Create", "/TN", monitorTaskName, "/XML", xml, "/F")
		if r.ExitCode != 0 {
			return "", fmt.Errorf("Nie utworzono zadania Harmonogramu: %s", lastLine(win.OEMString(r.Stdout)+win.OEMString(r.Stderr)))
		}
		// wpis Run tej kopii (np. z instalacji „dla mnie”) uruchamiałby drugi monitor
		if v, ok := runValue(); ok && sameExe(v, exe) {
			_ = deleteRunValue()
		}
		return "task", nil
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	return "run", k.SetStringValue(monitorRunValue, `"`+cmd+`" -Monitor`)
}

// unregisterAutostart odpowiada Unregister-SdMonitorAutostart; onlyThisCopy - tylko wpisy uruchamiające tę kopię
// (zadanie, którego programu nie udało się odczytać, zostaje). Wynik: usunięte wpisy.
func unregisterAutostart(exe string, onlyThisCopy bool) []string {
	var out []string
	if ok, cmd := monitorTask(); ok && (!onlyThisCopy || sameExe(cmd, exe)) {
		if r := win.Run(30*time.Second, schtasksExe(), "/Delete", "/TN", monitorTaskName, "/F"); r.ExitCode == 0 {
			out = append(out, "task")
			// pusty folder SerwisDiag zostałby po odinstalowaniu (z zadaniem wersji PowerShell w środku zostaje)
			win.WithCOM(func() { _, _ = win.DeleteEmptyTaskFolder(monitorTaskName[:strings.Index(monitorTaskName, `\`)]) })
		} else {
			out = append(out, "task: brak uprawnień")
		}
	}
	if v, ok := runValue(); ok && (!onlyThisCopy || sameExe(v, exe)) {
		if deleteRunValue() == nil {
			out = append(out, "run")
		}
	}
	return out
}

func lastLine(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' })
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func startMonitorProcess(exe string) error {
	return exec.Command(guiExe(exe), "-Monitor").Start()
}

// copyStopName: zdarzenie zatrzymania monitora tej kopii programu (każda kopia ma własne).
func copyStopName(exe string) string {
	_, stop := monitorNames()
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(filepath.Clean(guiExe(exe)))))
	return fmt.Sprintf("%s.%08x", stop, h.Sum32())
}

// stopMonitor prosi monitor działający w tej sesji o zakończenie; wait - czeka do 15 s na zwolnienie blokady.
func stopMonitor(wait bool) bool {
	_, stopName := monitorNames()
	return signalMonitor(stopName, wait)
}

// stopMonitorCopy zatrzymuje monitor tylko wtedy, gdy działa z tej kopii programu.
func stopMonitorCopy(exe string, wait bool) bool { return signalMonitor(copyStopName(exe), wait) }

func signalMonitor(event string, wait bool) bool {
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, u16(event))
	if err != nil {
		return false
	}
	_ = windows.SetEvent(h)
	windows.CloseHandle(h)
	if wait {
		runtime.LockOSThread() // przejęta blokada należy do wątku systemowego - zwalnia ją ten sam wątek
		defer runtime.UnlockOSThread()
		mutexName, _ := monitorNames()
		if m, err := windows.OpenMutex(windows.SYNCHRONIZE, false, u16(mutexName)); err == nil {
			if ev, _ := windows.WaitForSingleObject(m, 15000); ev == windows.WAIT_OBJECT_0 || ev == windows.WAIT_ABANDONED {
				_ = windows.ReleaseMutex(m)
			}
			windows.CloseHandle(m)
		}
	}
	return true
}

// EnableMonitor / DisableMonitor / StopMonitor - parametry -EnableMonitor, -DisableMonitor i -StopMonitor.
// Wynik EnableMonitor: "task" albo "run".
func EnableMonitor(exe, root string, start bool) (string, error) {
	how, err := registerAutostart(exe, root)
	if err != nil {
		return "", err
	}
	if !start {
		// instalator: poprzedni monitor tej kopii kończy pracę, nowy uruchomi instalator
		stopMonitorCopy(exe, true)
		return how, nil
	}
	stopMonitor(true)
	return how, startMonitorProcess(exe)
}

// DisableMonitor: autostart i działający monitor tej kopii (wynik: usunięte wpisy, czy monitor działał).
func DisableMonitor(exe string) ([]string, bool) {
	was := stopMonitorCopy(exe, true)
	return unregisterAutostart(exe, true), was
}

// StopMonitor - instalator przed podmianą plików: kończy monitor tej kopii, autostart zostaje.
func StopMonitor(exe string) bool { return stopMonitorCopy(exe, true) }

func monitorLog(msg string) { appendLog("monitor-go.log", msg) }

// ---------------------------------------------------------------- monitor

type monitor struct {
	h           uintptr
	exe, root   string
	statePath   string
	problems    []monProblem
	lastCheck   time.Time
	nextCheck   time.Time
	interval    int
	isAdmin     bool
	busy        bool
	checks      int
	icons       map[string]uintptr
	level       string
	tip         string
	lastBalloon string
	balloonKind string
	testPrefix  string
	exeTime     time.Time
	lastExeTest time.Time
	taskbarMsg  uint32
	added       bool
	win         *dialog
	winDoc      *doc
	mu          sync.Mutex
	done        []diag.Finding
	doneOK      bool
	doneAdmin   bool
	doneRel     *diag.Release
	updateRel   *diag.Release // nowa wersja z GitHuba (pozycja menu i powiadomienie raz na wersję)
}

// monitorLock: blokada „jeden monitor na sesję” - czeka do wait, aż poprzedni monitor ją zwolni (Mutex.WaitOne).
// CreateMutex z x/sys zwraca ERROR_ALREADY_EXISTS z poprawnym uchwytem, gdy blokada już istnieje (działa inny
// monitor) - to nie jest błąd. Wynik: uchwyt przejętej blokady albo ok=false (inny monitor jej nie zwolnił).
// Wywołujący musi mieć przypięty wątek systemowy (RunMonitor - initUI): blokadę zwalnia wątek, który ją przejął.
func monitorLock(name string, wait time.Duration) (windows.Handle, bool, error) {
	mx, err := windows.CreateMutex(nil, false, u16(name))
	if mx == 0 {
		return 0, false, err
	}
	if ev, _ := windows.WaitForSingleObject(mx, uint32(wait.Milliseconds())); ev != windows.WAIT_OBJECT_0 && ev != windows.WAIT_ABANDONED {
		windows.CloseHandle(mx)
		return 0, false, nil
	}
	return mx, true, nil
}

// RunMonitor: monitorowanie w tle do zakończenia (menu, wyłączenie, zatrzymanie przez program). Jeden monitor
// na sesję użytkownika (Start-SdMonitor).
func RunMonitor(root, exe string) int {
	initUI()
	mutexName, stopName := monitorNames()
	// po aktualizacji poprzedni monitor kończy pracę chwilę po uruchomieniu nowego
	mx, ok, err := monitorLock(mutexName, 20*time.Second)
	if err != nil {
		monitorLog("Blokada monitora: " + err.Error())
		return 3
	}
	if !ok {
		return 0 // działa inny monitor
	}
	defer windows.CloseHandle(mx)
	defer windows.ReleaseMutex(mx)
	// zatrzymanie: każdego monitora w sesji albo (instalator, odinstalowanie) tylko monitora tej kopii programu -
	// zdarzenie kopii powstaje dopiero po przejęciu blokady, więc istnieje tylko wtedy, gdy działa ta kopia
	var stops []windows.Handle
	for _, n := range []string{stopName, copyStopName(exe)} {
		// zdarzenie już otwarte przez inny proces: x/sys zwraca ERROR_ALREADY_EXISTS z poprawnym uchwytem
		if h, _ := windows.CreateEvent(nil, 0, 0, u16(n)); h != 0 {
			defer windows.CloseHandle(h)
			stops = append(stops, h)
		}
	}

	m := &monitor{exe: exe, root: root, statePath: monitorStatePath(), interval: 60, isAdmin: win.IsAdmin(),
		testPrefix: os.Getenv("SERWISDIAG_MONITOR_TEST"), icons: map[string]uintptr{}}
	if st, err := os.Stat(exe); err == nil {
		m.exeTime = st.ModTime()
	}
	m.lastExeTest = time.Now()
	st := readMonitorState(m.statePath)
	if st != nil {
		m.interval = st.Interval
		// poprzednie problemy od razu w oknie i w kolorze ikony - do pierwszego sprawdzenia w tej sesji
		if !st.LastCheck.IsZero() {
			m.problems, m.lastCheck = st.Problems, st.LastCheck
		}
	}
	m.nextCheck = time.Now().Add(monitorFirstDelay)
	if m.testPrefix != "" {
		m.nextCheck = time.Now().Add(2 * time.Second)
	}
	size := metricForDpi(smCxSmIcon, dpiOf(0))
	for _, s := range []string{"ok", "warn", "crit"} {
		m.icons[s] = drawAppIcon(size, s).hicon()
	}
	registerClass(monClass, idcArrow, 0)
	m.h = createWindow(m, monClass, "SerwisDiag Go - monitor", wsPopup, wsExToolWindow, 0, 0, 0, 0, 0, 0)
	m.taskbarMsg = uint32(call(pRegisterWindowMessageW, u16p("TaskbarCreated")))
	m.level = monitorLevel(m.problems)
	m.trayAdd()
	call(pSetTimer, m.h, timerMonitor, 2000, 0)
	if len(stops) > 0 {
		go func() {
			if ev, err := windows.WaitForMultipleObjects(stops, false, windows.INFINITE); err == nil && ev < windows.WAIT_OBJECT_0+uint32(len(stops)) {
				postMsg(m.h, wmClose, 0, 0)
			}
		}()
	}
	admin := ""
	if m.isAdmin {
		admin = " (administrator)"
	}
	monitorLog("Start monitora " + diag.Version + " (Go)" + admin)
	if m.testPrefix == "" {
		cleanupUpdates() // monitor startuje przy logowaniu - sprząta, nawet gdy nikt nie otwiera okna programu
	}
	pump(nil)
	m.trayDelete()
	for _, h := range m.icons {
		destroyIcon(h)
	}
	return 0
}

func (m *monitor) nid() notifyIconData {
	return notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Wnd: m.h, ID: idTray}
}

func (m *monitor) trayAdd() {
	m.tip = m.tipText()
	n := m.nid()
	n.Flags = nifMessage | nifIcon | nifTip
	n.CallbackMessage = wmTray
	n.Icon = m.icons[m.level]
	copy(n.Tip[:127], windows.StringToUTF16(m.tip))
	m.added = call(pShellNotifyIconW, nimAdd, uintptr(unsafe.Pointer(&n))) != 0
}

func (m *monitor) trayUpdate() {
	m.tip = m.tipText()
	n := m.nid()
	n.Flags = nifIcon | nifTip
	n.Icon = m.icons[m.level]
	copy(n.Tip[:127], windows.StringToUTF16(m.tip))
	call(pShellNotifyIconW, nimModify, uintptr(unsafe.Pointer(&n)))
}

func (m *monitor) trayDelete() {
	if m.added {
		n := m.nid()
		call(pShellNotifyIconW, nimDelete, uintptr(unsafe.Pointer(&n)))
		m.added = false
	}
}

// tipText: podpowiedź ikony (Set-SdMonitorTip), najwyżej 63 znaki.
func (m *monitor) tipText() string {
	cnt := len(m.problems)
	var t string
	switch {
	case m.busy:
		t = monProduct + ": sprawdzanie..."
	case m.lastCheck.IsZero():
		t = monProduct + ": monitorowanie"
	case cnt == 0:
		t = monProduct + ": brak problemów"
	default:
		t = monProduct + ": " + diag.FmtCount(cnt, "problem", "problemy", "problemów")
	}
	if !m.lastCheck.IsZero() && !m.busy {
		t += " (" + m.lastCheck.Format("15:04") + ")"
	}
	return diag.LimitText(t, 63)
}

// balloon: powiadomienie (w Windows 10/11 jako powiadomienie systemowe); kind - co otworzyć po kliknięciu.
func (m *monitor) balloon(title, text, kind string, icon uint32) {
	m.balloonKind = kind
	m.lastBalloon = title + ": " + text
	if m.testPrefix != "" || os.Getenv("SERWISDIAG_MONITOR_QUIET") != "" {
		return // autotest i pomiary nie pokazują powiadomień
	}
	n := m.nid()
	n.Flags = nifInfo
	n.InfoFlags = icon
	n.TimeoutOrVersion = 15000
	copy(n.InfoTitle[:63], windows.StringToUTF16(diag.LimitText(title, 63)))
	copy(n.Info[:255], windows.StringToUTF16(diag.LimitText(text, 250)))
	call(pShellNotifyIconW, nimModify, uintptr(unsafe.Pointer(&n)))
}

// startCheck: lekki zestaw testów w gorutynie (Start-SdMonitorCheck).
func (m *monitor) startCheck() {
	if m.busy {
		return
	}
	if st := readMonitorState(m.statePath); st != nil {
		m.interval = st.Interval
	}
	m.busy = true
	m.trayUpdate() // ikona bez zmian (bez mrugania co godzinę), w podpowiedzi „sprawdzanie...”
	m.updateWindow()
	h, root := m.h, m.root
	// nowa wersja: wersja zainstalowana, raz na dobę; autotest nie łączy się z internetem
	checkUpd := m.testPrefix == "" && diag.UpdateCheckDue(root, diag.LoadSettings(root), "", time.Now())
	online := m.testPrefix == ""
	go func() {
		var fs []diag.Finding
		var rel *diag.Release
		ok := false
		func() {
			defer func() {
				if p := recover(); p != nil {
					monitorLog(fmt.Sprintf("Sprawdzanie: %v", p))
				}
			}()
			s := diag.NewScan(diag.Options{Root: root, Level: "Quick", Only: monitorChecks, Parallel: true})
			s.Run()
			fs, ok = s.Result().Findings, true
			if checkUpd {
				r := diag.LatestRelease(diag.ProjectURL, 8*time.Second, "")
				_ = diag.SaveUpdateCheckState(r, "")
				rel = &r
			}
			// rejestracja zapisana bez internetu (np. na końcu instalacji)
			if online && diag.RegistrationDue("", time.Now()) {
				diag.SendRegistration("", nil)
			}
		}()
		m.mu.Lock()
		m.done, m.doneOK, m.doneAdmin, m.doneRel = fs, ok, win.IsAdmin(), rel
		m.mu.Unlock()
		postMsg(h, wmCheckDone, 0, 0)
	}()
}

// completeCheck: porównanie z poprzednim stanem, zapis, ikona, powiadomienie (Complete-SdMonitorCheck).
func (m *monitor) completeCheck() {
	m.mu.Lock()
	fs, ok, admin, rel := m.done, m.doneOK, m.doneAdmin, m.doneRel
	m.doneRel = nil
	m.mu.Unlock()
	m.busy = false
	now := time.Now()
	st := readMonitorState(m.statePath)
	if st != nil {
		m.interval = st.Interval
	}
	m.nextCheck = now.Add(time.Duration(m.interval) * time.Minute)
	if !ok {
		m.level = monitorLevel(m.problems)
		m.trayUpdate()
		m.updateWindow()
		return
	}
	baseline := st == nil || st.LastCheck.IsZero()
	var prev []monProblem
	var last time.Time
	if st != nil {
		prev, last = st.Problems, st.LastCheck
	}
	newOnes, problems, _ := monitorChanges(prev, fs, last)
	m.problems = make([]monProblem, 0, len(problems))
	for _, f := range problems {
		m.problems = append(m.problems, toProblem(f))
	}
	if err := saveMonitorState(m.statePath, m.problems, now, m.interval); err != nil {
		monitorLog("Zapis stanu: " + err.Error())
	}
	m.lastCheck, m.isAdmin = now, admin
	m.checks++
	m.level = monitorLevel(m.problems)
	m.trayUpdate()
	switch {
	case baseline:
		if len(m.problems) > 0 {
			var titles []string
			for _, p := range m.problems[:min(3, len(m.problems))] {
				titles = append(titles, p.Title)
			}
			m.balloon(monProduct+" monitoruje system", fmt.Sprintf("Znane problemy: %d (%s). Kliknij, aby zobaczyć szczegóły.", len(m.problems), strings.Join(titles, ", ")), "problems", niifInfo)
		}
	case len(newOnes) > 0:
		title := monProduct + ": nowy problem"
		text := newOnes[0].Title + ": " + newOnes[0].Value
		if len(newOnes) > 1 {
			title = fmt.Sprintf("%s: nowe problemy (%d)", monProduct, len(newOnes))
			var t []string
			for _, f := range newOnes {
				t = append(t, f.Title)
			}
			text = strings.Join(t, ", ")
		}
		icon := uint32(niifWarning)
		if slices.ContainsFunc(newOnes, func(f diag.Finding) bool { return f.Status == diag.Crit }) {
			icon = niifError
		}
		m.balloon(title, text+" Kliknij, aby zobaczyć szczegóły.", "problems", icon)
	}
	m.updateWindow()
	if rel != nil && rel.Ok && rel.Newer {
		m.updateRel = rel
		if diag.UpdateNotified("") != rel.Latest {
			m.balloon(monProduct+": dostępna nowa wersja", fmt.Sprintf("Wersja %s (masz %s). Kliknij, aby otworzyć program i zaktualizować.", rel.Latest, diag.Version), "update", niifInfo)
			_ = diag.SaveUpdateNotified(*rel, "")
		}
	}
	releaseMemory("monitor po sprawdzeniu")
	if m.testPrefix != "" {
		m.completeSelfTest()
	}
}

// completeSelfTest: okno wyników, zrzut okna i ikony, podsumowanie w pliku, koniec (SERWISDIAG_MONITOR_TEST=<prefiks>).
func (m *monitor) completeSelfTest() {
	m.showWindow()
	call(pUpdateWindow, m.win.h)
	if err := saveWindowPNG(m.win.h, m.testPrefix+"-okno.png"); err != nil {
		monitorLog("Zrzut okna: " + err.Error())
	}
	if f, err := os.Create(m.testPrefix + "-ikona.png"); err == nil {
		_ = png.Encode(f, drawAppIcon(metricForDpi(smCxSmIcon, dpiOf(0)), m.level).nrgba())
		f.Close()
	}
	adm := "False"
	if m.isAdmin {
		adm = "True"
	}
	lines := []string{fmt.Sprintf("problemy: %d", len(m.problems)), "podpowiedz: " + m.tip, "powiadomienie: " + m.lastBalloon, "administrator: " + adm}
	for _, p := range m.problems {
		lines = append(lines, "  "+p.Status+" | "+p.Title+" | "+p.Value)
	}
	_ = os.WriteFile(m.testPrefix+"-wynik.txt", []byte("\ufeff"+strings.Join(lines, "\r\n")), 0o644)
	postMsg(m.h, wmClose, 0, 0)
}

func (m *monitor) openProgram() {
	if err := exec.Command(guiExe(m.exe), "-Elevate").Start(); err != nil {
		monitorLog("Otwieranie programu: " + err.Error())
	}
}

func (m *monitor) menu() {
	const (
		cmdShow = 1 + iota
		cmdCheck
		cmdOpen
		cmdExit
		cmdOff
		cmdUpdate
		cmdInterval = 10
	)
	h := call(pCreatePopupMenu)
	sub := call(pCreatePopupMenu)
	for i, iv := range monitorIntervals {
		fl := uintptr(mfString | mftRadio)
		if iv == m.interval {
			fl |= mfChecked
		}
		call(pAppendMenuW, sub, fl, uintptr(cmdInterval+i), u16p(intervalText(iv)))
	}
	call(pAppendMenuW, h, mfString, cmdShow, u16p("Pokaż wyniki monitorowania"))
	call(pAppendMenuW, h, mfString, cmdCheck, u16p("Sprawdź teraz"))
	call(pAppendMenuW, h, mfString, cmdOpen, u16p("Otwórz SerwisDiag"))
	if m.updateRel != nil {
		call(pAppendMenuW, h, mfString, cmdUpdate, u16p("Dostępna wersja "+m.updateRel.Latest+" - otwórz SerwisDiag, aby zaktualizować"))
	}
	call(pAppendMenuW, h, mfSeparator, 0, 0)
	call(pAppendMenuW, h, mfPopup, sub, u16p("Sprawdzaj"))
	call(pAppendMenuW, h, mfSeparator, 0, 0)
	call(pAppendMenuW, h, mfString, cmdExit, u16p("Zakończ (do następnego logowania)"))
	call(pAppendMenuW, h, mfString, cmdOff, u16p("Wyłącz monitorowanie"))
	call(pSetMenuDefaultItem, h, cmdShow, 0)
	p := cursorPos()
	call(pSetForegroundWindow, m.h)
	cmd := int(call(pTrackPopupMenu, h, tpmRightButton|tpmReturnCmd|tpmNoNotify, uintptr(p.X), uintptr(p.Y), 0, m.h, 0))
	postMsg(m.h, wmNull, 0, 0)
	call(pDestroyMenu, h)
	switch {
	case cmd == cmdShow:
		m.showWindow()
	case cmd == cmdCheck:
		m.startCheck()
	case cmd == cmdOpen, cmd == cmdUpdate:
		m.openProgram()
	case cmd == cmdExit:
		postMsg(m.h, wmClose, 0, 0)
	case cmd == cmdOff:
		if msgBox(0, "Wyłączyć monitorowanie w tle? Nie uruchomi się też przy następnym logowaniu. Włączysz je ponownie w ustawieniach programu SerwisDiag.",
			monProduct, mbIconQuestion|mbYesNo|mbSetForeground) == idYes {
			if slices.Contains(unregisterAutostart(m.exe, false), "task: brak uprawnień") {
				msgBox(0, "Monitorowanie dla wszystkich użytkowników wyłączysz w ustawieniach programu SerwisDiag (wymaga uprawnień administratora). Teraz monitor zakończy pracę do następnego logowania.",
					monProduct, mbIconInfo|mbSetForeground)
			}
			postMsg(m.h, wmClose, 0, 0)
		}
	case cmd >= cmdInterval && cmd < cmdInterval+len(monitorIntervals):
		m.interval = monitorIntervals[cmd-cmdInterval]
		if err := setMonitorInterval(m.statePath, m.interval); err != nil {
			monitorLog("Zapis częstotliwości: " + err.Error())
		}
		if !m.lastCheck.IsZero() && !m.busy {
			m.nextCheck = m.lastCheck.Add(time.Duration(m.interval) * time.Minute)
		}
		m.updateWindow()
	}
}

// timer: co 2 s - pora sprawdzenia, a co minutę: czy program nie został usunięty albo zaktualizowany.
func (m *monitor) timer() {
	now := time.Now()
	if now.Sub(m.lastExeTest) >= time.Minute {
		m.lastExeTest = now
		st, err := os.Stat(m.exe)
		if err != nil {
			postMsg(m.h, wmClose, 0, 0) // program usunięty
			return
		}
		if !m.exeTime.IsZero() && !st.ModTime().Equal(m.exeTime) && !m.busy {
			// nowa wersja pliku programu: nowy monitor (czeka, aż ten zwolni blokadę)
			monitorLog("Nowa wersja pliku programu - ponowne uruchomienie monitora.")
			_ = startMonitorProcess(m.exe)
			postMsg(m.h, wmClose, 0, 0)
			return
		}
	}
	if !m.busy && !now.Before(m.nextCheck) {
		m.startCheck()
	}
}

func (m *monitor) proc(h uintptr, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmTray:
		switch uint32(lp & 0xFFFF) {
		case wmLButtonDblClk:
			m.showWindow()
		case wmRButtonUp, wmContextMenu:
			m.menu()
		case ninBalloonUserClick:
			if m.balloonKind == "update" {
				m.openProgram()
			} else {
				m.showWindow()
			}
		}
		return 0, true
	case wmTimer:
		m.timer()
		return 0, true
	case wmCheckDone:
		m.completeCheck()
		return 0, true
	case wmEndSession:
		// wylogowanie albo instalator (Menedżer ponownego uruchamiania) zamyka program przed podmianą plików
		if wp != 0 {
			call(pDestroyWindow, h)
		}
		return 0, true
	case wmClose:
		if m.win != nil && call(pIsWindow, m.win.h) != 0 {
			call(pDestroyWindow, m.win.h)
		}
		call(pDestroyWindow, h)
		return 0, true
	case wmDestroy:
		call(pKillTimer, h, timerMonitor)
		m.trayDelete()
		call(pPostQuitMessage, 0)
		return 0, true
	}
	if m.taskbarMsg != 0 && msg == m.taskbarMsg {
		m.added = false
		m.trayAdd() // Eksplorator uruchomiony ponownie - ikona od nowa
		return 0, true
	}
	return 0, false
}

// ---------------------------------------------------------------- okno wyników monitorowania

func (m *monitor) showWindow() {
	if m.win != nil && call(pIsWindow, m.win.h) != 0 {
		call(pShowWindow, m.win.h, swRestore)
		call(pSetForegroundWindow, m.win.h)
		return
	}
	d := newDialog(0, monProduct+" - monitorowanie systemu", 620, 520, true)
	d.minW, d.minH = 460, 320
	d.modeless = true
	m.win = d
	m.winDoc = newDoc(d.h, cWhite, m.layoutList)
	btnCheck := newButton(d.h, 601, "Sprawdź teraz")
	btnOpen := newButton(d.h, 602, "Otwórz SerwisDiag")
	btnClose := newButton(d.h, 603, "Zamknij")
	fonts := func() {
		for _, b := range []uintptr{btnCheck, btnOpen, btnClose} {
			setFont(b, d.font(130, fwNormal))
		}
		m.winDoc.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	head := func(w int) (textBlock, textBlock) {
		p := d.px(20)
		t := layoutText([]span{{text: m.windowTitle(), font: d.font(180, fwSemiBold), color: cDark}}, w-2*p, 0)
		s := layoutText([]span{{text: m.windowStatus(), font: d.font(130, fwNormal), color: cGray2}}, w-2*p, 0)
		return t, s
	}
	listTop := func(w int) int {
		t, s := head(w)
		return d.px(20) + t.h + d.px(4) + s.h + d.px(12)
	}
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(20)
		t, s := head(w)
		cv.block(t, p, p)
		cv.block(s, p, p+t.h+d.px(4))
		cv.card(p, listTop(w), w-p, h-p-d.px(32)-d.px(14), float64(d.px(8)), cWhite, cBorder, 1)
	}
	d.onSize = func(w, h int) {
		p := d.px(20)
		top := listTop(w)
		bottom := h - p - d.px(32) - d.px(14)
		moveWin(m.winDoc.h, p+d.px(1), top+d.px(1), w-2*p-d.px(2), bottom-top-d.px(2))
		f := d.font(130, fwNormal)
		cw := buttonWidth(f, "Sprawdź teraz", d.px(14))
		ow := buttonWidth(f, "Otwórz SerwisDiag", d.px(14))
		y := h - p - d.px(32)
		moveWin(btnCheck, p, y, cw, d.px(32))
		moveWin(btnClose, w-p-d.px(100), y, d.px(100), d.px(32))
		moveWin(btnOpen, w-p-d.px(100)-d.px(8)-ow, y, ow, d.px(32))
	}
	d.onCmd = func(id, code int) {
		switch id {
		case 601:
			if !m.busy {
				m.startCheck()
			}
		case 602:
			m.openProgram()
		case 603, idOK:
			d.close()
		}
	}
	d.onClosed = func() { m.win, m.winDoc = nil, nil }
	cr := clientRect(d.h)
	d.onSize(cr.w(), cr.h())
	call(pShowWindow, d.h, swShow)
	call(pSetForegroundWindow, d.h)
}

func (m *monitor) updateWindow() {
	if m.win == nil || call(pIsWindow, m.win.h) == 0 {
		return
	}
	cr := clientRect(m.win.h)
	m.win.onSize(cr.w(), cr.h())
	m.winDoc.relayout()
	invalidate(m.win.h)
}

func (m *monitor) windowTitle() string {
	cnt := len(m.problems)
	switch {
	case m.busy && m.lastCheck.IsZero():
		return "Trwa pierwsze sprawdzanie..."
	case cnt == 0:
		return "Nie wykryto problemów"
	}
	return "Wykryto " + diag.FmtCount(cnt, "problem", "problemy", "problemów")
}

func (m *monitor) windowStatus() string {
	var st []string
	if !m.lastCheck.IsZero() {
		st = append(st, "Ostatnie sprawdzenie: "+diag.FmtDt(m.lastCheck))
	}
	if m.busy {
		st = append(st, "trwa sprawdzanie...")
	} else {
		st = append(st, "następne: "+diag.FmtDt(m.nextCheck)+" ("+intervalText(m.interval)+")")
	}
	if !m.isAdmin {
		st = append(st, "bez uprawnień administratora - część testów (np. SMART dysków SATA) pominięto")
	}
	return strings.Join(st, " · ")
}

func (m *monitor) layoutList(d *doc, width int) int {
	pad := d.px(14)
	w := width - 2*pad
	y := d.px(10)
	f := d.font(130, fwNormal)
	bf := d.font(130, fwSemiBold)
	if len(m.problems) == 0 {
		t := "Wyniki pojawią się po pierwszym sprawdzeniu."
		if !m.lastCheck.IsZero() {
			t = "Sprawdzane: dyski (stan i miejsce), awarie i nieoczekiwane restarty, błędy sprzętu, urządzenia, antywirus, zapora i Windows Update."
		}
		tb := layoutText([]span{{text: t, font: f, color: cGray2}}, w, 0)
		d.block(tb, pad, y)
		return y + tb.h + d.px(10)
	}
	ps := slices.Clone(m.problems)
	slices.SortStableFunc(ps, func(a, b monProblem) int {
		ra, rb := 1, 1
		if a.Status == "crit" {
			ra = 0
		}
		if b.Status == "crit" {
			rb = 0
		}
		return ra - rb
	})
	for _, p := range ps {
		label, lc := "UWAGA", hexc("#B54708")
		if p.Status == "crit" {
			label, lc = "KRYTYCZNE", hexc("#B42318")
		}
		parts := []span{{text: "[" + label + "] ", font: bf, color: lc}, {text: p.Title, font: bf, color: cDark}}
		if p.Value != "" {
			parts = append(parts, span{text: p.Value, font: f, color: cGray3, br: true})
		}
		if p.Advice != "" {
			parts = append(parts, span{text: "Zalecenie: " + p.Advice, font: f, color: cGray, br: true})
		}
		tb := layoutText(parts, w, 0)
		d.block(tb, pad, y)
		y += tb.h + d.px(10)
	}
	return y
}
