// sdguibench mierzy okno programu i monitor przy zegarze w wersji PowerShell i Go (uruchamiane na zmianę,
// każde w osobnym obiekcie zadania - z procesami potomnymi):
//
//	-mode start     czas od uruchomienia do pokazania okna i pamięć okna po 5 s bezczynności (bez diagnostyki)
//	-mode selftest  autotest okna: szybka diagnostyka poza ekranem, 5 zrzutów stron, zamknięcie
//	-mode monitor   jedno sprawdzenie monitora w trybie testu: start, 8 testów, okno wyników, zrzut, koniec
//
// Raporty z autotestu (SELFTEST_*) zawierają dane komputera - narzędzie usuwa te, które utworzył pomiar.
//
//	go run ./cmd/sdguibench -mode start -runs 3
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	user32                   = windows.NewLazySystemDLL("user32.dll")
	pK32GetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
	pEnumWindows             = user32.NewProc("EnumWindows")
	pGetWindowThreadProcess  = user32.NewProc("GetWindowThreadProcessId")
	pIsWindowVisible         = user32.NewProc("IsWindowVisible")
	pGetWindowTextW          = user32.NewProc("GetWindowTextW")
	pPostMessageW            = user32.NewProc("PostMessageW")
	pSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

type pmc struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type accounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodUser, ThisPeriodKernel int64
	PageFaults, TotalProcesses, ActiveProcesses, Terminated          uint32
}

type result struct {
	variant                 string
	wall, ready, cpu        float64
	privMB, wsMB, peakJobMB float64
	peakWSMB                float64
	procs                   uint32
	exit                    uint32
}

type variant struct {
	name string
	exe  string
	args []string
	env  []string
}

func quote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// findWindow: widoczne okno procesu pid z tytułem zaczynającym się od prefix (jedno wywołanie zwrotne na cały
// program - liczba wywołań zwrotnych w Go jest ograniczona).
var (
	findPid    uint32
	findPrefix string
	found      uintptr
	enumCB     = windows.NewCallback(enumProc)
)

func findWindow(pid uint32, prefix string) uintptr {
	findPid, findPrefix, found = pid, prefix, 0
	pEnumWindows.Call(enumCB, 0)
	return found
}

func enumProc(h, lp uintptr) uintptr {
	var p uint32
	pGetWindowThreadProcess.Call(h, uintptr(unsafe.Pointer(&p)))
	if p != findPid {
		return 1
	}
	if r, _, _ := pIsWindowVisible.Call(h); r == 0 {
		return 1
	}
	buf := make([]uint16, 256)
	pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 256)
	if strings.HasPrefix(windows.UTF16ToString(buf), findPrefix) {
		found = h
		return 0
	}
	return 1
}

func memOf(p windows.Handle) (priv, ws, peakWS float64) {
	var c pmc
	c.Cb = uint32(unsafe.Sizeof(c))
	if r, _, _ := pK32GetProcessMemoryInfo.Call(uintptr(p), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb)); r != 0 {
		return float64(c.PagefileUsage) / (1 << 20), float64(c.WorkingSetSize) / (1 << 20), float64(c.PeakWorkingSetSize) / (1 << 20)
	}
	return 0, 0, 0
}

func runOnce(v variant, mode string, timeout time.Duration) (result, error) {
	r := result{variant: v.name}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return r, err
	}
	defer windows.CloseHandle(job)
	cmd := quote(v.exe)
	for _, a := range v.args {
		cmd += " " + quote(a)
	}
	var envBlock *uint16
	if len(v.env) > 0 {
		e := append(os.Environ(), v.env...)
		var u []uint16
		for _, s := range e {
			u = append(u, windows.StringToUTF16(s)...)
		}
		u = append(u, 0)
		envBlock = &u[0]
	}
	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	start := time.Now()
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(nil, windows.StringToUTF16Ptr(cmd), nil, nil, false, flags, envBlock, nil, si, &pi); err != nil {
		return r, fmt.Errorf("%s: %w", cmd, err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return r, err
	}
	windows.ResumeThread(pi.Thread)
	if mode == "start" {
		var h uintptr
		for time.Since(start) < timeout {
			if h = findWindow(pi.ProcessId, "SerwisDiag"); h != 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if h == 0 {
			windows.TerminateJobObject(job, 1)
			return r, fmt.Errorf("%s: okno nie pojawiło się", v.name)
		}
		r.ready = time.Since(start).Seconds()
		time.Sleep(5 * time.Second)
		r.privMB, r.wsMB, _ = memOf(pi.Process)
		var acc accounting
		windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil)
		r.cpu = float64(acc.TotalUserTime+acc.TotalKernelTime) / 1e7
		pPostMessageW.Call(h, 0x0010, 0, 0) // WM_CLOSE
	}
	if ev, _ := windows.WaitForSingleObject(pi.Process, uint32(timeout/time.Millisecond)); ev != windows.WAIT_OBJECT_0 {
		windows.TerminateJobObject(job, 1)
		return r, fmt.Errorf("%s: przekroczono czas", v.name)
	}
	r.wall = time.Since(start).Seconds()
	for i := 0; i < 50; i++ {
		var acc accounting
		windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil)
		if acc.ActiveProcesses == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var acc accounting
	if windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil) == nil {
		if mode != "start" {
			r.cpu = float64(acc.TotalUserTime+acc.TotalKernelTime) / 1e7
		}
		r.procs = acc.TotalProcesses
	}
	var ext windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&ext)), uint32(unsafe.Sizeof(ext)), nil) == nil {
		r.peakJobMB = float64(ext.PeakJobMemoryUsed) / (1 << 20)
	}
	_, _, r.peakWSMB = memOf(pi.Process)
	windows.GetExitCodeProcess(pi.Process, &r.exit)
	return r, nil
}

func median(v []float64) (m, lo, hi float64) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		m = s[n/2]
	} else {
		m = (s[n/2-1] + s[n/2]) / 2
	}
	return m, s[0], s[n-1]
}

func pl(f float64, d int) string {
	return strings.Replace(fmt.Sprintf("%.*f", d, f), ".", ",", 1)
}

func cell(v []float64, d int) string {
	m, lo, hi := median(v)
	if len(v) < 2 || math.Abs(hi-lo) < math.Pow(10, -float64(d)) {
		return pl(m, d)
	}
	return fmt.Sprintf("%s (%s–%s)", pl(m, d), pl(lo, d), pl(hi, d))
}

// cleanReports usuwa raporty autotestu (SELFTEST_*) utworzone po chwili since.
func cleanReports(dir string, since time.Time) int {
	n := 0
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), "SELFTEST_") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().After(since) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n
}

func main() {
	mode := flag.String("mode", "start", "start | selftest | monitor")
	runs := flag.Int("runs", 3, "liczba powtórzeń każdej wersji")
	psScript := flag.String("ps", `C:\serwis-diag\SerwisDiag.ps1`, "wersja PowerShell")
	goExe := flag.String("go", `C:\serwis-diag-go\serwisdiag.exe`, "wersja Go (plik okienkowy)")
	work := flag.String("work", os.TempDir(), "folder na zrzuty i pliki stanu monitora")
	psReports := flag.String("psreports", `C:\serwis-diag\raporty`, "folder raportów wersji PowerShell (usuwane tylko SELFTEST_* z pomiaru)")
	goReports := flag.String("goreports", `C:\serwis-diag-go\raporty`, "folder raportów wersji Go")
	state := flag.String("state", "", "monitor: plik stanu kopiowany przed każdym uruchomieniem (bez powiadomienia o znanych problemach)")
	flag.Parse()

	// komputer nie zaśnie w trakcie pomiaru
	pSetThreadExecutionState.Call(0x80000000 | 0x00000001)
	defer pSetThreadExecutionState.Call(0x80000000)

	ps := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	psBase := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-STA", "-File", *psScript}
	_ = os.MkdirAll(*work, 0o755)
	var variants []func(i int) variant
	timeout := 120 * time.Second
	switch *mode {
	case "start":
		variants = []func(int) variant{
			func(int) variant { return variant{name: "PowerShell (WPF)", exe: ps, args: psBase} },
			func(int) variant { return variant{name: "Go (Win32)", exe: *goExe} },
		}
	case "selftest":
		timeout = 300 * time.Second
		variants = []func(int) variant{
			func(i int) variant {
				return variant{name: "PowerShell (WPF)", exe: ps, args: append(append([]string{}, psBase...), "-SelfTest", "-ScreenshotPath", filepath.Join(*work, fmt.Sprintf("ps%d", i)))}
			},
			func(i int) variant {
				return variant{name: "Go (Win32)", exe: *goExe, args: []string{"-SelfTest", "-ScreenshotPath", filepath.Join(*work, fmt.Sprintf("go%d", i))}}
			},
		}
	case "monitor":
		mk := func(name, exe string, args []string, tag string, i int) variant {
			st := filepath.Join(*work, fmt.Sprintf("stan-%s%d.json", tag, i))
			if *state != "" {
				if b, err := os.ReadFile(*state); err == nil {
					_ = os.WriteFile(st, b, 0o644)
				}
			}
			return variant{name: name, exe: exe, args: args,
				env: []string{"SERWISDIAG_MONITOR_TEST=" + filepath.Join(*work, fmt.Sprintf("mon-%s%d", tag, i)), "SERWISDIAG_MONITOR_STATE=" + st}}
		}
		variants = []func(int) variant{
			func(i int) variant {
				return mk("PowerShell (WPF)", ps, append(append([]string{}, psBase...), "-Monitor"), "ps", i)
			},
			func(i int) variant { return mk("Go (Win32)", *goExe, []string{"-Monitor"}, "go", i) },
		}
	default:
		fmt.Println("nieznany tryb:", *mode)
		os.Exit(2)
	}

	t0 := time.Now()
	res := map[string][]result{}
	var order []string
	for i := 1; i <= *runs; i++ {
		for _, vf := range variants {
			v := vf(i)
			r, err := runOnce(v, *mode, timeout)
			if err != nil {
				fmt.Println("BŁĄD:", err)
				continue
			}
			if _, ok := res[v.name]; !ok {
				order = append(order, v.name)
			}
			res[v.name] = append(res[v.name], r)
			fmt.Printf("%-18s przebieg %d: start %ss, całość %ss, CPU %ss, prywatna %s MB, roboczy %s MB, szczyt zadania %s MB, procesy %d, kod %d\n",
				v.name, i, pl(r.ready, 2), pl(r.wall, 1), pl(r.cpu, 2), pl(r.privMB, 0), pl(r.wsMB, 0), pl(r.peakJobMB, 0), r.procs, r.exit)
			time.Sleep(3 * time.Second)
		}
	}
	if *mode == "selftest" {
		fmt.Printf("Usunięto raporty autotestu: PowerShell %d, Go %d\n", cleanReports(*psReports, t0), cleanReports(*goReports, t0))
	}
	fmt.Println()
	switch *mode {
	case "start":
		fmt.Println("| Wersja | Czas do pokazania okna [s] | CPU do tej chwili + 5 s [s] | Pamięć prywatna [MB] | Zestaw roboczy [MB] | Procesy |")
		fmt.Println("|---|---|---|---|---|---|")
	default:
		fmt.Println("| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci zadania [MB] | Szczyt pamięci procesu [MB] | Procesy |")
		fmt.Println("|---|---|---|---|---|---|")
	}
	for _, name := range order {
		var a, b, c, d []float64
		var procs uint32
		for _, r := range res[name] {
			if *mode == "start" {
				a, b, c, d = append(a, r.ready), append(b, r.cpu), append(c, r.privMB), append(d, r.wsMB)
			} else {
				a, b, c, d = append(a, r.wall), append(b, r.cpu), append(c, r.peakJobMB), append(d, r.peakWSMB)
			}
			procs = max(procs, r.procs)
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %d |\n", name, cell(a, 2), cell(b, 2), cell(c, 0), cell(d, 0), procs)
	}
}
