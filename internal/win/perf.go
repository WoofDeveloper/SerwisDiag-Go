package win

import (
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------- liczniki wydajności (PDH, nazwy angielskie)

var (
	pdh                             = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery                = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter        = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery               = pdh.NewProc("PdhCloseQuery")
)

// Pdh - zapytanie o liczniki wydajności (jak klasa SdPdh w wersji PowerShell).
type Pdh struct {
	q        uintptr
	counters []uintptr
}

func NewPdh() (*Pdh, error) {
	var q uintptr
	if r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return nil, &HResultError{Op: "PdhOpenQuery", Code: uint32(r)}
	}
	return &Pdh{q: q}, nil
}

// Add dodaje licznik (ścieżka po angielsku); -1, gdy licznik nie istnieje.
func (p *Pdh) Add(path string) int {
	p16, _ := windows.UTF16PtrFromString(path)
	var c uintptr
	if r, _, _ := procPdhAddEnglishCounter.Call(p.q, uintptr(unsafe.Pointer(p16)), 0, uintptr(unsafe.Pointer(&c))); r != 0 {
		return -1
	}
	p.counters = append(p.counters, c)
	return len(p.counters) - 1
}

func (p *Pdh) Collect() bool {
	r, _, _ := procPdhCollectQueryData.Call(p.q)
	return r == 0
}

// Get zwraca wartość licznika (PDH_FMT_DOUBLE | PDH_FMT_NOCAP100); ok = false przy błędzie.
func (p *Pdh) Get(i int) (float64, bool) {
	if i < 0 || i >= len(p.counters) {
		return 0, false
	}
	var typ uint32
	var v struct {
		CStatus uint32
		_       uint32
		Value   float64
	}
	if r, _, _ := procPdhGetFormattedCounterValue.Call(p.counters[i], 0x00008200, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&v))); r != 0 || v.CStatus != 0 {
		return 0, false
	}
	return v.Value, true
}

func (p *Pdh) Close() {
	if p != nil && p.q != 0 {
		procPdhCloseQuery.Call(p.q)
		p.q = 0
	}
}

// ---------------------------------------------------------------- procesy (NtQuerySystemInformation)

// Process - migawka procesu: te same dane co Win32_Process, pobrane jednym wywołaniem systemowym.
type Process struct {
	PID        uint32
	Name       string
	KernelTime int64 // 100 ns
	UserTime   int64
	WorkingSet uint64
	ReadBytes  int64
	WriteBytes int64
}

// Processes zwraca listę procesów (bez ścieżek - te pobiera ProcessPath).
func Processes() ([]Process, error) {
	size := uint32(512 * 1024)
	var buf []byte
	for {
		buf = make([]byte, size)
		var need uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			break
		}
		if err == windows.STATUS_INFO_LENGTH_MISMATCH {
			size = max(need, size) + 64*1024
			continue
		}
		return nil, err
	}
	var out []Process
	off := uint32(0)
	for {
		pi := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
		p := Process{
			PID:        uint32(pi.UniqueProcessID),
			KernelTime: pi.KernelTime,
			UserTime:   pi.UserTime,
			WorkingSet: uint64(pi.WorkingSetSize),
			ReadBytes:  pi.ReadTransferCount,
			WriteBytes: pi.WriteTransferCount,
		}
		if pi.ImageName.Buffer != nil && pi.ImageName.Length > 0 {
			p.Name = windows.UTF16ToString(unsafe.Slice(pi.ImageName.Buffer, pi.ImageName.Length/2))
		}
		switch p.PID {
		case 0:
			p.Name = "System Idle Process"
		case 4:
			if p.Name == "" {
				p.Name = "System"
			}
		}
		out = append(out, p)
		if pi.NextEntryOffset == 0 {
			break
		}
		off += pi.NextEntryOffset
	}
	return out, nil
}

// ProcessPath zwraca pełną ścieżkę pliku procesu (pusta, gdy brak dostępu - jak ExecutablePath w WMI).
func ProcessPath(pid uint32) string {
	if pid == 0 || pid == 4 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// ---------------------------------------------------------------- plan zasilania

var (
	powrprof                  = windows.NewLazySystemDLL("powrprof.dll")
	procPowerGetActiveScheme  = powrprof.NewProc("PowerGetActiveScheme")
	procPowerReadFriendlyName = powrprof.NewProc("PowerReadFriendlyName")
)

// ActivePowerScheme zwraca GUID aktywnego planu zasilania (małe litery) i jego nazwę w języku systemu.
func ActivePowerScheme() (guid, name string, ok bool) {
	var g *windows.GUID
	if r, _, _ := procPowerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&g))); r != 0 || g == nil {
		return "", "", false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(g)))
	guid = strings.ToLower(strings.Trim(g.String(), "{}"))
	var size uint32
	procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(g)), 0, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size > 0 {
		buf := make([]uint16, size/2+1)
		if r, _, _ := procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(g)), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
			name = windows.UTF16ToString(buf)
		}
	}
	return guid, name, true
}

// ---------------------------------------------------------------- uruchamianie programów

// RunResult - wynik programu uruchomionego bez okna (jak Invoke-Native).
type RunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
	Err      error
}

// Run uruchamia program bez okna konsoli z limitem czasu.
func Run(timeout time.Duration, name string, args ...string) RunResult {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	res := RunResult{ExitCode: -1}
	if err := cmd.Start(); err != nil {
		res.Err = err
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		res.TimedOut = true
		_ = cmd.Process.Kill()
		<-done
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	res.Stdout, res.Stderr = []byte(so.String()), []byte(se.String())
	return res
}

// RunRaw uruchamia program z gotowym wierszem poleceń (argumenty już zacytowane, na początku nazwa programu).
func RunRaw(timeout time.Duration, name, cmdLine string) RunResult {
	cmd := exec.Command(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW, CmdLine: cmdLine}
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	res := RunResult{ExitCode: -1}
	if err := cmd.Start(); err != nil {
		res.Err = err
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		res.TimedOut = true
		_ = cmd.Process.Kill()
		<-done
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	res.Stdout, res.Stderr = []byte(so.String()), []byte(se.String())
	return res
}

// OEMString dekoduje wyjście programu konsolowego zapisane w stronie kodowej OEM (np. 852).
func OEMString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	cp := getOEMCP() // programy uruchamiane bez okna dostają nową konsolę z domyślną stroną kodową OEM
	if cp == 65001 {
		return string(b)
	}
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	w := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &w[0], n); err != nil {
		return string(b)
	}
	return windows.UTF16ToString(w)
}

var procGetOEMCP = kernel32.NewProc("GetOEMCP")

func getOEMCP() uint32 {
	r, _, _ := procGetOEMCP.Call()
	return uint32(r)
}
