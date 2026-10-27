package diag

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Raporty: HTML (techniczny i dla klienta), JSON, TXT, raport anonimowy i PDF (lib\Report.ps1).

// Counts - liczba wyników według statusu.
type Counts map[Status]int

// Verdict - ocena ogólna.
type Verdict struct {
	Level Status
	Title string
	Text  string
}

// Result - wynik skanowania (Get-SdResult).
type Result struct {
	Tool      string
	Version   string
	Generated time.Time
	Started   time.Time
	Finished  time.Time
	Level     string
	Tests     []string
	IsAdmin   bool
	Meta      map[string]any
	Counts    Counts
	Verdict   Verdict
	Findings  []Finding
	Tables    []Table
	Timings   []Timing
	Log       []string
	Anonymous bool
}

func countFindings(fs []Finding) Counts {
	c := Counts{Crit: 0, Warn: 0, Info: 0, OK: 0, Skip: 0, Err: 0}
	for _, f := range fs {
		if _, ok := c[f.Status]; ok {
			c[f.Status]++
		}
	}
	return c
}

func verdictFor(c Counts) Verdict {
	switch {
	case c[Crit] > 0:
		return Verdict{Crit, "Wykryto poważne problemy", "Komputer wymaga interwencji serwisowej - szczegóły i zalecenia poniżej."}
	case c[Warn] > 0:
		return Verdict{Warn, "Wykryto zalecenia do wykonania", "Komputer działa, ale warto wykonać zalecone czynności serwisowe."}
	}
	return Verdict{OK, "Nie wykryto problemów", "Wszystkie wykonane testy zakończyły się pomyślnie."}
}

// customerFindings odpowiada Get-SdCustomerFindings: bez danych prywatnych, z wersją dla klienta (bez szczegółów).
func customerFindings(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Private {
			continue
		}
		if c := f.Customer; c != nil {
			g := f
			if c.Title != "" {
				g.Title = c.Title
			}
			if c.Status != "" {
				g.Status = c.Status
			}
			if c.Value != "" {
				g.Value = c.Value
			}
			g.Details, g.Advice, g.Customer = "", c.Advice, nil
			out = append(out, g)
		} else {
			out = append(out, f)
		}
	}
	return out
}

func metaStr(m map[string]any, k string) string { return win.AnyString(m[k]) }

// ---------------------------------------------------------------- HTML

// htmlEncode odpowiada WebUtility.HtmlEncode (.NET): <>&"' oraz znaki 160-255 jako encje liczbowe.
func htmlEncode(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&#39;")
		case r >= 160 && r <= 255, r > 0xFFFF:
			b.WriteString("&#" + strconv.Itoa(int(r)) + ";")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	urlRx     = regexp.MustCompile(`https://[^\s<>"]+`)
	newlineRx = regexp.MustCompile(`\r?\n`)
	cellURLRx = regexp.MustCompile(`^https://\S+$`)
)

// toHTML odpowiada ConvertTo-SdHtml: zakodowany tekst, adresy https jako linki, nowe wiersze jako <br>.
func toHTML(v any) string {
	s := htmlEncode(htmlString(v))
	s = urlRx.ReplaceAllStringFunc(s, func(u string) string {
		tail := ""
		for len(u) > 8 && strings.ContainsRune(".,;:)", rune(u[len(u)-1])) {
			tail = u[len(u)-1:] + tail
			u = u[:len(u)-1]
		}
		return `<a href="` + u + `" target="_blank" rel="noopener noreferrer">` + u + `</a>` + tail
	})
	return newlineRx.ReplaceAllString(s, "<br>")
}

// htmlString: wartość jako tekst jak [string] w PowerShellu (liczby w formacie niezależnym od języka).
func htmlString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case float64:
		s := strconv.FormatFloat(x, 'g', 15, 64)
		if strings.ContainsRune(s, 'e') {
			s = strings.ToUpper(s)
		}
		return s
	}
	return win.AnyString(v)
}

// toHTMLCell odpowiada ConvertTo-SdHtmlCell: sam adres https jako krótki link „otwórz”.
func toHTMLCell(v any) string {
	if s, ok := v.(string); ok && cellURLRx.MatchString(s) {
		return `<a href="` + htmlEncode(s) + `" target="_blank" rel="noopener noreferrer">otwórz</a>`
	}
	return toHTML(v)
}

const reportCSS = `:root{--bg:#f3f5f9;--card:#fff;--text:#1b2433;--muted:#667085;--line:#e4e7ec;--accent:#0f6cbd;
--crit:#b42318;--critbg:#fee4e2;--warn:#93370d;--warnbg:#fef0c7;--ok:#067647;--okbg:#d1fadf;--info:#175cd3;--infobg:#e0eaff;--skip:#475467;--skipbg:#eaecf0;--err:#6941c6;--errbg:#ebe9fe}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 "Segoe UI",system-ui,-apple-system,Roboto,Arial,sans-serif}
.wrap{max-width:1120px;margin:0 auto;padding:22px 18px 60px}
header.top{display:flex;gap:18px;align-items:center;justify-content:space-between;padding:20px 24px;background:linear-gradient(135deg,#0a3a66,#0f6cbd);color:#fff;border-radius:14px}
header.top h1{margin:0;font-size:22px;font-weight:650;letter-spacing:.2px}
header.top .sub{opacity:.85;font-size:13px;margin-top:2px}
header.top img{max-height:54px;max-width:160px;background:#fff;border-radius:8px;padding:4px}
.meta{font-size:12.5px;opacity:.92;text-align:right;line-height:1.55}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:18px 20px;margin-top:16px;box-shadow:0 1px 2px rgba(16,24,40,.04)}
.card h2{margin:0 0 12px;font-size:16px}
.verdict{display:flex;gap:18px;align-items:center;flex-wrap:wrap;border-left:8px solid var(--ok)}
.verdict.crit{border-left-color:var(--crit)}.verdict.warn{border-left-color:#f79009}
.verdict .big{font-size:22px;font-weight:700}.verdict.crit .big{color:var(--crit)}.verdict.warn .big{color:var(--warn)}.verdict.ok .big{color:var(--ok)}
.verdict .txt{color:var(--muted);margin-top:2px}
.counters{display:flex;gap:8px;flex-wrap:wrap;margin-left:auto}
.chip{padding:5px 12px;border-radius:999px;font-weight:600;font-size:12.5px;white-space:nowrap}
.chip.crit,.badge.crit{background:var(--critbg);color:var(--crit)}.chip.warn,.badge.warn{background:var(--warnbg);color:var(--warn)}
.chip.ok,.badge.ok{background:var(--okbg);color:var(--ok)}.chip.info,.badge.info{background:var(--infobg);color:var(--info)}
.chip.skip,.badge.skip{background:var(--skipbg);color:var(--skip)}.chip.error,.badge.error{background:var(--errbg);color:var(--err)}
.facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:2px 28px}
.facts div{display:flex;gap:10px;padding:6px 0;border-bottom:1px dashed var(--line)}
.facts b{min-width:130px;color:var(--muted);font-weight:600}.facts span{word-break:break-word}
.issue{border:1px solid var(--line);border-left:6px solid var(--warn);border-radius:10px;padding:12px 14px;margin:10px 0;background:#fff}
.issue.crit{border-left-color:var(--crit);background:#fffafa}.issue.warn{border-left-color:#f79009;background:#fffdf7}
.issue .h{display:flex;gap:10px;align-items:baseline;flex-wrap:wrap}.issue .h b{font-size:14.5px}
.issue .v{color:var(--muted);margin-top:3px}.adv{margin-top:6px;padding:7px 10px;background:#f4f8ff;border-radius:8px;border:1px solid #d6e4ff}
.badge{display:inline-block;padding:2px 9px;border-radius:6px;font-size:11px;font-weight:700;letter-spacing:.3px;white-space:nowrap}
nav.toc{display:flex;gap:8px;flex-wrap:wrap;margin-top:16px}
nav.toc a{text-decoration:none;color:var(--text);background:#fff;border:1px solid var(--line);padding:5px 12px;border-radius:999px;font-size:13px}
nav.toc a:hover{border-color:var(--accent);color:var(--accent)}
nav.toc i{display:inline-block;width:9px;height:9px;border-radius:50%;margin-right:6px;background:var(--ok)}
nav.toc i.crit{background:var(--crit)}nav.toc i.warn{background:#f79009}nav.toc i.info{background:var(--info)}
.tools{display:flex;gap:8px;flex-wrap:wrap;margin-top:16px;align-items:center}
.tools button{font:inherit;padding:6px 12px;border:1px solid var(--line);background:#fff;border-radius:8px;cursor:pointer}
.tools button.on,.tools button:hover{border-color:var(--accent);color:var(--accent)}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;color:var(--muted);font-weight:600;padding:6px 8px;border-bottom:2px solid var(--line);background:#fafbfc;position:sticky;top:0}
td{padding:7px 8px;border-bottom:1px solid var(--line);vertical-align:top}
table.f td.st{width:98px}table.f td.t{width:27%;font-weight:600}table.f td.v{word-break:break-word}
details{margin-top:4px}summary{cursor:pointer;color:var(--accent);font-size:12.5px}
.det{margin-top:4px;padding:8px 10px;background:#f8f9fb;border-radius:8px;color:#344054;font-size:12.5px}
.tw{overflow:auto;max-height:420px;margin-top:6px;border:1px solid var(--line);border-radius:8px}
details.tbl{margin-top:10px}details.tbl>summary{font-weight:600;font-size:13px}
td.sm{font-size:12px}h3{margin:18px 0 6px;font-size:14px;color:var(--muted);text-transform:uppercase;letter-spacing:.5px}
footer{margin-top:26px;color:var(--muted);font-size:12px}
.contact{margin-top:10px;font-size:13px;color:#fff;opacity:.95}
body[data-show=problems] tr.f.ok,body[data-show=problems] tr.f.info,body[data-show=problems] tr.f.skip{display:none}
body[data-show=crit] tr.f.ok,body[data-show=crit] tr.f.info,body[data-show=crit] tr.f.skip,body[data-show=crit] tr.f.warn{display:none}
ol.todo{padding-left:20px;margin:6px 0}ol.todo li{margin:6px 0}
@media (max-width:760px){.facts{grid-template-columns:1fr}.counters{margin-left:0}header.top{flex-direction:column;align-items:flex-start}.meta{text-align:left}}
@page{size:A4;margin:12mm}
@media print{body{background:#fff;-webkit-print-color-adjust:exact;print-color-adjust:exact}.wrap{padding:0}.card,.issue{box-shadow:none;break-inside:avoid}.tools,nav.toc{display:none}.tw{max-height:none;overflow:visible}tr{break-inside:avoid}}`

const reportJS = `(function(){
 var b=document.body;
 function setShow(v){b.setAttribute('data-show',v);var bs=document.querySelectorAll('.tools button[data-show]');for(var i=0;i<bs.length;i++){bs[i].className=(bs[i].getAttribute('data-show')===v)?'on':'';}}
 var bs=document.querySelectorAll('.tools button[data-show]');
 for(var i=0;i<bs.length;i++){bs[i].onclick=function(){setShow(this.getAttribute('data-show'));};}
 var ex=document.getElementById('expand');
 if(ex){ex.onclick=function(){var d=document.querySelectorAll('details');var open=ex.getAttribute('data-open')!=='1';for(var i=0;i<d.length;i++){d[i].open=open;}ex.setAttribute('data-open',open?'1':'0');ex.textContent=open?'Zwiń szczegóły':'Rozwiń szczegóły';};}
 window.addEventListener('beforeprint',function(){var d=document.querySelectorAll('.card details:not(.tbl)');for(var i=0;i<d.length;i++){d[i].open=true;}});
})();` // bez końcowego znaku nowego wiersza - jak here-string $SdReportJs

// inlineCSS odpowiada Get-SdInlineCss: zmienne CSS nie działają w IE11 (Windows 8.1) - wstawiamy wartości.
var inlineCSS = func() string {
	css := reportCSS
	m := regexp.MustCompile(`:root\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		return css
	}
	for _, v := range regexp.MustCompile(`--([a-z0-9-]+):([^;]+);?`).FindAllStringSubmatch(m[1], -1) {
		css = strings.ReplaceAll(css, "var(--"+v[1]+")", strings.TrimSpace(v[2]))
	}
	return css
}()

// Settings - ustawienia serwisu (config\settings.json, jak w wersji PowerShell).
type Settings struct {
	ShopName        string
	ShopPhone       string
	ShopEmail       string
	ShopAddress     string
	Technician      string
	LogoPath        string
	ReportDir       string
	Level           string
	OpenAfterScan   string
	AutoPdf         bool
	AutoUpdateCheck bool
}

// LoadSettings odpowiada Get-SdSettings.
func LoadSettings(root string) Settings {
	s := Settings{ShopName: "Serwis komputerowy", Level: "Quick", OpenAfterScan: "none", AutoUpdateCheck: true}
	if b, err := os.ReadFile(filepath.Join(root, "config", "settings.json")); err == nil {
		_ = json.Unmarshal(decodeText(b), &s)
	}
	if !slices.Contains([]string{"none", "customer", "technical"}, s.OpenAfterScan) {
		s.OpenAfterScan = "none"
	}
	return s
}

func logoHTML(s Settings) string {
	if s.LogoPath == "" {
		return ""
	}
	fi, err := os.Stat(s.LogoPath)
	if err != nil || fi.Size() >= 400*1024 {
		return ""
	}
	mime := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".svg": "image/svg+xml", ".gif": "image/gif"}[strings.ToLower(filepath.Ext(s.LogoPath))]
	if mime == "" {
		return ""
	}
	b, err := os.ReadFile(s.LogoPath)
	if err != nil {
		return ""
	}
	return `<img alt="" src="data:` + mime + `;base64,` + base64.StdEncoding.EncodeToString(b) + `">`
}

// HTMLReport odpowiada New-SdHtmlReport (customer = raport dla klienta).
func HTMLReport(r *Result, customer bool, settings Settings) string {
	var sb strings.Builder
	E := func(v any) string { return toHTML(v) }
	meta := r.Meta
	labels := map[Status]string{Crit: "KRYTYCZNE", Warn: "UWAGA", OK: "OK", Info: "INFO", Skip: "POMINIĘTO", Err: "BŁĄD TESTU"}
	if customer {
		labels = map[Status]string{Crit: "POWAŻNY PROBLEM", Warn: "ZALECENIE", OK: "OK", Info: "INFORMACJA", Skip: "POMINIĘTO", Err: "BŁĄD TESTU"}
	}
	findings := r.Findings
	if customer {
		findings = nil
		for _, f := range customerFindings(r.Findings) {
			if f.Status != Err && f.Status != Skip {
				findings = append(findings, f)
			}
		}
	}
	var tables []Table
	for _, t := range r.Tables {
		if !(customer && t.Private) {
			tables = append(tables, t)
		}
	}
	counts := countFindings(findings)
	verdict := verdictFor(counts)
	shop := settings.ShopName
	if shop == "" {
		shop = "Serwis komputerowy"
	}
	title := "Raport diagnostyczny komputera"
	if customer {
		title = "Raport z diagnostyki komputera"
	}
	if r.Anonymous {
		title = "Raport diagnostyczny (anonimowy)"
		shop = "Bez nazwy komputera i kont, numerów seryjnych, kluczy, adresów IP/MAC i danych klienta"
		settings = Settings{}
	}
	sb.WriteString(`<!DOCTYPE html><html lang="pl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	sb.WriteString(`<title>` + E(title+" - "+metaStr(meta, "ComputerName")) + `</title><style>` + inlineCSS + `</style></head>`)
	sb.WriteString(`<body data-show="all"><div class="wrap">`)

	// --- nagłówek
	sb.WriteString(`<header class="top"><div>` + logoHTML(settings) + `<h1>` + E(title) + `</h1><div class="sub">` + E(shop) + `</div>`)
	if contact := joinNonEmpty(" · ", settings.ShopPhone, settings.ShopEmail, settings.ShopAddress); contact != "" {
		sb.WriteString(`<div class="contact">` + E(contact) + `</div>`)
	}
	sb.WriteString(`</div><div class="meta">`)
	sb.WriteString("Data: " + fmtDt(r.Generated) + "<br>")
	for _, p := range [][2]string{{"Zlecenie", "Ticket"}, {"Klient", "Customer"}, {"Technik", "Technician"}} {
		if v := metaStr(meta, p[1]); v != "" {
			sb.WriteString(p[0] + ": " + E(v) + "<br>")
		}
	}
	sb.WriteString(`</div></header>`)

	// --- werdykt
	sb.WriteString(`<section class="card verdict ` + string(verdict.Level) + `"><div><div class="big">` + E(verdict.Title) + `</div><div class="txt">` + E(verdict.Text) + `</div></div><div class="counters">`)
	if n := counts[Crit]; n > 0 {
		t := fmtCount(n, "krytyczny", "krytyczne", "krytycznych")
		if customer {
			t = fmtCount(n, "poważny problem", "poważne problemy", "poważnych problemów")
		}
		sb.WriteString(`<span class="chip crit">` + E(t) + `</span>`)
	}
	if n := counts[Warn]; n > 0 {
		t := fmtCount(n, "ostrzeżenie", "ostrzeżenia", "ostrzeżeń")
		if customer {
			t = fmtCount(n, "zalecenie", "zalecenia", "zaleceń")
		}
		sb.WriteString(`<span class="chip warn">` + E(t) + `</span>`)
	}
	sb.WriteString(`<span class="chip ok">` + strconv.Itoa(counts[OK]) + ` w porządku</span>`)
	if !customer {
		sb.WriteString(`<span class="chip info">` + E(fmtCount(counts[Info], "informacja", "informacje", "informacji")) + `</span>`)
		if n := counts[Skip]; n > 0 {
			sb.WriteString(`<span class="chip skip">` + E(fmtCount(n, "pominięty", "pominięte", "pominiętych")) + `</span>`)
		}
		if n := counts[Err]; n > 0 {
			sb.WriteString(`<span class="chip error">` + E(fmtCount(n, "błąd testu", "błędy testu", "błędów testu")) + `</span>`)
		}
	}
	sb.WriteString(`</div></section>`)

	// --- dane komputera
	var facts [][2]string
	add := func(k string, v any) { // jak "if ($v)" w PowerShellu: pusty tekst, zero i $false pomijamy
		if win.AnyBool(v) {
			facts = append(facts, [2]string{k, htmlString(v)})
		}
	}
	add("Komputer", meta["Model"])
	add("System", meta["OS"])
	add("Procesor", meta["Cpu"])
	add("Pamięć RAM", meta["Ram"])
	add("Dyski", meta["Disks"])
	add("Grafika", meta["Gpu"])
	if v, ok := meta["BatteryHealth"]; ok && win.AnyFloat(v) != 0 {
		add("Kondycja baterii", fmt.Sprintf("%s%% pojemności fabrycznej", htmlString(v)))
	}
	if v := metaStr(meta, "Stability"); v != "" {
		add("Indeks stabilności", v+" / 10")
	}
	add("BIOS / UEFI", meta["Bios"])
	if !customer {
		add("Nazwa komputera", meta["ComputerName"])
		add("Numer seryjny", meta["Serial"])
		add("Płyta główna", meta["Board"])
		add("Typ obudowy", meta["Chassis"])
		add("Użytkownik", meta["UserName"])
		mode := "szybka"
		if r.Level == "Full" {
			mode = "pełna"
		}
		if len(r.Tests) > 0 {
			mode += " + testy obciążeniowe"
		}
		if win.AnyBool(meta["Offline"]) {
			mode += ", bez internetu"
		}
		add("Tryb diagnostyki", mode)
		if r.IsAdmin {
			add("Uprawnienia", "administrator")
		} else {
			add("Uprawnienia", "zwykły użytkownik (część testów pominięto)")
		}
		if !r.Started.IsZero() && !r.Finished.IsZero() {
			add("Czas skanowania", fmtSpan(r.Finished.Sub(r.Started)))
		}
		add("Pliki programu", meta["Integrity"])
	}
	add("Opis zgłoszenia", meta["DeviceNote"])
	if win.AnyBool(meta["Offline"]) && customer {
		add("Internet", "nie sprawdzano (diagnostyka bez dostępu do internetu)")
	}
	h2 := "Dane komputera i zlecenia"
	if customer || r.Anonymous {
		h2 = "Dane komputera"
	}
	sb.WriteString(`<section class="card"><h2>` + h2 + `</h2><div class="facts">`)
	for _, f := range facts {
		sb.WriteString(`<div><b>` + E(f[0]) + `</b><span>` + E(f[1]) + `</span></div>`)
	}
	sb.WriteString(`</div>`)
	if n := metaStr(meta, "Notes"); n != "" && !customer {
		sb.WriteString(`<h3>Uwagi technika</h3><div class="det">` + E(n) + `</div>`)
	}
	sb.WriteString(`</section>`)

	// --- problemy i zalecenia
	order := map[string]int{}
	for i, c := range Categories {
		order[c.Key] = i
	}
	catOrder := func(k string) int {
		if i, ok := order[k]; ok {
			return i
		}
		return 99
	}
	var issues []Finding
	for _, f := range findings {
		if f.Status == Crit || f.Status == Warn {
			issues = append(issues, f)
		}
	}
	psSort(issues, func(a, b Finding) int {
		ra, rb := 1, 1
		if a.Status == Crit {
			ra = 0
		}
		if b.Status == Crit {
			rb = 0
		}
		if ra != rb {
			return ra - rb
		}
		return catOrder(a.Category) - catOrder(b.Category)
	})
	h2 = "Wykryte problemy i zalecenia"
	if customer {
		h2 = "Co wykryto i co zalecamy"
	}
	sb.WriteString(`<section class="card"><h2>` + h2 + `</h2>`)
	if len(issues) == 0 {
		sb.WriteString(`<div class="txt">Nie wykryto problemów wymagających działania.</div>`)
	}
	for _, f := range issues {
		sb.WriteString(`<div class="issue ` + string(f.Status) + `"><div class="h"><span class="badge ` + string(f.Status) + `">` + labels[f.Status] + `</span><b>` + E(f.Title) + `</b><span class="sm" style="color:#667085;font-size:12px">` + E(categoryLabel(f.Category)) + `</span></div>`)
		if f.Value != "" {
			sb.WriteString(`<div class="v">` + E(f.Value) + `</div>`)
		}
		if f.Advice != "" {
			sb.WriteString(`<div class="adv"><b>Zalecenie:</b> ` + E(f.Advice) + `</div>`)
		}
		if f.Details != "" && !customer {
			sb.WriteString(`<details><summary>szczegóły</summary><div class="det">` + E(f.Details) + `</div></details>`)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`</section>`)

	worstOf := func(fs []Finding) Status {
		w := OK
		for _, f := range fs {
			if f.Status == Crit {
				return Crit
			}
			if f.Status == Warn {
				w = Warn
			}
		}
		return w
	}
	byCat := func(key string) ([]Finding, []Table) {
		var cf []Finding
		var ct []Table
		for _, f := range findings {
			if f.Category == key {
				cf = append(cf, f)
			}
		}
		for _, t := range tables {
			if t.Category == key {
				ct = append(ct, t)
			}
		}
		return cf, ct
	}
	privateBadge := ` <span class="badge skip" title="Dane widoczne tylko w raporcie technicznym">prywatne</span>`

	if customer {
		// --- zakres kontroli (dla klienta)
		sb.WriteString(`<section class="card"><h2>Co sprawdziliśmy</h2><table><thead><tr><th>Obszar</th><th>Liczba testów</th><th>Wynik</th></tr></thead><tbody>`)
		for _, c := range Categories {
			cf, _ := byCat(c.Key)
			if len(cf) == 0 {
				continue
			}
			w := worstOf(cf)
			wl := map[Status]string{Crit: "wymaga działania", Warn: "zalecenia", OK: "w porządku"}[w]
			sb.WriteString(`<tr><td>` + E(c.Label) + `</td><td>` + strconv.Itoa(len(cf)) + `</td><td><span class="badge ` + string(w) + `">` + wl + `</span></td></tr>`)
		}
		sb.WriteString(`</tbody></table></section>`)
		sb.WriteString(`<footer>Raport ma charakter diagnostyczny i opiera się na odczytach systemu w chwili badania. Przed wykonaniem napraw zalecamy kopię zapasową danych. `)
		sb.WriteString(`Wygenerowano programem SerwisDiag ` + E(r.Version) + `.</footer>`)
	} else {
		// --- spis kategorii
		sb.WriteString(`<nav class="toc">`)
		for _, c := range Categories {
			cf, ct := byCat(c.Key)
			if len(cf) == 0 && len(ct) == 0 {
				continue
			}
			sb.WriteString(`<a href="#cat-` + c.Key + `"><i class="` + string(worstOf(cf)) + `"></i>` + E(c.Label) + `</a>`)
		}
		sb.WriteString(`</nav><div class="tools"><span style="color:#667085">Pokaż:</span><button class="on" data-show="all">wszystko</button><button data-show="problems">tylko problemy</button><button data-show="crit">tylko krytyczne</button><button id="expand" data-open="0">Rozwiń szczegóły</button></div>`)
		// --- kategorie
		for _, c := range Categories {
			cf, ct := byCat(c.Key)
			if len(cf) == 0 && len(ct) == 0 {
				continue
			}
			sb.WriteString(`<section class="card" id="cat-` + c.Key + `"><h2>` + E(c.Label) + `</h2>`)
			if len(cf) > 0 {
				sb.WriteString(`<table class="f"><tbody>`)
				for _, f := range cf {
					pb := ""
					if f.Private {
						pb = privateBadge
					}
					sb.WriteString(`<tr class="f ` + string(f.Status) + `"><td class="st"><span class="badge ` + string(f.Status) + `">` + labels[f.Status] + `</span></td><td class="t">` + E(f.Title) + pb + `</td><td class="v">` + E(f.Value))
					if f.Advice != "" && (f.Status == Crit || f.Status == Warn) {
						sb.WriteString(`<div class="adv"><b>Zalecenie:</b> ` + E(f.Advice) + `</div>`)
					} else if f.Advice != "" {
						sb.WriteString(`<details><summary>zalecenie</summary><div class="det">` + E(f.Advice) + `</div></details>`)
					}
					if f.Details != "" {
						sb.WriteString(`<details><summary>szczegóły</summary><div class="det">` + E(f.Details) + `</div></details>`)
					}
					sb.WriteString(`</td></tr>`)
				}
				sb.WriteString(`</tbody></table>`)
			}
			for _, t := range ct {
				pb := ""
				if t.Private {
					pb = privateBadge
				}
				sb.WriteString(`<details class="tbl"><summary>` + E(t.Name) + ` (` + strconv.Itoa(len(t.Rows)) + `)` + pb + `</summary>`)
				if t.Note != "" {
					sb.WriteString(`<div class="det">` + E(t.Note) + `</div>`)
				}
				sb.WriteString(`<div class="tw"><table><thead><tr>`)
				for _, col := range t.Columns {
					sb.WriteString(`<th>` + E(col) + `</th>`)
				}
				sb.WriteString(`</tr></thead><tbody>`)
				for _, rw := range t.Rows {
					sb.WriteString(`<tr>`)
					for _, col := range t.Columns {
						sb.WriteString(`<td class="sm">` + toHTMLCell(rw.Get(col)) + `</td>`)
					}
					sb.WriteString(`</tr>`)
				}
				sb.WriteString(`</tbody></table></div></details>`)
			}
			sb.WriteString(`</section>`)
		}
		// --- stopka techniczna
		sb.WriteString(`<section class="card"><h2>Informacje o programie</h2><div class="det">`)
		sb.WriteString(`SerwisDiag ` + E(r.Version) + ` (licencja MIT, wersja w Go) · Go ` + E(metaStr(meta, "GoVersion")) + ` · wygenerowano ` + fmtDt(r.Generated) + `<br>`)
		if k := metaStr(meta, "Knowledge"); k != "" {
			sb.WriteString(`Baza wiedzy programu (okresy wsparcia Windows i ESU): ` + E(k) + `<br>`)
		}
		sb.WriteString(`Program pracuje lokalnie. Poza testami łączności (ping, DNS, kilka zapytań HTTP/HTTPS) nie wysyła żadnych danych do internetu.<br>`)
		if r.Anonymous {
			sb.WriteString(`Raport anonimowy: bez wyników prywatnych, nazwy komputera i kont, numerów seryjnych, kluczy, adresów IP i MAC, nazw sieci Wi-Fi oraz danych klienta. Program usuwa znane dane identyfikujące, ale przed publikacją przejrzyj raport.`)
		} else {
			sb.WriteString(`Dane wrażliwe (numery seryjne, klucz OEM, konta, wykluczenia antywirusa) oznaczono jako <i>prywatne</i> - nie ma ich w raporcie dla klienta.`)
		}
		sb.WriteString(`</div>`)
		var errs []string
		for _, l := range r.Log {
			if strings.Contains(l, "[WARN]") || strings.Contains(l, "[ERROR]") {
				errs = append(errs, l)
				if len(errs) == 60 {
					break
				}
			}
		}
		if len(errs) > 0 {
			sb.WriteString(`<details class="tbl"><summary>Dziennik programu: ostrzeżenia i błędy (` + strconv.Itoa(len(errs)) + `)</summary><div class="det">` + E(strings.Join(errs, "\n")) + `</div></details>`)
		}
		tm := slices.Clone(r.Timings)
		psSort(tm, func(a, b Timing) int { return cmpNum(b.Seconds, a.Seconds) })
		if len(tm) > 12 {
			tm = tm[:12]
		}
		if len(tm) > 0 {
			var lines []string
			for _, t := range tm {
				lines = append(lines, fmt.Sprintf("%6s s  %s", fmtG(t.Seconds), t.ID))
			}
			sb.WriteString(`<details class="tbl"><summary>Czasy wykonania testów</summary><div class="det">` + E(strings.Join(lines, "\n")) + `</div></details>`)
		}
		sb.WriteString(`</section>`)
	}
	sb.WriteString(`</div><script>` + reportJS + `</script></body></html>`)
	return sb.String()
}

// TextReport odpowiada New-SdTextReport (bez includePrivate - wersja dla klienta).
func TextReport(r *Result, settings Settings, includePrivate bool) string {
	m := r.Meta
	var lines []string
	shop := settings.ShopName
	if shop == "" {
		shop = "SerwisDiag"
	}
	lines = append(lines, "RAPORT DIAGNOSTYCZNY - "+shop, "Data: "+fmtDt(r.Generated))
	for _, p := range [][2]string{{"Zlecenie", "Ticket"}, {"Klient", "Customer"}, {"Technik", "Technician"}, {"Komputer", "Model"}, {"System", "OS"}, {"Procesor", "Cpu"}, {"RAM", "Ram"}, {"Dyski", "Disks"}, {"Grafika", "Gpu"}} {
		if v := metaStr(m, p[1]); v != "" {
			lines = append(lines, p[0]+": "+v)
		}
	}
	fs := r.Findings
	if !includePrivate {
		fs = customerFindings(r.Findings)
	}
	counts := countFindings(fs)
	v := verdictFor(counts)
	lines = append(lines, "", fmt.Sprintf("WYNIK: %s (krytyczne: %d, ostrzeżenia: %d)", strings.ToUpper(v.Title), counts[Crit], counts[Warn]), "")
	var issues []Finding
	for _, f := range fs {
		if f.Status == Crit || f.Status == Warn {
			issues = append(issues, f)
		}
	}
	psSort(issues, func(a, b Finding) int {
		if a.Status == b.Status {
			return 0
		}
		if a.Status == Crit {
			return -1
		}
		return 1
	})
	for _, f := range issues {
		label := "UWAGA"
		if f.Status == Crit {
			label = "KRYTYCZNE"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", label, f.Title, f.Value))
		if f.Advice != "" {
			lines = append(lines, "    Zalecenie: "+f.Advice)
		}
	}
	if len(issues) == 0 {
		lines = append(lines, "Nie wykryto problemów.")
	}
	lines = append(lines, "", "Wygenerowano: SerwisDiag "+r.Version)
	return strings.Join(lines, "\r\n")
}

// ---------------------------------------------------------------- JSON (ConvertTo-SdJsonReport)

type kv struct {
	k string
	v any
}

// omap - obiekt JSON z kluczami w ustalonej kolejności.
type omap []kv

func (o omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.k)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(p.v)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (r Row) MarshalJSON() ([]byte, error) {
	o := make(omap, len(r))
	for i, c := range r {
		o[i] = kv{c.Key, c.Val}
	}
	return o.MarshalJSON()
}

func isoTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format("2006-01-02T15:04:05")
}

// JSONReport odpowiada ConvertTo-SdJsonReport.
func JSONReport(r *Result, includePrivate bool) ([]byte, error) {
	keys := make([]string, 0, len(r.Meta))
	for k := range r.Meta {
		if includePrivate || k != "Serial" {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	meta := omap{}
	for _, k := range keys {
		meta = append(meta, kv{k, r.Meta[k]})
	}
	findings := []any{}
	for _, f := range r.Findings {
		if !includePrivate && f.Private {
			continue
		}
		var cust any
		if f.Customer != nil {
			cust = omap{{"title", f.Customer.Title}, {"status", string(f.Customer.Status)}, {"value", f.Customer.Value}, {"advice", f.Customer.Advice}}
		}
		fo := omap{{"id", f.CheckID}, {"category", f.Category}, {"title", f.Title}, {"status", string(f.Status)}, {"value", f.Value}, {"details", f.Details},
			{"advice", f.Advice}, {"private", f.Private}, {"customer", cust}}
		if !f.LastEvent.IsZero() {
			fo = append(fo, kv{"lastEvent", isoTime(f.LastEvent)})
		}
		findings = append(findings, fo)
	}
	tables := []any{}
	for _, t := range r.Tables {
		if !includePrivate && t.Private {
			continue
		}
		rows := t.Rows
		if rows == nil {
			rows = []Row{}
		}
		tables = append(tables, omap{{"category", t.Category}, {"name", t.Name}, {"columns", t.Columns}, {"rows", rows}})
	}
	counts := omap{{"crit", r.Counts[Crit]}, {"warn", r.Counts[Warn]}, {"info", r.Counts[Info]}, {"ok", r.Counts[OK]}, {"skip", r.Counts[Skip]}, {"error", r.Counts[Err]}}
	obj := omap{{"tool", r.Tool}, {"version", r.Version}, {"engine", "go"}, {"generated", isoTime(r.Generated)}, {"started", isoTime(r.Started)}, {"finished", isoTime(r.Finished)},
		{"level", r.Level}, {"isAdmin", r.IsAdmin}, {"meta", meta}, {"verdict", string(r.Verdict.Level)}, {"counts", counts}, {"findings", findings}, {"tables", tables}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ---------------------------------------------------------------- zapis (Save-SdReports)

var unsafeNameRx = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

// ReportPaths - ścieżki zapisanych raportów.
type ReportPaths struct {
	Technical, Customer, JSON, Text string
}

var bom = []byte{0xEF, 0xBB, 0xBF}

func writeText(path string, s string, withBOM bool) error {
	var b []byte
	if withBOM {
		b = append(b, bom...)
	}
	b = append(b, s...)
	return os.WriteFile(path, b, 0o644)
}

// SaveReports zapisuje raporty techniczny, dla klienta, JSON i podsumowanie tekstowe.
func SaveReports(r *Result, settings Settings, outDir string) (ReportPaths, error) {
	stamp := time.Now().Format("20060102-1504")
	base := joinNonEmpty("_", metaStr(r.Meta, "Ticket"), metaStr(r.Meta, "ComputerName"), stamp)
	base = unsafeNameRx.ReplaceAllString(base, "_")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return ReportPaths{}, err
	}
	p := ReportPaths{
		Technical: filepath.Join(outDir, base+"_techniczny.html"),
		Customer:  filepath.Join(outDir, base+"_klient.html"),
		JSON:      filepath.Join(outDir, base+".json"),
		Text:      filepath.Join(outDir, base+"_podsumowanie.txt"),
	}
	if err := writeText(p.Technical, HTMLReport(r, false, settings), true); err != nil {
		return p, err
	}
	if err := writeText(p.Customer, HTMLReport(r, true, settings), true); err != nil {
		return p, err
	}
	j, err := JSONReport(r, true)
	if err != nil {
		return p, err
	}
	if err := os.WriteFile(p.JSON, j, 0o644); err != nil {
		return p, err
	}
	return p, writeText(p.Text, TextReport(r, settings, false), true)
}

// ---------------------------------------------------------------- katalog raportów

// IsInstalledCopy: kopia z instalatora (znacznik instalacja.ini).
func IsInstalledCopy(root string) bool {
	st, err := os.Stat(filepath.Join(root, "instalacja.ini"))
	return err == nil && !st.IsDir()
}

func writableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	t := filepath.Join(dir, fmt.Sprintf(".w%d", time.Now().UnixNano()))
	if err := os.WriteFile(t, []byte("x"), 0o644); err != nil {
		return false
	}
	os.Remove(t)
	return true
}

// ResolveReportDir odpowiada Resolve-SdReportDir: katalog z ustawień, <program>\raporty, Dokumenty\SerwisDiag
// (w kopii zainstalowanej odwrotnie), na końcu %TEMP%\SerwisDiag.
func ResolveReportDir(root string, s Settings) string {
	var cands []string
	if s.ReportDir != "" {
		cands = append(cands, s.ReportDir)
	}
	docs := ""
	if home, err := os.UserHomeDir(); err == nil {
		docs = filepath.Join(win.DocumentsDir(home), "SerwisDiag")
	}
	if IsInstalledCopy(root) {
		cands = append(cands, docs, filepath.Join(root, "raporty"))
	} else {
		cands = append(cands, filepath.Join(root, "raporty"), docs)
	}
	cands = append(cands, filepath.Join(os.TempDir(), "SerwisDiag"))
	for _, c := range cands {
		if c != "" && writableDir(c) {
			return c
		}
	}
	return os.TempDir()
}
