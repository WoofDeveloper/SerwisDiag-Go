package diag

import (
	"embed"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Baza wiedzy wbudowana w plik programu (z data\*.psd1 wersji PowerShell - tools\export-data.ps1).
//
//go:embed data/*.json
var dataFS embed.FS

type bugcheckEntry struct{ Name, Cause, Hint string }

type eventHint struct{ Sev, Text string }

type lifecycleBuild struct{ Name, Home, Ent, Note string }

type esuEntry struct {
	LastFreeUbr   int
	Consumer      string
	Years         map[string]string
	ActivationIds map[string]int
}

type lifecycleData struct {
	Verified    string
	LatestWin11 string
	Builds      map[string]lifecycleBuild
	Esu         map[string]esuEntry
	Ltsc        map[string]string
	IotLtsc     map[string]string
}

type softwareFlag struct {
	Pattern  string
	Type     string
	Severity string
	Note     string
	End      string
	rx       *regexp.Regexp
}

type knowledge struct {
	bugchecks  map[string]bugcheckEntry
	hints      map[string]eventHint
	lifecycle  lifecycleData
	pciVendors map[string]string
	usbVendors map[string]string
	pnpErrors  map[string]string
	flags      []softwareFlag
}

var (
	kbOnce sync.Once
	kb     knowledge
)

func load(name string, v any) {
	b, err := dataFS.ReadFile("data/" + name + ".json")
	if err != nil {
		panic(err) // plik wbudowany - brak oznacza błąd budowania
	}
	if err := json.Unmarshal(b, v); err != nil {
		panic(name + ": " + err.Error())
	}
}

// data zwraca bazę wiedzy (wczytaną przy pierwszym użyciu).
func data() *knowledge {
	kbOnce.Do(func() {
		load("bugchecks", &kb.bugchecks)
		var h struct{ Hints map[string]eventHint }
		load("event-hints", &h)
		kb.hints = h.Hints
		load("lifecycle", &kb.lifecycle)
		var v struct{ Pci, Usb map[string]string }
		load("pci-vendors", &v)
		kb.pciVendors, kb.usbVendors = v.Pci, v.Usb
		load("pnp-errors", &kb.pnpErrors)
		var f struct{ Flags []softwareFlag }
		load("software-flags", &f)
		for i := range f.Flags {
			f.Flags[i].rx = rx(f.Flags[i].Pattern)
		}
		kb.flags = f.Flags
	})
	return &kb
}

// ---------------------------------------------------------------- wyszukiwanie w bazie wiedzy

var (
	venRx = regexp.MustCompile(`(?i)VEN_([0-9A-Fa-f]{4})`)
	vidRx = regexp.MustCompile(`(?i)VID_([0-9A-Fa-f]{4})`)
)

// vendorName odpowiada Get-SdVendorName: producent z identyfikatora sprzętu (PCI\VEN_8086&... / USB\VID_...).
func vendorName(hwid string) string {
	d := data()
	if m := venRx.FindStringSubmatch(hwid); m != nil {
		if v, ok := d.pciVendors[strings.ToUpper(m[1])]; ok {
			return v
		}
	}
	if m := vidRx.FindStringSubmatch(hwid); m != nil {
		k := strings.ToUpper(m[1])
		if v, ok := d.usbVendors[k]; ok {
			return v
		}
		if v, ok := d.pciVendors[k]; ok {
			return v
		}
	}
	return ""
}

type bugcheckInfo struct {
	Code  string
	Name  string
	Cause string
	Hint  string
}

// bugcheckFor odpowiada Get-SdBugcheckInfo (kod jako liczba).
func bugcheckFor(num int64) bugcheckInfo {
	key := strings.ToUpper(strconv.FormatUint(uint64(num), 16)) // jak Int64.ToString("X")
	r := bugcheckInfo{Code: "0x" + pad8(key),
		Name: "Nieznany kod", Cause: "unknown", Hint: "Brak opisu w lokalnej bazie – wyszukaj kod STOP w dokumentacji Microsoft."}
	if e, ok := data().bugchecks[key]; ok {
		r.Name, r.Cause, r.Hint = e.Name, e.Cause, e.Hint
	}
	return r
}

func pad8(s string) string {
	for len(s) < 8 {
		s = "0" + s
	}
	return s
}

// bugcheckFromText: "0x000000d1" -> informacja o kodzie; ok = false, gdy tekst nie jest liczbą szesnastkową.
func bugcheckFromText(s string) (bugcheckInfo, bool) {
	n, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, 64)
	if err != nil {
		return bugcheckInfo{}, false
	}
	return bugcheckFor(int64(n)), true
}

// pnpErrorText odpowiada Get-SdPnpErrorText.
func pnpErrorText(code int) string {
	if t, ok := data().pnpErrors[strconv.Itoa(code)]; ok {
		return t
	}
	return "Kod błędu " + strconv.Itoa(code)
}

// eventHintFor odpowiada Get-SdEventHint (najpierw "Dostawca|Id", potem "*|Id"); klucze jak w hashtable
// PowerShella - bez rozróżniania wielkości liter.
func eventHintFor(provider string, id int) (eventHint, bool) {
	hints := data().hints
	for _, k := range []string{provider + "|" + strconv.Itoa(id), "*|" + strconv.Itoa(id)} {
		for hk, h := range hints {
			if strings.EqualFold(hk, k) {
				return h, true
			}
		}
	}
	return eventHint{}, false
}

// ---------------------------------------------------------------- aktualność bazy wiedzy (Get-SdKnowledgeInfo)

const knowledgeMaxDays = 183

type knowledgeState struct {
	Date    time.Time
	AgeDays int
	Stale   bool
	Text    string
}

// KnowledgeNote: ostrzeżenie o nieaktualnej bazie wiedzy do wyświetlenia na starcie (pusty napis, gdy aktualna).
func KnowledgeNote() string {
	if k := knowledgeInfo(time.Now()); k.Stale {
		return k.Text
	}
	return ""
}

func knowledgeInfo(now time.Time) knowledgeState {
	r := knowledgeState{AgeDays: -1, Text: "brak danych"}
	d, err := time.ParseInLocation("2006-01-02", data().lifecycle.Verified, time.Local)
	if err != nil {
		return r
	}
	r.Date = d
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	r.AgeDays = int(today.Sub(d).Hours() / 24)
	r.Stale = r.AgeDays > knowledgeMaxDays
	r.Text = "stan na " + d.Format("02.01.2006")
	if r.Stale {
		m := int(float64(r.AgeDays) / 30.44)
		r.Text += " - ponad " + fmtCount(m, "miesiąc", "miesiące", "miesięcy") + ", pobierz nową wersję programu"
	}
	return r
}

func parseDay(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", s, time.Local)
	return t
}
