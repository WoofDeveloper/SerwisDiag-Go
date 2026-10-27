package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func kindsOf(items []CompareItem) map[string]string {
	m := map[string]string{}
	for _, it := range items {
		m[it.Key] = it.Kind
	}
	return m
}

func TestCompareFindingLists(t *testing.T) {
	since := time.Date(2026, 10, 27, 10, 0, 0, 0, time.Local)
	before := []Finding{
		{CheckID: "c.disk", Title: "Dysk C:", Status: Crit, Value: "5% wolnego"},           // -> ok: resolved
		{CheckID: "c.disk", Title: "Dysk D:", Status: Warn, Value: "12% wolnego"},          // zniknął, test wykonany: resolved
		{CheckID: "c.av", Title: "Antywirus", Status: Warn, Value: "wyłączony"},            // test niewykonany: notrechecked
		{CheckID: "c.smart", Title: "SMART", Status: Crit, Value: "realokowane: 8"},        // -> warn: improved
		{CheckID: "c.temp", Title: "Temperatura", Status: Warn, Value: "85 °C"},            // -> crit: worsened
		{CheckID: "c.wu", Title: "Windows Update", Status: Warn, Value: "błąd 0x80070005"}, // -> warn: remaining
		{CheckID: "c.bsod", Title: "Awarie systemu", Status: Crit, Value: "2 awarie"},      // stare zdarzenie: nonew
		{CheckID: "c.net", Title: "Internet", Status: Warn, Value: "brak"},                 // -> skip: notrechecked
		{CheckID: "c.ram", Title: "Pamięć", Status: OK, Value: "16 GB"},                    // -> warn: new
		{CheckID: "c.cpu", Title: "Procesor", Status: Info, Value: "4 rdzenie"},            // bez zmian
		{CheckID: "c.dup", Title: "Wpis", Status: OK, Value: "a"},
		{CheckID: "c.dup", Title: "Wpis", Status: Warn, Value: "b"}, // drugi wpis o tym samym tytule: klucz #1
	}
	after := []Finding{
		{CheckID: "c.disk", Title: "Dysk C:", Status: OK, Value: "40% wolnego"},
		{CheckID: "c.smart", Title: "SMART", Status: Warn, Value: "realokowane: 8"},
		{CheckID: "c.temp", Title: "Temperatura", Status: Crit, Value: "95 °C"},
		{CheckID: "c.wu", Title: "Windows Update", Status: Warn, Value: "błąd 0x80070005"},
		{CheckID: "c.bsod", Title: "Awarie systemu", Status: Crit, Value: "2 awarie", LastEvent: since.Add(-2 * time.Hour)},
		{CheckID: "c.net", Title: "Internet", Status: Skip, Value: "tryb bez internetu"},
		{CheckID: "c.ram", Title: "Pamięć", Status: Warn, Value: "8 GB"},
		{CheckID: "c.cpu", Title: "Procesor", Status: Info, Value: "8 rdzeni"},
		{CheckID: "c.dup", Title: "Wpis", Status: OK, Value: "a"},
		{CheckID: "c.dup", Title: "WPIS", Status: OK, Value: "b"},
		{CheckID: "c.usb", Title: "Urządzenie USB", Status: Warn, Value: "błąd sterownika"}, // nowy test: new, nie sprawdzano
		{CheckID: "c.disk", Title: "Dysk E:", Status: Crit, Value: "1% wolnego"},            // nowy wynik testu wykonanego wcześniej
	}
	items := compareFindingLists(before, after, since)
	want := map[string]string{
		"c.disk|dysk c:": "resolved", "c.disk|dysk d:": "resolved", "c.av|antywirus": "notrechecked", "c.smart|smart": "improved",
		"c.temp|temperatura": "worsened", "c.wu|windows update": "remaining", "c.bsod|awarie systemu": "nonew", "c.net|internet": "notrechecked",
		"c.ram|pamięć": "new", "c.cpu|procesor": "unchanged", "c.dup|wpis": "unchanged", "c.dup|wpis#1": "resolved",
		"c.usb|urządzenie usb": "new", "c.disk|dysk e:": "new",
	}
	got := kindsOf(items)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, oczekiwano %q", k, got[k], v)
		}
	}
	if len(items) != len(want) {
		t.Errorf("wyników: %d, oczekiwano %d", len(items), len(want))
	}
	for _, it := range items {
		switch it.Key {
		case "c.usb|urządzenie usb":
			if !it.NotCheckedBefore || it.Before != nil {
				t.Errorf("nowy test: NotCheckedBefore=%v", it.NotCheckedBefore)
			}
		case "c.disk|dysk e:":
			if it.NotCheckedBefore {
				t.Error("test wykonany przed naprawą oznaczony jako niesprawdzany")
			}
		case "c.smart|smart":
			if it.Before == nil || it.After == nil || it.Before.Value != "realokowane: 8" {
				t.Error("brak wyników przed/po")
			}
		}
	}
	// bez daty początku diagnostyki „przed” stare zdarzenie to nadal problem bez zmian
	if k := kindsOf(compareFindingLists(before, after, time.Time{}))["c.bsod|awarie systemu"]; k != "remaining" {
		t.Errorf("bez since: %q", k)
	}
	c := compareCounts(items)
	if s := ComparisonSummaryText(c); s != "rozwiązane: 3, bez nowych zdarzeń: 1, złagodzone: 1, pogorszone: 1, bez zmian: 1, nowe: 3, nie sprawdzono ponownie: 2" {
		t.Errorf("podsumowanie: %s", s)
	}
	if s := ComparisonSummaryText(compareCounts(nil)); s != "brak różnic w problemach" {
		t.Errorf("podsumowanie puste: %s", s)
	}
	v := comparisonVerdict(items, c, false)
	if v.Level != Crit || v.Title != "Rozwiązano 3 z 9 problemów, bez nowych zdarzeń: 1" || !strings.Contains(v.Text, "nie powtórzyły") {
		t.Errorf("werdykt: %+v", v)
	}
	if v := comparisonVerdict(items, c, true); v.Total != 7 {
		t.Errorf("werdykt klienta liczy niesprawdzone: %d", v.Total)
	}
	one := compareFindingLists([]Finding{{CheckID: "a", Title: "A", Status: Warn}}, []Finding{{CheckID: "a", Title: "A", Status: OK}}, since)
	if v := comparisonVerdict(one, compareCounts(one), false); v.Level != OK || v.Title != "Rozwiązano 1 z 1 problemu" {
		t.Errorf("werdykt 1: %+v", v)
	}
	if v := comparisonVerdict(nil, compareCounts(nil), false); v.Title != "Przed naprawą nie wykryto problemów" || v.Level != OK {
		t.Errorf("werdykt bez problemów: %+v", v)
	}
}

const testReportJSON = `{
    "tool":  "SerwisDiag",
    "version":  "1.5.0",
    "generated":  "2026-10-27T10:05:00",
    "started":  "2026-10-27T10:00:00",
    "level":  "Full",
    "isAdmin":  true,
    "meta":  {
                 "ComputerName":  "PC-SERWIS",
                 "Serial":  "ABC123",
                 "Ticket":  "2026/014",
                 "Offline":  false,
                 "Ram":  16
             },
    "findings":  [
                     {
                         "id":  "c.disk",
                         "category":  "storage",
                         "title":  "Dysk C:",
                         "status":  "CRIT",
                         "value":  "5% wolnego",
                         "details":  "szczegóły <b>",
                         "advice":  "Zwolnij miejsce",
                         "private":  false,
                         "customer":  {
                                          "title":  "Dysk systemowy",
                                          "status":  "warn",
                                          "value":  "mało miejsca",
                                          "advice":  "Usuń zbędne pliki"
                                      }
                     },
                     {
                         "id":  "c.user",
                         "category":  "system",
                         "title":  "Konto",
                         "status":  "info",
                         "value":  "jan",
                         "details":  "",
                         "advice":  "",
                         "private":  true,
                         "customer":  null
                     },
                     null,
                     {
                         "id":  "c.bsod",
                         "category":  "events",
                         "title":  "Awarie systemu",
                         "status":  "crit",
                         "value":  2,
                         "private":  false,
                         "customer":  null,
                         "lastEvent":  "2026-10-26T08:00:00"
                     }
                 ]
}`

func TestImportReportJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "raport.json")
	if err := os.WriteFile(p, append(append([]byte{}, bom...), testReportJSON...), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ImportReportJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Level != "Full" || !r.IsAdmin || r.Version != "1.5.0" || r.Started.Hour() != 10 || r.Generated.Minute() != 5 {
		t.Errorf("nagłówek: %+v", r)
	}
	if metaStr(r.Meta, "Ticket") != "2026/014" || metaStr(r.Meta, "Ram") != "16" {
		t.Errorf("meta: %v", r.Meta)
	}
	if len(r.Findings) != 3 {
		t.Fatalf("wyników: %d", len(r.Findings))
	}
	f := r.Findings[0]
	if f.Status != Crit || f.Customer == nil || f.Customer.Title != "Dysk systemowy" || f.Customer.Status != Warn || f.Details != "szczegóły <b>" {
		t.Errorf("wynik 1: %+v %+v", f, f.Customer)
	}
	if !r.Findings[1].Private || r.Findings[1].Customer != nil {
		t.Errorf("wynik prywatny: %+v", r.Findings[1])
	}
	if b := r.Findings[2]; b.Value != "2" || b.LastEvent.IsZero() || b.LastEvent.Day() != 26 {
		t.Errorf("wynik z historii zdarzeń: %+v", b)
	}
	// dziennik napraw i zwykły plik JSON to nie raporty
	rl := filepath.Join(dir, "PC_20261027-1010_naprawy.json")
	_ = os.WriteFile(rl, []byte(`{"tool":"SerwisDiag","version":"1.5.0","kind":"repair-log","results":[]}`), 0o644)
	if _, err := ImportReportJSON(rl); err == nil || !strings.Contains(err.Error(), "To nie jest raport JSON programu SerwisDiag") {
		t.Errorf("dziennik napraw: %v", err)
	}
	bad := filepath.Join(dir, "zly.json")
	_ = os.WriteFile(bad, []byte(`[1,2`), 0o644)
	if _, err := ImportReportJSON(bad); err == nil || !strings.Contains(err.Error(), "To nie jest raport") {
		t.Errorf("uszkodzony plik: %v", err)
	}
	if _, err := ImportReportJSON(filepath.Join(dir, "brak.json")); err == nil || !strings.HasPrefix(err.Error(), "Nie znaleziono pliku: ") {
		t.Errorf("brak pliku: %v", err)
	}
}

// TestReportJSONRoundTrip: raport zapisany przez program wczytuje się z tymi samymi wynikami.
func TestReportJSONRoundTrip(t *testing.T) {
	r := &Result{Tool: "SerwisDiag", Version: Version, Generated: time.Date(2026, 10, 27, 11, 0, 0, 0, time.Local), Started: time.Date(2026, 10, 27, 10, 58, 0, 0, time.Local),
		Level: "Quick", Meta: map[string]any{"ComputerName": "PC", "Serial": "S1", "Offline": true},
		Findings: []Finding{{CheckID: "a", Category: "system", Title: "A „cudzysłów”", Status: Warn, Value: "x", Details: "d\nd2", Advice: "r", Customer: &Customer{Title: "K"}},
			{CheckID: "b", Category: "events", Title: "B", Status: Crit, Value: "1", LastEvent: time.Date(2026, 10, 20, 7, 30, 0, 0, time.Local)}}}
	r.Counts = countFindings(r.Findings)
	r.Verdict = verdictFor(r.Counts)
	j, err := JSONReport(r, true)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, j, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ImportReportJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Generated.Equal(r.Generated) || !g.Started.Equal(r.Started) || g.Level != "Quick" || !jsonBool(g.Meta["Offline"]) {
		t.Errorf("nagłówek: %+v", g)
	}
	for i, f := range r.Findings {
		h := g.Findings[i]
		if h.CheckID != f.CheckID || h.Title != f.Title || h.Status != f.Status || h.Details != f.Details || !h.LastEvent.Equal(f.LastEvent) || (f.Customer != nil) != (h.Customer != nil) {
			t.Errorf("wynik %d: %+v / %+v", i, h, f)
		}
	}
}

func TestNewComparisonNotesAndHTML(t *testing.T) {
	before := &Result{Generated: time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local), Level: "Full", IsAdmin: true,
		Meta: map[string]any{"ComputerName": "PC1", "Serial": "AAA", "Offline": false, "Ticket": "Z/1", "Model": "Dell"},
		Findings: []Finding{
			{CheckID: "c.disk", Category: "storage", Title: "Dysk C:", Status: Crit, Value: "5%", Advice: "Zwolnij miejsce"},
			{CheckID: "c.av", Category: "security", Title: "Antywirus", Status: Warn, Value: "wyłączony"},
			{CheckID: "c.user", Category: "system", Title: "Konto", Status: Warn, Value: "jan", Private: true},
			{CheckID: "c.cpu", Category: "hardware", Title: "Procesor", Status: Info, Value: "4 rdzenie"},
		}}
	after := &Result{Generated: time.Date(2026, 10, 27, 11, 0, 0, 0, time.Local), Level: "Quick", IsAdmin: false,
		Meta: map[string]any{"ComputerName": "PC1", "Serial": "BBB", "Offline": true, "Customer": "Jan <Kowalski>"},
		Findings: []Finding{
			{CheckID: "c.disk", Category: "storage", Title: "Dysk C:", Status: Warn, Value: "15%", Advice: "Rozważ większy dysk", Details: "tylko dla technika"},
			{CheckID: "c.user", Category: "system", Title: "Konto", Status: Warn, Value: "jan", Private: true},
			{CheckID: "c.cpu", Category: "hardware", Title: "Procesor", Status: Info, Value: "8 rdzeni"},
		}}
	c := NewComparison(before, after, "Wymiana dysku\nCzyszczenie & pasta")
	if c.SameComputer == nil || *c.SameComputer {
		t.Error("inny numer seryjny to inny komputer (nazwa nie ma znaczenia)")
	}
	if len(c.Notes) != 5 {
		t.Errorf("uwagi: %d %v", len(c.Notes), c.Notes)
	}
	tech := ComparisonHTML(c, false, Settings{ShopName: "Serwis Test"})
	cust := ComparisonHTML(c, true, Settings{})
	for _, s := range []string{"Por&#243;wnanie diagnostyki przed i po naprawie - PC1", "Uwagi do porównania", "Złagodzone problemy (1)", "Nie sprawdzono ponownie",
		"Inne zmienione wyniki (1)", "<details><summary>szczegóły</summary>", "Wymiana dysku<br>Czyszczenie &amp; pasta", "Klient: Jan &lt;Kowalski&gt;<br>",
		"Zlecenie: Z/1<br>", "Tryb (przed / po)</b><span>pełny / szybki", "Serwis Test", "<b>Konto</b>", "Zalecenie:</b> Rozważ większy dysk"} {
		if !strings.Contains(tech, s) {
			t.Errorf("raport techniczny bez %q", s)
		}
	}
	for _, s := range []string{"Uwagi do porównania", "Nie sprawdzono ponownie", "Inne zmienione wyniki", "tylko dla technika", "<b>Konto</b>", "Tryb (przed / po)"} {
		if strings.Contains(cust, s) {
			t.Errorf("raport dla klienta zawiera %q", s)
		}
	}
	for _, s := range []string{"Raport z naprawy komputera", "Serwis komputerowy", "Rozwiązano 0 z 1 problemu", "Raport por&#243;wnuje wyniki diagnostyki"} {
		if !strings.Contains(cust, s) {
			t.Errorf("raport dla klienta bez %q", s)
		}
	}
	// ten sam komputer, ta sama kolejność i tryb: bez uwag
	after.Generated, after.Level, after.IsAdmin, after.Meta["Serial"], after.Meta["Offline"] = before.Generated.Add(time.Hour), "full", true, "aaa", false
	if c := NewComparison(before, after, ""); len(c.Notes) != 0 || c.SameComputer == nil || !*c.SameComputer {
		t.Errorf("uwagi: %v", c.Notes)
	}
	empty := &Result{Meta: map[string]any{}}
	if c := NewComparison(empty, empty, ""); c.SameComputer != nil || !strings.Contains(ComparisonHTML(c, true, Settings{}), "Ani przed, ani po naprawie nie wykryto problemów") {
		t.Error("porównanie bez wyników")
	}
}

func TestFindReportJSON(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	files := []struct {
		name string
		age  time.Duration
	}{
		{"PC-1_20261027-0900.json", 5 * time.Hour},
		{"Z_1_PC-1_20261027-1000.json", 4 * time.Hour},
		{"PC-1_20261027-1100.json", 3 * time.Hour},
		{"PC-1_20261027-1200_naprawy.json", 2 * time.Hour},
		{"PC-10_20261027-1300.json", 1 * time.Hour},
		{"INNY_20261027-1300.json", 0},
		{"PC-1_20261027-1100_techniczny.html", 0},
	}
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, now.Add(-f.age), now.Add(-f.age))
	}
	j := func(n string) string { return filepath.Join(dir, n) }
	if got := FindReportJSON(dir, "pc-1", ""); got != j("PC-1_20261027-1100.json") {
		t.Errorf("najnowszy: %s", got)
	}
	if got := FindReportJSON(dir, "PC-1", "Z 1"); got != j("Z_1_PC-1_20261027-1000.json") {
		t.Errorf("ze zleceniem: %s", got)
	}
	if got := FindReportJSON(dir, "PC-1", "", strings.ToUpper(j("PC-1_20261027-1100.json"))); got != j("Z_1_PC-1_20261027-1000.json") {
		t.Errorf("z pominięciem: %s", got)
	}
	if got := FindReportJSON(dir, "BRAK", ""); got != "" {
		t.Errorf("inny komputer: %s", got)
	}
	if got := FindReportJSON(filepath.Join(dir, "nie-ma"), "PC-1", ""); got != "" {
		t.Errorf("brak folderu: %s", got)
	}
}
