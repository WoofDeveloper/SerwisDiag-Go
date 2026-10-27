package diag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Porównanie dwóch diagnostyk tego samego komputera: przed naprawą i po niej (lib\Compare.ps1).
// Źródłem są raporty JSON zapisywane przy każdej diagnostyce (albo wynik bieżącego skanowania).
// Wynik: raport techniczny (wszystkie różnice) i raport dla klienta (bez danych prywatnych, z opisem wykonanych czynności).

// compareKinds - rodzaje zmian w kolejności z $SdCompareKinds.
var compareKinds = []struct{ key, label string }{
	{"resolved", "Rozwiązane"}, {"nonew", "Bez nowych zdarzeń"}, {"improved", "Złagodzone"}, {"worsened", "Pogorszone"},
	{"remaining", "Bez zmian"}, {"new", "Nowe"}, {"notrechecked", "Nie sprawdzono ponownie"},
}

// parseJSONDate odpowiada ConvertFrom-SdJsonDate: data z raportu JSON (format 's', czas lokalny); zero - brak daty.
func parseJSONDate(v any) time.Time {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "01/02/2006 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Local()
	}
	return time.Time{}
}

// jsonStr: wartość z JSON jak [string] w PowerShellu.
func jsonStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return htmlString(x)
	}
	return win.AnyString(v)
}

// jsonBool: wartość z JSON jak [bool] w PowerShellu.
func jsonBool(v any) bool {
	switch x := v.(type) {
	case []any:
		if len(x) == 1 {
			return jsonBool(x[0])
		}
		return len(x) > 1
	case map[string]any:
		return true
	}
	return win.AnyBool(v)
}

// ImportReportJSON odpowiada Import-SdReportJson: raport JSON programu (także wersji PowerShell) jako wynik
// diagnostyki - Meta, Findings, Level, IsAdmin, Generated, Started.
func ImportReportJSON(path string) (*Result, error) {
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return nil, errText("Nie znaleziono pliku: " + path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	notReport := errText("To nie jest raport JSON programu SerwisDiag: " + path)
	var o map[string]any
	if err := json.Unmarshal(decodeText(b), &o); err != nil || o == nil {
		return nil, notReport
	}
	if !strings.EqualFold(jsonStr(o["tool"]), "SerwisDiag") || o["findings"] == nil {
		return nil, notReport
	}
	r := &Result{Tool: "SerwisDiag", Version: jsonStr(o["version"]), Generated: parseJSONDate(o["generated"]), Started: parseJSONDate(o["started"]),
		Level: jsonStr(o["level"]), IsAdmin: jsonBool(o["isAdmin"]), Meta: map[string]any{}}
	if m, ok := o["meta"].(map[string]any); ok {
		for k, v := range m {
			r.Meta[k] = v
		}
	}
	list, ok := o["findings"].([]any)
	if !ok {
		list = []any{o["findings"]} // @($o.findings): pojedynczy obiekt jako lista
	}
	for _, x := range list {
		f, ok := x.(map[string]any)
		if !ok {
			continue
		}
		fd := Finding{CheckID: jsonStr(f["id"]), Category: jsonStr(f["category"]), Title: jsonStr(f["title"]), Status: Status(strings.ToLower(jsonStr(f["status"]))),
			Value: jsonStr(f["value"]), Details: jsonStr(f["details"]), Advice: jsonStr(f["advice"]), Private: jsonBool(f["private"])}
		if c, ok := f["customer"]; ok && jsonBool(c) {
			cm, _ := c.(map[string]any)
			fd.Customer = &Customer{Title: jsonStr(cm["title"]), Status: Status(strings.ToLower(jsonStr(cm["status"]))), Value: jsonStr(cm["value"]), Advice: jsonStr(cm["advice"])}
		}
		if le, ok := f["lastEvent"]; ok {
			fd.LastEvent = parseJSONDate(le)
		}
		r.Findings = append(r.Findings, fd)
	}
	return r, nil
}

func isProblemStatus(s Status) bool { return s == Crit || s == Warn }

// CompareItem - porównanie jednego wyniku: resolved | nonew | improved | worsened | remaining | new | notrechecked | unchanged.
type CompareItem struct {
	Key, Kind, Category, Title string
	Before, After              *Finding
	NotCheckedBefore           bool
}

type keyedFinding struct {
	key string
	f   *Finding
}

// findingKeys odpowiada Get-SdFindingKeys: Id testu + tytuł (bez wielkości liter); powtórzony tytuł w jednym
// teście dostaje numer wystąpienia.
func findingKeys(fs []Finding) []keyedFinding {
	seen := map[string]int{}
	out := make([]keyedFinding, 0, len(fs))
	for i := range fs {
		k := strings.ToLower(fs[i].CheckID + "|" + fs[i].Title)
		n := seen[k]
		seen[k] = n + 1
		if n > 0 {
			k += "#" + strconv.Itoa(n)
		}
		out = append(out, keyedFinding{k, &fs[i]})
	}
	return out
}

// compareFindingLists odpowiada Compare-SdFindingLists. Problem, którego wynik zniknął, uznajemy za rozwiązany
// tylko wtedy, gdy ten sam test wykonano ponownie. since: początek diagnostyki przed naprawą - wynik z historii
// zdarzeń, którego ostatnie zdarzenie jest wcześniejsze, zostaje w raporcie tylko dlatego, że mieści się
// w sprawdzanym okresie (np. 30 dni) - to nonew, a nie problem bez zmian.
func compareFindingLists(before, after []Finding, since time.Time) []CompareItem {
	b, a := findingKeys(before), findingKeys(after)
	aMap, bMap := map[string]*Finding{}, map[string]*Finding{}
	aChecks, bChecks := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		aMap[x.key] = x.f
		aChecks[strings.ToLower(x.f.CheckID)] = true
	}
	for _, x := range b {
		bMap[x.key] = x.f
		bChecks[strings.ToLower(x.f.CheckID)] = true
	}
	items := make([]CompareItem, 0, len(b)+len(a))
	for _, x := range b {
		bf, af := x.f, aMap[x.key]
		kind := "unchanged"
		if isProblemStatus(bf.Status) {
			switch {
			case af == nil:
				kind = "notrechecked"
				if aChecks[strings.ToLower(bf.CheckID)] {
					kind = "resolved"
				}
			case af.Status == Skip || af.Status == Err:
				kind = "notrechecked"
			case !isProblemStatus(af.Status):
				kind = "resolved"
			case bf.Status == Crit && af.Status == Warn:
				kind = "improved"
			case bf.Status == Warn && af.Status == Crit:
				kind = "worsened"
			default:
				kind = "remaining"
			}
			if af != nil && !since.IsZero() && !af.LastEvent.IsZero() && !af.LastEvent.After(since) &&
				(kind == "improved" || kind == "worsened" || kind == "remaining") {
				kind = "nonew"
			}
		} else if af != nil && isProblemStatus(af.Status) {
			kind = "new"
		}
		ref := bf
		if af != nil {
			ref = af
		}
		items = append(items, CompareItem{Key: x.key, Kind: kind, Category: ref.Category, Title: ref.Title, Before: bf, After: af})
	}
	for _, x := range a {
		if _, ok := bMap[x.key]; ok {
			continue
		}
		kind := "unchanged"
		if isProblemStatus(x.f.Status) {
			kind = "new"
		}
		items = append(items, CompareItem{Key: x.key, Kind: kind, Category: x.f.Category, Title: x.f.Title, After: x.f,
			NotCheckedBefore: !bChecks[strings.ToLower(x.f.CheckID)]})
	}
	return items
}

func compareCounts(items []CompareItem) map[string]int {
	c := map[string]int{}
	for _, k := range compareKinds {
		c[k.key] = 0
	}
	for _, it := range items {
		if _, ok := c[it.Kind]; ok {
			c[it.Kind]++
		}
	}
	return c
}

// ComparisonSummaryText odpowiada Get-SdComparisonSummaryText, np. „rozwiązane: 3, bez zmian: 1”.
func ComparisonSummaryText(counts map[string]int) string {
	var parts []string
	for _, k := range compareKinds {
		if n := counts[k.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", strings.ToLower(k.label), n))
		}
	}
	if len(parts) == 0 {
		return "brak różnic w problemach"
	}
	return strings.Join(parts, ", ")
}

func levelText(level string) string {
	if strings.EqualFold(level, "Full") {
		return "pełny"
	}
	return "szybki"
}

// Comparison - porównanie diagnostyki przed naprawą i po niej (New-SdComparison).
type Comparison struct {
	Before, After          *Result
	WorkDone               string
	Generated              time.Time
	Items, CustomerItems   []CompareItem
	Counts, CustomerCounts map[string]int
	SameComputer           *bool // nil - nie wiadomo (raporty bez numeru seryjnego i nazwy komputera)
	Notes                  []string
}

// NewComparison odpowiada New-SdComparison; before / after: wynik bieżącej diagnostyki albo ImportReportJSON.
func NewComparison(before, after *Result, workDone string) *Comparison {
	// zdarzenia „nowe” to te od początku diagnostyki przed naprawą (raport bez tej daty: czas jego zapisu)
	since := before.Started
	if since.IsZero() {
		since = before.Generated
	}
	c := &Comparison{Before: before, After: after, WorkDone: workDone, Generated: time.Now()}
	c.Items = compareFindingLists(before.Findings, after.Findings, since)
	c.CustomerItems = compareFindingLists(customerFindings(before.Findings), customerFindings(after.Findings), since)
	c.Counts, c.CustomerCounts = compareCounts(c.Items), compareCounts(c.CustomerItems)
	bm, am := before.Meta, after.Meta
	for _, k := range []string{"Serial", "ComputerName"} {
		if b, a := metaStr(bm, k), metaStr(am, k); b != "" && a != "" {
			same := strings.EqualFold(b, a)
			c.SameComputer = &same
			break
		}
	}
	if c.SameComputer != nil && !*c.SameComputer {
		c.Notes = append(c.Notes, "Raporty pochodzą z różnych komputerów (inna nazwa lub numer seryjny) - sprawdź, czy wybrano właściwe pliki.")
	}
	if !before.Generated.IsZero() && !after.Generated.IsZero() && after.Generated.Before(before.Generated) {
		c.Notes = append(c.Notes, "Raport „po naprawie” jest starszy niż raport „przed naprawą” - sprawdź kolejność plików.")
	}
	if !strings.EqualFold(before.Level, after.Level) {
		c.Notes = append(c.Notes, fmt.Sprintf("Inny tryb diagnostyki (przed: %s, po: %s) - część testów wykonano tylko w jednej z nich.", levelText(before.Level), levelText(after.Level)))
	}
	if before.IsAdmin != after.IsAdmin {
		c.Notes = append(c.Notes, "Jedną z diagnostyk wykonano bez uprawnień administratora - część testów pominięto.")
	}
	if win.AnyBool(bm["Offline"]) != win.AnyBool(am["Offline"]) {
		c.Notes = append(c.Notes, "Jedną z diagnostyk wykonano w trybie bez internetu - testy łączności i wyszukiwanie sterowników pominięto.")
	}
	return c
}

type compareVerdict struct {
	Level       Status
	Title, Text string
	Total       int
}

// comparisonVerdict odpowiada Get-SdComparisonVerdict: ile problemów sprzed naprawy rozwiązano i czy coś zostało.
// customer: raport dla klienta nie pokazuje wyników niesprawdzonych ponownie, więc nie liczy ich też w tytule.
func comparisonVerdict(items []CompareItem, counts map[string]int, customer bool) compareVerdict {
	kinds := []string{"resolved", "nonew", "improved", "worsened", "remaining"}
	if !customer {
		kinds = append(kinds, "notrechecked")
	}
	v := compareVerdict{Level: OK}
	for _, k := range kinds {
		v.Total += counts[k]
	}
	for _, it := range items {
		if !slices.Contains([]string{"improved", "worsened", "remaining", "new"}, it.Kind) {
			continue
		}
		if it.After != nil && it.After.Status == Crit {
			v.Level = Crit
			break
		}
		v.Level = Warn
	}
	if v.Total == 0 {
		v.Title = "Przed naprawą nie wykryto problemów"
	} else {
		word := "problemów"
		if v.Total == 1 {
			word = "problemu"
		}
		v.Title = fmt.Sprintf("Rozwiązano %d z %d %s", counts["resolved"], v.Total, word)
		if n := counts["nonew"]; n > 0 {
			v.Title += ", bez nowych zdarzeń: " + strconv.Itoa(n)
		}
	}
	switch v.Level {
	case Crit:
		v.Text = "Część problemów nadal wymaga pilnej interwencji - szczegóły poniżej."
	case Warn:
		v.Text = "Pozostały zalecenia do wykonania - szczegóły poniżej."
	default:
		v.Text = "Po naprawie nie wykryto problemów wymagających działania."
	}
	if counts["nonew"] > 0 {
		v.Text += " Zdarzenia zapisane w dziennikach Windows przed naprawą (np. restarty, błędy dysku) od tamtej pory się nie powtórzyły."
	}
	return v
}

// ComparisonHTML odpowiada New-SdComparisonHtml (customer = raport dla klienta).
func ComparisonHTML(c *Comparison, customer bool, settings Settings) string {
	E := func(v any) string { return toHTML(v) }
	items, counts := c.Items, c.Counts
	if customer {
		items, counts = c.CustomerItems, c.CustomerCounts
	}
	am, bm := c.After.Meta, c.Before.Meta
	mv := func(k string) string {
		if v := metaStr(am, k); v != "" {
			return v
		}
		return metaStr(bm, k)
	}
	labels := map[Status]string{Crit: "KRYTYCZNE", Warn: "UWAGA", OK: "OK", Info: "INFO", Skip: "POMINIĘTO", Err: "BŁĄD TESTU"}
	if customer {
		labels = map[Status]string{Crit: "POWAŻNY PROBLEM", Warn: "ZALECENIE", OK: "OK", Info: "INFORMACJA", Skip: "POMINIĘTO", Err: "BŁĄD TESTU"}
	}
	verdict := comparisonVerdict(items, counts, customer)
	shop := settings.ShopName
	if shop == "" {
		shop = "Serwis komputerowy"
	}
	title := "Porównanie diagnostyki przed i po naprawie"
	if customer {
		title = "Raport z naprawy komputera"
	}
	order := map[string]int{}
	for i, cat := range Categories {
		order[strings.ToLower(cat.Key)] = i
	}
	sortItems := func(list []CompareItem) []CompareItem {
		crit := func(it CompareItem) int {
			if it.After != nil && it.After.Status == Crit {
				return 0
			}
			return 1
		}
		ord := func(it CompareItem) int {
			if o, ok := order[strings.ToLower(it.Category)]; ok {
				return o
			}
			return 99
		}
		psSort(list, func(a, b CompareItem) int {
			if r := cmpNum(crit(a), crit(b)); r != 0 {
				return r
			}
			return cmpNum(ord(a), ord(b))
		})
		return list
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="pl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	sb.WriteString(`<title>` + E(title+" - "+mv("ComputerName")) + `</title><style>` + inlineCSS)
	sb.WriteString(`.ba{display:grid;grid-template-columns:1fr 1fr;gap:6px 14px;margin-top:6px;font-size:13px}.ba div{padding:6px 9px;border-radius:7px;background:#f8f9fb}.ba b{display:block;color:#667085;font-size:11.5px;font-weight:600}`)
	sb.WriteString(`.issue.resolved{border-left-color:#12b76a;background:#fbfffc}.issue.info{border-left-color:#2e90fa}.work{white-space:pre-wrap}@media (max-width:760px){.ba{grid-template-columns:1fr}}`)
	sb.WriteString(`</style></head><body data-show="all"><div class="wrap">`)

	// --- nagłówek
	sb.WriteString(`<header class="top"><div>` + logoHTML(settings) + `<h1>` + E(title) + `</h1><div class="sub">` + E(shop) + `</div>`)
	if contact := joinNonEmpty(" · ", settings.ShopPhone, settings.ShopEmail, settings.ShopAddress); contact != "" {
		sb.WriteString(`<div class="contact">` + E(contact) + `</div>`)
	}
	sb.WriteString(`</div><div class="meta">`)
	sb.WriteString("Data: " + fmtDt(c.Generated) + "<br>")
	for _, p := range [][2]string{{"Zlecenie", "Ticket"}, {"Klient", "Customer"}, {"Technik", "Technician"}} {
		if v := mv(p[1]); v != "" {
			sb.WriteString(p[0] + ": " + E(v) + "<br>")
		}
	}
	sb.WriteString(`</div></header>`)

	// --- podsumowanie
	sb.WriteString(`<section class="card verdict ` + string(verdict.Level) + `"><div><div class="big">` + E(verdict.Title) + `</div><div class="txt">` + E(verdict.Text) + `</div></div><div class="counters">`)
	chips := [][3]string{{"resolved", "ok", "rozwiązane"}, {"nonew", "info", "bez nowych zdarzeń"}, {"improved", "info", "złagodzone"}, {"remaining", "warn", "bez zmian"},
		{"worsened", "crit", "pogorszone"}, {"new", "warn", "nowe"}}
	if !customer {
		chips = append(chips, [3]string{"notrechecked", "skip", "nie sprawdzono ponownie"})
	}
	for _, ch := range chips {
		if n := counts[ch[0]]; n > 0 {
			sb.WriteString(`<span class="chip ` + ch[1] + `">` + strconv.Itoa(n) + ` ` + ch[2] + `</span>`)
		}
	}
	sb.WriteString(`</div></section>`)

	// --- wykonane czynności i uwagi
	if c.WorkDone != "" {
		sb.WriteString(`<section class="card"><h2>Wykonane czynności</h2><div class="work">` + E(c.WorkDone) + `</div></section>`)
	}
	if !customer && len(c.Notes) > 0 {
		sb.WriteString(`<section class="card"><h2>Uwagi do porównania</h2><ul>`)
		for _, n := range c.Notes {
			sb.WriteString(`<li>` + E(n) + `</li>`)
		}
		sb.WriteString(`</ul></section>`)
	}

	// --- dane komputera i daty diagnostyk
	sb.WriteString(`<section class="card"><h2>Komputer</h2><div class="facts">`)
	facts := [][2]string{{"Komputer", mv("Model")}, {"System", mv("OS")}, {"Procesor", mv("Cpu")}, {"Pamięć RAM", mv("Ram")}, {"Dyski", mv("Disks")},
		{"Diagnostyka przed naprawą", fmtDt(c.Before.Generated)}, {"Diagnostyka po naprawie", fmtDt(c.After.Generated)}}
	if !customer {
		facts = append(facts, [2]string{"Nazwa komputera", mv("ComputerName")},
			[2]string{"Tryb (przed / po)", levelText(c.Before.Level) + " / " + levelText(c.After.Level)})
	}
	for _, f := range facts {
		if f[1] != "" && f[1] != "-" {
			sb.WriteString(`<div><b>` + E(f[0]) + `</b><span>` + E(f[1]) + `</span></div>`)
		}
	}
	sb.WriteString(`</div></section>`)

	// --- sekcje z problemami
	card := func(it CompareItem, cls string, showAdvice bool) string {
		st := Status("")
		if it.After != nil {
			st = it.After.Status
		} else if it.Before != nil {
			st = it.Before.Status
		}
		badgeSt, badge, issueCls := string(st), labels[st], string(st)
		switch cls {
		case "resolved":
			badgeSt, badge, issueCls = "ok", "ROZWIĄZANE", "resolved"
		case "nonew":
			badgeSt, badge, issueCls = "info", "BEZ NOWYCH ZDARZEŃ", "info"
		}
		h := `<div class="issue ` + issueCls + `"><div class="h"><span class="badge ` + badgeSt + `">` + badge + `</span><b>` + E(it.Title) +
			`</b><span style="color:#667085;font-size:12px">` + E(categoryLabel(it.Category)) + `</span></div>`
		bv, av := "bez problemu", "nie sprawdzono"
		switch {
		case it.Before != nil:
			bv = it.Before.Value
		case it.NotCheckedBefore:
			bv = "nie sprawdzano"
		}
		switch {
		case it.After != nil:
			av = it.After.Value
		case cls == "resolved":
			av = "problem nie występuje"
		}
		h += `<div class="ba"><div><b>Przed naprawą</b>` + E(bv) + `</div><div><b>Po naprawie</b>` + E(av) + `</div></div>`
		if showAdvice && it.After != nil && it.After.Advice != "" {
			h += `<div class="adv"><b>Zalecenie:</b> ` + E(it.After.Advice) + `</div>`
		}
		if !customer && it.After != nil && it.After.Details != "" && cls != "resolved" {
			h += `<details><summary>szczegóły</summary><div class="det">` + E(it.After.Details) + `</div></details>`
		}
		return h + `</div>`
	}
	type section struct {
		kinds      []string
		title, cls string
		advice     bool
		note       string
	}
	remainingTitle, newTitle := "Bez poprawy (nadal wymagają uwagi)", "Nowe problemy (nie było ich przed naprawą)"
	if customer {
		remainingTitle, newTitle = "Nadal wymagają uwagi", "Wykryte podczas kontroli po naprawie"
	}
	sections := []section{
		{kinds: []string{"resolved"}, title: "Rozwiązane problemy", cls: "resolved"},
		{kinds: []string{"nonew"}, title: "Bez nowych zdarzeń od poprzedniej diagnostyki", cls: "nonew",
			note: "Te wyniki opisują zdarzenia zapisane w dziennikach Windows przed naprawą. Zostają w raporcie przez okres, który sprawdza program (np. 30 dni), ale od diagnostyki przed naprawą nie wystąpiły ponownie."},
		{kinds: []string{"improved"}, title: "Złagodzone problemy", cls: "improved", advice: true},
		{kinds: []string{"worsened", "remaining"}, title: remainingTitle, cls: "remaining", advice: true},
		{kinds: []string{"new"}, title: newTitle, cls: "new", advice: true},
	}
	if !customer {
		sections = append(sections, section{kinds: []string{"notrechecked"}, title: "Nie sprawdzono ponownie (test pominięty lub niewykonany po naprawie)", cls: "notrechecked"})
	}
	for _, s := range sections {
		var list []CompareItem
		for _, it := range items {
			if slices.Contains(s.kinds, it.Kind) {
				list = append(list, it)
			}
		}
		if len(list) == 0 {
			continue
		}
		list = sortItems(list)
		sb.WriteString(`<section class="card"><h2>` + E(s.title) + ` (` + strconv.Itoa(len(list)) + `)</h2>`)
		if s.note != "" {
			sb.WriteString(`<div class="txt">` + E(s.note) + `</div>`)
		}
		for _, it := range list {
			sb.WriteString(card(it, s.cls, s.advice))
		}
		sb.WriteString(`</section>`)
	}
	if !slices.ContainsFunc(items, func(it CompareItem) bool { return it.Kind != "unchanged" }) {
		sb.WriteString(`<section class="card"><div class="txt">Ani przed, ani po naprawie nie wykryto problemów wymagających działania.</div></section>`)
	}

	// --- technik: pozostałe zmiany wartości (np. wolne miejsce, wyniki testów wydajności)
	if !customer {
		var changed []CompareItem
		for _, it := range items {
			if it.Kind == "unchanged" && it.Before != nil && it.After != nil && !strings.EqualFold(it.Before.Value, it.After.Value) {
				changed = append(changed, it)
				if len(changed) == 120 {
					break
				}
			}
		}
		if len(changed) > 0 {
			sb.WriteString(`<section class="card"><details class="tbl"><summary>Inne zmienione wyniki (` + strconv.Itoa(len(changed)) + `)</summary><div class="tw"><table><thead><tr><th>Test</th><th>Przed</th><th>Po</th></tr></thead><tbody>`)
			for _, it := range changed {
				sb.WriteString(`<tr><td class="sm">` + E(it.Title) + `</td><td class="sm">` + E(it.Before.Value) + `</td><td class="sm">` + E(it.After.Value) + `</td></tr>`)
			}
			sb.WriteString(`</tbody></table></div></details></section>`)
		}
	}
	foot := "Porównanie: " + ComparisonSummaryText(counts) + "."
	if customer {
		foot = "Raport porównuje wyniki diagnostyki wykonanej przed naprawą i po niej. Opiera się na odczytach systemu w chwili badania."
	}
	sb.WriteString(`<footer>` + E(foot) + ` Wygenerowano programem SerwisDiag ` + E(Version) + `.</footer>`)
	sb.WriteString(`</div><script>` + reportJS + `</script></body></html>`)
	return sb.String()
}

// ComparisonPaths - ścieżki zapisanego porównania.
type ComparisonPaths struct {
	Technical, Customer string
}

// SaveComparisonReports odpowiada Save-SdComparisonReports: porównanie techniczne i dla klienta.
func SaveComparisonReports(c *Comparison, settings Settings, outDir string) (ComparisonPaths, error) {
	m := c.After.Meta
	base := joinNonEmpty("_", metaStr(m, "Ticket"), metaStr(m, "ComputerName"), time.Now().Format("20060102-1504"))
	base = unsafeNameRx.ReplaceAllString(base, "_") + "_porownanie"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return ComparisonPaths{}, err
	}
	p := ComparisonPaths{Technical: filepath.Join(outDir, base+"_techniczne.html"), Customer: filepath.Join(outDir, base+"_klient.html")}
	if err := writeText(p.Technical, ComparisonHTML(c, false, settings), true); err != nil {
		return p, err
	}
	return p, writeText(p.Customer, ComparisonHTML(c, true, settings), true)
}

// FindReportJSON odpowiada Find-SdReportJson: najnowszy raport JSON tego komputera w folderze raportów (najpierw
// z tym samym numerem zlecenia), inny niż exclude. Nazwy plików mają postać [zlecenie_]komputer_rrrrMMdd-ggmm.json,
// więc wystarczy dopasować nazwę, bez czytania plików. Pomijamy dzienniki napraw (*_naprawy.json) - w wersji
// PowerShell najnowszy plik po naprawach to dziennik, a nie raport sprzed naprawy.
func FindReportJSON(dir, computer, ticket string, exclude ...string) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	pc := strings.ToLower(unsafeNameRx.ReplaceAllString(computer, "_"))
	type cand struct {
		path, base string
		mod        time.Time
	}
	var files []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		p := filepath.Join(dir, name)
		if slices.ContainsFunc(exclude, func(x string) bool { return x != "" && strings.EqualFold(filepath.Clean(x), p) }) {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if strings.HasSuffix(base, "_naprawy") || !(strings.HasPrefix(base, pc+"_") || strings.Contains(base, "_"+pc+"_")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, cand{p, base, info.ModTime()})
	}
	if len(files) == 0 {
		return ""
	}
	slices.SortStableFunc(files, func(a, b cand) int { return b.mod.Compare(a.mod) })
	if ticket != "" {
		t := strings.ToLower(unsafeNameRx.ReplaceAllString(ticket, "_"))
		for _, f := range files {
			if strings.HasPrefix(f.base, t+"_") {
				return f.path
			}
		}
	}
	return files[0].path
}
