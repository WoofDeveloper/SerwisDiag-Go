package diag

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/win"
)

// Kopia przed reinstalacją Windows (lib\Backup.ps1): w jednym folderze (np. na pendrive) to, co zwykle ginie przy
// reinstalacji: sterowniki producentów, lista programów, klucz Windows z BIOS/UEFI i dane komputera, a na życzenie
// także profile Wi-Fi z hasłami i klucze odzyskiwania BitLocker. Program niczego w systemie przy tym nie zmienia.

// BackupPart - element kopii ($SdBackupParts).
type BackupPart struct {
	Key, Label, Description   string
	Default, Admin, Sensitive bool
}

// BackupParts - elementy kopii w kolejności wykonywania.
var BackupParts = []BackupPart{
	{"drivers", "Sterowniki producentów", "Eksport wszystkich sterowników spoza Windows z magazynu sterowników (karta graficzna, Wi-Fi, chipset, dźwięk..., także starsze wersje) - po reinstalacji wskażesz ten folder w Menedżerze urządzeń. Od kilkuset MB do kilku GB, kilka minut; program sprawdzi wcześniej miejsce na nośniku.", true, true, false},
	{"programs", "Lista zainstalowanych programów", "Co zainstalować ponownie (nazwa, wersja, producent) - HTML do wydruku i CSV.", true, false, false},
	{"system", "Dane komputera i klucz Windows", "Model, numer seryjny, podzespoły, wersja i aktywacja Windows, klucz zapisany w BIOS/UEFI, konta i drukarki.", true, false, true},
	{"wifi", "Profile Wi-Fi z hasłami", "Zapisane sieci Wi-Fi razem z hasłami (jawny tekst) - klient nie musi ich pamiętać. Usuń je po przywróceniu.", false, true, true},
	{"bitlocker", "Klucze odzyskiwania BitLocker", "Hasła odzyskiwania zaszyfrowanych dysków - potrzebne np. do odczytu dysku z danymi po wymianie płyty głównej. Dane bardzo wrażliwe.", false, true, true},
}

func backupPart(key string) BackupPart {
	for _, p := range BackupParts {
		if p.Key == key {
			return p
		}
	}
	return BackupPart{Key: key}
}

var includeSplitRx = rx(`[,;\s]+`)

// BackupIncludeList odpowiada Get-SdBackupIncludeList: "drivers,wifi" -> lista kluczy w kolejności elementów;
// pusta wartość -> elementy domyślne. Nieznane nazwy pomija.
func BackupIncludeList(text string) []string {
	var sel []string
	for _, s := range includeSplitRx.Split(text, -1) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" && slices.ContainsFunc(BackupParts, func(p BackupPart) bool { return p.Key == s }) {
			sel = append(sel, s)
		}
	}
	if len(sel) == 0 {
		for _, p := range BackupParts {
			if p.Default {
				sel = append(sel, p.Key)
			}
		}
	}
	var out []string
	for _, p := range BackupParts {
		if slices.Contains(sel, p.Key) {
			out = append(out, p.Key)
		}
	}
	return out
}

// BackupDirName odpowiada Get-SdBackupDirName: <KOMPUTER>_<data>_kopia (krótka - eksport sterowników podlega
// limitowi długości ścieżki).
func BackupDirName(now time.Time) string {
	return unsafeNameRx.ReplaceAllString(os.Getenv("COMPUTERNAME")+"_"+now.Format("20060102-1504")+"_kopia", "_")
}

// u16len: długość napisu w znakach UTF-16 (jak String.Length w .NET - limit ścieżek Windows).
func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

// DriverExportInfo - pakiety, które wyeksportuje DISM: liczba, łączny rozmiar i najdłuższa ścieżka pliku względem
// FileRepository (MaxRel) z nazwą jej pakietu.
type DriverExportInfo struct {
	Count   int
	Bytes   float64
	MaxRel  int
	Package string
}

var infSplitRx = rx(`\.inf_`)

// driverExportInfo odpowiada Get-SdDriverExportInfo: wszystkie sterowniki spoza Windows w magazynie (także starsze
// wersje). Pakiet producenta rozpoznajemy po tym, że jego plik INF ma kopię %windir%\INF\oemNN.inf (ten sam rozmiar
// i suma SHA-256) - bez administratora i w kilka sekund (Get-WindowsDriver daje to samo w ok. minutę).
func driverExportInfo() DriverExportInfo {
	root := os.Getenv("SystemRoot")
	fr := filepath.Join(root, `System32\DriverStore\FileRepository`)
	hash := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		s := sha256.Sum256(b)
		return string(s[:])
	}
	type oemInf struct {
		path, hash string
		done       bool
	}
	bySize := map[int64][]*oemInf{}
	if es, err := os.ReadDir(filepath.Join(root, "INF")); err == nil {
		for _, e := range es {
			n := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasPrefix(n, "oem") || !strings.HasSuffix(n, ".inf") {
				continue
			}
			if fi, err := e.Info(); err == nil {
				bySize[fi.Size()] = append(bySize[fi.Size()], &oemInf{path: filepath.Join(root, "INF", e.Name())})
			}
		}
	}
	var r DriverExportInfo
	dirs, _ := os.ReadDir(fr)
	frLen := u16len(fr)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		inf := filepath.Join(fr, d.Name(), infSplitRx.Split(d.Name(), 2)[0]+".inf")
		fi, err := os.Stat(inf)
		if err != nil || fi.IsDir() || len(bySize[fi.Size()]) == 0 {
			continue
		}
		h := hash(inf)
		hit := false
		for _, o := range bySize[fi.Size()] {
			if !o.done {
				o.hash, o.done = hash(o.path), true
			}
			if h != "" && o.hash == h {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		r.Count++
		_ = filepath.WalkDir(filepath.Join(fr, d.Name()), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if info, err := e.Info(); err == nil {
				r.Bytes += float64(info.Size())
			}
			if rel := u16len(p) - frLen - 1; rel > r.MaxRel {
				r.MaxRel, r.Package = rel, strings.SplitN(d.Name(), "_", 2)[0]
			}
			return nil
		})
	}
	return r
}

// DriverExportCheck - wynik TestDriverExport.
type DriverExportCheck struct {
	Ok      bool
	Need    int
	Bytes   float64
	Free    float64
	Message string
}

// TestDriverExport odpowiada Test-SdDriverExport: czy eksport sterowników zmieści się w folderze - limit długości
// ścieżki Windows (259 znaków) i wolne miejsce na nośniku. free < 0: odczytaj wolne miejsce dysku.
func TestDriverExport(driverDir string, info *DriverExportInfo, free float64) DriverExportCheck {
	if info == nil {
		i := driverExportInfo()
		info = &i
	}
	r := DriverExportCheck{Ok: true, Need: u16len(strings.TrimRight(driverDir, `\`)) + 1 + info.MaxRel, Bytes: info.Bytes, Free: free}
	if free < 0 {
		r.Free = -1
		if p, err := windows.UTF16PtrFromString(filepath.VolumeName(driverDir) + `\`); err == nil {
			var avail, total, totalFree uint64
			if windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree) == nil {
				r.Free = float64(avail)
			}
		}
	}
	pkg := info.Package
	if pkg == "" {
		pkg = "pakiet producenta"
	}
	switch {
	case r.Need > 259:
		r.Ok = false
		r.Message = fmt.Sprintf(`ścieżka folderu docelowego jest za długa: najdłuższy plik sterownika (%s) wymagałby %d znaków, a Windows pozwala na 259. Wybierz krótszy folder, np. główny katalog pendrive'a (E:\).`, pkg, r.Need)
	case r.Free >= 0 && r.Bytes > 0 && r.Free < r.Bytes*1.05+100*1024*1024:
		r.Ok = false
		r.Message = fmt.Sprintf("za mało miejsca: eksport zajmie ok. %s (%s, także starsze wersje sterowników), a na nośniku jest wolne %s.",
			fmtSize(r.Bytes, 1), fmtCount(info.Count, "pakiet", "pakiety", "pakietów"), fmtSize(r.Free, 1))
	}
	return r
}

// dismExportError odpowiada Get-SdDismExportError: kody wyjścia DISM przy eksporcie sterowników -> opis po polsku.
func dismExportError(code int, output string) string {
	switch code {
	case 206:
		return "nazwa pliku sterownika jest za długa dla tego folderu (limit 260 znaków) - wybierz krótszy folder, np. główny katalog pendrive'a"
	case 112:
		return "za mało miejsca na dysku docelowym"
	case 5:
		return "odmowa dostępu - uruchom program jako administrator i sprawdź, czy nośnik nie jest zabezpieczony przed zapisem"
	case 3:
		return "nie znaleziono folderu docelowego"
	case 19:
		return "nośnik jest zabezpieczony przed zapisem"
	}
	return fmt.Sprintf("DISM zakończył się kodem %s: %s", fmtHr(int64(code)), lastLine(output))
}

const blNamespace = `root\cimv2\Security\MicrosoftVolumeEncryption`

// bitLockerVolumes odpowiada Get-SdBitLockerVolumes: woluminy z włączoną ochroną BitLocker (wymaga administratora).
func bitLockerVolumes(c *Ctx) []win.WmiObject {
	if !win.IsAdmin() {
		return nil
	}
	var out []win.WmiObject
	for _, v := range c.Cim(Q{NS: blNamespace, Class: "Win32_EncryptableVolume", Props: []string{"DeviceID", "DriveLetter", "ProtectionStatus"}}) {
		if v.Int("ProtectionStatus") == 1 {
			out = append(out, v)
		}
	}
	return out
}

// wmiKeyPath: ścieżka obiektu WMI z kluczem tekstowym, np. Win32_EncryptableVolume.DeviceID="\\\\?\\Volume{...}\\".
func wmiKeyPath(class, key, value string) string {
	return class + "." + key + `="` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

// sizeOrDash: Format-Size wartości z WMI - brak wartości to „-”.
func sizeOrDash(o win.WmiObject, name string, digits int) string {
	if !o.Has(name) {
		return "-"
	}
	return fmtSize(o.Float(name), digits)
}

var licChannelRx = rx(`,\s*(\S+)\s+channel`)

// backupSystemText odpowiada Get-SdBackupSystemText: dane komputera do pliku system.txt.
func backupSystemText(c *Ctx) string {
	var L []string
	add := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			L = append(L, fmt.Sprintf("%-28s %s", k+":", v))
		}
	}
	cs, osObj, w := csObj(c), osObj(c), winInfo(c)
	bios, bb := c.cim1("Win32_BIOS"), c.cim1("Win32_BaseBoard")
	L = append(L, "KOMPUTER")
	add("Nazwa komputera", os.Getenv("COMPUTERNAME"))
	if cs != nil {
		add("Producent i model", joinNonEmpty(" ", cleanStr(cs.Str("Manufacturer"), ""), cleanStr(cs.Str("Model"), "")))
	}
	if bios != nil {
		add("Numer seryjny", cleanStr(bios.Str("SerialNumber"), ""))
		rd, _ := bios.Time("ReleaseDate")
		add("BIOS / UEFI", joinNonEmpty(" ", bios.Str("Manufacturer"), bios.Str("SMBIOSBIOSVersion"))+" ("+fmtDate(rd)+")")
	}
	if bb != nil {
		add("Płyta główna", joinNonEmpty(" ", cleanStr(bb.Str("Manufacturer"), ""), cleanStr(bb.Str("Product"), "")))
	}
	for _, p := range cpuObjs(c) {
		add("Procesor", strings.TrimSpace(p.Str("Name")))
	}
	if cs != nil && cs.Float("TotalPhysicalMemory") != 0 {
		add("Pamięć RAM", fmtSize(cs.Float("TotalPhysicalMemory"), 1))
	}
	for _, d := range c.cim("Win32_DiskDrive", "Model", "Size", "InterfaceType") {
		add("Dysk", joinNonEmpty(", ", strings.TrimSpace(d.Str("Model")), sizeOrDash(d, "Size", 0), d.Str("InterfaceType")))
	}
	for _, g := range c.cim("Win32_VideoController", "Name") {
		add("Karta graficzna", g.Str("Name"))
	}
	L = append(L, "", "WINDOWS")
	add("System", joinNonEmpty(", ", w.Caption, w.Version, fmt.Sprintf("kompilacja %d.%d", w.Build, w.Ubr), w.Arch))
	add("Edycja", w.Edition)
	if osObj != nil {
		t, _ := osObj.Time("InstallDate")
		add("Data instalacji", fmtDt(t))
	}
	lic := c.Cim(Q{Class: "SoftwareLicensingProduct", Props: []string{"LicenseStatus", "Description", "PartialProductKey"},
		Where: "PartialProductKey IS NOT NULL AND ApplicationId='55c92734-d682-4d71-983e-d6ec3f16059f'"})
	if len(lic) > 0 {
		l := lic[0]
		st := map[int64]string{0: "nieaktywowany", 1: "aktywowany", 2: "okres prolongaty", 3: "okres prolongaty", 4: "okres prolongaty", 5: "wymaga aktywacji", 6: "okres prolongaty"}
		add("Aktywacja", st[l.Int("LicenseStatus")])
		ch := ""
		if m := licChannelRx.FindStringSubmatch(l.Str("Description")); m != nil {
			ch = m[1]
		}
		add("Kanał licencji", ch)
		add("Klucz (ostatnie znaki)", "*****-*****-*****-*****-"+l.Str("PartialProductKey"))
	}
	oem := ""
	if svc := c.cim1("SoftwareLicensingService", "OA3xOriginalProductKey"); svc != nil {
		oem = svc.Str("OA3xOriginalProductKey")
	}
	if oem == "" {
		oem = "brak (licencja cyfrowa, klucz detaliczny albo starszy komputer)"
	}
	add("Klucz w BIOS/UEFI (OEM)", oem)
	L = append(L, "", "KONTA I URZĄDZENIA")
	for _, u := range c.Cim(Q{Class: "Win32_UserAccount", Props: []string{"Name", "Disabled"}, Where: "LocalAccount=True"}) {
		if !u.Bool("Disabled") {
			add("Konto lokalne", u.Str("Name"))
		}
	}
	for _, p := range c.cim("Win32_Printer", "Name", "DriverName", "PortName") {
		add("Drukarka", joinNonEmpty(" · ", p.Str("Name"), "sterownik: "+p.Str("DriverName"), "port: "+p.Str("PortName")))
	}
	return strings.Join(L, "\r\n")
}

// writeCSV odpowiada Write-SdCsv (Export-Csv -Delimiter ';' -Encoding UTF8): CSV dla polskiego Excela - średnik,
// UTF-8 z BOM, wartości w cudzysłowach.
func writeCSV(path string, cols []string, rows [][]string) error {
	q := func(v []string) string {
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		return strings.Join(out, ";")
	}
	lines := []string{q(cols)}
	for _, r := range rows {
		lines = append(lines, q(r))
	}
	return writeText(path, strings.Join(lines, "\r\n")+"\r\n", true)
}

// BackupPartResult - wynik elementu kopii (Result: ok | warn | skip | error).
type BackupPartResult struct {
	Key, Label, Result, Message string
}

// BackupResult - wynik Invoke-SdBackup.
type BackupResult struct {
	Dir      string
	Parts    []BackupPartResult
	Warnings []string
}

// RunBackup odpowiada Invoke-SdBackup: kopia w <dest>\<KOMPUTER>_<data>_kopia. step (może być nil) dostaje opis
// bieżącego kroku (pusty napis na końcu). Błąd - tylko gdy nie udało się utworzyć folderu kopii.
func RunBackup(dest string, include []string, step func(string)) (BackupResult, error) {
	if step == nil {
		step = func(string) {}
	}
	c := standaloneCtx()
	isAdmin := win.IsAdmin()
	dir := filepath.Join(dest, BackupDirName(time.Now()))
	out := BackupResult{Dir: dir}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, err
	}
	part := func(k, res, msg string) {
		out.Parts = append(out.Parts, BackupPartResult{Key: k, Label: backupPart(k).Label, Result: res, Message: msg})
	}
	var files []string

	// ostrzeżenia ogólne: kopia na dysku systemowym, zaszyfrowane dyski bez zapisanego klucza
	if root, sd := filepath.VolumeName(dir), os.Getenv("SystemDrive"); root != "" && sd != "" && strings.EqualFold(root, sd) {
		out.Warnings = append(out.Warnings, "Kopia leży na dysku systemowym - przed reinstalacją przenieś ją na pendrive albo inny dysk, bo zostanie usunięta razem z systemem.")
	}
	bl := bitLockerVolumes(c)
	if len(bl) > 0 && !slices.Contains(include, "bitlocker") {
		var letters []string
		for _, v := range bl {
			letters = append(letters, v.Str("DriveLetter"))
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf("Zaszyfrowane BitLockerem: %s. Przed zmianami zapisz klucz odzyskiwania (element „Klucze odzyskiwania BitLocker”) albo upewnij się, że klient ma go na koncie Microsoft (aka.ms/myrecoverykey).", strings.Join(letters, ", ")))
	}

	for _, meta := range BackupParts {
		k := meta.Key
		if !slices.Contains(include, k) {
			continue
		}
		if meta.Admin && !isAdmin {
			part(k, "skip", "wymaga uprawnień administratora")
			continue
		}
		var err error
		switch k {
		case "drivers":
			err = backupDrivers(c, dir, step, part, &files)
		case "programs":
			step("Lista zainstalowanych programów...")
			var n int
			if n, err = backupPrograms(c, dir); err == nil {
				files = append(files, "programy.html", "programy.csv")
				part(k, "ok", fmtCount(n, "program", "programy", "programów"))
			}
		case "system":
			step("Dane komputera i klucz Windows...")
			txt := "DANE KOMPUTERA - SerwisDiag " + Version + " (" + fmtDt(time.Now()) + ")\r\n\r\n" + backupSystemText(c)
			if err = writeText(filepath.Join(dir, "system.txt"), txt, true); err == nil {
				files = append(files, "system.txt")
				part(k, "ok", "zapisano")
			}
		case "wifi":
			step("Profile Wi-Fi...")
			err = backupWifi(dir, part, &files)
		case "bitlocker":
			step("Klucze odzyskiwania BitLocker...")
			err = backupBitLocker(dir, bl, part, &files)
		}
		if err != nil {
			part(k, "error", err.Error())
		}
	}

	// instrukcja w folderze kopii
	R := []string{"KOPIA PRZED REINSTALACJĄ WINDOWS - SerwisDiag " + Version, fmt.Sprintf("Komputer: %s   Data: %s", os.Getenv("COMPUTERNAME"), fmtDt(time.Now())), "", "ZAWARTOŚĆ"}
	desc := map[string]string{
		`sterowniki\`: "sterowniki zainstalowane spoza Windows (eksport DISM)", "sterowniki-lista.csv": "lista tych sterowników: urządzenie, dostawca, wersja, data",
		"programy.html": "zainstalowane programy - do ponownej instalacji (do wydruku)", "programy.csv": "to samo w formacie dla Excela",
		"system.txt": "dane komputera, wersja i aktywacja Windows, klucz z BIOS/UEFI, konta, drukarki",
		`wifi\`:      "profile Wi-Fi z hasłami (jawny tekst)", "bitlocker-klucze.txt": "klucze odzyskiwania BitLocker",
	}
	for _, f := range files {
		R = append(R, fmt.Sprintf("  %-22s %s", f, desc[f]))
	}
	R = append(R, "", "PRZYWRACANIE PO REINSTALACJI")
	if slices.Contains(files, `sterowniki\`) {
		R = append(R, "  Sterowniki: Menedżer urządzeń > urządzenie > Aktualizuj sterownik > Przeglądaj mój komputer > wskaż folder",
			`  "sterowniki" (z podfolderami), albo jako administrator: pnputil /add-driver "<folder>\sterowniki\*.inf" /subdirs /install`)
	}
	if slices.Contains(files, `wifi\`) {
		R = append(R, `  Wi-Fi: netsh wlan add profile filename="<folder>\wifi\<plik>.xml"`)
	}
	R = append(R, "  Windows: komputer z kluczem w BIOS/UEFI aktywuje się sam po instalacji tej samej edycji (Home albo Pro).")
	if slices.ContainsFunc(BackupParts, func(p BackupPart) bool { return p.Sensitive && slices.Contains(include, p.Key) }) {
		s := "UWAGA: folder zawiera dane wrażliwe (numer seryjny, klucz Windows"
		if slices.Contains(files, `wifi\`) {
			s += ", hasła Wi-Fi"
		}
		if slices.Contains(files, "bitlocker-klucze.txt") {
			s += ", klucze BitLocker"
		}
		R = append(R, "", s+").", "Przechowuj go bezpiecznie i usuń z nośnika serwisowego po zakończeniu naprawy.")
	}
	for _, p := range out.Parts {
		switch p.Result {
		case "error":
			R = append(R, "", fmt.Sprintf("BŁĄD: %s - %s", p.Label, p.Message))
		case "warn":
			R = append(R, "", fmt.Sprintf("UWAGA: %s - %s", p.Label, p.Message))
		}
	}
	for _, wn := range out.Warnings {
		R = append(R, "", "UWAGA: "+wn)
	}
	err := writeText(filepath.Join(dir, "CZYTAJ.txt"), strings.Join(R, "\r\n"), true)
	step("")
	return out, err
}

func backupDrivers(c *Ctx, dir string, step func(string), part func(k, res, msg string), files *[]string) error {
	w := winInfo(c)
	type drv struct{ dev, cls, prov, ver, date, inf string }
	var list []drv
	for _, d := range c.Cim(Q{Class: "Win32_PnPSignedDriver", Props: []string{"DeviceName", "DeviceClass", "DriverProviderName", "DriverVersion", "DriverDate", "InfName"}, Where: "InfName LIKE 'oem%'"}) {
		t, _ := d.Time("DriverDate")
		list = append(list, drv{d.Str("DeviceName"), d.Str("DeviceClass"), d.Str("DriverProviderName"), d.Str("DriverVersion"), fmtDate(t), d.Str("InfName")})
	}
	psSort(list, func(a, b drv) int {
		if r := cmpText(a.cls, b.cls); r != 0 {
			return r
		}
		return cmpText(a.dev, b.dev)
	})
	if len(list) > 0 {
		rows := make([][]string, len(list))
		for i, d := range list {
			rows[i] = []string{d.dev, d.cls, d.prov, d.ver, d.date, d.inf}
		}
		if err := writeCSV(filepath.Join(dir, "sterowniki-lista.csv"), []string{"Urządzenie", "Klasa", "Dostawca sterownika", "Wersja", "Data", "Plik INF"}, rows); err != nil {
			return err
		}
		*files = append(*files, "sterowniki-lista.csv")
	}
	if w.Build > 0 && w.Build < 9600 {
		part("drivers", "warn", "eksport sterowników wymaga Windows 8.1 lub nowszego - zapisano tylko ich listę")
		return nil
	}
	dd := filepath.Join(dir, "sterowniki")
	// zbyt długa ścieżka albo brak miejsca przerwałyby eksport w połowie - sprawdzamy przed startem
	step("Sprawdzanie sterowników do eksportu...")
	chk := TestDriverExport(dd, nil, -1)
	if !chk.Ok {
		part("drivers", "error", "nie wyeksportowano: "+chk.Message+" Lista sterowników została zapisana.")
		return nil
	}
	step(fmt.Sprintf("Eksport sterowników (ok. %s, zwykle kilka minut)...", fmtSize(chk.Bytes, 1)))
	if err := os.MkdirAll(dd, 0o755); err != nil {
		return err
	}
	dism := filepath.Join(os.Getenv("SystemRoot"), `System32\Dism.exe`)
	r := win.RunRaw(30*time.Minute, dism, FormatCmdArg(dism)+" /Online /Export-Driver /Destination:"+FormatCmdArg(dd))
	infs, size := 0, 0.0
	_ = filepath.WalkDir(dd, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".inf") {
			infs++
		}
		if info, err := e.Info(); err == nil {
			size += float64(info.Size())
		}
		return nil
	})
	*files = append(*files, `sterowniki\`)
	n := fmtCount(infs, "sterownik", "sterowniki", "sterowników")
	if r.ExitCode != 0 {
		msg := ""
		if r.Err != nil {
			msg = r.Err.Error()
		}
		part("drivers", "error", "eksport niepełny ("+n+"): "+dismExportError(r.ExitCode, win.OEMString(r.Stdout)+win.OEMString(r.Stderr)+msg))
	} else {
		part("drivers", "ok", n+", "+fmtSize(size, 1))
	}
	return nil
}

func backupPrograms(c *Ctx, dir string) (int, error) {
	apps := installedApps(c)
	rows := make([][]string, len(apps))
	for i, a := range apps {
		rows[i] = []string{a.Name, a.Version, a.Publisher, fmtDate(a.Installed), a.Scope}
	}
	if err := writeCSV(filepath.Join(dir, "programy.csv"), []string{"Nazwa", "Wersja", "Producent", "Zainstalowano", "Zakres"}, rows); err != nil {
		return 0, err
	}
	E := func(s string) string { return toHTML(s) }
	pc := os.Getenv("COMPUTERNAME")
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="pl"><head><meta charset="utf-8"><title>Programy - ` + E(pc) + `</title><style>` + inlineCSS + `</style></head><body><div class="wrap"><section class="card">`)
	sb.WriteString(`<h2>Zainstalowane programy: ` + E(pc) + ` (` + fmtDt(time.Now()) + `)</h2><table><thead><tr><th>Nazwa</th><th>Wersja</th><th>Producent</th><th>Zainstalowano</th></tr></thead><tbody>`)
	for _, r := range rows {
		sb.WriteString(`<tr><td>` + E(r[0]) + `</td><td>` + E(r[1]) + `</td><td>` + E(r[2]) + `</td><td>` + E(r[3]) + `</td></tr>`)
	}
	sb.WriteString(`</tbody></table></section></div></body></html>`)
	return len(apps), writeText(filepath.Join(dir, "programy.html"), sb.String(), true)
}

func backupWifi(dir string, part func(k, res, msg string), files *[]string) error {
	wd := filepath.Join(dir, "wifi")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		return err
	}
	netsh := filepath.Join(os.Getenv("SystemRoot"), `System32\netsh.exe`)
	r := win.RunRaw(60*time.Second, netsh, FormatCmdArg(netsh)+" wlan export profile key=clear folder="+FormatCmdArg(wd))
	xml := 0
	if es, err := os.ReadDir(wd); err == nil {
		for _, e := range es {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
				xml++
			}
		}
	}
	if xml > 0 {
		*files = append(*files, `wifi\`)
		part("wifi", "ok", fmtCount(xml, "profil", "profile", "profili"))
		return nil
	}
	_ = os.Remove(wd)
	if r.ExitCode != 0 {
		part("wifi", "skip", "brak karty Wi-Fi albo usługa WLAN nie działa")
	} else {
		part("wifi", "skip", "brak zapisanych sieci Wi-Fi")
	}
	return nil
}

func backupBitLocker(dir string, bl []win.WmiObject, part func(k, res, msg string), files *[]string) error {
	var lines []string
	for _, v := range bl {
		path := wmiKeyPath("Win32_EncryptableVolume", "DeviceID", v.Str("DeviceID"))
		kp, err := win.WmiExecMethod(blNamespace, path, "GetKeyProtectors", map[string]any{"KeyProtectorType": uint32(3)})
		if err != nil {
			return err
		}
		for _, id := range kp.Strs("VolumeKeyProtectorID") {
			np, err := win.WmiExecMethod(blNamespace, path, "GetKeyProtectorNumericalPassword", map[string]any{"VolumeKeyProtectorID": id})
			if err != nil {
				return err
			}
			lines = append(lines, "Dysk "+v.Str("DriveLetter"), "  identyfikator klucza: "+id, "  klucz odzyskiwania:   "+np.Str("NumericalPassword"), "")
		}
	}
	if len(lines) == 0 {
		if len(bl) == 0 {
			part("bitlocker", "skip", "brak dysków zaszyfrowanych BitLockerem")
		} else {
			part("bitlocker", "skip", "zaszyfrowane dyski nie mają klucza odzyskiwania w postaci hasła")
		}
		return nil
	}
	head := []string{"KLUCZE ODZYSKIWANIA BITLOCKER - " + os.Getenv("COMPUTERNAME") + " (" + fmtDt(time.Now()) + ")", "Dane bardzo wrażliwe: przekaż je klientowi i usuń ten plik z nośnika serwisowego.", ""}
	if err := writeText(filepath.Join(dir, "bitlocker-klucze.txt"), strings.Join(append(head, lines...), "\r\n"), true); err != nil {
		return err
	}
	*files = append(*files, "bitlocker-klucze.txt")
	part("bitlocker", "ok", fmtCount(len(bl), "dysk", "dyski", "dysków"))
	return nil
}
