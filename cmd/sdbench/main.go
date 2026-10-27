// sdbench mierzy wydajność wersji PowerShell i Go programu SerwisDiag na tym samym komputerze.
//
// Każde uruchomienie działa w osobnym obiekcie zadania (job object), więc pomiar obejmuje też procesy potomne
// (np. powershell.exe uruchamiany przez Start-Job, powercfg, netsh). Mierzone są: czas całkowity, czas procesora
// (użytkownika + jądra), szczytowa pamięć głównego procesu i całego zadania, liczba procesów oraz czas procesora
// zużyty w tym czasie przez dostawców WMI (WmiPrvSE.exe) - to koszt zapytań WMI ponoszony poza programem.
//
//	go run ./cmd/sdbench -runs 3 -level Quick -out bench\wyniki
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/win"
)

type variant struct {
	Name string
	Exe  string
	Args []string
}

type measurement struct {
	Variant     string  `json:"variant"`
	Run         int     `json:"run"`
	WallSec     float64 `json:"wallSec"`
	CPUSec      float64 `json:"cpuSec"`
	WmiCPUSec   float64 `json:"wmiCpuSec"`
	PeakWSMB    float64 `json:"peakWorkingSetMB"`
	PeakJobMB   float64 `json:"peakJobCommitMB"`
	Processes   uint32  `json:"processes"`
	ExitCode    uint32  `json:"exitCode"`
	ScanSec     float64 `json:"scanSec"`
	Findings    int     `json:"findings"`
	Verdict     string  `json:"verdict"`
	CountsShort string  `json:"counts"`
	SleptSec    float64 `json:"sleptSec"` // komputer był uśpiony w trakcie pomiaru - pomiar nieważny
}

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procK32GetProcessMemoryInfo    = kernel32.NewProc("K32GetProcessMemoryInfo")
	procQueryUnbiasedInterruptTime = kernel32.NewProc("QueryUnbiasedInterruptTime")
	procSetThreadExecutionState    = kernel32.NewProc("SetThreadExecutionState")
)

// unbiasedSeconds: czas od startu systemu bez okresów uśpienia i hibernacji.
func unbiasedSeconds() float64 {
	var t uint64
	procQueryUnbiasedInterruptTime.Call(uintptr(unsafe.Pointer(&t)))
	return float64(t) / 1e7
}

type processMemoryCounters struct {
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

type basicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func quote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// wmiCPU: łączny czas procesora procesów WmiPrvSE.exe (dostawców WMI) w sekundach.
func wmiCPU() float64 {
	ps, err := win.Processes()
	if err != nil {
		return 0
	}
	t := int64(0)
	for _, p := range ps {
		if strings.EqualFold(p.Name, "WmiPrvSE.exe") {
			t += p.KernelTime + p.UserTime
		}
	}
	return float64(t) / 1e7
}

func runOnce(v variant, outDir string, logPath string) (measurement, error) {
	m := measurement{Variant: v.Name}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return m, err
	}
	defer windows.CloseHandle(job)

	cmdLine := quote(v.Exe)
	for _, a := range v.Args {
		cmdLine += " " + quote(a)
	}
	cmdLine += " -OutDir " + quote(outDir)
	logF, err := os.Create(logPath)
	if err != nil {
		return m, err
	}
	defer logF.Close()
	h := windows.Handle(logF.Fd())
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return m, err
	}
	si := &windows.StartupInfo{Flags: windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE, StdOutput: h, StdErr: h}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	cl, _ := windows.UTF16PtrFromString(cmdLine)
	wmiBefore := wmiCPU()
	start := time.Now()
	startAwake := unbiasedSeconds()
	if err := windows.CreateProcess(nil, cl, nil, nil, true, windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW, nil, nil, si, &pi); err != nil {
		return m, fmt.Errorf("%s: %w", cmdLine, err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return m, err
	}
	windows.ResumeThread(pi.Thread)
	windows.WaitForSingleObject(pi.Process, windows.INFINITE)
	m.WallSec = time.Since(start).Seconds()
	if slept := m.WallSec - (unbiasedSeconds() - startAwake); slept > 2 {
		m.SleptSec = slept
	}
	// procesy potomne (np. zadanie w tle) mogą chwilę dłużej kończyć pracę
	for i := 0; i < 50; i++ {
		var acc basicAccounting
		windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil)
		if acc.ActiveProcesses == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.WmiCPUSec = wmiCPU() - wmiBefore
	var acc basicAccounting
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil); err == nil {
		m.CPUSec = float64(acc.TotalUserTime+acc.TotalKernelTime) / 1e7
		m.Processes = acc.TotalProcesses
	}
	var ext windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&ext)), uint32(unsafe.Sizeof(ext)), nil); err == nil {
		m.PeakJobMB = float64(ext.PeakJobMemoryUsed) / (1 << 20)
	}
	var pmc processMemoryCounters
	pmc.Cb = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(pi.Process), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.Cb)); r != 0 {
		m.PeakWSMB = float64(pmc.PeakWorkingSetSize) / (1 << 20)
	}
	windows.GetExitCodeProcess(pi.Process, &m.ExitCode)
	readReport(outDir, &m)
	return m, nil
}

// readReport odczytuje z raportu JSON czas skanowania i liczbę wyników (bez danych komputera).
func readReport(dir string, m *measurement) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		return
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		return
	}
	b = []byte(strings.TrimPrefix(string(b), string(rune(0xFEFF))))
	var r struct {
		Started  string           `json:"started"`
		Finished string           `json:"finished"`
		Verdict  string           `json:"verdict"`
		Counts   map[string]int   `json:"counts"`
		Findings []map[string]any `json:"findings"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	s, e1 := time.ParseInLocation("2006-01-02T15:04:05", r.Started, time.Local)
	f, e2 := time.ParseInLocation("2006-01-02T15:04:05", r.Finished, time.Local)
	if e1 == nil && e2 == nil {
		m.ScanSec = f.Sub(s).Seconds()
	}
	m.Findings = len(r.Findings)
	m.Verdict = r.Verdict
	m.CountsShort = fmt.Sprintf("kryt. %d, ostrz. %d, info %d, OK %d, pomin. %d", r.Counts["crit"], r.Counts["warn"], r.Counts["info"], r.Counts["ok"], r.Counts["skip"])
}

type stat struct{ med, min, max float64 }

func stats(v []float64) stat {
	if len(v) == 0 {
		return stat{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return stat{med, s[0], s[len(s)-1]}
}

func pl(f float64, d int) string {
	return strings.Replace(fmt.Sprintf("%.*f", d, f), ".", ",", 1)
}

func main() {
	runs := flag.Int("runs", 3, "liczba powtórzeń każdej wersji")
	level := flag.String("level", "Quick", "poziom diagnostyki: Quick albo Full")
	offline := flag.Bool("offline", false, "tryb bez internetu (bez Windows Update i testów internetu)")
	psScript := flag.String("ps", `C:\serwis-diag\SerwisDiag.ps1`, "wersja PowerShell (SerwisDiag.ps1)")
	goExe := flag.String("go", `C:\serwis-diag-go\serwisdiag-konsola.exe`, "wersja Go (serwisdiag-konsola.exe)")
	out := flag.String("out", "bench-wyniki", "folder wyników")
	keep := flag.Bool("keep", false, "zachowaj raporty z każdego uruchomienia (zawierają dane komputera)")
	only := flag.String("only", "", "tylko wybrane wersje: ps,go,goseq")
	flag.Parse()

	// komputer nie może zasnąć w trakcie pomiaru (prośba obowiązuje tylko do końca działania sdbench)
	runtime.LockOSThread()
	procSetThreadExecutionState.Call(0x80000001) // ES_CONTINUOUS | ES_SYSTEM_REQUIRED
	defer procSetThreadExecutionState.Call(0x80000000)

	common := []string{"-Level", *level}
	if *offline {
		common = append(common, "-Offline")
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`)
	all := []variant{
		{"PowerShell", ps, append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", *psScript, "-NoGui"}, common...)},
		{"Go (równolegle)", *goExe, append([]string{"-Quiet"}, common...)},
		{"Go (po kolei)", *goExe, append([]string{"-Quiet", "-Sequential"}, common...)},
	}
	var vs []variant
	for i, v := range all {
		key := []string{"ps", "go", "goseq"}[i]
		if *only == "" || strings.Contains(","+*only+",", ","+key+",") {
			vs = append(vs, v)
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	scenario := *level
	if *offline {
		scenario += " bez internetu"
	}
	stamp := time.Now().Format("20060102-150405")
	var results []measurement
	for r := 1; r <= *runs; r++ {
		order := append([]variant(nil), vs...)
		if r%2 == 0 { // naprzemienna kolejność - żadna wersja nie korzysta stale z "rozgrzanego" systemu
			for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
				order[i], order[j] = order[j], order[i]
			}
		}
		for _, v := range order {
			dir := filepath.Join(*out, fmt.Sprintf("%s-%s-%d-%s", stamp, strings.Map(func(c rune) rune {
				if c == ' ' || c == '(' || c == ')' {
					return -1
				}
				return c
			}, v.Name), r, strings.ToLower(*level)))
			os.MkdirAll(dir, 0o755)
			fmt.Printf("[%d/%d] %s (%s)... ", r, *runs, v.Name, scenario)
			m, err := runOnce(v, dir, filepath.Join(dir, "konsola.txt"))
			if err != nil {
				fmt.Println("BŁĄD:", err)
				continue
			}
			m.Run = r
			fmt.Printf("%s s, CPU %s s, pamięć %s MB, WMI %s s", pl(m.WallSec, 1), pl(m.CPUSec, 1), pl(m.PeakWSMB, 0), pl(m.WmiCPUSec, 1))
			if m.SleptSec > 0 {
				fmt.Printf(" - NIEWAŻNY: komputer był uśpiony przez %s s", pl(m.SleptSec, 0))
			}
			fmt.Println()
			results = append(results, m)
			if !*keep {
				os.RemoveAll(dir)
			}
			time.Sleep(3 * time.Second)
		}
	}
	// zestawienie
	var md strings.Builder
	fmt.Fprintf(&md, "## Scenariusz: %s (%d %s każdej wersji, %s)\n\n", scenario, *runs, map[bool]string{true: "pomiar", false: "pomiary"}[*runs == 1], time.Now().Format("2006-01-02 15:04"))
	md.WriteString("| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci procesu [MB] | Szczyt pamięci zadania [MB] | Procesy | CPU usługi WMI [s] | Wynik |\n")
	md.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, v := range vs {
		var wall, cpu, ws, job, wmi []float64
		procs := uint32(0)
		verdict := ""
		for _, m := range results {
			if m.Variant != v.Name || m.SleptSec > 0 {
				continue
			}
			wall = append(wall, m.WallSec)
			cpu = append(cpu, m.CPUSec)
			ws = append(ws, m.PeakWSMB)
			job = append(job, m.PeakJobMB)
			wmi = append(wmi, m.WmiCPUSec)
			procs = max(procs, m.Processes)
			verdict = m.CountsShort
		}
		f := func(s stat, d int) string {
			if math.Abs(s.max-s.min) < math.Pow(10, -float64(d)) {
				return pl(s.med, d)
			}
			return fmt.Sprintf("%s (%s–%s)", pl(s.med, d), pl(s.min, d), pl(s.max, d))
		}
		fmt.Fprintf(&md, "| %s | %s | %s | %s | %s | %d | %s | %s |\n", v.Name, f(stats(wall), 1), f(stats(cpu), 1), f(stats(ws), 0), f(stats(job), 0), procs, f(stats(wmi), 1), verdict)
	}
	md.WriteString("\nW nawiasach: najkrótszy i najdłuższy pomiar; poza nawiasem mediana.\n")
	for _, m := range results {
		if m.SleptSec > 0 {
			fmt.Fprintf(&md, "Pominięto pomiar nr %d wersji %s - komputer był w jego trakcie uśpiony przez %s s.\n", m.Run, m.Variant, pl(m.SleptSec, 0))
		}
	}
	fmt.Println()
	fmt.Println(md.String())
	base := filepath.Join(*out, "wyniki-"+strings.ReplaceAll(strings.ToLower(scenario), " ", "-")+"-"+stamp)
	os.WriteFile(base+".md", []byte(md.String()), 0o644)
	if b, err := json.MarshalIndent(results, "", "  "); err == nil {
		os.WriteFile(base+".json", b, 0o644)
	}
	fmt.Println("Zapisano:", base+".md")
}
