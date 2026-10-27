// SerwisDiag w Go - diagnostyka komputerów z Windows 10/11 dla serwisu komputerowego.
// Przeniesienie wersji PowerShell (C:\serwis-diag) do jednego pliku .exe bez PowerShella i C#.
//
// Ten sam kod buduje dwa pliki (build.ps1):
//   - serwisdiag.exe (aplikacja okienkowa): bez parametrów okno programu, -NoGui - tryb konsolowy,
//     -Monitor - monitorowanie w tle (ikona przy zegarze), -EnableMonitor / -DisableMonitor - autostart monitora;
//   - serwisdiag-konsola.exe (aplikacja konsolowa): zawsze tryb konsolowy - do skryptów i pomiarów.
//
// Przykłady:
//
//	serwisdiag.exe -Ticket 2025-014 -Customer "Jan Kowalski"      (okno z wypełnionymi danymi zlecenia)
//	serwisdiag-konsola.exe -Level Full -Ticket 2025-014 -Customer "Jan Kowalski"
//	serwisdiag-konsola.exe -Level Quick -TestIds tt.disk,tt.cpu -OutDir D:\raporty
//	serwisdiag-konsola.exe -Sequential      (testy po kolei, jak w wersji PowerShell - do porównań)
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/diag"
	"serwisdiag/internal/ui"
	"serwisdiag/internal/win"
)

// guiBuild = "1" w pliku okienkowym (go build -ldflags "-H windowsgui -X main.guiBuild=1").
var guiBuild string

type options struct {
	level                                         string
	testIDs, outDir, ticket, customer, technician string
	note, screenshot                              string
	open, offline, pdf, anonymous, elevate, pause bool
	sequential, quiet, noGui, selfTest            bool
	monitor, enableMonitor, disableMonitor        bool
	stopMonitor, noStart                          bool
	update, register                              bool
	repair, repairList, installDrivers            bool
	repairIDs, undoRepair                         string
	compareWith, compareBefore, compareAfter      string
	workDone, backup, backupInclude               string
	help                                          bool
}

var switches = []string{"NoGui", "Open", "Offline", "Pdf", "Anonymous", "Elevate", "Pause", "Parallel", "Sequential", "Quiet", "SkipIntegrity",
	"InstallDrivers", "Repair", "RepairList", "Monitor", "EnableMonitor", "DisableMonitor", "StopMonitor", "NoStart", "Update", "Register", "SelfTest"}
var valued = []string{"Level", "TestIds", "OutDir", "Ticket", "Customer", "Technician", "Note", "RepairIds", "CompareWith", "CompareBefore",
	"CompareAfter", "WorkDone", "Backup", "BackupInclude", "UndoRepair", "ScreenshotPath"}

// resolveParam: nazwa parametru bez względu na wielkość liter, także jednoznaczny początek (jak w PowerShellu).
func resolveParam(name string) (string, bool) {
	var hits []string
	for _, n := range append(append([]string{}, switches...), valued...) {
		if strings.EqualFold(n, name) {
			return n, true
		}
		if strings.HasPrefix(strings.ToLower(n), strings.ToLower(name)) {
			hits = append(hits, n)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

func parseArgs(args []string) (options, error) {
	o := options{level: "Quick"}
	isSwitch := func(n string) bool {
		for _, s := range switches {
			if s == n {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "/?" || strings.EqualFold(a, "-h") || strings.EqualFold(a, "-help") || strings.EqualFold(a, "--help") {
			o.help = true
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return o, fmt.Errorf("nieznany argument: %s", a)
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), ":")
		p, ok := resolveParam(name)
		if !ok {
			return o, fmt.Errorf("nieznany parametr: %s", a)
		}
		if isSwitch(p) {
			on := !hasVal || !strings.EqualFold(strings.TrimPrefix(val, "$"), "false")
			switch p {
			case "Open":
				o.open = on
			case "Offline":
				o.offline = on
			case "Pdf":
				o.pdf = on
			case "Anonymous":
				o.anonymous = on
			case "Elevate":
				o.elevate = on
			case "Pause":
				o.pause = on
			case "Sequential":
				o.sequential = on
			case "Parallel":
				o.sequential = !on
			case "Quiet":
				o.quiet = on
			case "NoGui":
				o.noGui = on
			case "SelfTest":
				o.selfTest = on
			case "Monitor":
				o.monitor = on
			case "EnableMonitor":
				o.enableMonitor = on
			case "DisableMonitor":
				o.disableMonitor = on
			case "StopMonitor":
				o.stopMonitor = on
			case "NoStart":
				o.noStart = on
			case "Update":
				o.update = on
			case "Register":
				o.register = on
			case "Repair":
				o.repair = on
			case "RepairList":
				o.repairList = on
			case "InstallDrivers":
				o.installDrivers = on
			}
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return o, fmt.Errorf("brak wartości parametru -%s", p)
			}
			i++
			val = args[i]
		}
		switch p {
		case "Level":
			switch strings.ToLower(val) {
			case "quick":
				o.level = "Quick"
			case "full":
				o.level = "Full"
			default:
				return o, fmt.Errorf("-Level przyjmuje Quick albo Full")
			}
		case "TestIds":
			o.testIDs = val
		case "OutDir":
			o.outDir = val
		case "Ticket":
			o.ticket = val
		case "Customer":
			o.customer = val
		case "Technician":
			o.technician = val
		case "Note":
			o.note = val
		case "ScreenshotPath":
			o.screenshot = val
		case "RepairIds":
			o.repairIDs = val
		case "UndoRepair":
			o.undoRepair = val
		case "CompareWith":
			o.compareWith = val
		case "CompareBefore":
			o.compareBefore = val
		case "CompareAfter":
			o.compareAfter = val
		case "WorkDone":
			o.workDone = val
		case "Backup":
			o.backup = val
		case "BackupInclude":
			o.backupInclude = val
		}
	}
	return o, nil
}

const usage = `SerwisDiag (wersja Go) - diagnostyka komputera z Windows 10/11.

Okno programu:
  serwisdiag.exe [-Ticket <nr>] [-Customer <klient>] [-Note <opis>] [-Offline] [-Elevate]
Monitor przy zegarze:
  serwisdiag.exe -Monitor | -EnableMonitor [-NoStart] | -DisableMonitor | -StopMonitor
  (-DisableMonitor i -StopMonitor dotyczą tylko tej kopii programu; -StopMonitor - bez zmiany autostartu)
Wersja zainstalowana:
  serwisdiag.exe -Update      sprawdź, pobierz i zainstaluj nową wersję z GitHuba
  serwisdiag.exe -Register    okno dobrowolnej rejestracji programu (bez okna głównego)
Tryb konsolowy (serwisdiag-konsola.exe albo serwisdiag.exe -NoGui):
  serwisdiag-konsola.exe [-Level Quick|Full] [-TestIds tt.disk,tt.cpu,tt.net,tt.volscan,tt.dism]
                 [-OutDir <folder>] [-Ticket <nr>] [-Customer <klient>] [-Technician <technik>] [-Note <opis>]
                 [-Offline] [-Pdf] [-Anonymous] [-Open] [-Elevate] [-Pause] [-Sequential] [-Quiet]

  -Sequential  testy po kolei, jak w wersji PowerShell (domyślnie niezależne testy działają równolegle)
  -Quiet       bez listy wykonywanych testów

Naprawy po diagnostyce (tryb konsolowy, wymagają zwykle uprawnień administratora - dodaj -Elevate):
  -RepairList               tylko lista tego, co program może naprawić (bez zmian w systemie)
  -Repair                   zalecane naprawy (przed nimi punkt przywracania; dziennik napraw obok raportów)
  -RepairIds fx.dism,fx.sfc wskazane naprawy (identyfikatory z -RepairList)
  -UndoRepair <plik *_naprawy.json | ostatni>
                            cofnięcie zmian ustawień z dziennika napraw (bez diagnostyki)
  -InstallDrivers           po diagnostyce zalecane sterowniki z Windows Update (punkt przywracania przed instalacją)

Raport z naprawy - porównanie diagnostyki przed i po naprawie (raport techniczny i dla klienta):
  -CompareWith <raport.json> [-WorkDone <opis>]
                            po diagnostyce porównaj ją z raportem JSON sprzed naprawy
  -CompareBefore <raport.json> -CompareAfter <raport.json> [-WorkDone <opis>] [-OutDir <folder>] [-Open]
                            porównanie dwóch zapisanych raportów (bez diagnostyki)

Kopia przed reinstalacją Windows (bez diagnostyki; sterowniki, Wi-Fi i BitLocker wymagają administratora):
  -Backup <folder> [-BackupInclude drivers,programs,system,wifi,bitlocker]
                            domyślnie: sterowniki, lista programów, dane komputera i klucz Windows

Kody wyjścia: 0 - brak problemów, 1 - ostrzeżenia, 2 - problemy krytyczne, 3 - błąd uruchomienia.`

// ---------------------------------------------------------------- konsola z kolorami (Write-Host -ForegroundColor)

var (
	stdout      = windows.Handle(os.Stdout.Fd())
	isConsole   bool
	defaultAttr uint16 = 7
)

const (
	cDarkGray   = 8
	cDarkYellow = 6
	cGreen      = 10
	cCyan       = 11
	cRed        = 12
	cMagenta    = 13
	cYellow     = 14
)

func initConsole() {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(stdout, &info) == nil {
		isConsole = true
		defaultAttr = info.Attributes
	}
}

var procSetConsoleTextAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleTextAttribute")

func setColor(attr uint16) { procSetConsoleTextAttribute.Call(uintptr(stdout), uintptr(attr)) }

func say(color uint16, format string, a ...any) {
	if isConsole && color != 0 {
		setColor((defaultAttr & 0xF0) | color)
		defer setColor(defaultAttr)
	}
	fmt.Fprintf(os.Stdout, format+"\n", a...)
}

func main() { os.Exit(run(os.Args[1:])) }

// attachConsole: plik okienkowy w trybie konsolowym (-NoGui) pisze do konsoli, z której go uruchomiono,
// a bez niej - do nowego okna konsoli. Wyjście przekierowane do pliku albo potoku zostaje bez zmian.
func attachConsole() {
	if t, err := windows.GetFileType(windows.Handle(os.Stdout.Fd())); err == nil && t != windows.FILE_TYPE_UNKNOWN {
		return
	}
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	if r, _, _ := k32.NewProc("AttachConsole").Call(^uintptr(0)); r == 0 { // ATTACH_PARENT_PROCESS
		k32.NewProc("AllocConsole").Call()
	}
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = f
	}
	stdout = windows.Handle(os.Stdout.Fd())
}

func run(args []string) int {
	o, err := parseArgs(args)
	gui := guiBuild == "1"
	compareOnly := o.compareBefore != "" || o.compareAfter != ""
	consoleMode := !gui || o.noGui || err != nil || o.help || o.undoRepair != "" || compareOnly || o.backup != "" || o.update
	if gui && consoleMode {
		attachConsole()
	}
	initConsole()
	if err != nil {
		say(cRed, "%s", err)
		say(0, "%s", usage)
		return 3
	}
	if o.help {
		say(0, "%s", usage)
		return 0
	}
	exe, _ := os.Executable()
	root := filepath.Dir(exe)
	switch {
	case o.monitor:
		return ui.RunMonitor(root, exe)
	case o.enableMonitor:
		how, err := ui.EnableMonitor(exe, root, !o.noStart)
		if err != nil {
			say(cRed, "Nie włączono monitorowania: %v", err)
			return 1
		}
		if how == "task" {
			say(0, "Monitorowanie włączone: zadanie Harmonogramu dla wszystkich użytkowników.")
		} else {
			say(0, "Monitorowanie włączone: autostart bieżącego użytkownika.")
		}
		return 0
	case o.disableMonitor:
		// tylko ta kopia programu: odinstalowanie nie wyłącza monitora innej kopii (np. przenośnej)
		removed, was := ui.DisableMonitor(exe)
		msg := "Monitorowanie wyłączone"
		if len(removed) > 0 {
			msg += " (usunięto: " + strings.Join(removed, ", ") + ")"
		}
		if was {
			msg += "; działający monitor zakończy pracę"
		}
		say(0, "%s.", msg)
		return 0
	case o.stopMonitor:
		ui.StopMonitor(exe)
		return 0
	case o.register:
		return ui.RunRegistration(root, o.screenshot)
	}
	if !consoleMode {
		if o.elevate && !win.IsAdmin() && relaunchElevated(exe, args) {
			return 0
		}
		var once sync.Once
		var integrity string
		return ui.RunGUI(ui.Options{Root: root, Exe: exe, Ticket: o.ticket, Customer: o.customer, Note: o.note, Offline: o.offline,
			SelfTest: o.selfTest, Screenshot: o.screenshot,
			Integrity: func() string { once.Do(func() { integrity = selfHash(exe) }); return integrity }})
	}
	defer func() {
		if o.pause {
			fmt.Println()
			fmt.Print("Naciśnij Enter, aby zamknąć okno")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
	}()
	if o.update {
		return update(root)
	}
	// ścieżki względne - względem bieżącego folderu (program uruchomiony ponownie przez UAC startuje w innym)
	undoLatest := strings.EqualFold(o.undoRepair, "ostatni") || strings.EqualFold(o.undoRepair, "latest")
	args = absPathArgs(args, undoLatest)
	for _, p := range []*string{&o.outDir, &o.undoRepair, &o.compareWith, &o.compareBefore, &o.compareAfter, &o.backup} {
		if *p != "" && !(p == &o.undoRepair && undoLatest) {
			if abs, err := filepath.Abs(*p); err == nil {
				*p = abs
			}
		}
	}
	if o.elevate && !win.IsAdmin() {
		if relaunchElevated(exe, args) {
			return 0
		}
		say(cYellow, "Nie uzyskano uprawnień administratora (anulowano UAC) - program działa bez nich, część testów zostanie pominięta.")
	}

	settings := diag.LoadSettings(root)
	if o.technician != "" {
		settings.Technician = o.technician
	}
	if compareOnly {
		return compareReports(o, settings)
	}
	if o.backup != "" {
		return backup(o)
	}
	if o.undoRepair != "" {
		return undoRepair(o, root, settings, undoLatest)
	}
	var tests []string
	for _, t := range strings.Split(o.testIDs, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tests = append(tests, t)
		}
	}
	scan := diag.NewScan(diag.Options{Root: root, Level: o.level, Tests: tests, Offline: o.offline, Parallel: !o.sequential,
		OnProgress: func(i, n int, ch *diag.Check) {
			if o.quiet {
				return
			}
			if i < 0 {
				say(cDarkGray, "      %s", ch.Title)
				return
			}
			say(cDarkGray, "[%2d/%d] %s", i, n, ch.Title)
		}})
	scan.SetMeta("Ticket", o.ticket)
	scan.SetMeta("Customer", o.customer)
	scan.SetMeta("Technician", settings.Technician)
	scan.SetMeta("Notes", o.note)
	scan.SetMeta("Integrity", selfHash(exe))

	mode := "szybki"
	if o.level == "Full" {
		mode = "pełny"
	}
	extra := ""
	if len(tests) > 0 {
		extra += " + testy: " + strings.Join(tests, ", ")
	}
	if o.offline {
		extra += " (bez internetu)"
	}
	run := "równolegle"
	if o.sequential {
		run = "po kolei"
	}
	fmt.Println()
	say(cCyan, "SerwisDiag %s (Go) - tryb %s%s; testy %s", diag.Version, mode, extra, run)
	if !scan.IsAdmin {
		say(cYellow, "Uwaga: program działa bez uprawnień administratora - część testów (SMART, BitLocker, dzienniki) zostanie pominięta.")
	}
	if k := diag.KnowledgeNote(); k != "" {
		say(cYellow, "Uwaga: baza wiedzy programu (okresy wsparcia Windows i ESU): %s.", k)
	}
	var before *diag.Result
	if o.compareWith != "" {
		if before, err = diag.ImportReportJSON(o.compareWith); err != nil {
			say(cYellow, "Nie wczytano raportu do porównania: %v", err)
		}
	}
	scan.Run()
	res := scan.Result()
	dir := o.outDir
	if dir == "" {
		dir = diag.ResolveReportDir(root, settings)
	}
	paths, err := diag.SaveReports(res, settings, dir)
	if err != nil {
		say(cRed, "Nie zapisano raportów: %v", err)
		return 3
	}
	colors := map[diag.Status]uint16{diag.Crit: cRed, diag.Warn: cYellow, diag.Err: cMagenta}
	fmt.Println()
	for _, f := range res.Findings {
		if c, ok := colors[f.Status]; ok {
			say(c, "[%s] %s: %s", f.Status.Label(), f.Title, f.Value)
			if f.Advice != "" {
				say(cDarkYellow, "      zalecenie: %s", f.Advice)
			}
		}
	}
	fmt.Println()
	vc := uint16(cGreen)
	switch res.Verdict.Level {
	case diag.Crit:
		vc = cRed
	case diag.Warn:
		vc = cYellow
	}
	c := res.Counts
	say(vc, "WYNIK: %s (krytyczne: %d, ostrzeżenia: %d, informacje: %d, OK: %d, pominięte: %d)", res.Verdict.Title, c[diag.Crit], c[diag.Warn], c[diag.Info], c[diag.OK], c[diag.Skip])
	say(0, "Raport techniczny : %s", paths.Technical)
	say(0, "Raport dla klienta: %s", paths.Customer)
	say(0, "Dane JSON         : %s", paths.JSON)
	if o.pdf || settings.AutoPdf {
		if p, err := diag.SavePDF(paths.Customer); err == nil {
			say(0, "PDF dla klienta   : %s", p)
		} else {
			say(cYellow, "PDF dla klienta   : nie zapisano - %v", err)
		}
	}
	if o.anonymous {
		if h, _, err := diag.SaveAnonymousReports(res, dir); err == nil {
			say(0, "Raport anonimowy  : %s (przejrzyj przed publikacją)", h)
		} else {
			say(cYellow, "Raport anonimowy  : nie zapisano - %v", err)
		}
	}
	if before != nil {
		cmp := diag.NewComparison(before, res, o.workDone)
		if cp, err := diag.SaveComparisonReports(cmp, settings, dir); err != nil {
			say(cYellow, "Nie udało się utworzyć porównania: %v", err)
		} else {
			say(cCyan, "Porównanie z raportem sprzed naprawy: %s", diag.ComparisonSummaryText(cmp.Counts))
			sayComparison(cmp, cp)
		}
	}
	say(cDarkGray, "Czas skanowania: %s", res.Finished.Sub(res.Started).Round(100*time.Millisecond))
	if o.repair || o.repairIDs != "" || o.repairList {
		repairAfterScan(o, res, scan.IsAdmin, dir)
	}
	if o.installDrivers {
		installDrivers(o.offline, scan.IsAdmin)
	}
	if o.open {
		openFile(paths.Technical)
	}
	switch {
	case c[diag.Crit] > 0:
		return 2
	case c[diag.Warn] > 0:
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- porównanie przed/po naprawie

func sayComparison(cmp *diag.Comparison, cp diag.ComparisonPaths) {
	for _, n := range cmp.Notes {
		say(cYellow, "Uwaga: %s", n)
	}
	say(0, "Porównanie (technik): %s", cp.Technical)
	say(0, "Porównanie (klient) : %s", cp.Customer)
}

// compareReports: porównanie dwóch zapisanych raportów JSON (-CompareBefore, -CompareAfter) bez diagnostyki.
func compareReports(o options, settings diag.Settings) int {
	if o.compareBefore == "" || o.compareAfter == "" {
		say(cRed, "Porównanie wymaga obu parametrów: -CompareBefore <raport.json> i -CompareAfter <raport.json>.")
		return 3
	}
	fail := func(err error) int {
		say(cRed, "Nie udało się porównać raportów: %v", err)
		return 3
	}
	before, err := diag.ImportReportJSON(o.compareBefore)
	if err != nil {
		return fail(err)
	}
	after, err := diag.ImportReportJSON(o.compareAfter)
	if err != nil {
		return fail(err)
	}
	cmp := diag.NewComparison(before, after, o.workDone)
	dir := o.outDir
	if dir == "" {
		dir = filepath.Dir(o.compareAfter)
	}
	cp, err := diag.SaveComparisonReports(cmp, settings, dir)
	if err != nil {
		return fail(err)
	}
	say(0, "Porównanie: %s", diag.ComparisonSummaryText(cmp.Counts))
	sayComparison(cmp, cp)
	if o.open {
		openFile(cp.Technical)
	}
	return 0
}

// ---------------------------------------------------------------- kopia przed reinstalacją Windows

// backup: kopia przed reinstalacją Windows do wskazanego folderu (-Backup, bez diagnostyki).
// update - parametr -Update (wersja zainstalowana): sprawdzenie, pobranie i uruchomienie instalatora nowej wersji.
func update(root string) int {
	u, err := diag.RunUpdate(root, false, "")
	if err != nil {
		say(cRed, "%s", err)
		return 1
	}
	c := uint16(cCyan)
	if u.Started {
		c = cGreen
	}
	say(c, "%s", u.Message)
	if !u.Release.Ok {
		return 1
	}
	return 0
}

func backup(o options) int {
	inc := diag.BackupIncludeList(o.backupInclude)
	say(cCyan, "Kopia przed reinstalacją Windows: %s", strings.Join(inc, ", "))
	if !win.IsAdmin() {
		say(cYellow, "Uwaga: bez uprawnień administratora sterowniki, profile Wi-Fi i klucze BitLocker zostaną pominięte (dodaj -Elevate).")
	}
	bk, err := diag.RunBackup(o.backup, inc, func(t string) {
		if t != "" {
			say(cDarkGray, "%s", t)
		}
	})
	if err != nil {
		say(cRed, "Nie udało się utworzyć kopii: %v", err)
		return 3
	}
	failed := false
	for _, p := range bk.Parts {
		say(resultColors[p.Result], "  [%s] %s: %s", strings.ToUpper(p.Result), p.Label, p.Message)
		failed = failed || p.Result == "error"
	}
	for _, w := range bk.Warnings {
		say(cYellow, "Uwaga: %s", w)
	}
	say(0, "Folder kopii: %s", bk.Dir)
	if failed {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- naprawy i ich cofanie

var resultColors = map[string]uint16{"ok": cGreen, "warn": cYellow, "error": cRed, "skip": cDarkGray}

// repairAfterScan: naprawy po diagnostyce (-RepairList: tylko lista; -Repair: zalecane; -RepairIds: wskazane).
func repairAfterScan(o options, res *diag.Result, isAdmin bool, dir string) {
	fmt.Println()
	cands := diag.FixCandidates(diag.NewFixContext(res, isAdmin, o.offline))
	var ids []string
	for _, s := range strings.Split(o.repairIDs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			ids = append(ids, strings.ToLower(s))
		}
	}
	say(cCyan, "Program może naprawić: %s", diag.FmtCount(len(cands), "problem", "problemy", "problemów"))
	for _, c := range cands {
		mark, blocked := " ", ""
		switch {
		case c.Blocked != "":
			mark, blocked = "-", "  (niedostępne: "+c.Blocked+")"
		case c.Default:
			mark = "x"
		}
		say(0, "  [%s] %-14s %s%s", mark, c.FixID, c.Title, blocked)
		if c.Problem != "" {
			say(cDarkGray, "                       %s", c.Problem)
		}
	}
	say(cDarkGray, "  [x] = zalecane (-Repair); pozostałe wskaż przez -RepairIds")
	var sel []diag.FixCandidate
	for _, c := range cands {
		if (len(ids) > 0 && slices.Contains(ids, strings.ToLower(c.FixID))) || (len(ids) == 0 && o.repair && c.Default) {
			sel = append(sel, c)
		}
	}
	if o.repairList {
		return
	}
	if len(sel) == 0 {
		say(cGreen, "Brak napraw do wykonania.")
		return
	}
	say(cYellow, "Naprawy zmieniają ustawienia systemu. Program jest udostępniany bez gwarancji - wykonujesz je na własną odpowiedzialność.")
	var runnable, restoreOn, rest []diag.FixCandidate
	for _, c := range sel {
		if c.Blocked == "" {
			runnable = append(runnable, c)
		}
		if c.FixID == "fx.restoreon" && c.Blocked == "" {
			restoreOn = append(restoreOn, c)
		} else {
			rest = append(rest, c)
		}
	}
	var results []diag.FixResult
	if len(runnable) > 0 {
		// najpierw włączenie ochrony systemu (jeśli wybrane) - bez niej punkt przywracania nie powstanie
		if len(restoreOn) > 0 {
			results = diag.RunFixes(restoreOn, nil)
		}
		if err := diag.CreateRepairRestorePoint(); err != nil {
			say(cYellow, "Nie utworzono punktu przywracania: %v", err)
		} else {
			say(0, "Punkt przywracania: utworzony (albo istnieje punkt z ostatnich 24 godzin).")
		}
	}
	results = append(results, diag.RunFixes(rest, func(i, n int, title string) {
		if title != "" {
			say(cDarkGray, "[%d/%d] %s...", i, n, title)
		}
	})...)
	reboot := false
	for _, r := range results {
		msg := ""
		if r.Message != "" {
			msg = " - " + r.Message
		}
		say(resultColors[r.Result], "  %s: %s%s", r.Title, strings.ToUpper(r.Result), msg)
		reboot = reboot || (r.Reboot && r.Result != "error")
	}
	if p, err := diag.SaveRepairLog(results, res.Meta, dir); err != nil {
		say(cRed, "Nie zapisano dziennika napraw: %v", err)
	} else {
		say(0, "Dziennik napraw: %s", p)
	}
	if reboot {
		say(cYellow, "Część napraw zadziała po ponownym uruchomieniu komputera.")
	}
}

// installDrivers: zalecane sterowniki z Windows Update po diagnostyce (-InstallDrivers - tylko na wyraźne żądanie).
func installDrivers(offline, isAdmin bool) {
	fmt.Println()
	switch {
	case offline:
		say(cYellow, "Instalację sterowników pominięto - tryb bez internetu.")
		return
	case !isAdmin:
		say(cYellow, "Instalacja sterowników wymaga uprawnień administratora (dodaj -Elevate).")
		return
	}
	say(cCyan, "Wyszukiwanie sterowników w Windows Update...")
	ws := diag.WuDriverSearch()
	defer ws.Release()
	sel := diag.SelectDriversToInstall(ws.Items)
	switch {
	case ws.Error != "":
		say(cRed, "Nie udało się wyszukać sterowników: %s", ws.Error)
		return
	case len(sel) == 0:
		say(cGreen, "Brak zalecanych sterowników do zainstalowania.")
		return
	}
	var ids []string
	for _, s := range sel {
		say(0, " - %s", s.Title)
		ids = append(ids, s.UpdateID)
	}
	if err := win.CreateRestorePoint("SerwisDiag - przed instalacją sterowników", win.RestoreDeviceDriverInstall); err != nil {
		say(cYellow, "Nie utworzono punktu przywracania: %v", err)
	} else {
		say(0, "Punkt przywracania: utworzony (albo istnieje punkt z ostatnich 24 godzin).")
	}
	ins := ws.Install(ids, nil)
	if ins.Error != "" {
		say(cRed, "Instalacja nie powiodła się: %s", ins.Error)
	}
	for _, x := range ins.Results {
		c := uint16(cYellow)
		if x.ResultCode == 2 {
			c = cGreen
		}
		say(c, "%s: %s", x.Title, diag.WuResultText(x.ResultCode))
	}
	if ins.RebootRequired {
		say(cYellow, "Wymagane ponowne uruchomienie komputera.")
	}
}

// undoRepair: cofnięcie zmian ustawień z dziennika napraw (bez diagnostyki).
func undoRepair(o options, root string, settings diag.Settings, latest bool) int {
	path := o.undoRepair
	computer := os.Getenv("COMPUTERNAME")
	if latest {
		dir := o.outDir
		if dir == "" {
			dir = diag.ResolveReportDir(root, settings)
		}
		logs := diag.FindRepairLogs(dir, computer)
		if len(logs) == 0 {
			say(cRed, "Nie znaleziono dziennika napraw tego komputera w: %s", dir)
			return 3
		}
		path = logs[0]
	}
	rl, err := diag.ReadRepairLog(path)
	if err != nil {
		say(cRed, "Nie wczytano dziennika napraw: %v", err)
		return 3
	}
	if !strings.EqualFold(rl.Computer, computer) {
		say(cRed, "Dziennik pochodzi z innego komputera (%s) - przerwano.", rl.Computer)
		return 3
	}
	if !win.IsAdmin() {
		say(cRed, "Cofanie zmian wymaga uprawnień administratora (dodaj -Elevate).")
		return 3
	}
	items := diag.UndoItems(rl)
	say(0, "Dziennik napraw: %s", path)
	if len(items) == 0 {
		say(cGreen, "Brak zmian ustawień do cofnięcia.")
		return 0
	}
	undo := diag.RunUndo(items)
	failed := false
	for _, u := range undo {
		msg := ""
		if u.Message != "" {
			msg = " - " + u.Message
		}
		say(resultColors[u.Result], "  [%s] %s%s", strings.ToUpper(u.Result), u.Text, msg)
		failed = failed || u.Result == "error"
	}
	for _, t := range diag.NotUndoable(rl) {
		say(cDarkGray, "  bez cofania (punkt przywracania): %s", t)
	}
	if p, err := diag.SaveUndoLog(undo, path); err == nil {
		say(0, "Dziennik cofnięcia: %s", p)
	}
	if failed {
		return 1
	}
	return 0
}

// absPathArgs: wartości parametrów ze ścieżkami jako ścieżki bezwzględne (przekazywane do procesu uruchomionego
// ponownie przez UAC, którego katalog bieżący jest inny).
func absPathArgs(args []string, undoLatest bool) []string {
	pathParams := []string{"OutDir", "UndoRepair", "CompareWith", "CompareBefore", "CompareAfter", "Backup", "ScreenshotPath"}
	out := slices.Clone(args)
	for i := 0; i < len(out); i++ {
		name, val, hasVal := strings.Cut(strings.TrimLeft(out[i], "-"), ":")
		p, ok := resolveParam(name)
		if !ok || !slices.Contains(pathParams, p) || (p == "UndoRepair" && undoLatest) {
			continue
		}
		if hasVal {
			if abs, err := filepath.Abs(val); err == nil {
				out[i] = "-" + p + ":" + abs
			}
		} else if i+1 < len(out) {
			if abs, err := filepath.Abs(out[i+1]); err == nil {
				out[i+1] = abs
			}
			i++
		}
	}
	return out
}

// selfHash: plik programu to jeden .exe - zamiast manifestu podajemy początek jego sumy SHA-256.
func selfHash(exe string) string {
	f, err := os.Open(exe)
	if err != nil {
		return "nie sprawdzono"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "nie sprawdzono"
	}
	return "jeden plik programu, SHA-256 " + hex.EncodeToString(h.Sum(nil))[:16] + "…"
}

func relaunchElevated(exe string, args []string) bool {
	var fwd []string
	for _, a := range args {
		if strings.EqualFold(strings.TrimLeft(a, "-"), "Elevate") {
			continue
		}
		fwd = append(fwd, diag.FormatCmdArg(a))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(fwd, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL) == nil
}

func openFile(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(path)
	_ = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
