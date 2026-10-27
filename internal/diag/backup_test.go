package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupIncludeList(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "drivers,programs,system",
		"wifi, DRIVERS;nieznane": "drivers,wifi",
		"bitlocker system":       "system,bitlocker",
		"nieznane":               "drivers,programs,system",
	} {
		if got := strings.Join(BackupIncludeList(in), ","); got != want {
			t.Errorf("BackupIncludeList(%q) = %s, oczekiwano %s", in, got, want)
		}
	}
	name := BackupDirName(time.Date(2026, 10, 27, 9, 5, 0, 0, time.Local))
	if !strings.HasSuffix(name, "_20261027-0905_kopia") || strings.ContainsAny(name, ` \/:`) {
		t.Errorf("nazwa folderu: %s", name)
	}
}

func TestTestDriverExport(t *testing.T) {
	info := &DriverExportInfo{Count: 110, Bytes: 8 * 1024 * 1024 * 1024, MaxRel: 147, Package: "udcdriver.inf"}
	if r := TestDriverExport(`E:\DESKTOP-ABC_20261027-0905_kopia\sterowniki`, info, 64*1024*1024*1024.0); !r.Ok || r.Need != 45+1+147 {
		t.Errorf("krótka ścieżka: %+v", r)
	}
	long := `E:\` + strings.Repeat("a", 100) + `\sterowniki`
	if r := TestDriverExport(long, info, 64*1024*1024*1024.0); r.Ok || !strings.Contains(r.Message, "najdłuższy plik sterownika (udcdriver.inf) wymagałby 262 znaków") {
		t.Errorf("za długa ścieżka: %+v", r)
	}
	if r := TestDriverExport(`E:\k\sterowniki`, info, 4*1024*1024*1024.0); r.Ok || r.Message != "za mało miejsca: eksport zajmie ok. 8,0 GB (110 pakietów, także starsze wersje sterowników), a na nośniku jest wolne 4,0 GB." {
		t.Errorf("za mało miejsca: %+v", r)
	}
	if r := TestDriverExport(`E:\k\sterowniki`, &DriverExportInfo{MaxRel: 10}, 0); !r.Ok {
		t.Errorf("bez rozmiaru eksportu nie ma czego sprawdzać: %+v", r)
	}
}

func TestBackupHelpers(t *testing.T) {
	if s := dismExportError(206, ""); !strings.HasPrefix(s, "nazwa pliku sterownika jest za długa") {
		t.Errorf("206: %s", s)
	}
	if s := dismExportError(-2146498529, "Błąd: 0x800f081f\r\n\r\nPlik dziennika DISM: C:\\dism.log\r\n"); s != `DISM zakończył się kodem 0x800F081F: Plik dziennika DISM: C:\dism.log` {
		t.Errorf("inny kod: %s", s)
	}
	if p := wmiKeyPath("Win32_EncryptableVolume", "DeviceID", `\\?\Volume{1234}\`); p != `Win32_EncryptableVolume.DeviceID="\\\\?\\Volume{1234}\\"` {
		t.Errorf("ścieżka WMI: %s", p)
	}
	path := filepath.Join(t.TempDir(), "a.csv")
	if err := writeCSV(path, []string{"Nazwa", "Wersja"}, [][]string{{`Program "X"`, ""}, {"Zażółć", "1.0"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if want := "\xEF\xBB\xBF\"Nazwa\";\"Wersja\"\r\n\"Program \"\"X\"\"\";\"\"\r\n\"Zażółć\";\"1.0\"\r\n"; string(b) != want {
		t.Errorf("CSV: %q", b)
	}
}
