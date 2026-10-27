package diag

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFmtNumLikeDotNetFramework(t *testing.T) {
	// wartości sprawdzone w Windows PowerShell 5.1 (ToString("N", pl-PL)) - zaokrąglanie połówki od zera po 15 cyfrach
	cases := []struct {
		v    float64
		d    int
		want string
	}{
		{12345.678, 0, "12 346"}, {12345.678, 1, "12 345,7"}, {12345.678, 2, "12 345,68"},
		{2.5, 0, "3"}, {3.5, 0, "4"}, {0.125, 2, "0,13"}, {1.005, 2, "1,01"}, {-0.4, 0, "0"}, {-0.4, 1, "-0,4"},
		{0, 1, "0,0"}, {1234567.891, 0, "1 234 568"}, {0.5, 0, "1"}, {1.5, 0, "2"}, {-2.5, 0, "-3"},
		{999.95, 0, "1 000"}, {999.95, 1, "1 000,0"}, {999.95, 2, "999,95"}, {4.35, 1, "4,4"}, {0.045, 1, "0,0"}, {0.045, 2, "0,05"},
		{1.45, 1, "1,5"}, {2.675, 2, "2,68"}, {99.5, 0, "100"}, {100.5, 0, "101"}, {0.05, 1, "0,1"},
		{1e16, 0, "10 000 000 000 000 000"}, {123456789.125, 1, "123 456 789,1"}, {123456789.125, 2, "123 456 789,13"},
	}
	for _, c := range cases {
		got := strings.ReplaceAll(fmtNum(c.v, c.d), nbsp, " ")
		if got != c.want {
			t.Errorf("fmtNum(%v, %d) = %q, oczekiwano %q", c.v, c.d, got, c.want)
		}
	}
}

func TestPsSortMatchesWindowsPowerShell(t *testing.T) {
	// kolejność z Windows PowerShell 5.1: 1..30 | % { @{N=$_; C=$_ % 3} } | Sort-Object C -Descending
	type it struct{ n, c int }
	var s []it
	for i := 1; i <= 30; i++ {
		s = append(s, it{i, i % 3})
	}
	psSort(s, func(a, b it) int { return b.c - a.c })
	var got []string
	for _, x := range s {
		got = append(got, strconv.Itoa(x.n))
	}
	want := "8,14,17,26,11,29,2,23,5,20,19,16,25,22,28,7,4,13,1,10,27,30,24,9,6,3,12,21,18,15"
	if strings.Join(got, ",") != want {
		t.Errorf("psSort: %s\noczekiwano: %s", strings.Join(got, ","), want)
	}
	names := []string{"x", "y", "z"} // Group-Object | Sort-Object Count -Descending przy remisie
	psSort(names, func(a, b string) int { return 0 })
	if strings.Join(names, " ") != "z y x" {
		t.Errorf("remis trzech: %v", names)
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int64]string{1: "zdarzenie", 2: "zdarzenia", 5: "zdarzeń", 12: "zdarzeń", 22: "zdarzenia", 112: "zdarzeń", 0: "zdarzeń"} {
		if got := plural(n, "zdarzenie", "zdarzenia", "zdarzeń"); got != want {
			t.Errorf("plural(%d) = %s", n, got)
		}
	}
}

func TestCpuInfo(t *testing.T) {
	ci := cpuInfoFor("12th Gen Intel(R) Core(TM) i5-12450HX")
	if ci.Gen != "12. generacja Core" || ci.Year != 2021 || ci.Win11 != Yes {
		t.Errorf("i5-12450HX: %+v", ci)
	}
	if ci := cpuInfoFor("Intel(R) Core(TM) i7-7700HQ CPU @ 2.80GHz"); ci.Win11 != No || ci.Note == "" {
		t.Errorf("i7-7700HQ: %+v", ci)
	}
	if ci := cpuInfoFor("AMD Ryzen 5 3600 6-Core Processor"); ci.Gen != "Ryzen serii 3000" || ci.Win11 != Yes {
		t.Errorf("Ryzen 3600: %+v", ci)
	}
	if ci := cpuInfoFor("Intel(R) Pentium(R) CPU G4560 @ 3.50GHz"); ci.Win11 != No {
		t.Errorf("G4560: %+v", ci)
	}
}

func TestResolveSupportStatus(t *testing.T) {
	today := time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local)
	r := resolveSupportStatus(time.Date(2028, 10, 10, 0, 0, 0, 0, time.Local), today, 26300, "Windows 11 26H2", "26H2", nil, time.Time{})
	if r.Status != OK || r.Value != "Windows 11 26H2: wsparcie do 2028-10-10" {
		t.Errorf("26H2: %+v", r)
	}
	esu := &EsuState{Applies: true, ConsumerCode: 3, ConsumerUntil: time.Date(2027, 10, 12, 0, 0, 0, 0, time.Local), UbrAfterEos: true, LastFreeUbr: 6456, Ubr: 6600}
	r = resolveSupportStatus(time.Date(2025, 10, 14, 0, 0, 0, 0, time.Local), today, 19045, "Windows 10 22H2", "26H2", esu, today.AddDate(0, 0, -10))
	if r.Status != Info || !strings.Contains(r.Value, "objęty ESU (program konsumencki) do 2027-10-12") {
		t.Errorf("ESU: %+v", r)
	}
	r = resolveSupportStatus(time.Date(2025, 10, 14, 0, 0, 0, 0, time.Local), today, 19045, "Windows 10 22H2", "26H2", &EsuState{Applies: true, ConsumerCode: -1}, time.Time{})
	if r.Status != Crit || r.Covered {
		t.Errorf("bez ESU: %+v", r)
	}
}

func TestAnonymizeText(t *testing.T) {
	in := `C:\Users\Jan Kowalski\AppData; konto KOMPUTER-JANA, IP 192.168.1.15 i 8.8.8.8, MAC 00-1A-2B-3C-4D-5E, e-mail jan@example.com, klucz ABCDE-12345-FGHIJ-67890-KLMNO, C:\Users\Public\x` + "\nSSID: MojaSiec"
	got := anonymizeText(in, []string{"KOMPUTER-JANA"})
	for _, bad := range []string{"Jan Kowalski", "KOMPUTER-JANA", "192.168.1.15", "00-1A-2B", "jan@example.com", "ABCDE-12345", "MojaSiec"} {
		if strings.Contains(got, bad) {
			t.Errorf("pozostało %q w: %s", bad, got)
		}
	}
	for _, keep := range []string{"8.8.8.8", `C:\Users\Public\x`, `C:\Users\<użytkownik>\AppData`} {
		if !strings.Contains(got, keep) {
			t.Errorf("brak %q w: %s", keep, got)
		}
	}
}

func TestHtmlEncodeLikeWebUtility(t *testing.T) {
	if got := htmlEncode(`<a href="x">ó ą & ' ` + "\u00a0"); got != `&lt;a href=&quot;x&quot;&gt;&#243; ą &amp; &#39; &#160;` {
		t.Errorf("htmlEncode: %s", got)
	}
	if got := toHTML("Zobacz https://example.com/a. Dalej\r\nnowa"); got != `Zobacz <a href="https://example.com/a" target="_blank" rel="noopener noreferrer">https://example.com/a</a>. Dalej<br>nowa` {
		t.Errorf("toHTML: %s", got)
	}
}
