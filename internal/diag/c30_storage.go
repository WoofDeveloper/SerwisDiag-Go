package diag

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"serwisdiag/internal/win"
)

// Testy kategorii: Dyski (checks\30-storage.ps1)

var storageChecks = []*Check{
	{ID: "st.disks", Category: "storage", Title: "Dyski fizyczne i stan zdrowia (SMART)", Level: "Quick", Run: checkDisks},
	{ID: "st.volumes", Category: "storage", Title: "Wolumeny i wolne miejsce", Level: "Quick", Run: checkVolumes},
	{ID: "st.layout", Category: "storage", Title: "Partycje, kontroler dysku, TRIM", Level: "Quick", Run: checkLayout},
	{ID: "st.pagefile", Category: "storage", Title: "Plik wymiany i hibernacji", Level: "Quick", Run: checkPagefile},
	{ID: "st.cleanup", Category: "storage", Title: "Miejsce możliwe do odzyskania", Level: "Full", Run: checkCleanup},
}

var smartNames = map[int]string{
	1: "Raw Read Error Rate", 2: "Throughput Performance", 3: "Spin-Up Time", 4: "Start/Stop Count", 5: "Reallocated Sectors Count",
	7: "Seek Error Rate", 8: "Seek Time Performance", 9: "Power-On Hours", 10: "Spin Retry Count", 11: "Calibration Retry Count",
	12: "Power Cycle Count", 13: "Soft Read Error Rate", 168: "SATA PHY Error Count", 169: "Remaining Life / Bad Block Count",
	170: "Available Reserved Space", 171: "Program Fail Count", 172: "Erase Fail Count", 173: "Wear Leveling Count",
	174: "Unexpected Power Loss Count", 175: "Program Fail Count (Chip)", 176: "Erase Fail Count (Chip)", 177: "Wear Leveling Count",
	178: "Used Reserved Block Count (Chip)", 179: "Used Reserved Block Count (Total)", 180: "Unused Reserved Block Count",
	181: "Program Fail Count (Total)", 182: "Erase Fail Count", 183: "Runtime Bad Block / SATA Downshift", 184: "End-to-End Error",
	187: "Reported Uncorrectable Errors", 188: "Command Timeout", 189: "High Fly Writes", 190: "Airflow Temperature",
	191: "G-Sense Error Rate", 192: "Unsafe Shutdown Count", 193: "Load/Unload Cycle Count", 194: "Temperature",
	195: "Hardware ECC Recovered", 196: "Reallocation Event Count", 197: "Current Pending Sector Count",
	198: "Offline Uncorrectable Sector Count", 199: "UDMA CRC Error Count", 200: "Write Error Rate", 201: "Soft Read Error Rate",
	202: "Percent Lifetime Used", 206: "Flying Height", 209: "Remaining Life", 220: "Disk Shift", 222: "Loaded Hours",
	223: "Load/Unload Retry Count", 224: "Load Friction", 226: "Load-in Time", 230: "Drive Life Protection Status",
	231: "SSD Life Left", 232: "Available Reserved Space", 233: "Media Wearout Indicator", 234: "Vendor Specific",
	235: "Vendor Specific", 240: "Head Flying Hours", 241: "Total LBAs Written", 242: "Total LBAs Read", 250: "Read Error Retry Rate",
}

// issue - pojedyncze zastrzeżenie do dysku.
type issue struct {
	Status Status
	Text   string
}

func worstIssue(list []issue) Status {
	st := make([]Status, len(list))
	for i, x := range list {
		st[i] = x.Status
	}
	return worst(st...)
}

// ---------------------------------------------------------------- SMART ATA (HDD / SSD SATA) przez WMI

type smartAttr struct {
	ID, Current, Worst, Threshold int
	Name                          string
	Raw                           int64
}

// parseSmartData odpowiada ConvertFrom-SdSmartData: 2 bajty rewizji + do 30 wpisów po 12 bajtów.
func parseSmartData(data, thresholds []byte) []smartAttr {
	thr := map[int]int{}
	if len(thresholds) >= 14 {
		for i := 2; i <= len(thresholds)-12 && i < 362; i += 12 {
			if id := int(thresholds[i]); id != 0 {
				thr[id] = int(thresholds[i+1])
			}
		}
	}
	var list []smartAttr
	if len(data) >= 14 {
		for i := 2; i <= len(data)-12 && i < 362; i += 12 {
			id := int(data[i])
			if id == 0 {
				continue
			}
			var raw int64
			for b := 5; b >= 0; b-- {
				raw = raw*256 + int64(data[i+5+b])
			}
			name, ok := smartNames[id]
			if !ok {
				name = "Vendor Specific"
			}
			list = append(list, smartAttr{ID: id, Name: name, Current: int(data[i+3]), Worst: int(data[i+4]), Threshold: thr[id], Raw: raw})
		}
	}
	return list
}

type ataSmartInfo struct {
	PredictFailure bool
	Attributes     []smartAttr
}

// ataSmart odpowiada Get-SdAtaSmart: klucz = PNPDeviceID wielkimi literami (bez sufiksu _0).
func ataSmart(c *Ctx) map[string]*ataSmartInfo {
	res := map[string]*ataSmartInfo{}
	if !c.IsAdmin() {
		return res
	}
	st := c.Cim(Q{NS: `root\wmi`, Class: "MSStorageDriver_FailurePredictStatus", Props: []string{"InstanceName", "PredictFailure"}})
	if len(st) == 0 {
		return res
	}
	dt := c.Cim(Q{NS: `root\wmi`, Class: "MSStorageDriver_FailurePredictData", Props: []string{"InstanceName", "VendorSpecific"}})
	th := c.Cim(Q{NS: `root\wmi`, Class: "MSStorageDriver_FailurePredictThresholds", Props: []string{"InstanceName", "VendorSpecific"}})
	find := func(list []win.WmiObject, inst string) win.WmiObject {
		for _, x := range list {
			if eqi(x.Str("InstanceName"), inst) {
				return x
			}
		}
		return nil
	}
	suffix := regexp.MustCompile(`_\d+$`)
	for _, s := range st {
		inst := s.Str("InstanceName")
		var thrBytes []byte
		if t := find(th, inst); t != nil {
			thrBytes = t.Bytes("VendorSpecific")
		}
		var attrs []smartAttr
		if d := find(dt, inst); d != nil {
			attrs = parseSmartData(d.Bytes("VendorSpecific"), thrBytes)
		}
		res[strings.ToUpper(suffix.ReplaceAllString(inst, ""))] = &ataSmartInfo{PredictFailure: s.Bool("PredictFailure"), Attributes: attrs}
	}
	return res
}

type ataVerdictResult struct {
	Issues                              []issue
	Temp, Hours, LifeLeft               *int64
	Reallocated, Pending, Uncorrectable *int64
}

func i64p(v int64) *int64 { return &v }

// ataVerdict odpowiada Get-SdAtaVerdict.
func ataVerdict(sm *ataSmartInfo, isSsd bool) ataVerdictResult {
	a := map[int]smartAttr{}
	for _, x := range sm.Attributes {
		a[x.ID] = x
	}
	raw32 := func(id int) *int64 {
		if x, ok := a[id]; ok {
			return i64p(x.Raw & 0xFFFFFFFF)
		}
		return nil
	}
	var is []issue
	if sm.PredictFailure {
		is = append(is, issue{Crit, "SMART zgłasza przewidywaną awarię dysku"})
	}
	re, pe, un, ru, crc, cto := raw32(5), raw32(197), raw32(198), raw32(187), raw32(199), raw32(188)
	if re != nil && *re > 0 {
		st := Warn
		if *re >= 100 {
			st = Crit
		}
		is = append(is, issue{st, fmt.Sprintf("Przeniesione (reallocated) sektory: %d", *re)})
	}
	if pe != nil && *pe > 0 {
		is = append(is, issue{Crit, fmt.Sprintf("Sektory oczekujące na przeniesienie (pending): %d", *pe)})
	}
	if un != nil && *un > 0 {
		is = append(is, issue{Crit, fmt.Sprintf("Niemożliwe do odczytania sektory (uncorrectable): %d", *un)})
	}
	if ru != nil && *ru > 0 {
		is = append(is, issue{Warn, fmt.Sprintf("Zgłoszone nieskorygowane błędy odczytu: %d", *ru)})
	}
	if crc != nil && *crc > 0 {
		is = append(is, issue{Warn, fmt.Sprintf("Błędy transmisji UDMA CRC: %d (sprawdź kabel SATA / złącza)", *crc)})
	}
	if cto != nil && *cto > 20 {
		is = append(is, issue{Warn, fmt.Sprintf("Przekroczenia czasu poleceń: %d", *cto)})
	}
	critIDs := []int{5, 10, 184, 187, 196, 197, 198, 231, 233}
	for _, x := range sm.Attributes {
		if x.Threshold > 0 && x.Current <= x.Threshold && slices.Contains(critIDs, x.ID) {
			is = append(is, issue{Crit, fmt.Sprintf("Atrybut %d (%s) osiągnął próg SMART", x.ID, x.Name)})
		}
	}
	var temp *int64
	for _, id := range []int{194, 190} {
		if x, ok := a[id]; ok && temp == nil {
			if t := x.Raw & 0xFF; t > 0 && t < 120 {
				temp = i64p(t)
			}
		}
	}
	hours := raw32(9)
	var life *int64
	for _, id := range []int{231, 233, 169, 173, 177} {
		if x, ok := a[id]; ok && life == nil && isSsd {
			if x.Current > 0 && x.Current <= 100 {
				life = i64p(int64(x.Current))
			}
		}
	}
	if x, ok := a[202]; ok && life == nil && isSsd {
		if u := x.Raw & 0xFF; u <= 100 {
			life = i64p(100 - u)
		}
	}
	if life != nil {
		if *life <= 10 {
			is = append(is, issue{Crit, fmt.Sprintf("Szacowany pozostały czas życia SSD: %d%%", *life)})
		} else if *life <= 30 {
			is = append(is, issue{Warn, fmt.Sprintf("Szacowany pozostały czas życia SSD: %d%%", *life)})
		}
	}
	if temp != nil {
		tw, tc := int64(55), int64(60)
		if isSsd {
			tw, tc = 70, 80
		}
		if *temp >= tc {
			is = append(is, issue{Crit, fmt.Sprintf("Temperatura dysku: %d °C", *temp)})
		} else if *temp >= tw {
			is = append(is, issue{Warn, fmt.Sprintf("Temperatura dysku: %d °C", *temp)})
		}
	}
	if !isSsd && hours != nil && *hours >= 40000 {
		is = append(is, issue{Warn, fmt.Sprintf("Dysk pracował %s godzin (ponad %d lat) - zwiększone ryzyko awarii", fmtNum(float64(*hours), 0), *hours/8760)})
	}
	return ataVerdictResult{Issues: is, Temp: temp, Hours: hours, LifeLeft: life, Reallocated: re, Pending: pe, Uncorrectable: un}
}

// ---------------------------------------------------------------- NVMe (dziennik zdrowia)

type nvmeHealth struct {
	CriticalWarning, AvailableSpare, SpareThreshold, PercentUsed   int
	Temperature                                                    *int64
	BytesRead, BytesWritten                                        float64
	PowerCycles, PowerOnHours, UnsafeShutdowns                     int64
	MediaErrors, ErrorLogEntries, WarnTempMinutes, CritTempMinutes int64
}

// parseNvmeLog odpowiada ConvertFrom-SdNvmeLog (Log Page 02h).
func parseNvmeLog(l []byte) nvmeHealth {
	k := binary.LittleEndian.Uint16(l[1:])
	var temp *int64
	if k > 273 {
		temp = i64p(int64(k) - 273)
	}
	u64 := func(o int) uint64 { return binary.LittleEndian.Uint64(l[o:]) }
	return nvmeHealth{
		CriticalWarning: int(l[0]), Temperature: temp, AvailableSpare: int(l[3]), SpareThreshold: int(l[4]), PercentUsed: int(l[5]),
		BytesRead: float64(u64(32)) * 512000, BytesWritten: float64(u64(48)) * 512000,
		PowerCycles: int64(u64(112)), PowerOnHours: int64(u64(128)), UnsafeShutdowns: int64(u64(144)),
		MediaErrors: int64(u64(160)), ErrorLogEntries: int64(u64(176)),
		WarnTempMinutes: int64(binary.LittleEndian.Uint32(l[192:])), CritTempMinutes: int64(binary.LittleEndian.Uint32(l[196:])),
	}
}

// nvmeVerdict odpowiada Get-SdNvmeVerdict.
func nvmeVerdict(h nvmeHealth) []issue {
	var is []issue
	if h.CriticalWarning != 0 {
		var w []string
		for bit, t := range []string{"zapas bloków poniżej progu", "temperatura poza zakresem", "niezawodność pogorszona", "dysk w trybie tylko do odczytu", "awaria pamięci podręcznej"} {
			if h.CriticalWarning&(1<<bit) != 0 {
				w = append(w, t)
			}
		}
		is = append(is, issue{Crit, "Ostrzeżenie krytyczne dysku NVMe: " + strings.Join(w, ", ")})
	}
	if h.AvailableSpare <= h.SpareThreshold && h.CriticalWarning == 0 {
		is = append(is, issue{Crit, fmt.Sprintf("Zapas zapasowych bloków (%d%%) osiągnął próg", h.AvailableSpare)})
	} else if h.AvailableSpare < 50 {
		is = append(is, issue{Warn, fmt.Sprintf("Mały zapas zapasowych bloków: %d%%", h.AvailableSpare)})
	}
	if h.PercentUsed >= 90 {
		is = append(is, issue{Crit, fmt.Sprintf("Zużycie SSD: %d%% deklarowanej wytrzymałości", h.PercentUsed)})
	} else if h.PercentUsed >= 70 {
		is = append(is, issue{Warn, fmt.Sprintf("Zużycie SSD: %d%% deklarowanej wytrzymałości", h.PercentUsed)})
	}
	if h.MediaErrors >= 10 {
		is = append(is, issue{Crit, fmt.Sprintf("Błędy nośnika i integralności danych: %d", h.MediaErrors)})
	} else if h.MediaErrors > 0 {
		is = append(is, issue{Warn, fmt.Sprintf("Błędy nośnika i integralności danych: %d", h.MediaErrors)})
	}
	if h.Temperature != nil {
		if *h.Temperature >= 80 {
			is = append(is, issue{Crit, fmt.Sprintf("Temperatura dysku: %d °C", *h.Temperature)})
		} else if *h.Temperature >= 70 {
			is = append(is, issue{Warn, fmt.Sprintf("Temperatura dysku: %d °C", *h.Temperature)})
		}
	}
	if h.CritTempMinutes > 0 {
		is = append(is, issue{Warn, fmt.Sprintf("Dysk spędził %d min w temperaturze krytycznej (przegrzewanie)", h.CritTempMinutes)})
	}
	return is
}

// ---------------------------------------------------------------- inwentarz dysków

var (
	busTypes = map[int64]string{0: "Unknown", 1: "SCSI", 2: "ATAPI", 3: "ATA", 4: "1394", 5: "SSA", 6: "Fibre Channel", 7: "USB", 8: "RAID", 9: "iSCSI",
		10: "SAS", 11: "SATA", 12: "SD", 13: "MMC", 14: "Virtual", 15: "File Backed Virtual", 16: "Spaces", 17: "NVMe", 18: "SCM", 19: "UFS"}
	mediaTypes  = map[int64]string{0: "Unspecified", 3: "HDD", 4: "SSD", 5: "SCM"}
	healthNames = map[int64]string{0: "Healthy", 1: "Warning", 2: "Unhealthy", 5: "Unknown"}
	opStatus    = map[int64]string{1: "Other", 2: "OK", 3: "Degraded", 4: "Stressed", 5: "Predictive Failure", 6: "Error", 7: "Non-Recoverable Error",
		8: "Starting", 9: "Stopping", 10: "Stopped", 11: "In Service", 12: "No Contact", 13: "Lost Communication", 14: "Aborted", 15: "Dormant",
		16: "Supporting Entity in Error", 17: "Completed", 18: "Power Mode", 19: "Relocating", 53252: "Failed Media", 53253: "Split",
		53254: "Stale Metadata", 53255: "IO Error", 53256: "Unrecognized Metadata", 53269: "Removing From Pool", 53270: "In Maintenance Mode",
		53271: "Updating Firmware", 53272: "Device Hardware Error", 53273: "Not Usable", 53274: "Transient Error", 53276: "Starting Maintenance Mode",
		53277: "Stopping Maintenance Mode", 53285: "Threshold Exceeded", 53286: "Abnormal Latency"}
	partStyles = map[int64]string{0: "RAW", 1: "MBR", 2: "GPT"}
)

func enumName(m map[int64]string, v int64, def string) string {
	if s, ok := m[v]; ok {
		return s
	}
	return def
}

// Disk - dysk fizyczny (Get-SdDiskInventory).
type Disk struct {
	Index       int64
	Model       string
	Serial      string
	Size        float64
	Iface       string
	Firmware    string
	Pnp         string
	WmiStatus   string
	Partitions  int64
	Bus         string
	Media       string
	Health      string
	Operational string
	PartStyle   string
	IsSystem    bool
	pdPath      string // __RELPATH obiektu MSFT_PhysicalDisk (liczniki niezawodności)
}

func diskInventory(c *Ctx) []*Disk {
	return cached(c, "Disks", func() []*Disk {
		drives := c.cim("Win32_DiskDrive", "Index", "Model", "SerialNumber", "Size", "InterfaceType", "FirmwareRevision", "PNPDeviceID", "Status", "Partitions")
		const stor = `root\Microsoft\Windows\Storage`
		pds := c.Cim(Q{NS: stor, Class: "MSFT_PhysicalDisk", Props: []string{"__RELPATH", "DeviceId", "BusType", "MediaType", "HealthStatus", "OperationalStatus"}})
		gds := c.Cim(Q{NS: stor, Class: "MSFT_Disk", Props: []string{"Number", "PartitionStyle", "IsSystem"}})
		var out []*Disk
		for _, d := range drives {
			idx := d.Int("Index")
			disk := &Disk{Index: idx, Model: strings.TrimSpace(spacesRx.ReplaceAllString(d.Str("Model"), " ")), Serial: strings.TrimSpace(d.Str("SerialNumber")),
				Size: d.Float("Size"), Iface: d.Str("InterfaceType"), Firmware: d.Str("FirmwareRevision"), Pnp: d.Str("PNPDeviceID"),
				WmiStatus: d.Str("Status"), Partitions: d.Int("Partitions")}
			for _, pd := range pds {
				if pd.Str("DeviceId") == fmt.Sprint(idx) {
					disk.Bus = enumName(busTypes, pd.Int("BusType"), "Unknown")
					disk.Media = enumName(mediaTypes, pd.Int("MediaType"), "Unspecified")
					disk.Health = enumName(healthNames, pd.Int("HealthStatus"), "Unknown")
					var ops []string
					for _, o := range pd.Ints("OperationalStatus") {
						ops = append(ops, enumName(opStatus, o, "Unknown"))
					}
					disk.Operational = strings.Join(ops, ",")
					disk.pdPath = pd.Str("__RELPATH")
					break
				}
			}
			for _, gd := range gds {
				if gd.Int("Number") == idx {
					disk.PartStyle = enumName(partStyles, gd.Int("PartitionStyle"), "RAW")
					disk.IsSystem = gd.Bool("IsSystem")
					break
				}
			}
			out = append(out, disk)
		}
		return out
	})
}

var (
	nvmeRx    = rx(`NVME`)
	ssdNameRx = rx(`SSD|Solid State|NVMe`)
	vdiskRx   = rx(`Virtual|VMware|VBOX|QEMU`)
)

// diskTypeName odpowiada Get-SdDiskTypeName.
func diskTypeName(d *Disk) string {
	if eqi(d.Bus, "NVMe") || nvmeRx.MatchString(d.Pnp) {
		return "NVMe SSD"
	}
	pre := ""
	if eqi(d.Bus, "USB") || eqi(d.Iface, "USB") {
		pre = "USB "
	}
	switch {
	case eqi(d.Media, "SSD"):
		return pre + "SSD"
	case eqi(d.Media, "HDD"):
		return pre + "HDD"
	case ssdNameRx.MatchString(d.Model):
		return pre + "SSD"
	case vdiskRx.MatchString(d.Model):
		return "Dysk wirtualny"
	}
	return pre + "dysk"
}

var opBadRx = rx(`Predictive|Lost Communication|IO Error|Failed|Stopping|Not Ready`)

func checkDisks(c *Ctx) {
	disks := diskInventory(c)
	if len(disks) == 0 {
		c.Add(F{Title: "Dyski fizyczne", Status: Err, Value: "Nie wykryto żadnego dysku (WMI).",
			Advice: "Jeśli system działa, sprawdź w BIOS/UEFI, czy dysk jest widoczny, oraz tryb kontrolera (AHCI/RAID)."})
		return
	}
	ata := ataSmart(c)
	vm := isVM(c)
	var tableRows []Row
	var metaParts, serialLines []string
	for _, d := range disks {
		typ := diskTypeName(d)
		isSsd := strings.Contains(strings.ToUpper(typ), "SSD")
		sizeTxt := fmtSize(d.Size, 0)
		metaParts = append(metaParts, typ+" "+sizeTxt)
		var is []issue
		style := d.PartStyle
		if style == "" {
			style = "-"
		}
		fw := d.Firmware
		if fw == "" {
			fw = "-"
		}
		lines := []string{
			"Model: " + d.Model,
			"Typ: " + typ + "; interfejs: " + joinNonEmpty(" / ", d.Bus, d.Iface),
			fmt.Sprintf("Pojemność: %s; partycji: %d; styl: %s", sizeTxt, d.Partitions, style),
			"Firmware: " + fw,
		}
		if d.Serial != "" {
			serialLines = append(serialLines, fmt.Sprintf("Dysk %d (%s): %s", d.Index, d.Model, d.Serial))
		}
		// 1) stan wg systemu (Storage / WMI)
		if eqi(d.Health, "Unhealthy") {
			is = append(is, issue{Crit, "System Windows ocenia dysk jako niesprawny (Unhealthy)"})
		} else if eqi(d.Health, "Warning") {
			is = append(is, issue{Warn, "System Windows zgłasza ostrzeżenie dla dysku (Warning)"})
		}
		if opBadRx.MatchString(d.Operational) {
			is = append(is, issue{Crit, "Stan operacyjny: " + d.Operational})
		}
		if eqi(d.WmiStatus, "Pred Fail") {
			is = append(is, issue{Crit, "Dysk przewiduje awarię (Pred Fail)"})
		}
		var detailRows, smartRows []Row
		tempTxt, wearTxt := "", ""
		deep := false

		// 2) NVMe
		if typ == "NVMe SSD" {
			if log, code := win.NvmeHealthLog(int(d.Index)); log != nil {
				h := parseNvmeLog(log)
				deep = true
				is = append(is, nvmeVerdict(h)...)
				tbw := h.BytesWritten / 1e12
				lines = append(lines,
					fmt.Sprintf("NVMe: zużycie %d%%, zapas bloków %d%% (próg %d%%)", h.PercentUsed, h.AvailableSpare, h.SpareThreshold),
					fmt.Sprintf("Zapisano łącznie: %s TB; odczytano: %s TB", fmtNum(tbw, 1), fmtNum(h.BytesRead/1e12, 1)),
					fmt.Sprintf("Godziny pracy: %s; cykle zasilania: %s; nieczyste wyłączenia: %s", fmtNum(float64(h.PowerOnHours), 0), fmtNum(float64(h.PowerCycles), 0), fmtNum(float64(h.UnsafeShutdowns), 0)),
					fmt.Sprintf("Błędy nośnika: %d; wpisy w dzienniku błędów: %d", h.MediaErrors, h.ErrorLogEntries))
				tempVal := "-"
				if h.Temperature != nil {
					tempTxt = fmt.Sprintf("%d °C", *h.Temperature)
					tempVal = tempTxt
				}
				wearTxt = fmt.Sprintf("zużycie %d%%, zapisano %s TB", h.PercentUsed, fmtNum(tbw, 1))
				detailRows = []Row{
					row("Parametr", "Ostrzeżenie krytyczne", "Wartość", int64(h.CriticalWarning)),
					row("Parametr", "Temperatura złożona", "Wartość", tempVal),
					row("Parametr", "Zapas zapasowych bloków", "Wartość", fmt.Sprintf("%d%% (próg %d%%)", h.AvailableSpare, h.SpareThreshold)),
					row("Parametr", "Zużycie (Percentage Used)", "Wartość", fmt.Sprintf("%d%%", h.PercentUsed)),
					row("Parametr", "Zapisano łącznie", "Wartość", fmtNum(tbw, 2)+" TB"),
					row("Parametr", "Odczytano łącznie", "Wartość", fmtNum(h.BytesRead/1e12, 2)+" TB"),
					row("Parametr", "Godziny pracy", "Wartość", fmtNum(float64(h.PowerOnHours), 0)),
					row("Parametr", "Cykle zasilania", "Wartość", fmtNum(float64(h.PowerCycles), 0)),
					row("Parametr", "Nieczyste wyłączenia", "Wartość", fmtNum(float64(h.UnsafeShutdowns), 0)),
					row("Parametr", "Błędy nośnika i integralności", "Wartość", fmtNum(float64(h.MediaErrors), 0)),
					row("Parametr", "Wpisy w dzienniku błędów", "Wartość", fmtNum(float64(h.ErrorLogEntries), 0)),
					row("Parametr", "Czas w temp. ostrzegawczej / krytycznej (min)", "Wartość", fmt.Sprintf("%d / %d", h.WarnTempMinutes, h.CritTempMinutes)),
				}
			} else {
				c.Log("INFO", fmt.Sprintf("NVMe dysk %d: brak dziennika zdrowia (Win32 %d)", d.Index, code))
			}
		}

		// 3) SATA / HDD - atrybuty SMART (wymaga administratora)
		if sm, ok := ata[strings.ToUpper(d.Pnp)]; !deep && ok {
			deep = true
			v := ataVerdict(sm, isSsd)
			is = append(is, v.Issues...)
			if v.Temp != nil {
				tempTxt = fmt.Sprintf("%d °C", *v.Temp)
				lines = append(lines, "Temperatura: "+tempTxt)
			}
			if v.Hours != nil {
				lines = append(lines, "Godziny pracy: "+fmtNum(float64(*v.Hours), 0))
			}
			if v.LifeLeft != nil {
				wearTxt = fmt.Sprintf("pozostało ok. %d%%", *v.LifeLeft)
				lines = append(lines, fmt.Sprintf("Szacowany pozostały czas życia SSD: %d%%", *v.LifeLeft))
			}
			ns := func(p *int64) string {
				if p == nil {
					return "-"
				}
				return fmt.Sprint(*p)
			}
			lines = append(lines, fmt.Sprintf("Sektory: przeniesione %s, oczekujące %s, niemożliwe do odczytu %s", ns(v.Reallocated), ns(v.Pending), ns(v.Uncorrectable)))
			for _, x := range sm.Attributes {
				flag := ""
				if x.Threshold > 0 && x.Current <= x.Threshold {
					flag = "PRÓG PRZEKROCZONY"
				} else if slices.Contains([]int{5, 197, 198, 187}, x.ID) && x.Raw&0xFFFFFFFF > 0 {
					flag = "UWAGA"
				}
				smartRows = append(smartRows, row("ID", int64(x.ID), "Atrybut", x.Name, "Wartość", int64(x.Current), "Najgorsza", int64(x.Worst), "Próg", int64(x.Threshold), "Surowa", x.Raw, "Ocena", flag))
			}
		}

		// 4) liczniki niezawodności Storage (administrator) - jeśli nic głębszego nie mamy
		if !deep && c.IsAdmin() && d.pdPath != "" {
			rc := c.Cim(Q{NS: `root\Microsoft\Windows\Storage`, Class: "MSFT_StorageReliabilityCounter",
				WQL:   "ASSOCIATORS OF {" + d.pdPath + "} WHERE ResultClass = MSFT_StorageReliabilityCounter",
				Props: []string{"Temperature", "TemperatureMax", "PowerOnHours", "Wear", "ReadErrorsUncorrected", "WriteErrorsUncorrected"}})
			if len(rc) > 0 {
				r := rc[0]
				deep = true
				if r.Int("Temperature") != 0 {
					tempTxt = fmt.Sprintf("%d °C", r.Int("Temperature"))
					lines = append(lines, fmt.Sprintf("Temperatura: %s (maks. %d °C)", tempTxt, r.Int("TemperatureMax")))
				}
				if r.Int("PowerOnHours") != 0 {
					lines = append(lines, "Godziny pracy: "+fmtNum(r.Float("PowerOnHours"), 0))
				}
				if r.Has("Wear") {
					wear := r.Int("Wear")
					wearTxt = fmt.Sprintf("zużycie %d%%", wear)
					lines = append(lines, fmt.Sprintf("Zużycie: %d%%", wear))
					if wear >= 90 {
						is = append(is, issue{Crit, fmt.Sprintf("Zużycie SSD: %d%%", wear)})
					} else if wear >= 70 {
						is = append(is, issue{Warn, fmt.Sprintf("Zużycie SSD: %d%%", wear)})
					}
				}
				if n := r.Int("ReadErrorsUncorrected"); n > 0 {
					is = append(is, issue{Crit, fmt.Sprintf("Nieskorygowane błędy odczytu: %d", n)})
				}
				if n := r.Int("WriteErrorsUncorrected"); n > 0 {
					is = append(is, issue{Crit, fmt.Sprintf("Nieskorygowane błędy zapisu: %d", n)})
				}
				if t := r.Int("Temperature"); t != 0 && !isSsd && t >= 55 {
					st := Warn
					if t >= 60 {
						st = Crit
					}
					is = append(is, issue{st, fmt.Sprintf("Temperatura dysku: %d °C", t)})
				}
			}
		}
		if !deep && !vm {
			switch {
			case eqi(d.Bus, "USB") || eqi(d.Iface, "USB"):
				lines = append(lines, "Dysk USB: szczegółowe dane SMART zwykle nie są dostępne przez mostek USB.")
			case !c.IsAdmin():
				lines = append(lines, "Szczegółowe dane SMART wymagają uruchomienia programu jako administrator.")
			default:
				lines = append(lines, "Sterownik nie udostępnia szczegółowych danych SMART.")
			}
		}

		status := worstIssue(is)
		if len(is) == 0 {
			status = OK
		}
		sorted := slices.Clone(is)
		psSort(sorted, func(a, b issue) int { return b.Status.Rank() - a.Status.Rank() })
		var issueTxt []string
		for _, x := range sorted {
			issueTxt = append(issueTxt, x.Text)
		}
		value := joinNonEmpty(", ", typ, sizeTxt, tempTxt, wearTxt)
		if len(is) > 0 {
			value += " — " + strings.Join(issueTxt, "; ")
		} else if deep {
			value += " — stan dobry"
		}
		advice := ""
		switch status {
		case Crit:
			advice = "Natychmiast wykonaj kopię zapasową danych i wymień dysk. Nie uruchamiaj chkdsk /r na uszkodzonym dysku przed skopiowaniem danych - może pogorszyć sytuację."
		case Warn:
			advice = "Wykonaj kopię zapasową ważnych danych i obserwuj dysk; przy wzroście liczby błędów lub spadku wydajności zaplanuj jego wymianę."
		}
		if vm {
			status, advice = Info, ""
		}
		// ocena dla testów obciążeniowych (tt.disk nie obciąża dysku z objawami awarii)
		ds := OK
		if len(is) > 0 {
			ds = worstIssue(is)
		}
		c.s.mu.Lock()
		c.s.diskStatus[int(d.Index)] = ds
		c.s.mu.Unlock()
		c.Add(F{Title: fmt.Sprintf("Dysk %d: %s", d.Index, d.Model), Status: status, Value: value, Details: strings.Join(lines, "\n"), Advice: advice})
		if detailRows != nil {
			c.AddTable(T{Name: fmt.Sprintf("Zdrowie NVMe - dysk %d", d.Index), Rows: detailRows})
		}
		if smartRows != nil {
			c.AddTable(T{Name: fmt.Sprintf("Atrybuty SMART - dysk %d", d.Index), Rows: smartRows})
		}
		stan := d.Health
		if stan == "" {
			stan = d.WmiStatus
		}
		tableRows = append(tableRows, row("Nr", d.Index, "Model", d.Model, "Typ", typ, "Pojemność", sizeTxt, "Interfejs", joinNonEmpty(" / ", d.Bus, d.Iface),
			"Firmware", d.Firmware, "Styl partycji", d.PartStyle, "Stan", stan))
	}
	c.SetMeta("Disks", strings.Join(metaParts, "; "))
	c.AddTable(T{Name: "Dyski fizyczne", Rows: tableRows})
	if len(serialLines) > 0 {
		c.Add(F{Title: "Numery seryjne dysków", Status: Info, Value: strings.Join(serialLines, "; "), Private: true})
	}
}

func checkVolumes(c *Ctx) {
	type vol struct {
		letter, label, fs   string
		capacity, free      float64
		dirty, sysVol, boot bool
	}
	var vols []vol
	for _, v := range c.Cim(Q{Class: "Win32_Volume", Props: []string{"DriveLetter", "Label", "FileSystem", "Capacity", "FreeSpace", "DirtyBitSet", "SystemVolume", "BootVolume"}, Where: "DriveType=3"}) {
		vols = append(vols, vol{v.Str("DriveLetter"), v.Str("Label"), v.Str("FileSystem"), v.Float("Capacity"), v.Float("FreeSpace"), v.Bool("DirtyBitSet"), v.Bool("SystemVolume"), v.Bool("BootVolume")})
	}
	if len(vols) == 0 {
		for _, l := range c.Cim(Q{Class: "Win32_LogicalDisk", Props: []string{"DeviceID", "VolumeName", "FileSystem", "Size", "FreeSpace"}, Where: "DriveType=3"}) {
			vols = append(vols, vol{letter: l.Str("DeviceID"), label: l.Str("VolumeName"), fs: l.Str("FileSystem"), capacity: l.Float("Size"), free: l.Float("FreeSpace")})
		}
	}
	key := func(v vol) string {
		if v.letter != "" {
			return v.letter
		}
		return "ZZ"
	}
	psSort(vols, func(a, b vol) int { return cmpText(key(a), key(b)) })
	var rows []Row
	sd := sysDrive()
	for _, v := range vols {
		if v.capacity == 0 {
			continue
		}
		pct := 100 * v.free / v.capacity
		name := v.letter
		if name == "" {
			name = "(bez litery)"
		}
		var uw []string
		if v.sysVol {
			uw = append(uw, "systemowy (rozruch)")
		}
		if v.boot {
			uw = append(uw, "zawiera Windows")
		}
		rows = append(rows, row("Wolumin", name, "Etykieta", v.label, "System plików", v.fs, "Pojemność", fmtSize(v.capacity, 1), "Wolne", fmtSize(v.free, 1),
			"Wolne %", fmtNum(pct, 1), "Uwagi", joinNonEmpty(", ", uw...)))
		if v.letter == "" {
			continue // partycje techniczne (EFI, Recovery) oceniamy tylko w tabeli
		}
		isSys := eqi(v.letter, sd)
		freeGb := v.free / (1 << 30)
		status, adv := OK, ""
		if isSys {
			if pct < 5 || freeGb < 3 {
				status = Crit
			} else if pct < 15 || freeGb < 10 {
				status = Warn
			}
		} else {
			if pct < 2 {
				status = Crit
			} else if pct < 10 {
				status = Warn
			}
		}
		if status != OK {
			adv = "Zwolnij miejsce: Oczyszczanie dysku (cleanmgr), usunięcie niepotrzebnych plików i programów, przeniesienie danych na inny nośnik. Przy zapełnieniu dysku system i aktualizacje działają niestabilnie."
		}
		fs := v.fs
		if fs == "" {
			fs = "brak systemu plików"
		}
		val := fmt.Sprintf("%s wolne z %s (%s%%), %s", fmtSize(v.free, 1), fmtSize(v.capacity, 1), fmtNum(pct, 0), fs)
		var det []string
		if v.label != "" {
			det = append(det, "Etykieta: "+v.label)
		}
		if isSys {
			det = append(det, "Dysk systemowy.")
		}
		c.Add(F{Title: "Wolumin " + v.letter, Status: status, Value: val, Details: strings.Join(det, "\n"), Advice: adv})
		if v.dirty {
			c.Add(F{Title: "Wolumin " + v.letter + " - znacznik „brudny”", Status: Warn, Value: "System plików oznaczony jako wymagający sprawdzenia",
				Details: "Ustawiony bit „dirty” oznacza nieprawidłowe zamknięcie lub błędy systemu plików.",
				Advice:  "Zaplanuj sprawdzenie dysku (chkdsk) - po wcześniejszym skopiowaniu danych, jeśli dysk wykazuje oznaki awarii."})
		}
	}
	c.AddTable(T{Name: "Wolumeny", Rows: rows})
}

var (
	ideServiceRx = rx(`^(pciide|intelide|atapi|viaide|amdide)$`)
	atapiRx      = rx(`ATAPI`)
)

func checkLayout(c *Ctx) {
	disks := diskInventory(c)
	anySsd := false
	for _, d := range disks {
		if strings.Contains(strings.ToUpper(diskTypeName(d)), "SSD") {
			anySsd = true
		}
	}
	var mis []string
	for _, p := range c.cim("Win32_DiskPartition", "DiskIndex", "Index", "StartingOffset") {
		if p.Has("StartingOffset") && uint64(p.Int("StartingOffset"))%4096 != 0 {
			mis = append(mis, fmt.Sprintf("Dysk %d, partycja %d", p.Int("DiskIndex"), p.Int("Index")+1))
		}
	}
	if len(mis) > 0 {
		c.Add(F{Title: "Wyrównanie partycji", Status: Warn, Value: "Niewyrównane do 4 KB: " + strings.Join(mis, "; "),
			Details: "Partycje nie są wyrównane do granicy 4 KB, co obniża wydajność SSD i dysków Advanced Format.",
			Advice:  "Wyrównaj partycje narzędziem do partycjonowania (po wykonaniu kopii zapasowej)."})
	} else {
		c.Add(F{Title: "Wyrównanie partycji", Status: OK, Value: "Partycje wyrównane do 4 KB"})
	}

	var ctrl []win.WmiObject
	for _, p := range pnpObjs(c) {
		if eqi(p.Str("PNPClass"), "HDC") || eqi(p.Str("PNPClass"), "SCSIAdapter") {
			ctrl = append(ctrl, p)
		}
	}
	if len(ctrl) > 0 {
		var rows []Row
		var ide, bad []win.WmiObject
		for _, p := range ctrl {
			stan := "OK"
			if code := p.Int("ConfigManagerErrorCode"); code != 0 {
				stan = fmt.Sprintf("błąd %d", code)
				bad = append(bad, p)
			}
			rows = append(rows, row("Kontroler", p.Str("Name"), "Usługa", p.Str("Service"), "Producent", p.Str("Manufacturer"), "Stan", stan))
			if ideServiceRx.MatchString(p.Str("Service")) && !atapiRx.MatchString(p.Str("Name")) {
				ide = append(ide, p)
			}
		}
		c.AddTable(T{Name: "Kontrolery pamięci masowej", Rows: rows})
		switch {
		case len(ide) > 0 && anySsd:
			var n []string
			for _, p := range ide {
				n = append(n, p.Str("Name"))
			}
			c.Add(F{Title: "Tryb kontrolera dysku", Status: Warn, Value: "Kontroler działa w trybie IDE / kompatybilności", Details: strings.Join(n, "\n"),
				Advice: "SSD w trybie IDE traci wydajność i funkcje (TRIM, NCQ). Przełączenie na AHCI w BIOS wymaga wcześniejszego włączenia sterownika AHCI w Windows (tryb awaryjny), inaczej system się nie uruchomi."})
		case len(ide) > 0:
			c.Add(F{Title: "Tryb kontrolera dysku", Status: Info, Value: "Kontroler działa w trybie IDE (kompatybilności)",
				Advice: "Tryb AHCI zapewnia lepszą wydajność i obsługę nowoczesnych funkcji. Zmianę trybu należy zaplanować (patrz uwagi o sterowniku AHCI)."})
		default:
			var n []string
			for i, p := range ctrl {
				if i < 3 {
					n = append(n, p.Str("Name"))
				}
			}
			c.Add(F{Title: "Tryb kontrolera dysku", Status: OK, Value: strings.Join(n, "; ")})
		}
		if len(bad) > 0 {
			var b []string
			for _, p := range bad {
				b = append(b, fmt.Sprintf("%s (kod %d)", p.Str("Name"), p.Int("ConfigManagerErrorCode")))
			}
			c.Add(F{Title: "Kontroler dysku - błąd urządzenia", Status: Crit, Value: strings.Join(b, "; "),
				Advice: "Zainstaluj lub zaktualizuj sterownik kontrolera pamięci masowej (Intel RST / AMD / producent płyty)."})
		}
	}
	if anySsd {
		const fsKey = `HKLM\SYSTEM\CurrentControlSet\Control\FileSystem`
		if regInt(fsKey, "DisableDeleteNotification", 0) == 1 || regInt(fsKey, "NtfsDisableDeleteNotification", 0) == 1 {
			c.Add(F{Title: "TRIM dla SSD", Status: Warn, Value: "Wyłączony", Advice: "Włącz TRIM poleceniem: fsutil behavior set DisableDeleteNotify 0 (jako administrator)."})
		} else {
			c.Add(F{Title: "TRIM dla SSD", Status: OK, Value: "Włączony"})
		}
	}
}

func checkPagefile(c *Ctx) {
	cs := csObj(c)
	pf := c.cim("Win32_PageFileUsage", "Name", "AllocatedBaseSize", "CurrentUsage", "PeakUsage")
	auto := cs != nil && cs.Bool("AutomaticManagedPagefile")
	if len(pf) == 0 {
		c.Add(F{Title: "Plik wymiany (pagefile)", Status: Warn, Value: "Wyłączony lub nie wykryto",
			Advice: "Włącz plik wymiany zarządzany przez system (Zaawansowane ustawienia systemu > Wydajność). Jego brak powoduje błędy braku pamięci i uniemożliwia zrzuty awaryjne."})
	} else {
		var d []string
		worstPct := 0.0
		for _, p := range pf {
			alloc, peak := p.Float("AllocatedBaseSize"), p.Float("PeakUsage")
			pct := 0.0
			if alloc > 0 {
				pct = 100 * peak / alloc
			}
			if pct > worstPct {
				worstPct = pct
			}
			d = append(d, fmt.Sprintf("%s: przydzielono %s MB, bieżące użycie %d MB, szczyt %s MB (%s%%)", p.Str("Name"), fmtG(alloc), p.Int("CurrentUsage"), fmtG(peak), fmtNum(pct, 0)))
		}
		if auto {
			d = append(d, "Rozmiar zarządzany automatycznie przez system.")
		} else {
			d = append(d, "Rozmiar ustawiony ręcznie.")
		}
		if worstPct >= 90 {
			c.Add(F{Title: "Plik wymiany (pagefile)", Status: Warn, Value: fmt.Sprintf("Szczytowe użycie %s%% przydziału", fmtNum(worstPct, 0)), Details: strings.Join(d, "\n"),
				Advice: "Plik wymiany bywa wykorzystany niemal w całości - zwiększ jego rozmiar lub włącz zarządzanie automatyczne; rozważ dołożenie RAM."})
		} else {
			mode := "ręczny"
			if auto {
				mode = "automatyczny"
			}
			c.Add(F{Title: "Plik wymiany (pagefile)", Status: OK, Value: fmt.Sprintf("%s; szczytowe użycie %s%%", mode, fmtNum(worstPct, 0)), Details: strings.Join(d, "\n")})
		}
	}
	if size, ok := win.FileSize(filepath.Join(sysDrive()+`\`, "hiberfil.sys")); ok {
		c.Add(F{Title: "Plik hibernacji (hiberfil.sys)", Status: Info, Value: fmtSize(float64(size), 1),
			Details: "Plik zajmuje miejsce na dysku systemowym; jest wymagany dla hibernacji i szybkiego uruchamiania.",
			Advice:  "Jeśli brakuje miejsca na dysku, a hibernacja nie jest potrzebna: powercfg /h off (wyłącza też szybkie uruchamianie)."})
	}
}

// UserProfile - profil użytkownika (Win32_UserProfile bez profili specjalnych).
type UserProfile struct {
	LocalPath string
	SID       string
}

func userProfiles(c *Ctx) []UserProfile {
	return cached(c, "Profiles", func() []UserProfile {
		var out []UserProfile
		for _, p := range c.cim("Win32_UserProfile", "Special", "LocalPath", "SID") {
			if p.Bool("Special") || p.Str("LocalPath") == "" {
				continue
			}
			if _, err := os.Stat(p.Str("LocalPath")); err != nil {
				continue
			}
			out = append(out, UserProfile{LocalPath: p.Str("LocalPath"), SID: p.Str("SID")})
		}
		return out
	})
}

var firefoxRx = rx(`Firefox`)

func checkCleanup(c *Ctx) {
	sd := sysDrive()
	winDir := getenv("SystemRoot")
	type item struct {
		name, path string
		safe       bool
		note       string
	}
	items := []item{
		{"Pliki tymczasowe systemu", filepath.Join(winDir, "Temp"), true, "Bezpieczne do usunięcia"},
		{"Pamięć podręczna Windows Update", filepath.Join(winDir, `SoftwareDistribution\Download`), true, "Pobrane pakiety aktualizacji"},
		{"Raporty błędów Windows (WER)", filepath.Join(getenv("ProgramData"), `Microsoft\Windows\WER`), true, "Raporty awarii"},
		{"Poprzednia instalacja Windows (Windows.old)", sd + `\Windows.old`, false, "Umożliwia powrót do poprzedniej wersji; usuń przez Oczyszczanie dysku po upewnieniu się, że nie jest potrzebna"},
		{"Kosz", sd + `\$Recycle.Bin`, false, "Zawartość kosza użytkowników"},
	}
	for _, up := range userProfiles(c) {
		user := filepath.Base(up.LocalPath)
		items = append(items,
			item{"Temp użytkownika " + user, filepath.Join(up.LocalPath, `AppData\Local\Temp`), true, "Bezpieczne do usunięcia (poza otwartymi plikami)"},
			item{"Pamięć podręczna Chrome (" + user + ")", filepath.Join(up.LocalPath, `AppData\Local\Google\Chrome\User Data\Default\Cache`), true, "Cache przeglądarki"},
			item{"Pamięć podręczna Edge (" + user + ")", filepath.Join(up.LocalPath, `AppData\Local\Microsoft\Edge\User Data\Default\Cache`), true, "Cache przeglądarki"},
			item{"Pamięć podręczna Firefox (" + user + ")", filepath.Join(up.LocalPath, `AppData\Local\Mozilla\Firefox\Profiles`), true, "Cache przeglądarki (wraz z profilem - rozmiar zawyżony)"})
	}
	var rows []Row
	safeTotal, winOld := 0.0, 0.0
	for _, it := range items {
		if _, err := os.Lstat(it.path); err != nil {
			continue
		}
		b, files := win.DirSize(it.path)
		if b <= 0 {
			continue
		}
		if it.safe && !firefoxRx.MatchString(it.name) {
			safeTotal += float64(b)
		}
		if strings.Contains(it.name, "Windows.old") {
			winOld = float64(b)
		}
		bez := "sprawdź"
		if it.safe {
			bez = "tak"
		}
		rows = append(rows, row("Lokalizacja", it.name, "Rozmiar", fmtSize(float64(b), 1), "Liczba plików", files, "Bezpieczne", bez, "Ścieżka", it.path, "Uwagi", it.note))
	}
	if len(rows) == 0 {
		c.Add(F{Title: "Miejsce do odzyskania", Status: OK, Value: "Brak dużych zbędnych plików"})
		return
	}
	c.AddTable(T{Name: "Miejsce możliwe do odzyskania", Rows: rows})
	val := fmt.Sprintf("Około %s z plików tymczasowych i pamięci podręcznych", fmtSize(safeTotal, 1))
	if winOld > 0 {
		val += "; dodatkowo Windows.old: " + fmtSize(winOld, 1)
	}
	st := OK
	if safeTotal >= 5*(1<<30) || winOld >= 5*(1<<30) {
		st = Info
	}
	c.Add(F{Title: "Miejsce do odzyskania", Status: st, Value: val,
		Advice: "Wyczyść pliki tymczasowe (Oczyszczanie dysku / Ustawienia > System > Pamięć). Szczegóły w tabeli „Miejsce możliwe do odzyskania”."})
}

// fmtG: liczba jak w operatorze -f PowerShella bez formatu (pl-PL, do 15 cyfr, np. 2432 albo 22,8).
func fmtG(x float64) string {
	if x == math.Trunc(x) && math.Abs(x) < 1e15 {
		return fmt.Sprintf("%d", int64(x))
	}
	s := fmt.Sprintf("%.15g", x)
	s = strings.Replace(s, ".", ",", 1)
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		s = s[:i] + "E" + s[i+1:]
	}
	return s
}
