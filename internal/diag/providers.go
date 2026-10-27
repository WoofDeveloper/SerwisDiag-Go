package diag

import (
	"fmt"
	"math"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Dostawcy danych z pamięcią podręczną (lib\Providers.ps1): każdy odpytuje system co najwyżej raz na skanowanie.

func osObj(c *Ctx) win.WmiObject {
	return cached(c, "Os", func() win.WmiObject { return c.cim1("Win32_OperatingSystem") })
}

func csObj(c *Ctx) win.WmiObject {
	return cached(c, "Cs", func() win.WmiObject { return c.cim1("Win32_ComputerSystem") })
}

func cpuObjs(c *Ctx) []win.WmiObject {
	return cached(c, "Cpu", func() []win.WmiObject { return c.cim("Win32_Processor") })
}

func pnpObjs(c *Ctx) []win.WmiObject {
	return cached(c, "Pnp", func() []win.WmiObject {
		return c.cim("Win32_PnPEntity", "Name", "PNPDeviceID", "PNPClass", "ConfigManagerErrorCode", "HardwareID", "Manufacturer", "Service")
	})
}

// firmwareType odpowiada Get-SdFirmwareType: UEFI, BIOS albo "nieznany".
func firmwareType(c *Ctx) string {
	return cached(c, "Fw", func() string {
		t := win.FirmwareType()
		if t == 0 && win.RegKeyExists(`HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State`) {
			t = 2
		}
		switch t {
		case 2:
			return "UEFI"
		case 1:
			return "BIOS"
		}
		return "nieznany"
	})
}

// WinInfo - wersja systemu (Get-SdWinInfo).
type WinInfo struct {
	Build                   int64
	Ubr                     int64
	Version                 string
	Edition                 string
	Family                  string
	Caption                 string
	IsServer                bool
	Arch                    string
	IsLtsc, IsIot, IsEntEdu bool
}

var (
	ltscRx   = rx(`^(EnterpriseS|IoTEnterpriseS|EnterpriseSN)`)
	iotRx    = rx(`^IoT`)
	entEduRx = rx(`^(Enterprise|Education|IoTEnterprise|EnterpriseN|EducationN)`)
)

func winInfo(c *Ctx) *WinInfo {
	return cached(c, "WinInfo", func() *WinInfo {
		os := osObj(c)
		const cvKey = `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`
		_, cv, _ := win.RegValues(cvKey)
		w := &WinInfo{}
		if os != nil {
			w.Build, _ = strconv.ParseInt(os.Str("BuildNumber"), 10, 64)
		}
		w.Ubr = win.AnyInt(cv["ubr"])
		if s := win.AnyString(cv["displayversion"]); s != "" {
			w.Version = s
		} else {
			w.Version = win.AnyString(cv["releaseid"])
		}
		w.Edition = win.AnyString(cv["editionid"])
		if os != nil && os.Has("ProductType") && os.Int("ProductType") != 1 {
			w.IsServer = true
		}
		w.Caption = "Windows"
		if os != nil && os.Str("Caption") != "" {
			w.Caption = regexp.MustCompile(`(?i)^Microsoft\s+`).ReplaceAllString(os.Str("Caption"), "")
		}
		b := w.Build
		switch {
		case w.IsServer:
			w.Family = w.Caption
		case b >= 22000:
			w.Family = "Windows 11"
		case b >= 10240:
			w.Family = "Windows 10"
		case b == 9600:
			w.Family = "Windows 8.1"
		case b == 9200:
			w.Family = "Windows 8"
		case b > 0 && b < 9200:
			w.Family = "Windows (starszy niż 8)"
		default:
			w.Family = "Windows"
		}
		is64 := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
		w.Arch = "x86"
		if cpus := cpuObjs(c); len(cpus) > 0 {
			switch cpus[0].Int("Architecture") {
			case 9:
				w.Arch = "x64"
			case 12:
				w.Arch = "ARM64"
			case 5:
				w.Arch = "ARM"
			case 0:
				w.Arch = "x86"
			default:
				if is64 {
					w.Arch = "x64"
				}
			}
		} else if is64 {
			w.Arch = "x64"
		}
		w.IsLtsc = ltscRx.MatchString(w.Edition)
		w.IsIot = iotRx.MatchString(w.Edition)
		w.IsEntEdu = entEduRx.MatchString(w.Edition)
		return w
	})
}

// isLaptop odpowiada Test-SdLaptop: typ obudowy, a gdy nieokreślony - obecność baterii.
func isLaptop(c *Ctx) bool {
	return cached(c, "IsLaptop", func() bool {
		var types []int64
		if enc := c.cim("Win32_SystemEnclosure", "ChassisTypes"); len(enc) > 0 {
			types = enc[0].Ints("ChassisTypes")
		}
		lap, desk := false, false
		for _, t := range types {
			switch t {
			case 8, 9, 10, 11, 14, 30, 31, 32:
				lap = true
			case 3, 4, 5, 6, 7, 13, 15, 16, 17, 23, 24, 35, 36:
				desk = true
			}
		}
		if lap {
			return true
		}
		if desk {
			return false
		}
		return len(c.cim("Win32_Battery", "DeviceID")) > 0
	})
}

var vmRx = rx(`VMware|VirtualBox|Virtual Machine|KVM|QEMU|Xen|Parallels|Bochs|innotek|Hyper-V`)

// isVM odpowiada Test-SdVm.
func isVM(c *Ctx) bool {
	return cached(c, "IsVM", func() bool {
		cs := csObj(c)
		if cs == nil {
			return false
		}
		return vmRx.MatchString(joinNonEmpty(", ", cs.Str("Manufacturer"), cs.Str("Model")))
	})
}

// TpmInfo - moduł TPM (Get-SdTpm). Enabled: 0 nieznany, 1 tak, 2 nie.
type TpmInfo struct {
	Present      bool
	Enabled      Tri
	Activated    Tri
	Version      string
	Manufacturer string
	Source       string
}

// Tri - wartość trójstanowa ($true / $false / $null).
type Tri int8

const (
	Unknown Tri = iota
	Yes
	No
)

func triOf(b bool) Tri {
	if b {
		return Yes
	}
	return No
}

var (
	tpmPnpRx  = rx(`^ACPI\\(MSFT0101|PNP0C31|IFX0|INTC0102|NTC0|ATM12|BCM01|SMO0|STM0|WEC0|SNP0|NSC0)`)
	tpmNameRx = rx(`Trusted Platform|\bTPM\b`)
)

func tpmInfo(c *Ctx) *TpmInfo {
	return cached(c, "Tpm", func() *TpmInfo {
		r := &TpmInfo{}
		if c.IsAdmin() {
			t := c.Cim(Q{NS: `root\cimv2\Security\MicrosoftTpm`, Class: "Win32_Tpm"})
			if len(t) > 0 {
				r.Present, r.Source = true, "Win32_Tpm"
				r.Enabled = triOf(t[0].Bool("IsEnabled_InitialValue"))
				r.Activated = triOf(t[0].Bool("IsActivated_InitialValue"))
				if spec := t[0].Str("SpecVersion"); spec != "" {
					r.Version = strings.TrimSpace(strings.Split(spec, ",")[0])
				}
				r.Manufacturer = t[0].Str("ManufacturerIdTxt")
				return r
			}
		}
		// Bez uprawnień administratora: rozpoznanie po urządzeniu ACPI w Menedżerze urządzeń.
		for _, d := range pnpObjs(c) {
			if tpmPnpRx.MatchString(d.Str("PNPDeviceID")) || (eqi(d.Str("PNPClass"), "SecurityDevices") && tpmNameRx.MatchString(d.Str("Name"))) {
				r.Present, r.Source = true, "PnP"
				id, name := d.Str("PNPDeviceID"), d.Str("Name")
				switch {
				case rx(`MSFT0101`).MatchString(id) || strings.Contains(name, "2.0"):
					r.Version = "2.0"
				case rx(`PNP0C31`).MatchString(id) || strings.Contains(name, "1.2"):
					r.Version = "1.2"
				}
				r.Enabled = triOf(d.Int("ConfigManagerErrorCode") == 0)
				break
			}
		}
		return r
	})
}

// SecureBootInfo - stan Secure Boot (Get-SdSecureBoot).
type SecureBootInfo struct {
	Firmware string
	Enabled  bool
	Known    bool
}

func secureBoot(c *Ctx) *SecureBootInfo {
	return cached(c, "SecureBoot", func() *SecureBootInfo {
		r := &SecureBootInfo{Firmware: firmwareType(c)}
		if r.Firmware == "UEFI" {
			if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State`, "UEFISecureBootEnabled"); ok {
				r.Enabled, r.Known = win.AnyInt(v) == 1, true
			} else if c.IsAdmin() {
				if on, ok := win.SecureBootUEFI(); ok {
					r.Enabled, r.Known = on, true
				}
			}
		}
		return r
	})
}

// ---------------------------------------------------------------- heurystyka procesora (Get-SdCpuInfo)

// CpuInfo: generacja, rok i zgodność z Windows 11 - szacunek po nazwie procesora.
type CpuInfo struct {
	Vendor string
	Gen    string
	Year   int
	Win11  Tri
	Note   string
}

var (
	cpuIntel     = rx(`Intel`)
	cpuUltra     = rx(`Core\(TM\) Ultra|Core Ultra`)
	cpuAlderN    = rx(`Core\(TM\)\s+i[3579]-N\d{3}|Intel\(R\)\s+N\d{2,3}\b`)
	cpuPentCel   = rx(`(Pentium|Celeron)\(R\)\s+(?:CPU\s+)?(?:(?:Gold|Silver)\s+)?([GNJ])\s?(\d{4,5})`)
	cpuCoreI     = rx(`Core\(TM\)\s+i[3579](?:\s+CPU)?[\s-]+(\d{3,5})`)
	cpuCoreIOld  = rx(`Core\(TM\)\s+i[3579]\s+CPU\s+[MUL]\s+\d{3}`)
	cpuCoreM     = rx(`Core\(TM\)\s+m[3579]-(\d)`)
	cpuOldIntel  = rx(`Core\(TM\)\s?2|Core2|Atom|Pentium|Celeron`)
	cpuXeon      = rx(`Xeon`)
	cpuGenPrefix = rx(`(\d{1,2})(?:st|nd|rd|th) Gen`)
	cpuAMD       = rx(`AMD`)
	cpuRyzenAI   = rx(`Ryzen\s+AI`)
	cpuRyzen     = rx(`Ryzen\s+(?:Threadripper\s+)?(?:PRO\s+)?[3579]?\s*(?:PRO\s+)?(\d)(\d{3})`)
	cpuAthlon    = rx(`Athlon`)
	cpuAthlonNew = rx(`Athlon.*(3\d{3}|4\d{3}|300U)`)
	cpuOldAMD    = rx(`FX\(tm\)|FX-|A\d+-\d|Phenom|Sempron|Turion|E-\d{3}|C-\d{2}`)
	cpuEpyc      = rx(`EPYC`)
	cpuARM       = rx(`Snapdragon|Qualcomm|Microsoft SQ|Apple|ARM`)
	intelYears   = map[int]int{1: 2010, 2: 2011, 3: 2012, 4: 2013, 5: 2015, 6: 2015, 7: 2017, 8: 2017, 9: 2018, 10: 2019, 11: 2020, 12: 2021, 13: 2022, 14: 2023}
	ryzenYears   = map[int]int{1: 2017, 2: 2018, 3: 2019, 4: 2020, 5: 2021, 6: 2022, 7: 2023, 8: 2024, 9: 2024}
)

func cpuInfoFor(name string) CpuInfo {
	var r CpuInfo
	if name == "" {
		return r
	}
	switch {
	case cpuIntel.MatchString(name):
		r.Vendor = "Intel"
		gen := 0
		switch {
		case cpuUltra.MatchString(name):
			r.Gen, r.Year, r.Win11 = "Core Ultra", 2023, Yes
		case cpuAlderN.MatchString(name):
			r.Gen, r.Year, r.Win11 = "Alder Lake-N", 2023, Yes
		case cpuPentCel.MatchString(name):
			m := cpuPentCel.FindStringSubmatch(name)
			isPentium := eqi(m[1], "Pentium")
			series := strings.ToUpper(m[2])
			num, _ := strconv.Atoi(m[3])
			r.Gen = m[1]
			if isPentium {
				r.Win11 = triOf(num >= 4000)
				if series == "G" {
					r.Win11 = triOf(num >= 5000)
				}
			} else {
				r.Win11 = triOf(num >= 4000)
				if series == "G" {
					r.Win11 = triOf(num >= 4900)
				}
			}
		case cpuCoreI.MatchString(name):
			d := cpuCoreI.FindStringSubmatch(name)[1]
			switch len(d) {
			case 3:
				gen = 1
			case 4:
				if d[0] == '1' {
					gen, _ = strconv.Atoi(d[:2])
				} else {
					gen = int(d[0] - '0')
				}
			case 5:
				gen, _ = strconv.Atoi(d[:2])
			}
		case cpuCoreIOld.MatchString(name):
			gen = 1
		case cpuCoreM.MatchString(name):
			gen = int(cpuCoreM.FindStringSubmatch(name)[1][0] - '0')
			r.Gen = "Core M"
		case cpuOldIntel.MatchString(name):
			r.Gen, r.Year, r.Win11 = "Starsza architektura", 2009, No
		case cpuXeon.MatchString(name):
			r.Gen, r.Note = "Xeon", "Zgodność procesorów Xeon należy sprawdzić na liście Microsoft."
		}
		// Nazwa w Windows 11 zawiera często prefiks "12th Gen" - jest dokładniejszy niż numer modelu.
		if m := cpuGenPrefix.FindStringSubmatch(name); m != nil {
			gen, _ = strconv.Atoi(m[1])
		}
		if gen > 0 {
			if r.Gen == "" {
				r.Gen = fmt.Sprintf("%d. generacja Core", gen)
			}
			if y, ok := intelYears[gen]; ok {
				r.Year = y
			} else if gen > 14 {
				r.Year = 2024
			}
			r.Win11 = triOf(gen >= 8)
			if gen == 7 {
				r.Note = "Generacja 7 jest zgodna tylko w nielicznych wyjątkach (np. i7-7820HQ w Surface Studio 2)."
			}
		}
	case cpuAMD.MatchString(name):
		r.Vendor = "AMD"
		switch {
		case cpuRyzenAI.MatchString(name):
			r.Gen, r.Year, r.Win11 = "Ryzen AI", 2024, Yes
		case cpuRyzen.MatchString(name):
			series, _ := strconv.Atoi(cpuRyzen.FindStringSubmatch(name)[1])
			r.Gen = fmt.Sprintf("Ryzen serii %d000", series)
			if y, ok := ryzenYears[series]; ok {
				r.Year = y
			}
			r.Win11 = triOf(series >= 2)
		case cpuAthlon.MatchString(name):
			r.Gen = "Athlon"
			r.Win11 = triOf(cpuAthlonNew.MatchString(name))
		case cpuOldAMD.MatchString(name):
			r.Gen, r.Year, r.Win11 = "Starsza architektura", 2012, No
		case cpuEpyc.MatchString(name):
			r.Gen = "EPYC"
		}
	case cpuARM.MatchString(name):
		r.Vendor, r.Gen, r.Win11 = "ARM", "ARM64", Yes
	}
	return r
}

// ---------------------------------------------------------------- Windows Update: historia, ostatnia poprawka, ESU

var notOsPatchRx = rx(`KB2267602|KB4052623|KB890830|KB915597|KB2461484|KB2310138|KB5007651|KB4023057|KB5001716|KB5005463|Defender|Security Intelligence|analizy zabezpieczeń|Definition Update|usuwania złośliwego|Malicious Software Removal`)

// WuEntry - wpis historii Windows Update (daty w czasie lokalnym).
type WuEntry struct {
	Date   time.Time
	Title  string
	Result int64
	Hr     int64
	Op     int64
}

type wuHistoryResult struct {
	Entries []WuEntry
	Error   string
}

// wuHistory odpowiada Get-SdWuHistory (interfejs COM Windows Update zwraca daty w UTC).
func wuHistory(c *Ctx) *wuHistoryResult {
	return cached(c, "WuHistory", func() *wuHistoryResult {
		r := &wuHistoryResult{}
		err := func() error {
			s, err := win.CreateObject("Microsoft.Update.Session")
			if err != nil {
				return err
			}
			defer s.Release()
			srch, err := s.Obj("CreateUpdateSearcher")
			if err != nil {
				return err
			}
			defer srch.Release()
			nv, err := srch.Get("GetTotalHistoryCount")
			if err != nil {
				return err
			}
			n := win.AnyInt(nv)
			if n <= 0 {
				return nil
			}
			h, err := srch.Obj("QueryHistory", 0, int(min(n, 300)))
			if err != nil {
				return err
			}
			defer h.Release()
			return h.Items(func(e *win.Dispatch) {
				var d time.Time
				if t, ok := e.Value("Date").(time.Time); ok {
					d = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC).Local()
				}
				r.Entries = append(r.Entries, WuEntry{Date: d, Title: e.Str("Title"), Result: e.Int("ResultCode"), Hr: e.Int("HResult"), Op: e.Int("Operation")})
			})
		}()
		if err != nil {
			r.Error = err.Error()
			c.Log("WARN", "Windows Update COM: "+r.Error)
		}
		return r
	})
}

// parseQfeDate odpowiada ConvertFrom-SdQfeDate: "M/d/yyyy" niezależnie od języka, w starszych systemach FILETIME.
func parseQfeDate(s string) (time.Time, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return time.Time{}, false
	}
	if regexp.MustCompile(`^[0-9A-Fa-f]{16}$`).MatchString(t) {
		n, err := strconv.ParseInt(t, 16, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(0, (n-116444736000000000)*100).Local(), true
	}
	for _, f := range []string{"1/2/2006", "1/2/2006 15:04:05", "1/2/2006 3:04:05 PM", "20060102", "2006-01-02"} {
		if d, err := time.ParseInLocation(f, t, time.Local); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

var (
	kbRx      = rx(`\bKB\d{6,7}\b`)
	osPatchRx = rx(`Windows|\.NET Framework|\(KB\d{6,7}\)\s*\(\d{5}\.\d+\)`)
)

// isOsPatchTitle odpowiada Test-SdOsPatchTitle.
func isOsPatchTitle(title string) bool {
	return title != "" && kbRx.MatchString(title) && osPatchRx.MatchString(title) && !notOsPatchRx.MatchString(title)
}

// OsPatch - ostatnia poprawka systemu.
type OsPatch struct {
	Date   time.Time
	Title  string
	Source string
}

// lastOsPatch odpowiada Get-SdLastOsPatch: lista poprawek (QFE) i udane instalacje KB z historii WU.
func lastOsPatch(c *Ctx) *OsPatch {
	return cached(c, "LastOsPatch", func() *OsPatch {
		var best *OsPatch
		limit := time.Now().AddDate(0, 0, 1)
		for _, q := range c.cim("Win32_QuickFixEngineering", "HotFixID", "Description", "InstalledOn") {
			d, ok := parseQfeDate(q.Str("InstalledOn"))
			if ok && !d.After(limit) && (best == nil || d.After(best.Date)) {
				best = &OsPatch{Date: d, Title: joinNonEmpty(" - ", q.Str("HotFixID"), q.Str("Description")), Source: "QFE"}
			}
		}
		for _, e := range wuHistory(c).Entries {
			if e.Result != 2 || e.Op != 1 || e.Date.IsZero() || e.Date.After(limit) || !isOsPatchTitle(e.Title) {
				continue
			}
			if best == nil || e.Date.After(best.Date) {
				best = &OsPatch{Date: e.Date, Title: e.Title, Source: "WU"}
			}
		}
		return best
	})
}

// EsuState - rozszerzone aktualizacje zabezpieczeń (Get-SdEsuState).
type EsuState struct {
	Applies       bool
	LicenseYear   int
	LicenseUntil  time.Time
	ConsumerCode  int // -1 = brak danych
	ConsumerUntil time.Time
	UbrAfterEos   bool
	LastFreeUbr   int
	Ubr           int64
}

var sidUserRx = regexp.MustCompile(`^S-1-5-21-[\d-]+$`)

func esuState(c *Ctx) *EsuState {
	return cached(c, "Esu", func() *EsuState {
		w := winInfo(c)
		lc := data().lifecycle
		r := &EsuState{ConsumerCode: -1, Ubr: w.Ubr}
		e, ok := lc.Esu[strconv.FormatInt(w.Build, 10)]
		if !ok || w.IsLtsc || w.IsServer {
			return r
		}
		r.Applies = true
		r.LastFreeUbr = e.LastFreeUbr
		r.UbrAfterEos = w.Ubr > int64(e.LastFreeUbr)

		ids := make([]string, 0, len(e.ActivationIds))
		for id := range e.ActivationIds {
			ids = append(ids, "ID='"+id+"'")
		}
		sort.Strings(ids)
		filter := "(" + strings.Join(ids, " OR ") + ") AND PartialProductKey IS NOT NULL"
		for _, p := range c.Cim(Q{Class: "SoftwareLicensingProduct", Props: []string{"ID", "LicenseStatus"}, Where: filter}) {
			y := e.ActivationIds[strings.ToLower(p.Str("ID"))]
			if y > 0 && p.Int("LicenseStatus") == 1 && y > r.LicenseYear {
				r.LicenseYear = y
			}
		}
		if r.LicenseYear > 0 {
			r.LicenseUntil = parseDay(e.Years[strconv.Itoa(r.LicenseYear)])
		}

		sub := `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Windows\ConsumerESU`
		paths := []string{`HKCU\` + sub, `HKLM\` + sub}
		for _, k := range win.RegSubKeys(`HKU`) {
			if sidUserRx.MatchString(k) {
				paths = append(paths, `HKU\`+k+`\`+sub)
			}
		}
		for _, p := range paths {
			v, ok := win.RegValue(p, "ESUEligibility")
			if !ok {
				continue
			}
			n := int(win.AnyInt(v))
			if r.ConsumerCode == -1 || n == 3 || n == 5 {
				r.ConsumerCode = n
			}
			if n == 3 || n == 5 {
				break
			}
		}
		if r.ConsumerCode == 3 || r.ConsumerCode == 5 {
			r.ConsumerUntil = parseDay(e.Consumer)
		}
		return r
	})
}

// esuConsumerText odpowiada Get-SdEsuConsumerText.
func esuConsumerText(code int) string {
	switch code {
	case -1:
		return ""
	case 1:
		return "urządzenie nie kwalifikuje się"
	case 2:
		return "kwalifikuje się, ale nie jest zapisane"
	case 3:
		return "urządzenie zapisane"
	case 5:
		return "zapisane (konto Microsoft)"
	case 8:
		return "wymaga zalogowania kontem głównym, aby się zapisać"
	}
	return fmt.Sprintf("stan nieustalony (kod %d)", code)
}

// SupportStatus - ocena okresu wsparcia (Resolve-SdSupportStatus).
type SupportStatus struct {
	Status  Status
	Value   string
	Advice  string
	Covered bool
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func daysBetween(a, b time.Time) int {
	return int(math.Round(dayOf(a).Sub(dayOf(b)).Hours() / 24))
}

// resolveSupportStatus - funkcja czysta (testy jednostkowe) jak w wersji PowerShell.
func resolveSupportStatus(end, today time.Time, build int64, verName, latestWin11 string, esu *EsuState, lastPatch time.Time) SupportStatus {
	days := daysBetween(end, today)
	endTxt := fmtDate(end)
	r := SupportStatus{Status: OK, Covered: true}
	if days > 180 {
		r.Value = fmt.Sprintf("%s: wsparcie do %s", verName, endTxt)
		return r
	}
	if days >= 0 {
		r.Status = Warn
		r.Value = fmt.Sprintf("%s: wsparcie kończy się %s (za %d dni)", verName, endTxt, days)
		r.Advice = "Zaplanuj aktualizację systemu do nowszej wersji, zanim skończy się okres wsparcia."
		return r
	}
	past := -days
	migrate := "Zaktualizuj do Windows 11 (jeśli sprzęt spełnia wymagania - patrz test „Gotowość na Windows 11”) albo wymień komputer."

	if esu != nil && esu.Applies {
		var until time.Time
		program := ""
		if !esu.ConsumerUntil.IsZero() {
			until, program = esu.ConsumerUntil, "program konsumencki"
		}
		if !esu.LicenseUntil.IsZero() && (until.IsZero() || esu.LicenseUntil.After(until)) {
			until, program = esu.LicenseUntil, fmt.Sprintf("licencja ESU, rok %d", esu.LicenseYear)
		}
		sincePatch := math.MaxFloat64
		if !lastPatch.IsZero() {
			sincePatch = today.Sub(lastPatch).Hours() / 24
		}
		stale := sincePatch > 60
		base := fmt.Sprintf("%s: standardowe wsparcie zakończone %s", verName, endTxt)
		if !until.IsZero() && !dayOf(until).Before(dayOf(today)) {
			left := daysBetween(until, today)
			r.Value = fmt.Sprintf("%s; komputer jest objęty ESU (%s) do %s", base, program, fmtDate(until))
			r.Status = Info
			r.Advice = "ESU to rozwiązanie przejściowe - zaplanuj przejście na nowszy system przed " + fmtDate(until) + ". " + migrate
			switch {
			case !esu.UbrAfterEos:
				r.Status = Warn
				r.Value += ", ale nie zainstalowano jeszcze żadnej poprawki ESU"
				r.Advice = "Komputer jest zapisany do ESU, ale nie ma poprawek wydanych po zakończeniu wsparcia - uruchom Windows Update i zainstaluj aktualizacje zbiorcze. " + r.Advice
			case stale:
				r.Status = Warn
				r.Value += "; brak nowych poprawek od ponad 60 dni"
				r.Advice = "Sprawdź Windows Update - komputer objęty ESU powinien co miesiąc otrzymywać poprawki. " + r.Advice
			case left <= 90:
				r.Status = Warn
				r.Value += fmt.Sprintf(" (zostało %d dni)", left)
			}
			return r
		}
		if !until.IsZero() {
			r.Covered = false
			r.Value = fmt.Sprintf("%s; okres ESU (%s) zakończył się %s", base, program, fmtDate(until))
			r.Advice = "Komputer firmowy: zainstaluj klucz ESU na kolejny rok; domowy: sprawdź zapis do programu w Ustawieniach > Windows Update. " + migrate
			if esu.UbrAfterEos && sincePatch <= 45 {
				r.Status = Warn
				r.Value += fmt.Sprintf("; ostatnia poprawka: %s", fmtDate(lastPatch))
			} else {
				r.Status = Crit
				r.Advice = "Komputer nie otrzymuje już poprawek. " + r.Advice
			}
			return r
		}
		if esu.UbrAfterEos {
			r.Value = fmt.Sprintf("%s; zainstalowano poprawki ESU (kompilacja %d.%d)", base, build, esu.Ubr)
			if stale {
				r.Status = Warn
				r.Value += ", ale od ponad 60 dni brak nowych"
				r.Advice = "Komputer otrzymywał poprawki ESU, ale od dawna nie ma nowych - sprawdź zapis do programu ESU i Windows Update. " + migrate
			} else {
				r.Status = Info
				r.Advice = "Komputer otrzymuje rozszerzone aktualizacje zabezpieczeń (nie ustalono, w ramach którego programu). Zaplanuj przejście na nowszy system. " + migrate
			}
			return r
		}
	}

	r.Covered = false
	r.Status = Warn
	if past > 180 || build < 19045 {
		r.Status = Crit
	}
	r.Value = fmt.Sprintf("%s: wsparcie zakończone %s (%d dni temu)", verName, endTxt, past)
	switch {
	case build < 10240:
		r.Advice = "System nie otrzymuje aktualizacji bezpieczeństwa od lat. Zalecana instalacja nowszego systemu (Windows 10/11) albo wymiana komputera; do czasu migracji ogranicz korzystanie z internetu i poczty."
	case build < 22000:
		r.Advice = "Windows 10 nie otrzymuje już standardowych aktualizacji bezpieczeństwa. " + migrate
		if esu != nil && esu.Applies {
			r.Advice += " Na przejściowo: zapisz komputer do programu rozszerzonych aktualizacji (ESU) w Ustawieniach > Windows Update (użytkownicy domowi do 12.10.2027) lub kup klucz ESU dla firmy."
			if ct := esuConsumerText(esu.ConsumerCode); ct != "" {
				r.Value += "; ESU: " + ct
			}
		} else {
			r.Advice += " Ta kompilacja jest starsza niż 22H2 - najpierw zainstaluj aktualizację funkcji (program ESU obejmuje tylko 22H2)."
		}
	default:
		latest := latestWin11
		if latest == "" {
			latest = "najnowszej"
		}
		r.Advice = "Zaktualizuj Windows 11 do najnowszej wersji (" + latest + ") przez Windows Update - ta wersja nie otrzymuje już poprawek bezpieczeństwa."
	}
	return r
}

// SupportInfo - okres wsparcia bieżącej kompilacji (Get-SdSupportInfo); nil, gdy brak danych lub serwer.
type SupportInfo struct {
	EndDate   time.Time
	Name      string
	Note      string
	Esu       *EsuState
	LastPatch *OsPatch
	Result    SupportStatus
}

func supportInfo(c *Ctx) *SupportInfo {
	return cached(c, "Support", func() *SupportInfo {
		w := winInfo(c)
		lc := data().lifecycle
		if w.IsServer {
			return nil
		}
		key := strconv.FormatInt(w.Build, 10)
		var end time.Time
		name, note := "", ""
		if w.IsLtsc {
			m := lc.Ltsc
			if w.IsIot {
				m = lc.IotLtsc
			}
			if d, ok := m[key]; ok {
				end, name = parseDay(d), w.Caption
			}
		} else if b, ok := lc.Builds[key]; ok {
			name, note = b.Name, b.Note
			if w.IsEntEdu {
				end = parseDay(b.Ent)
			} else {
				end = parseDay(b.Home)
			}
		}
		if end.IsZero() {
			return nil
		}
		var esu *EsuState
		var last *OsPatch
		if end.Before(time.Now()) {
			esu = esuState(c)
			last = lastOsPatch(c)
		}
		var lastDate time.Time
		if last != nil {
			lastDate = last.Date
		}
		st := resolveSupportStatus(end, time.Now(), w.Build, name, lc.LatestWin11, esu, lastDate)
		return &SupportInfo{EndDate: end, Name: name, Note: note, Esu: esu, LastPatch: last, Result: st}
	})
}
