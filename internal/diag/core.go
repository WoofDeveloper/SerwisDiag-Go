// Package diag to silnik diagnostyczny SerwisDiag w Go: testy, ocena wyników i raporty - ta sama logika co
// w wersji PowerShell (checks\*.ps1, lib\*.ps1), z tymi samymi tekstami i progami.
package diag

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"serwisdiag/internal/win"
)

// Version - wersja programu (zgodna z wersją PowerShell, na której wzorowano tę wersję).
const Version = "1.5.1"

const (
	Author  = "Kacper Suliński - INDUSIP.PL"
	License = "MIT"
)

// Status wyniku testu.
type Status string

const (
	Crit Status = "crit"
	Warn Status = "warn"
	Err  Status = "error"
	Info Status = "info"
	OK   Status = "ok"
	Skip Status = "skip"
)

var statusInfo = map[Status]struct {
	Label string
	Rank  int
}{
	Crit: {"KRYTYCZNE", 5},
	Warn: {"UWAGA", 4},
	Err:  {"BŁĄD TESTU", 3},
	Info: {"INFO", 2},
	OK:   {"OK", 1},
	Skip: {"POMINIĘTO", 0},
}

// Rank: im wyżej, tym poważniej (sortowanie i werdykt).
func (s Status) Rank() int { return statusInfo[s].Rank }

// Label: etykieta do raportu technicznego i konsoli.
func (s Status) Label() string { return statusInfo[s].Label }

// worst odpowiada Get-SdWorstStatus.
func worst(statuses ...Status) Status {
	best, rank := OK, -1
	for _, s := range statuses {
		if r := s.Rank(); r > rank {
			rank, best = r, s
		}
	}
	return best
}

// Category - kategoria testów (kolejność jak w raporcie).
type Category struct{ Key, Label string }

var Categories = []Category{
	{"system", "System"},
	{"hardware", "Sprzęt"},
	{"storage", "Dyski"},
	{"power", "Zasilanie i bateria"},
	{"perf", "Wydajność"},
	{"events", "Awarie i zdarzenia"},
	{"drivers", "Sterowniki i urządzenia"},
	{"security", "Bezpieczeństwo"},
	{"updates", "Aktualizacje i licencja"},
	{"network", "Sieć"},
	{"software", "Oprogramowanie i autostart"},
	{"tests", "Testy obciążeniowe"},
}

func categoryLabel(key string) string {
	for _, c := range Categories {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

// Customer - osobna, neutralna wersja wyniku dla raportu klienta.
type Customer struct {
	Title  string
	Status Status
	Value  string
	Advice string
}

// Finding - pojedynczy wynik testu.
type Finding struct {
	CheckID   string
	Category  string
	Title     string
	Status    Status
	Value     string
	Details   string
	Advice    string
	Private   bool
	Customer  *Customer
	Time      string
	LastEvent time.Time
}

// Cell / Row - wiersz tabeli z kolumnami w ustalonej kolejności (jak [pscustomobject]).
type Cell struct {
	Key string
	Val any
}

type Row []Cell

// row buduje wiersz z par: nazwa kolumny, wartość.
func row(kv ...any) Row {
	r := make(Row, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		r = append(r, Cell{kv[i].(string), kv[i+1]})
	}
	return r
}

// Get zwraca wartość kolumny (nil, gdy brak).
func (r Row) Get(key string) any {
	for _, c := range r {
		if c.Key == key {
			return c.Val
		}
	}
	return nil
}

// Table - tabela szczegółów w raporcie technicznym.
type Table struct {
	Category string
	Name     string
	Columns  []string
	Rows     []Row
	Note     string
	Private  bool
}

// Timing - czas wykonania testu.
type Timing struct {
	ID      string
	Seconds float64
}

// Check - test diagnostyczny.
type Check struct {
	ID            string
	Category      string
	Title         string
	Level         string // Quick, Full albo Test (tylko na żądanie)
	NeedsAdmin    bool
	NeedsInternet bool
	MinBuild      int
	Run           func(c *Ctx)
}

// Checks zwraca wszystkie testy w kolejności raportu.
func Checks() []*Check {
	return slices.Concat(systemChecks, hardwareChecks, storageChecks, powerChecks, perfChecks, eventChecks,
		driverChecks, securityChecks, updateChecks, networkChecks, softwareChecks, testChecks)
}

// Options - parametry skanowania.
type Options struct {
	Root     string
	Level    string   // Quick | Full
	Tests    []string // testy obciążeniowe (poziom Test)
	Only     []string // tylko wskazane testy (puste = wszystkie z poziomu)
	Offline  bool     // tryb bez internetu
	Parallel bool     // niezależne testy równolegle (goroutines)
	// OnProgress: wywoływane przy rozpoczęciu testu (i - numer, n - liczba testów).
	OnProgress func(i, n int, ch *Check)
}

type checkRun struct {
	check    *Check
	findings []Finding
	tables   []Table
	log      []string
	seconds  float64
	executed bool // test faktycznie wykonano (nie pominięto z braku uprawnień, internetu itp.)
}

// Scan - stan jednego skanowania.
type Scan struct {
	Opt      Options
	IsAdmin  bool
	Started  time.Time
	Finished time.Time

	mu    sync.Mutex
	meta  map[string]any
	runs  []*checkRun
	tail  *checkRun // wyniki dopisywane po testach (baza wiedzy, WMI)
	cache sync.Map
	// resMu chroni wyniki, tabele i dziennik testów: okno programu odczytuje je w trakcie skanowania.
	resMu sync.RWMutex

	cancel     atomic.Bool
	wmiCalls   atomic.Int64
	wmiSlow    atomic.Int64
	wmiSkipped atomic.Int64
	wmiBroken  atomic.Bool
	wmiSlowMu  sync.Mutex
	diskStatus map[int]Status
}

// NewScan przygotowuje skanowanie.
func NewScan(opt Options) *Scan {
	if opt.Level == "" {
		opt.Level = "Quick"
	}
	return &Scan{Opt: opt, IsAdmin: win.IsAdmin(), meta: map[string]any{}, tail: &checkRun{}, diskStatus: map[int]Status{}}
}

// Cancel przerywa skanowanie po bieżącym teście.
func (s *Scan) Cancel() { s.cancel.Store(true) }

// SetMeta ustawia wartość metadanych raportu (komputer, system, zlecenie...).
func (s *Scan) SetMeta(key string, v any) {
	s.mu.Lock()
	s.meta[key] = v
	s.mu.Unlock()
}

// Meta zwraca wartość metadanych.
func (s *Scan) Meta(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta[key]
}

// Selected zwraca testy wybrane do skanowania (Get-SdSelectedChecks).
func (s *Scan) Selected() []*Check {
	var sel []*Check
	for _, c := range Checks() {
		run := false
		switch c.Level {
		case "Quick":
			run = s.Opt.Level == "Quick" || s.Opt.Level == "Full"
		case "Full":
			run = s.Opt.Level == "Full"
		case "Test":
			run = containsi(s.Opt.Tests, c.ID)
		}
		if run && len(s.Opt.Only) > 0 && !containsi(s.Opt.Only, c.ID) {
			run = false
		}
		if run {
			sel = append(sel, c)
		}
	}
	return sel
}

// Run wykonuje skanowanie (Invoke-SdScan).
func (s *Scan) Run() {
	s.Started = time.Now()
	s.SetMeta("ComputerName", os.Getenv("COMPUTERNAME"))
	s.SetMeta("UserName", os.Getenv("USERNAME"))
	s.SetMeta("IsAdmin", s.IsAdmin)
	s.SetMeta("ToolVersion", Version)
	s.SetMeta("GoVersion", strings.TrimPrefix(runtime.Version(), "go"))
	s.SetMeta("Offline", s.Opt.Offline)

	boot := &Ctx{s: s, r: &checkRun{}}
	build := 0
	win.WithCOM(func() { build = int(osObj(boot).Int("BuildNumber")) })
	sel := s.Selected()
	s.resMu.Lock()
	s.tail.log = append(s.tail.log, boot.r.log...)
	s.runs = make([]*checkRun, len(sel))
	for i, c := range sel {
		s.runs[i] = &checkRun{check: c}
	}
	s.resMu.Unlock()
	if s.Opt.Parallel {
		s.runParallel(sel, build)
	} else {
		for i, c := range sel {
			if s.cancel.Load() {
				s.resMu.Lock()
				s.tail.log = append(s.tail.log, logLine("WARN", "Przerwano na żądanie użytkownika"))
				s.resMu.Unlock()
				break
			}
			if s.Opt.OnProgress != nil {
				s.Opt.OnProgress(i+1, len(sel), c)
			}
			s.runCheck(s.runs[i], build)
		}
	}
	s.finish()
}

// runParallel: testy w osobnych goroutines. Próbkowanie obciążenia (pf.sample) idzie samo na początku, żeby
// praca innych testów nie zawyżała pomiaru procesora i dysku; testy obciążeniowe (poziom Test) - na końcu,
// po kolei.
func (s *Scan) runParallel(sel []*Check, build int) {
	n := len(sel)
	started := atomic.Int64{}
	progress := func(c *Check) {
		i := int(started.Add(1))
		if s.Opt.OnProgress != nil {
			s.mu.Lock()
			s.Opt.OnProgress(i, n, c)
			s.mu.Unlock()
		}
	}
	var exclusiveFirst, parallel, last []int
	for i, c := range sel {
		switch {
		case c.ID == "pf.sample":
			exclusiveFirst = append(exclusiveFirst, i)
		case c.Level == "Test":
			last = append(last, i)
		default:
			parallel = append(parallel, i)
		}
	}
	for _, i := range exclusiveFirst {
		progress(sel[i])
		s.runCheck(s.runs[i], build)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(8, runtime.NumCPU()))
	for _, i := range parallel {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if s.cancel.Load() {
				return
			}
			progress(sel[i])
			s.runCheck(s.runs[i], build)
		}(i)
	}
	wg.Wait()
	for _, i := range last {
		if s.cancel.Load() {
			break
		}
		progress(sel[i])
		s.runCheck(s.runs[i], build)
	}
}

func (s *Scan) runCheck(r *checkRun, build int) {
	c := r.check
	ctx := &Ctx{s: s, r: r, cat: c.Category, id: c.ID, title: c.Title}
	if c.NeedsAdmin && !s.IsAdmin {
		ctx.Add(F{Title: c.Title, Status: Skip, Value: "Wymaga uprawnień administratora (uruchom program jako administrator)."})
		return
	}
	if c.NeedsInternet && s.Opt.Offline {
		ctx.Add(F{Title: c.Title, Status: Skip, Value: offlineSkipText})
		return
	}
	if c.MinBuild > 0 && build > 0 && build < c.MinBuild {
		ctx.Add(F{Title: c.Title, Status: Skip, Value: "Niedostępne w tej wersji systemu."})
		return
	}
	r.executed = true
	start := time.Now()
	func() {
		defer func() {
			if p := recover(); p != nil {
				msg := fmt.Sprint(p)
				ctx.Add(F{Title: c.Title, Status: Err, Value: "Test zakończył się błędem.", Details: msg + "\n" + string(debug.Stack())})
				ctx.Log("ERROR", fmt.Sprintf("%s: %s", c.ID, msg))
			}
		}()
		win.WithCOM(func() { c.Run(ctx) })
	}()
	r.seconds = psRoundN(time.Since(start).Seconds(), 1)
}

func (s *Scan) finish() {
	ctx := &Ctx{s: s, r: s.tail}
	kb := knowledgeInfo(time.Now())
	s.SetMeta("Knowledge", kb.Text)
	if kb.Stale {
		ctx.Add(F{Title: "Baza wiedzy programu", Category: "system", ID: "sys.knowledge", Status: Info, Private: true, Value: kb.Text,
			Details: "Okresy wsparcia Windows i terminy ESU pochodzą z bazy wiedzy dołączonej do tej wersji programu.",
			Advice:  "Pobierz nową wersję programu - daty wsparcia i zalecenia w raporcie mogą być nieaktualne."})
	}
	if n := s.wmiSlow.Load(); n > 0 {
		broken := s.wmiBroken.Load()
		det := fmt.Sprintf("Zapytania WMI przekraczające limit czasu lub trwające ponad %d s: %d", wmiSlowSec, n)
		if broken {
			det += "\n" + fmt.Sprintf("Pominięte zapytania (bezpiecznik): %d - wyniki części testów są niepełne.", s.wmiSkipped.Load())
		}
		st, val := Warn, "WMI odpowiada bardzo wolno"
		if broken {
			st, val = Crit, "WMI nie odpowiada - część testów pominięto, ich wyniki mogą być niepełne"
		}
		ctx.Add(F{Title: "Usługa WMI (Instrumentacja zarządzania Windows)", Category: "system", ID: "sys.wmi", Status: st, Value: val, Details: det,
			Advice: "Uszkodzone lub przeciążone repozytorium WMI spowalnia system i programy. Jako administrator: winmgmt /verifyrepository, a przy błędach winmgmt /salvagerepository; uruchom ponownie usługę Winmgmt lub komputer i powtórz diagnostykę."})
	} else if s.wmiCalls.Load() > 0 && !s.cancel.Load() {
		ctx.Add(F{Title: "Usługa WMI (Instrumentacja zarządzania Windows)", Category: "system", ID: "sys.wmi", Status: OK, Value: fmt.Sprintf("Odpowiada poprawnie (zapytań: %d)", s.wmiCalls.Load())})
	}
	s.Finished = time.Now()
}

// Result zbiera wyniki skanowania (Get-SdResult).
func (s *Scan) Result() *Result {
	r := &Result{Tool: "SerwisDiag", Version: Version, Generated: time.Now(), Started: s.Started, Finished: s.Finished,
		Level: s.Opt.Level, Tests: s.Opt.Tests, IsAdmin: s.IsAdmin, Meta: map[string]any{}}
	s.mu.Lock()
	for k, v := range s.meta {
		r.Meta[k] = v
	}
	s.mu.Unlock()
	s.resMu.RLock()
	defer s.resMu.RUnlock()
	for _, run := range append(slices.Clone(s.runs), s.tail) {
		r.Findings = append(r.Findings, run.findings...)
		r.Tables = append(r.Tables, run.tables...)
		r.Log = append(r.Log, run.log...)
		if run.executed {
			r.Timings = append(r.Timings, Timing{ID: run.check.ID, Seconds: run.seconds})
		}
	}
	r.Counts = countFindings(r.Findings)
	r.Verdict = verdictFor(r.Counts)
	return r
}

// Snapshot zwraca kopię dotychczasowych wyników i tabel - także w trakcie skanowania (okno programu).
func (s *Scan) Snapshot() ([]Finding, []Table) {
	s.resMu.RLock()
	defer s.resMu.RUnlock()
	var fs []Finding
	var ts []Table
	for _, run := range append(slices.Clone(s.runs), s.tail) {
		fs = append(fs, run.findings...)
		ts = append(ts, run.tables...)
	}
	return fs, ts
}

// FindingsCount: liczba dotychczasowych wyników (okno odświeża widok, gdy się zmieni).
func (s *Scan) FindingsCount() int {
	s.resMu.RLock()
	defer s.resMu.RUnlock()
	n := len(s.tail.findings)
	for _, run := range s.runs {
		n += len(run.findings)
	}
	return n
}

// ---------------------------------------------------------------- kontekst testu

// Ctx - kontekst wykonywanego testu: dopisuje wyniki, tabele i wpisy dziennika do jego części raportu.
type Ctx struct {
	s     *Scan
	r     *checkRun
	cat   string
	id    string
	title string
}

// Scan zwraca skanowanie, do którego należy test.
func (c *Ctx) Scan() *Scan { return c.s }

// IsAdmin: program działa z uprawnieniami administratora.
func (c *Ctx) IsAdmin() bool { return c.s.IsAdmin }

// Offline: tryb bez internetu (Test-SdOffline).
func (c *Ctx) Offline() bool { return c.s.Opt.Offline }

// Full: pełny poziom diagnostyki.
func (c *Ctx) Full() bool { return c.s.Opt.Level == "Full" }

const offlineSkipText = "Pominięto - diagnostyka w trybie bez internetu."

// F - parametry wyniku (Add-Finding).
type F struct {
	Title          string
	Status         Status
	Value          string
	Details        string
	Advice         string
	Category       string
	ID             string
	Private        bool
	CustomerTitle  string
	CustomerStatus Status
	CustomerValue  string
	CustomerAdvice string
	// LastEvent: czas ostatniego zdarzenia (wyniki z historii) - trafia do wartości jako "ostatnio ...".
	LastEvent time.Time
}

// Add dodaje wynik (Add-Finding).
func (c *Ctx) Add(f F) {
	cat, id := f.Category, f.ID
	if cat == "" {
		cat = c.cat
	}
	if id == "" {
		id = c.id
	}
	st := f.Status
	if st == "" {
		st = Info
	}
	val := f.Value
	cv := f.CustomerValue
	if !f.LastEvent.IsZero() {
		val = joinNonEmpty("; ", val, "ostatnio "+fmtDt(f.LastEvent))
		if cv != "" {
			cv = joinNonEmpty("; ", cv, "ostatnio "+fmtDt(f.LastEvent))
		}
	}
	var cust *Customer
	if f.CustomerTitle != "" || f.CustomerStatus != "" || cv != "" {
		cs := f.CustomerStatus
		if _, ok := statusInfo[cs]; !ok {
			cs = ""
		}
		cust = &Customer{Title: f.CustomerTitle, Status: cs, Value: cv, Advice: f.CustomerAdvice}
	}
	c.s.resMu.Lock()
	c.r.findings = append(c.r.findings, Finding{CheckID: id, Category: cat, Title: f.Title, Status: st, Value: val,
		Details: f.Details, Advice: f.Advice, Private: f.Private, Customer: cust, Time: time.Now().Format("15:04:05"), LastEvent: f.LastEvent})
	c.s.resMu.Unlock()
}

// T - parametry tabeli (Add-Table).
type T struct {
	Name     string
	Rows     []Row
	Category string
	Note     string
	Private  bool
}

// AddTable dodaje tabelę; kolumny to suma kolumn wszystkich wierszy w kolejności wystąpienia.
func (c *Ctx) AddTable(t T) {
	var rows []Row
	for _, r := range t.Rows {
		if r != nil {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return
	}
	var cols []string
	for _, r := range rows {
		for _, cell := range r {
			if !slices.Contains(cols, cell.Key) {
				cols = append(cols, cell.Key)
			}
		}
	}
	cat := t.Category
	if cat == "" {
		cat = c.cat
	}
	c.s.resMu.Lock()
	c.r.tables = append(c.r.tables, Table{Category: cat, Name: t.Name, Columns: cols, Rows: rows, Note: t.Note, Private: t.Private})
	c.s.resMu.Unlock()
}

// SetMeta ustawia metadane raportu.
func (c *Ctx) SetMeta(key string, v any) { c.s.SetMeta(key, v) }

func logLine(level, msg string) string {
	return fmt.Sprintf("%s [%s] %s", time.Now().Format("15:04:05"), level, msg)
}

// Log dopisuje wpis do dziennika programu (Write-SdLog).
func (c *Ctx) Log(level, msg string) {
	c.s.resMu.Lock()
	c.r.log = append(c.r.log, logLine(level, msg))
	c.s.resMu.Unlock()
}

// Progress ustawia opis bieżącej czynności (Set-SdProgressText) - używany przez testy obciążeniowe.
func (c *Ctx) Progress(text string) {
	if c.s.Opt.OnProgress != nil && text != "" {
		c.s.mu.Lock()
		c.s.Opt.OnProgress(-1, -1, &Check{ID: c.id, Title: text})
		c.s.mu.Unlock()
	}
}

// cached odpowiada Get-SdCached: dane współdzielone przez testy pobierane raz na skanowanie
// (także przy testach równoległych - kolejne wywołania czekają na pierwsze).
func cached[T any](c *Ctx, key string, f func() T) T {
	type entry struct {
		once sync.Once
		v    T
	}
	e, _ := c.s.cache.LoadOrStore(key, &entry{})
	en := e.(*entry)
	en.once.Do(func() {
		defer func() {
			if p := recover(); p != nil {
				c.Log("ERROR", fmt.Sprintf("%s: %v", key, p))
			}
		}()
		en.v = f()
	})
	return en.v
}

// ---------------------------------------------------------------- WMI z bezpiecznikiem (Get-Cim)

const (
	wmiSlowSec    = 20 // zapytanie WMI dłuższe niż tyle sekund uznajemy za objaw problemu
	wmiBreakAfter = 3  // po tylu takich zapytaniach kolejne są pomijane
)

// Q - zapytanie WMI. Props: właściwości do odczytania (puste: wszystkie).
type Q struct {
	NS      string
	Class   string
	Props   []string
	Where   string
	WQL     string // całe zapytanie (np. ASSOCIATORS OF) - zamiast Class/Where
	Timeout time.Duration
}

// Cim wykonuje zapytanie WMI jak Get-Cim: błąd = pusta lista i wpis w dzienniku; bardzo wolne zapytania
// uruchamiają bezpiecznik (uszkodzone repozytorium WMI potrafi wstrzymać każde zapytanie do limitu czasu).
func (c *Ctx) Cim(q Q) []win.WmiObject {
	s := c.s
	s.wmiCalls.Add(1)
	if s.wmiBroken.Load() {
		s.wmiSkipped.Add(1)
		return nil
	}
	ns := q.NS
	if ns == "" {
		ns = `root\cimv2`
	}
	to := q.Timeout
	if to == 0 {
		to = 40 * time.Second
	}
	wql := q.WQL
	if wql == "" {
		sel := "*"
		if len(q.Props) > 0 {
			sel = strings.Join(q.Props, ",")
		}
		wql = "SELECT " + sel + " FROM " + q.Class
		if q.Where != "" {
			wql += " WHERE " + q.Where
		}
	}
	what := ns + `\` + q.Class
	start := time.Now()
	objs, err := win.WmiQuery(ns, wql, q.Props, to)
	var he *win.HResultError
	if err != nil && q.WQL == "" && len(q.Props) > 0 && errors.As(err, &he) && he.Code == 0x80041017 {
		// WBEM_E_INVALID_QUERY: starszy system nie zna którejś właściwości - pobierz wszystkie
		wql = "SELECT * FROM " + q.Class
		if q.Where != "" {
			wql += " WHERE " + q.Where
		}
		objs, err = win.WmiQuery(ns, wql, q.Props, to)
	}
	el := time.Since(start).Seconds()
	if err != nil {
		if el >= to.Seconds()-2 {
			c.registerWmiSlow(what, el)
		}
		c.Log("WARN", fmt.Sprintf("CIM %s: %v", what, err))
		return nil
	}
	if el >= min(float64(wmiSlowSec), to.Seconds()-1) {
		c.registerWmiSlow(what, el)
	}
	return objs
}

// cim: krótka forma zapytania w root\cimv2.
func (c *Ctx) cim(class string, props ...string) []win.WmiObject {
	return c.Cim(Q{Class: class, Props: props})
}

// cim1 zwraca pierwszy obiekt (nil, gdy brak).
func (c *Ctx) cim1(class string, props ...string) win.WmiObject {
	r := c.cim(class, props...)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func (c *Ctx) registerWmiSlow(what string, sec float64) {
	s := c.s
	n := s.wmiSlow.Add(1)
	c.Log("WARN", fmt.Sprintf("WMI odpowiada bardzo wolno: %s (%s s)", what, fmtNum(sec, 0)))
	if n >= wmiBreakAfter && !s.wmiBroken.Swap(true) {
		c.Log("ERROR", fmt.Sprintf("WMI: %d zapytania przekroczyły limit czasu - kolejne zapytania WMI są pomijane.", n))
	}
}

func psRoundN(x float64, digits int) float64 {
	p := 1.0
	for i := 0; i < digits; i++ {
		p *= 10
	}
	return psRound(x*p) / p
}
