package ui

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"serwisdiag/internal/diag"
)

func TestMonitorChanges(t *testing.T) {
	last := time.Date(2026, 10, 27, 3, 0, 0, 0, time.Local)
	prev := []monProblem{
		{Key: "st.disks|dysk 0", Title: "Dysk 0", Status: "warn"},
		{Key: "se.av|antywirus", Title: "Antywirus", Status: "warn"},
		{Key: "ev.crash|restarty", Title: "Restarty", Status: "warn"},
	}
	fs := []diag.Finding{
		{CheckID: "st.disks", Title: "Dysk 0", Status: diag.Crit},                                            // pogorszenie
		{CheckID: "ev.crash", Title: "Restarty", Status: diag.Warn, LastEvent: last.Add(time.Hour)},          // nowe zdarzenie
		{CheckID: "dr.problem", Title: "Urządzenia", Status: diag.Warn},                                      // nowy problem
		{CheckID: "se.firewall", Title: "Zapora", Status: diag.OK},                                           // bez problemu
		{CheckID: "ev.hardware", Title: "Błędy sprzętu", Status: diag.Warn, LastEvent: last.Add(-time.Hour)}, // nowy (brak w stanie)
	}
	newOnes, problems, resolved := monitorChanges(prev, fs, last)
	if len(problems) != 4 {
		t.Fatalf("problemy: %d, oczekiwano 4", len(problems))
	}
	if len(newOnes) != 4 {
		t.Fatalf("nowe: %d (%v), oczekiwano 4", len(newOnes), newOnes)
	}
	if len(resolved) != 1 || resolved[0] != "Antywirus" {
		t.Fatalf("ustąpione: %v", resolved)
	}
	// bez zmian: ten sam stan i stare zdarzenie - brak nowych
	n2, _, _ := monitorChanges([]monProblem{{Key: "ev.crash|restarty", Status: "warn"}},
		[]diag.Finding{{CheckID: "ev.crash", Title: "Restarty", Status: diag.Warn, LastEvent: last.Add(-time.Hour)}}, last)
	if len(n2) != 0 {
		t.Fatalf("nowe przy braku zmian: %v", n2)
	}
}

func TestMonitorLevelAndInterval(t *testing.T) {
	if monitorLevel(nil) != "ok" || monitorLevel([]monProblem{{Status: "warn"}}) != "warn" || monitorLevel([]monProblem{{Status: "warn"}, {Status: "crit"}}) != "crit" {
		t.Fatal("poziom ikony")
	}
	for m, want := range map[int]string{60: "co godzinę", 240: "co 4 godziny", 1440: "raz na dobę", 300: "co 5 godzin", 30: "co 30 minut"} {
		if got := intervalText(m); got != want {
			t.Errorf("intervalText(%d) = %q, oczekiwano %q", m, got, want)
		}
	}
}

func TestMonitorStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "monitor.json")
	if readMonitorState(p) != nil {
		t.Fatal("stan przed pierwszym zapisem")
	}
	when := time.Date(2026, 10, 27, 10, 30, 0, 0, time.Local)
	ev := "2026-10-26T08:00:00"
	if err := saveMonitorState(p, []monProblem{{Key: "a|b", Title: "B", Status: "crit", LastEvent: &ev}}, when, 240); err != nil {
		t.Fatal(err)
	}
	st := readMonitorState(p)
	if st == nil || !st.LastCheck.Equal(when) || st.Interval != 240 || len(st.Problems) != 1 || *st.Problems[0].LastEvent != ev {
		t.Fatalf("odczyt: %+v", st)
	}
	// zmiana częstotliwości zachowuje datę i problemy
	if err := setMonitorInterval(p, 1440); err != nil {
		t.Fatal(err)
	}
	st = readMonitorState(p)
	if st.Interval != 1440 || !st.LastCheck.Equal(when) || len(st.Problems) != 1 {
		t.Fatalf("po zmianie częstotliwości: %+v", st)
	}
}

func TestAppIcon(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32} {
		for _, state := range []string{"ok", "warn", "crit"} {
			im := drawAppIcon(size, state)
			px := func(x, y int) rgbaF { return im.pix[y*size+x] }
			if a := px(0, 0).a; a > 0.5 { // zaokrąglony róg: przy 16 px promień 2 px pokrywa 38% piksela
				t.Errorf("%d %s: róg nie jest zaokrąglony (%.2f)", size, state, a)
			}
			if a := px(size/4, size/4).a; a < 0.99 {
				t.Errorf("%d %s: środek plakietki nie jest kryjący (%.2f)", size, state, a)
			}
			c := px(size-size/5, size-size/5)
			switch state {
			case "crit":
				if c.r < 0.7 || c.g > 0.4 {
					t.Errorf("%d crit: plakietka nie jest czerwona: %+v", size, c)
				}
			case "warn":
				if c.r < 0.8 || c.g < 0.4 || c.b > 0.3 {
					t.Errorf("%d warn: plakietka nie jest pomarańczowa: %+v", size, c)
				}
			}
		}
	}
}

// TestWriteIcons: SERWISDIAG_ICON_DIR=<folder> zapisuje ikony do obejrzenia (16, 24, 32 i 64 px).
func TestWriteIcons(t *testing.T) {
	dir := os.Getenv("SERWISDIAG_ICON_DIR")
	if dir == "" {
		t.Skip("SERWISDIAG_ICON_DIR nie ustawiono")
	}
	for _, size := range []int{16, 24, 32, 64} {
		for _, state := range []string{"ok", "warn", "crit"} {
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("ikona-%d-%s.png", size, state)))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, drawAppIcon(size, state).nrgba()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}

func TestKeyName(t *testing.T) {
	cases := []struct {
		vk   int
		lp   uintptr
		want string
	}{
		{'A', 0, "A"}, {'7', 0, "D7"}, {0x67, 0, "NumPad7"}, {0x70, 0, "F1"}, {0x7B, 0, "F12"},
		{vkShift, 0x2A << 16, "LeftShift"}, {vkShift, 0x36 << 16, "RightShift"},
		{vkControl, 0, "LeftCtrl"}, {vkControl, 1 << 24, "RightCtrl"}, {vkMenu, 1 << 24, "RightAlt"},
		{0xC0, 0, "OemTilde"}, {0xDC, 0, "OemPipe"}, {0x2C, 0, "Snapshot"},
	}
	for _, c := range cases {
		if got := keyName(c.vk, c.lp); got != c.want {
			t.Errorf("keyName(%#x) = %q, oczekiwano %q", c.vk, got, c.want)
		}
	}
}

func TestLayoutText(t *testing.T) {
	f := getFont(96, 130, fwNormal)
	one := layoutText([]span{{text: "Krótki tekst", font: f}}, 1000, 0)
	if len(one.frags) != 1 || one.h != f.ascent+f.descent {
		t.Fatalf("jeden wiersz: %+v", one)
	}
	long := "Program sprawdza komputer z systemem Windows 10 lub 11: sprzęt, dyski, baterię, awarie i błędy systemu."
	tb := layoutText([]span{{text: long, font: f}}, 200, 0)
	if tb.h <= one.h || tb.w > 200 {
		t.Fatalf("zawijanie: wysokość %d, szerokość %d", tb.h, tb.w)
	}
	// twarda spacja (separator tysięcy) nie dzieli liczby
	nb := layoutText([]span{{text: "zapisano 14\u00a0700 GB", font: f}}, strWidth(f, "zapisano 14"), 0)
	for _, fr := range nb.frags {
		if s := string(utf16ToRunes(fr.text)); s == "14" {
			t.Fatal("liczba z twardą spacją podzielona")
		}
	}
	// słowo dłuższe niż wiersz dzielone na znaki, bez wychodzenia poza szerokość
	path := layoutText([]span{{text: `C:\Users\Jan\AppData\Local\Temp\SerwisDiag\bardzo\dluga\sciezka\raport.html`, font: f}}, 120, 0)
	if path.w > 120 || len(path.frags) < 2 {
		t.Fatalf("długie słowo: %+v", path.w)
	}
	// dwa kroje w jednym akapicie i nowy wiersz
	mixed := layoutText([]span{{text: "Zalecenie: ", font: getFont(96, 130, fwSemiBold)}, {text: "sprawdź dysk", font: f}, {text: "drugi wiersz", font: f, br: true}}, 1000, 0)
	if len(mixed.frags) != 3 || mixed.frags[2].y <= mixed.frags[0].y {
		t.Fatalf("akapit mieszany: %+v", mixed.frags)
	}
}

func utf16ToRunes(u []uint16) []rune {
	out := make([]rune, 0, len(u))
	for _, c := range u {
		out = append(out, rune(c))
	}
	return out
}
