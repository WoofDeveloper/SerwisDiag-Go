// sdcompare porównuje dwa raporty JSON SerwisDiag (np. wersji PowerShell i Go z tego samego komputera):
// wyniki obecne tylko w jednym raporcie, różnice statusu i treści, liczby wierszy tabel i metadane.
//
//	go run ./cmd/sdcompare raport-ps.json raport-go.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type finding struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Value    string `json:"value"`
	Details  string `json:"details"`
	Advice   string `json:"advice"`
}

type table struct {
	Category string            `json:"category"`
	Name     string            `json:"name"`
	Columns  []string          `json:"columns"`
	Rows     []json.RawMessage `json:"rows"`
}

type report struct {
	Version  string         `json:"version"`
	Engine   string         `json:"engine"`
	Level    string         `json:"level"`
	IsAdmin  bool           `json:"isAdmin"`
	Verdict  string         `json:"verdict"`
	Counts   map[string]int `json:"counts"`
	Meta     map[string]any `json:"meta"`
	Findings []finding      `json:"findings"`
	Tables   []table        `json:"tables"`
}

func load(path string) (*report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimPrefix(string(b), string(rune(0xFEFF))))
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// ConvertTo-Json z PowerShella zapisuje pojedynczą tablicę jako obiekt - tu nie ma to znaczenia
	return &r, nil
}

func short(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("Użycie: sdcompare <raport-A.json> <raport-B.json>")
		os.Exit(2)
	}
	a, err := load(os.Args[1])
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	b, err := load(os.Args[2])
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	name := func(r *report) string {
		if r.Engine == "go" {
			return "Go"
		}
		return "PS"
	}
	na, nb := name(a), name(b)
	fmt.Printf("A = %s %s (%s, admin: %v), B = %s %s (%s, admin: %v)\n", na, a.Version, a.Level, a.IsAdmin, nb, b.Version, b.Level, b.IsAdmin)
	fmt.Printf("Werdykt: A %s, B %s\n", a.Verdict, b.Verdict)
	for _, k := range []string{"crit", "warn", "info", "ok", "skip", "error"} {
		mark := ""
		if a.Counts[k] != b.Counts[k] {
			mark = "  <-- różnica"
		}
		fmt.Printf("  %-5s A %3d  B %3d%s\n", k, a.Counts[k], b.Counts[k], mark)
	}

	key := func(f finding) string { return f.ID + " | " + f.Title }
	ma, mb := map[string]finding{}, map[string]finding{}
	var order []string
	for _, f := range a.Findings {
		ma[key(f)] = f
		order = append(order, key(f))
	}
	for _, f := range b.Findings {
		if _, ok := mb[key(f)]; !ok {
			mb[key(f)] = f
		}
		if _, ok := ma[key(f)]; !ok {
			order = append(order, key(f))
		}
	}
	same, diffStatus, diffText := 0, 0, 0
	fmt.Println("\n=== Wyniki")
	for _, k := range order {
		fa, okA := ma[k]
		fb, okB := mb[k]
		switch {
		case !okB:
			fmt.Printf("[tylko %s] %s: %s — %s\n", na, k, fa.Status, short(fa.Value))
		case !okA:
			fmt.Printf("[tylko %s] %s: %s — %s\n", nb, k, fb.Status, short(fb.Value))
		case fa.Status != fb.Status:
			diffStatus++
			fmt.Printf("[STATUS] %s: %s -> %s\n    A: %s\n    B: %s\n", k, fa.Status, fb.Status, short(fa.Value), short(fb.Value))
		case fa.Value != fb.Value || fa.Advice != fb.Advice || fa.Details != fb.Details:
			diffText++
			fmt.Printf("[treść] %s (%s)\n", k, fa.Status)
			if fa.Value != fb.Value {
				fmt.Printf("    wartość A: %s\n    wartość B: %s\n", short(fa.Value), short(fb.Value))
			}
			if fa.Advice != fb.Advice {
				fmt.Printf("    zalecenie A: %s\n    zalecenie B: %s\n", short(fa.Advice), short(fb.Advice))
			}
			if fa.Details != fb.Details {
				fmt.Printf("    szczegóły A: %s\n    szczegóły B: %s\n", short(fa.Details), short(fb.Details))
			}
		default:
			same++
		}
	}
	fmt.Printf("\nWyniki identyczne: %d, inny status: %d, inna treść: %d\n", same, diffStatus, diffText)

	fmt.Println("\n=== Tabele (liczba wierszy)")
	ta, tb := map[string]table{}, map[string]table{}
	var tk []string
	for _, t := range a.Tables {
		k := t.Category + " / " + t.Name
		ta[k] = t
		tk = append(tk, k)
	}
	for _, t := range b.Tables {
		k := t.Category + " / " + t.Name
		tb[k] = t
		if _, ok := ta[k]; !ok {
			tk = append(tk, k)
		}
	}
	for _, k := range tk {
		x, okA := ta[k]
		y, okB := tb[k]
		switch {
		case !okB:
			fmt.Printf("  [tylko %s] %s (%d)\n", na, k, len(x.Rows))
		case !okA:
			fmt.Printf("  [tylko %s] %s (%d)\n", nb, k, len(y.Rows))
		case len(x.Rows) != len(y.Rows) || strings.Join(x.Columns, "|") != strings.Join(y.Columns, "|"):
			fmt.Printf("  %s: A %d wierszy, B %d wierszy; kolumny A [%s] B [%s]\n", k, len(x.Rows), len(y.Rows), strings.Join(x.Columns, ", "), strings.Join(y.Columns, ", "))
		}
	}
	fmt.Println("\n=== Metadane")
	var mk []string
	for k := range a.Meta {
		mk = append(mk, k)
	}
	for k := range b.Meta {
		if _, ok := a.Meta[k]; !ok {
			mk = append(mk, k)
		}
	}
	sort.Strings(mk)
	for _, k := range mk {
		va, vb := fmt.Sprint(a.Meta[k]), fmt.Sprint(b.Meta[k])
		if va != vb {
			fmt.Printf("  %s: A %q, B %q\n", k, short(va), short(vb))
		}
	}
}
