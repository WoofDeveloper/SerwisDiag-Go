package diag

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"serwisdiag/internal/win"
)

// Naprawy wymagające przygotowania: DISM RestoreHealth z płyty / pendrive'a / obrazu ISO Windows (lib\Repair.ps1).
// Bez internetu DISM nie pobierze plików z Windows Update - źródłem musi być install.wim / install.esd tej samej
// wersji Windows (ta sama kompilacja bazowa, edycja, język i architektura).

// buildFamily odpowiada Get-SdBuildFamily: wydania różniące się tylko pakietem aktywującym mają wspólne pliki
// systemu (Windows 10 2004-22H2 = 19041, Windows 11 22H2/23H2 = 22621, 24H2/25H2/26H2 = 26100).
func buildFamily(build int) int {
	fam := map[int]int{18363: 18362, 19042: 19041, 19043: 19041, 19044: 19041, 19045: 19041, 22631: 22621, 26200: 26100, 26300: 26100}
	if f, ok := fam[build]; ok {
		return f
	}
	return build
}

// archName odpowiada ConvertTo-SdArchName: architektura obrazu (liczba albo nazwa) -> x86 | x64 | ARM | ARM64.
func archName(v string) string {
	switch strings.ToLower(v) {
	case "9", "x64", "amd64":
		return "x64"
	case "0", "x86":
		return "x86"
	case "12", "arm64":
		return "ARM64"
	case "5", "arm":
		return "ARM"
	}
	return v
}

// ImageEdition - edycja Windows w obrazie instalacyjnym.
type ImageEdition struct {
	Index     int
	Name      string
	EditionID string
	Build     int
	Arch      string
	Languages []string
}

// ImageSelection - edycja wybrana do naprawy; Index 0 - nic nie pasuje (powody w Problems).
type ImageSelection struct {
	Index    int
	Image    *ImageEdition
	Problems []string
}

var (
	proEduRx   = rx(`^Professional(Education|Workstation)$`)
	eduEntRx   = rx(`^(Education|Enterprise)$`)
	editionNRx = rx(`^(Core|Professional)N$`)
)

// SelectImageIndex odpowiada Select-SdImageIndex: edycja z obrazu instalacyjnego pasująca do zainstalowanego
// systemu - dokładnie ta sama, a gdy jej brak - najbliższa (Home Single Language -> Home, Pro Education -> Pro).
func SelectImageIndex(images []ImageEdition, editionID string, build int, arch, language string) ImageSelection {
	var r ImageSelection
	if len(images) == 0 {
		r.Problems = append(r.Problems, "Obraz nie zawiera żadnej edycji Windows.")
		return r
	}
	fam := buildFamily(build)
	var same []ImageEdition
	for _, im := range images {
		if buildFamily(im.Build) == fam {
			same = append(same, im)
		}
	}
	if len(same) == 0 {
		var have []string
		for _, im := range images {
			if b := strconv.Itoa(im.Build); !slices.Contains(have, b) {
				have = append(have, b)
			}
		}
		r.Problems = append(r.Problems, fmt.Sprintf("Obraz jest z innej wersji Windows (kompilacja %s), a zainstalowany system to kompilacja %d. Potrzebny jest obraz tej samej wersji.", strings.Join(have, ", "), build))
		return r
	}
	if arch != "" {
		var ok []ImageEdition
		for _, im := range same {
			if im.Arch == "" || eqi(im.Arch, arch) {
				ok = append(ok, im)
			}
		}
		if len(ok) == 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("Obraz jest dla architektury %s, a system to %s.", same[0].Arch, arch))
			return r
		}
		same = ok
	}
	pref := []string{editionID}
	switch {
	case eqi(editionID, "CoreSingleLanguage"), eqi(editionID, "CoreCountrySpecific"):
		pref = append(pref, "Core")
	case proEduRx.MatchString(editionID), eduEntRx.MatchString(editionID):
		pref = append(pref, "Professional")
	case editionNRx.MatchString(editionID):
		pref = append(pref, editionID[:len(editionID)-1])
	}
	var pick *ImageEdition
	for _, e := range pref {
		if i := slices.IndexFunc(same, func(im ImageEdition) bool { return eqi(im.EditionID, e) }); i >= 0 {
			pick = &same[i]
			break
		}
	}
	switch {
	case pick == nil:
		pick = &same[0]
		r.Problems = append(r.Problems, fmt.Sprintf("W obrazie nie ma edycji %s - użyto „%s”. Naprawa zwykle się uda, ale pliki właściwe tylko dla innej edycji mogą nie zostać odtworzone.", editionID, pick.Name))
	case !eqi(pick.EditionID, editionID):
		r.Problems = append(r.Problems, fmt.Sprintf("Użyto pokrewnej edycji „%s” (zainstalowana: %s).", pick.Name, editionID))
	}
	if language != "" && len(pick.Languages) > 0 && !containsi(pick.Languages, language) {
		r.Problems = append(r.Problems, fmt.Sprintf("Język obrazu (%s) różni się od języka systemu (%s) - DISM może nie znaleźć plików językowych.", strings.Join(pick.Languages, ", "), language))
	}
	if pick.Build != build {
		r.Problems = append(r.Problems, fmt.Sprintf("Obraz ma kompilację %d, system %d - to ta sama rodzina wydań, naprawa powinna działać.", pick.Build, build))
	}
	r.Index, r.Image = pick.Index, pick
	return r
}

var whitespaceRx = regexp.MustCompile(`\s`)

// DismSourceArg odpowiada Get-SdDismSourceArg: parametr /Source dla DISM, np. /Source:wim:E:\sources\install.wim:6.
func DismSourceArg(imagePath string, index int) string {
	kind := "wim"
	if strings.HasSuffix(strings.ToLower(imagePath), ".esd") {
		kind = "esd"
	}
	a := fmt.Sprintf("/Source:%s:%s:%d", kind, imagePath, index)
	if whitespaceRx.MatchString(a) {
		a = `"` + a + `"`
	}
	return a
}

// InstallImage - plik obrazu do naprawy (Resolve-SdInstallImage). MountedISO: obraz ISO podłączony przez program
// (do odłączenia po naprawie albo gdy nie nadaje się do naprawy).
type InstallImage struct {
	ImagePath, MountedISO, Error string
}

// ResolveInstallImage odpowiada Resolve-SdInstallImage: ścieżka wybrana przez technika (obraz ISO, install.wim /
// install.esd albo katalog / litera nośnika) -> plik obrazu. Obraz ISO zostaje podłączony jako napęd.
func ResolveInstallImage(path string) InstallImage {
	var r InstallImage
	low := strings.ToLower(path)
	if strings.HasSuffix(low, ".wim") || strings.HasSuffix(low, ".esd") {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			r.ImagePath = path
		} else {
			r.Error = "Nie znaleziono pliku: " + path
		}
		return r
	}
	root := path
	if strings.HasSuffix(low, ".iso") {
		letter, mounted, err := win.MountISO(path)
		if err != nil {
			r.Error = "Nie udało się zamontować obrazu ISO: " + err.Error()
			return r
		}
		if mounted {
			r.MountedISO = path
		}
		if letter == "" {
			r.Error = "Obraz ISO zamontowano, ale nie otrzymał litery dysku."
			return r
		}
		root = letter + `:\`
	}
	for _, n := range []string{`sources\install.wim`, `sources\install.esd`} {
		if p := filepath.Join(root, n); fileExists(p) {
			r.ImagePath = p
			return r
		}
	}
	r.Error = `Na nośniku nie ma pliku sources\install.wim ani sources\install.esd - to nie jest nośnik instalacyjny Windows (albo obraz jest podzielony na pliki .swm, których DISM nie użyje do naprawy).`
	return r
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// wimXML - opis obrazów zapisany w pliku WIM / ESD.
type wimXML struct {
	Images []struct {
		Index   int    `xml:"INDEX,attr"`
		Name    string `xml:"NAME"`
		Windows struct {
			Arch      string   `xml:"ARCH"`
			EditionID string   `xml:"EDITIONID"`
			Languages []string `xml:"LANGUAGES>LANGUAGE"`
			Build     string   `xml:"VERSION>BUILD"`
		} `xml:"WINDOWS"`
	} `xml:"IMAGE"`
}

// ImageEditions odpowiada Get-SdImageEditions: edycje w install.wim / install.esd. Wersja PowerShell pyta moduł
// DISM (Get-WindowsImage, wymaga administratora); tu program czyta opis obrazów z pliku - ten sam, z którego
// korzysta DISM: zasób XML (UTF-16) wskazany w nagłówku WIM (offset 0x48: rozmiar, flagi, położenie).
func ImageEditions(path string) ([]ImageEdition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var h [208]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:8]) != "MSWIM\x00\x00\x00" {
		return nil, errText("To nie jest obraz Windows (install.wim / install.esd): " + path)
	}
	res := h[0x48:0x60]
	size := int64(binary.LittleEndian.Uint64(res[0:8]) & 0x00FFFFFFFFFFFFFF)
	off := int64(binary.LittleEndian.Uint64(res[8:16]))
	if size < 4 || size > 64<<20 || res[7]&0x04 != 0 { // 0x04: zasób skompresowany (opis obrazów nigdy nie jest)
		return nil, errText("Nieobsługiwany zapis opisu obrazów w pliku: " + path)
	}
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, err
	}
	if buf[0] == 0xFF && buf[1] == 0xFE {
		buf = buf[2:]
	}
	u := make([]uint16, len(buf)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(buf[2*i:])
	}
	var x wimXML
	if err := xml.Unmarshal([]byte(string(utf16.Decode(u))), &x); err != nil {
		return nil, errText("Nie udało się odczytać opisu obrazów: " + err.Error())
	}
	var out []ImageEdition
	for _, im := range x.Images {
		e := ImageEdition{Index: im.Index, Name: strings.TrimSpace(im.Name), EditionID: strings.TrimSpace(im.Windows.EditionID),
			Arch: archName(strings.TrimSpace(im.Windows.Arch))}
		e.Build, _ = strconv.Atoi(strings.TrimSpace(im.Windows.Build))
		for _, l := range im.Windows.Languages {
			if l = strings.TrimSpace(l); l != "" {
				e.Languages = append(e.Languages, l)
			}
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b ImageEdition) int { return cmpNum(a.Index, b.Index) })
	return out, nil
}

// SystemForImage: edycja, kompilacja, architektura i język instalacji systemu - do wyboru edycji z obrazu.
func SystemForImage() (edition string, build int, arch, language string) {
	w := winInfo(standaloneCtx())
	return w.Edition, int(w.Build), w.Arch, win.InstalledUILanguage()
}

// standaloneCtx: kontekst testów poza skanowaniem (okna narzędzi serwisowych) - zapytania WMI z bezpiecznikiem
// i pamięcią podręczną jak w diagnostyce.
func standaloneCtx() *Ctx { return &Ctx{s: NewScan(Options{}), r: &checkRun{}} }
