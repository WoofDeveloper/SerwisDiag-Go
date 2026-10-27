package diag

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"serwisdiag/internal/win"
)

func TestBuildFamilyAndArch(t *testing.T) {
	for b, want := range map[int]int{19045: 19041, 26300: 26100, 22631: 22621, 22000: 22000} {
		if got := buildFamily(b); got != want {
			t.Errorf("buildFamily(%d) = %d, oczekiwano %d", b, got, want)
		}
	}
	for v, want := range map[string]string{"9": "x64", "AMD64": "x64", "0": "x86", "12": "ARM64", "arm64": "ARM64", "5": "ARM", "7": "7"} {
		if got := archName(v); got != want {
			t.Errorf("archName(%q) = %q, oczekiwano %q", v, got, want)
		}
	}
}

// testy z tools\run-tests.ps1 (Select-SdImageIndex, Get-SdDismSourceArg)
func TestSelectImageIndex(t *testing.T) {
	imgs := []ImageEdition{
		{Index: 1, Name: "Windows 11 Home", EditionID: "Core", Build: 26100, Arch: "x64", Languages: []string{"pl-PL"}},
		{Index: 3, Name: "Windows 11 Education", EditionID: "Education", Build: 26100, Arch: "x64", Languages: []string{"pl-PL"}},
		{Index: 6, Name: "Windows 11 Pro", EditionID: "Professional", Build: 26100, Arch: "x64", Languages: []string{"pl-PL"}},
	}
	if s := SelectImageIndex(imgs, "Professional", 26100, "x64", "pl-PL"); s.Index != 6 || len(s.Problems) != 0 {
		t.Errorf("ta sama edycja: %+v", s)
	}
	if s := SelectImageIndex(imgs, "CoreSingleLanguage", 26100, "x64", "pl-PL"); s.Index != 1 || len(s.Problems) != 1 || !strings.Contains(s.Problems[0], "pokrewnej edycji „Windows 11 Home”") {
		t.Errorf("Home Single Language -> Home: %+v", s)
	}
	if s := SelectImageIndex(imgs, "Professional", 26300, "x64", "pl-PL"); s.Index != 6 || !strings.Contains(strings.Join(s.Problems, " "), "rodzina") {
		t.Errorf("24H2 naprawia 26H2: %+v", s)
	}
	if s := SelectImageIndex(imgs, "Professional", 19045, "x64", "pl-PL"); s.Index != 0 || !strings.Contains(s.Problems[0], "kompilacja 26100), a zainstalowany system to kompilacja 19045") {
		t.Errorf("inna wersja Windows: %+v", s)
	}
	if s := SelectImageIndex(imgs, "Professional", 26100, "ARM64", "pl-PL"); s.Index != 0 || s.Problems[0] != "Obraz jest dla architektury x64, a system to ARM64." {
		t.Errorf("inna architektura: %+v", s)
	}
	if s := SelectImageIndex(imgs, "Professional", 26100, "x64", "en-US"); !strings.Contains(strings.Join(s.Problems, " "), "Język obrazu (pl-PL)") {
		t.Errorf("inny język: %+v", s)
	}
	if s := SelectImageIndex(imgs, "ProfessionalWorkstation", 26100, "x64", "pl-PL"); s.Index != 6 {
		t.Errorf("Pro for Workstations -> Pro: %+v", s)
	}
	if s := SelectImageIndex(imgs, "ProfessionalN", 26100, "x64", "pl-PL"); s.Index != 6 {
		t.Errorf("Pro N -> Pro: %+v", s)
	}
	if s := SelectImageIndex(imgs, "ServerStandard", 26100, "x64", "pl-PL"); s.Index != 1 || !strings.Contains(s.Problems[0], "W obrazie nie ma edycji ServerStandard") {
		t.Errorf("brak edycji: %+v", s)
	}
	if s := SelectImageIndex(nil, "Core", 26100, "x64", ""); s.Index != 0 || s.Problems[0] != "Obraz nie zawiera żadnej edycji Windows." {
		t.Errorf("pusty obraz: %+v", s)
	}
	for _, c := range [][3]string{{`E:\sources\install.wim`, "6", `/Source:wim:E:\sources\install.wim:6`}, {`E:\sources\install.ESD`, "2", `/Source:esd:E:\sources\install.ESD:2`},
		{`C:\Obrazy Win\install.wim`, "1", `"/Source:wim:C:\Obrazy Win\install.wim:1"`}} {
		n := map[string]int{"6": 6, "2": 2, "1": 1}[c[1]]
		if got := DismSourceArg(c[0], n); got != c[2] {
			t.Errorf("DismSourceArg(%s) = %s", c[0], got)
		}
	}
}

// writeTestWim: plik z nagłówkiem WIM (208 bajtów) i opisem obrazów (UTF-16 z BOM) na końcu - jak w install.wim.
func writeTestWim(t *testing.T, path, xmlText string) {
	t.Helper()
	u := utf16.Encode([]rune(xmlText))
	x := []byte{0xFF, 0xFE}
	for _, c := range u {
		x = binary.LittleEndian.AppendUint16(x, c)
	}
	h := make([]byte, 208)
	copy(h, "MSWIM\x00\x00\x00")
	binary.LittleEndian.PutUint32(h[8:], 208)
	binary.LittleEndian.PutUint64(h[0x48:], uint64(len(x))) // rozmiar (7 bajtów) i flagi (0)
	binary.LittleEndian.PutUint64(h[0x50:], 208+1000)       // położenie opisu (za „danymi” obrazu)
	binary.LittleEndian.PutUint64(h[0x58:], uint64(len(x)))
	data := append(append(h, make([]byte, 1000)...), x...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImageEditionsAndResolve(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nosnik", "sources")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	wim := filepath.Join(src, "install.esd")
	writeTestWim(t, wim, `<WIM><TOTALBYTES>1</TOTALBYTES>`+
		`<IMAGE INDEX="2"><WINDOWS><ARCH>9</ARCH><EDITIONID>Professional</EDITIONID><LANGUAGES><LANGUAGE>pl-PL</LANGUAGE><DEFAULT>pl-PL</DEFAULT></LANGUAGES>`+
		`<VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>26100</BUILD><SPBUILD>1742</SPBUILD></VERSION></WINDOWS><NAME>Windows 11 Pro</NAME></IMAGE>`+
		`<IMAGE INDEX="1"><WINDOWS><ARCH>9</ARCH><EDITIONID>Core</EDITIONID><LANGUAGES><LANGUAGE>pl-PL</LANGUAGE><LANGUAGE>en-US</LANGUAGE></LANGUAGES>`+
		`<VERSION><BUILD>26100</BUILD></VERSION></WINDOWS><NAME>Windows 11 Home</NAME></IMAGE></WIM>`)
	eds, err := ImageEditions(wim)
	if err != nil {
		t.Fatal(err)
	}
	if len(eds) != 2 || eds[0].Index != 1 || eds[0].Name != "Windows 11 Home" || eds[0].EditionID != "Core" || eds[0].Build != 26100 || eds[0].Arch != "x64" ||
		strings.Join(eds[0].Languages, ",") != "pl-PL,en-US" || eds[1].Index != 2 || eds[1].EditionID != "Professional" {
		t.Errorf("edycje: %+v", eds)
	}
	if s := SelectImageIndex(eds, "Professional", 26200, "x64", "pl-PL"); s.Index != 2 {
		t.Errorf("wybór z obrazu: %+v", s)
	}
	// prawdziwy plik WIM z Windows (bez edycji Windows w opisie) - odczyt nagłówka i opisu
	if real := filepath.Join(os.Getenv("SystemRoot"), "System32", "DrtmAuthTxt.wim"); fileExists(real) {
		if r, err := ImageEditions(real); err != nil || len(r) != 1 || r[0].Index != 1 || r[0].Name == "" || r[0].Build != 0 {
			t.Errorf("DrtmAuthTxt.wim: %+v %v", r, err)
		}
	}
	bad := filepath.Join(dir, "zly.wim")
	_ = os.WriteFile(bad, []byte("to nie jest obraz"), 0o644)
	if _, err := ImageEditions(bad); err == nil || !strings.Contains(err.Error(), "To nie jest obraz Windows") {
		t.Errorf("zły plik: %v", err)
	}
	// nośnik (folder) -> sources\install.esd; plik obrazu wprost; brak pliku; folder bez obrazu
	if r := ResolveInstallImage(filepath.Join(dir, "nosnik")); r.ImagePath != wim || r.Error != "" {
		t.Errorf("nośnik: %+v", r)
	}
	if r := ResolveInstallImage(wim); r.ImagePath != wim {
		t.Errorf("plik obrazu: %+v", r)
	}
	if r := ResolveInstallImage(`C:\nie-ma-takiego\install.wim`); !strings.HasPrefix(r.Error, "Nie znaleziono pliku: ") {
		t.Errorf("brak pliku: %+v", r)
	}
	if r := ResolveInstallImage(dir); !strings.Contains(r.Error, `nie ma pliku sources\install.wim`) {
		t.Errorf("folder bez obrazu: %+v", r)
	}
}

func TestDriverInfoAndSort(t *testing.T) {
	ds := []WuDriver{
		{UpdateID: "a", Title: "Intel - Display", Class: "Display", SizeBytes: 512 * 1024 * 1024},
		{UpdateID: "b", Title: "Realtek - Net", Class: "Net", DeviceProblem: 28, Optional: true},
		{UpdateID: "c", Title: "AMD - Chipset", Class: "System", Optional: true},
	}
	s := SortDrivers(ds)
	if s[0].UpdateID != "b" || s[1].UpdateID != "a" || s[2].UpdateID != "c" {
		t.Errorf("kolejność: %v %v %v", s[0].UpdateID, s[1].UpdateID, s[2].UpdateID)
	}
	if got := DriverInfo(ds[1]); got != "Net · wersja z - · 0 B · naprawia urządzenie z błędem (kod 28) · opcjonalny" {
		t.Errorf("opis: %q", got)
	}
	if n := len(SelectDriversToInstall(ds)); n != 2 {
		t.Errorf("domyślnie zaznaczone: %d", n)
	}
}

// TestDismFromISOLive: cała przygotowawcza część „DISM z płyty/ISO” na obrazie ISO z plikiem WIM
// (SERWISDIAG_TEST_ISO=<plik .iso>): podłączenie, odczyt edycji, wybór (obraz testowy nie pasuje), odłączenie.
func TestDismFromISOLive(t *testing.T) {
	iso := os.Getenv("SERWISDIAG_TEST_ISO")
	if iso == "" {
		t.Skip("ustaw SERWISDIAG_TEST_ISO=<plik .iso>")
	}
	img := ResolveInstallImage(iso)
	if img.MountedISO != "" {
		defer func() {
			if err := win.DismountISO(img.MountedISO); err != nil {
				t.Errorf("odłączenie: %v", err)
			}
		}()
	}
	if img.Error != "" || !strings.HasSuffix(strings.ToLower(img.ImagePath), `:\sources\install.wim`) || img.MountedISO != iso {
		t.Fatalf("obraz: %+v", img)
	}
	eds, err := ImageEditions(img.ImagePath)
	if err != nil || len(eds) == 0 {
		t.Fatalf("edycje: %v %v", eds, err)
	}
	ed, build, arch, lang := SystemForImage()
	t.Logf("system: %s %d %s %s; obraz: %+v", ed, build, arch, lang, eds)
	if ed == "" || build == 0 || arch == "" || lang == "" {
		t.Errorf("dane systemu: %q %d %q %q", ed, build, arch, lang)
	}
	s := SelectImageIndex(eds, ed, build, arch, lang)
	if s.Index != 0 || len(s.Problems) == 0 || !strings.Contains(s.Problems[0], "innej wersji Windows") {
		t.Errorf("wybór: %+v", s)
	}
}

// TestDriverExportInfoLive: pakiety sterowników do eksportu (porównanie z Get-SdDriverExportInfo wersji PowerShell).
func TestDriverExportInfoLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test na żywym systemie")
	}
	i := driverExportInfo()
	t.Logf("pakiety: %d, bajty: %.0f, najdłuższa ścieżka: %d (%s)", i.Count, i.Bytes, i.MaxRel, i.Package)
	if i.Count == 0 || i.Bytes == 0 || i.MaxRel == 0 {
		t.Errorf("brak pakietów: %+v", i)
	}
}
