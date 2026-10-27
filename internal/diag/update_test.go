package diag

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/win"
)

func TestVersionAndRepo(t *testing.T) {
	for in, want := range map[string]string{"https://github.com/WoofDeveloper/SerwisDiag": "WoofDeveloper/SerwisDiag", "https://github.com/a/b.git": "a/b",
		"https://github.com/a/b/": "a/b", "https://gitlab.com/a/b": "", "": ""} {
		if got := GitHubRepo(in); got != want {
			t.Errorf("GitHubRepo(%q) = %q", in, got)
		}
	}
	v, ok := ParseVersion("v1.2.3")
	w, _ := ParseVersion("SerwisDiag-1.10")
	if !ok || v.String() != "1.2.3" || w.String() != "1.10.0" || v.Compare(w) >= 0 || w.Compare(v) <= 0 || v.Compare(v) != 0 {
		t.Errorf("wersje: %v %v", v, w)
	}
	if _, ok := ParseVersion("brak"); ok {
		t.Error("napis bez numeru")
	}
}

const testReleaseJSON = `{"tag_name":"v9.1.0","html_url":"https://github.com/a/b/releases/tag/v9.1.0","body":"Nowe funkcje",
"assets":[{"name":"SerwisDiag-9.1.0-setup.exe","browser_download_url":"https://x/ps-setup.exe"},
{"name":"SerwisDiag-9.1.0-setup.exe.sha256","browser_download_url":"https://x/ps-setup.exe.sha256"},
{"name":"SerwisDiag-Go-9.1.0-instalator.exe","browser_download_url":"https://x/go.exe"},
{"name":"SerwisDiag-Go-9.1.0-instalator.exe.sha256","browser_download_url":"https://x/go.exe.sha256"},
{"name":"SerwisDiag-9.1.0.zip","browser_download_url":"https://x/ps.zip"},
{"name":"SerwisDiag-Go-9.1.0.zip","browser_download_url":"https://x/go.zip"}]}`

func TestLatestRelease(t *testing.T) {
	r := LatestRelease("https://github.com/a/b", time.Second, testReleaseJSON)
	if !r.Ok || r.Latest != "9.1.0" || !r.Newer || r.Setup != "https://x/go.exe" || r.SetupSha != "https://x/go.exe.sha256" || r.SetupName != "SerwisDiag-Go-9.1.0-instalator.exe" ||
		r.Zip != "https://x/go.zip" || r.Notes != "Nowe funkcje" || r.URL != "https://github.com/a/b/releases/tag/v9.1.0" {
		t.Errorf("wydanie: %+v", r)
	}
	if s := LatestReleaseText(r); !strings.HasPrefix(s, "Dostępna jest nowsza wersja: 9.1.0 (masz "+Version+")") {
		t.Errorf("tekst: %s", s)
	}
	old := LatestRelease("https://github.com/a/b", time.Second, `{"tag_name":"v1.0.0"}`)
	if !old.Ok || old.Newer || old.Setup != "" || old.URL != "https://github.com/a/b/releases" || LatestReleaseText(old) != "Masz najnowszą wersję ("+Version+")." {
		t.Errorf("starsze wydanie: %+v", old)
	}
	for _, c := range [][2]string{{`{}`, "Niezrozumiała odpowiedź serwera."}, {`{"tag_name":"latest"}`, "Nie rozpoznano numeru wersji: latest"}} {
		if r := LatestRelease("https://github.com/a/b", time.Second, c[0]); r.Ok || r.Error != c[1] {
			t.Errorf("%s: %+v", c[0], r)
		}
	}
	if r := LatestRelease("", time.Second, ""); r.Error != "Nie ustawiono adresu strony projektu na GitHubie." {
		t.Errorf("bez adresu: %+v", r)
	}
	// odpowiedzi serwera (lokalny serwer zamiast api.github.com)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/a/brak/releases/latest":
			http.NotFound(w, r)
		case "/repos/a/blad/releases/latest":
			w.WriteHeader(403)
		default:
			if r.Header.Get("User-Agent") != userAgent {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, testReleaseJSON)
		}
	}))
	defer srv.Close()
	saved := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = saved }()
	if r := LatestRelease("https://github.com/a/b", 5*time.Second, ""); !r.Ok || r.Latest != "9.1.0" {
		t.Errorf("serwer: %+v", r)
	}
	if r := LatestRelease("https://github.com/a/brak", 5*time.Second, ""); r.Error != "Projekt nie ma jeszcze opublikowanych wydań." {
		t.Errorf("404: %+v", r)
	}
	if r := LatestRelease("https://github.com/a/blad", 5*time.Second, ""); r.Error != "Nie udało się połączyć z GitHubem: serwer zwrócił błąd HTTP 403." {
		t.Errorf("403: %+v", r)
	}
}

func TestInstallModeAndUpdateDue(t *testing.T) {
	dir := t.TempDir()
	if InstallMode(dir) != "" || IsInstalledCopy(dir) {
		t.Error("kopia przenośna")
	}
	ini := filepath.Join(dir, "instalacja.ini")
	for txt, want := range map[string]string{"[SerwisDiag]\r\nWersja=1.5.0\r\nTryb=wszyscy\r\n": "allusers", "[SerwisDiag]\r\nTryb = biezacy\r\n": "currentuser", "[SerwisDiag]\r\n": "currentuser"} {
		_ = os.WriteFile(ini, []byte(txt), 0o644)
		if got := InstallMode(dir); got != want {
			t.Errorf("tryb dla %q: %s", txt, got)
		}
	}
	if a := UpdateArguments("allusers", true); a != "/SILENT /SUPPRESSMSGBOXES /NORESTART /NOCANCEL /ALLUSERS /RELAUNCH=1" {
		t.Errorf("parametry: %s", a)
	}
	saved := ProjectURL
	defer func() { ProjectURL = saved }()
	st := filepath.Join(dir, "aktualizacje-go.json")
	now := time.Now()
	ProjectURL = ""
	if UpdateCheckDue(dir, Settings{AutoUpdateCheck: true}, st, now) {
		t.Error("bez adresu projektu nie sprawdzamy")
	}
	ProjectURL = "https://github.com/a/b"
	if !UpdateCheckDue(dir, Settings{AutoUpdateCheck: true}, st, now) || UpdateCheckDue(dir, Settings{}, st, now) {
		t.Error("pierwsze sprawdzenie / wyłączone w ustawieniach")
	}
	if err := SaveUpdateCheckState(Release{Latest: "9.1.0", Newer: true}, st); err != nil {
		t.Fatal(err)
	}
	// powiadomienie monitora raz na wersję: zapis sprawdzenia nie kasuje informacji o powiadomieniu
	_ = SaveUpdateNotified(Release{Latest: "9.1.0", Newer: true}, st)
	_ = SaveUpdateCheckState(Release{Latest: "9.1.0", Newer: true}, st)
	if UpdateNotified(st) != "9.1.0" || UpdateNotified(filepath.Join(dir, "brak.json")) != "" {
		t.Error("wersja, o której powiadomiono")
	}
	if UpdateCheckDue(dir, Settings{AutoUpdateCheck: true}, st, now.Add(time.Hour)) || !UpdateCheckDue(dir, Settings{AutoUpdateCheck: true}, st, now.Add(25*time.Hour)) ||
		!UpdateCheckDue(dir, Settings{AutoUpdateCheck: true}, st, now.Add(-time.Hour)) {
		t.Error("raz na dobę (zegar cofnięty - od razu)")
	}
}

// TestDownloadUpdate: pobranie (file://), suma SHA-256 i numer wersji pliku - na pliku systemowym z zasobem wersji.
func TestDownloadUpdate(t *testing.T) {
	src := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	fv, ok := ParseVersion(win.FileVersion(src))
	if !ok {
		t.Skip("notepad.exe bez numeru wersji")
	}
	dir := t.TempDir()
	b, _ := os.ReadFile(src)
	sum := sha256.Sum256(b)
	setup := filepath.Join(dir, "zrodlo.exe")
	_ = os.WriteFile(setup, b, 0o644)
	good := filepath.Join(dir, "dobra.sha256")
	_ = os.WriteFile(good, []byte(hex.EncodeToString(sum[:])+"  SerwisDiag-Go-x-instalator.exe\n"), 0o644)
	bad := filepath.Join(dir, "zla.sha256")
	_ = os.WriteFile(bad, []byte(strings.Repeat("0", 64)), 0o644)
	u := func(p string) string { return "file:///" + filepath.ToSlash(p) }
	rel := Release{Latest: fv.String(), Setup: u(setup), SetupSha: u(good), SetupName: "SerwisDiag-Go-" + fv.String() + "-instalator.exe"}
	p, err := DownloadUpdate(rel, filepath.Join(dir, "pobrane"))
	if err != nil || filepath.Base(p) != rel.SetupName || fileSha256(p) != hex.EncodeToString(sum[:]) {
		t.Fatalf("pobranie: %s %v", p, err)
	}
	rel.SetupSha = u(bad)
	if _, err := DownloadUpdate(rel, filepath.Join(dir, "pobrane2")); err == nil || !strings.Contains(err.Error(), "Suma kontrolna pobranego instalatora się nie zgadza") {
		t.Errorf("zła suma: %v", err)
	}
	rel.SetupSha, rel.Latest = u(good), "1.0.0"
	if _, err := DownloadUpdate(rel, filepath.Join(dir, "pobrane3")); err == nil || !strings.Contains(err.Error(), "inny numer wersji ("+fv.String()+") niż wydanie (1.0.0)") {
		t.Errorf("inna wersja: %v", err)
	}
	if _, err := DownloadUpdate(Release{Latest: "1.0.0"}, dir); err == nil || !strings.HasPrefix(err.Error(), "Wydanie nie zawiera instalatora") {
		t.Errorf("bez instalatora: %v", err)
	}
	rel.Setup = u(filepath.Join(dir, "brak.exe"))
	if _, err := DownloadUpdate(rel, filepath.Join(dir, "pobrane4")); err == nil || !strings.HasPrefix(err.Error(), "Nie udało się pobrać aktualizacji: ") {
		t.Errorf("brak pliku: %v", err)
	}
}

func TestRegistration(t *testing.T) {
	saved := Registration
	defer func() { Registration = saved }()
	Registration.FormURL = ""
	if RegistrationEnabled() {
		t.Error("bez adresu formularza wyłączona")
	}
	Registration.FormURL = "https://docs.google.com/forms/d/e/TEST/formResponse"
	Registration.Fields = map[string]string{"Name": "entry.1", "Email": "entry.2", "Phone": "entry.3", "Program": "entry.4"}
	if !RegistrationEnabled() {
		t.Error("z adresem i polami włączona")
	}
	if e := RegistrationInputErrors("Łukasz Żółć", "lukasz.zolc@example.pl", "+48 600-700-800", true); len(e) != 0 {
		t.Errorf("poprawne dane: %v", e)
	}
	if e := RegistrationInputErrors("Jan Kowalski", "jan@firma.com.pl", "", true); len(e) != 0 {
		t.Errorf("telefon nieobowiązkowy: %v", e)
	}
	if e := strings.Join(RegistrationInputErrors("J", "jan@example", "600 7OO 800", false), " | "); strings.Count(e, "|") != 3 ||
		!strings.Contains(e, "imię") || !strings.Contains(e, "e-mail") || !strings.Contains(e, "telefonu") || !strings.Contains(e, "zgodę") {
		t.Errorf("błędy: %s", e)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "program")
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "instalacja.ini"), []byte("[SerwisDiag]\r\nTryb=biezacy\r\n"), 0o644)
	rp := filepath.Join(dir, "rejestracja.json")
	if _, err := SaveRegistration("  Łukasz   Żółć ", " lukasz.zolc@example.pl ", "+48 600  700 800", root, rp); err != nil {
		t.Fatal(err)
	}
	rr := ReadRegistration(rp)
	if rr == nil || rr.Status != "pending" || rr.Name != "Łukasz Żółć" || rr.Email != "lukasz.zolc@example.pl" || rr.Phone != "+48 600 700 800" || rr.ConsentAt == "" ||
		rr.Version != Version+" (Go)" || rr.Mode != "instalacja dla mnie" || rr.Clause != Registration.Clause {
		t.Fatalf("zapis: %+v", rr)
	}
	body := registrationBody(*rr)
	if !strings.HasPrefix(body, "entry.1=%C5%81ukasz%20%C5%BB%C3%B3%C5%82%C4%87&entry.2=lukasz.zolc%40example.pl&entry.3=%2B48%20600%20700%20800&entry.4=SerwisDiag%20") ||
		!strings.Contains(body, "zgoda%20"+time.Now().Format("2006-01-02")) {
		t.Errorf("treść: %s", body)
	}
	calls := 0
	okT := func(u, b string) (string, error) { calls++; return u, nil }
	// blokada wysyłania otwarta przez inny proces (np. wersję PowerShell) - wysyłanie na nią czeka, a nie kończy się
	// błędem (x/sys zwraca wtedy ERROR_ALREADY_EXISTS)
	other, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(`Local\SerwisDiag.Registration`))
	if other == 0 {
		t.Fatal(err)
	}
	defer windows.CloseHandle(other)
	ok1, msg1 := SendRegistration(rp, okT)
	ok2, _ := SendRegistration(rp, okT)
	if rr = ReadRegistration(rp); !ok1 || !ok2 || calls != 1 || rr.Status != "sent" || rr.SentAt == "" || RegistrationDue(rp, time.Now()) {
		t.Errorf("wysłana raz: %v %s %d %+v", ok1, msg1, calls, rr)
	}
	_, _ = SaveRegistration("Jan Kowalski", "jan@example.pl", "", root, rp)
	failT := func(u, b string) (string, error) {
		return "", &net.DNSError{Err: "brak sieci", Name: "docs.google.com"}
	}
	ok3, msg3 := SendRegistration(rp, failT)
	if rr = ReadRegistration(rp); ok3 || !strings.HasPrefix(msg3, "Brak połączenia") || rr.Status != "pending" || rr.Attempts != 1 ||
		RegistrationDue(rp, time.Now()) || !RegistrationDue(rp, time.Now().Add(31*time.Minute)) {
		t.Errorf("bez internetu: %v %s %+v", ok3, msg3, rr)
	}
	ok4, msg4 := SendRegistration(rp, func(u, b string) (string, error) { return "https://docs.google.com/forms/d/e/TEST/closedform", nil })
	if ok4 || !strings.Contains(msg4, "nie przyjmuje") || ReadRegistration(rp).Attempts != 2 {
		t.Errorf("zamknięty formularz: %s", msg4)
	}
	if _, msg := SendRegistration(rp, func(u, b string) (string, error) { return "", httpStatusError(404) }); !strings.Contains(msg, "HTTP 404") {
		t.Errorf("404: %s", msg)
	}
	// prawdziwe zapytanie HTTP do serwera na tym komputerze: metoda, nagłówek i treść bez zmian
	var got struct{ method, path, ctype, body, expect string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.ctype, got.body, got.expect = r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b), r.Header.Get("Expect")
		io.WriteString(w, "OK")
	}))
	defer srv.Close()
	if _, err := postRegistration(srv.URL+"/forms/formResponse", body); err != nil || got.method != "POST" || got.path != "/forms/formResponse" ||
		got.ctype != "application/x-www-form-urlencoded; charset=utf-8" || got.body != body || got.expect != "" {
		t.Errorf("POST: %v %+v", err, got)
	}
	if c := RegistrationClause(); !strings.Contains(c, Registration.Controller) || !strings.Contains(c, Registration.Contact) || !strings.Contains(c, "Urzędu Ochrony Danych Osobowych") {
		t.Error("informacja RODO")
	}
	Registration.FormURL = ""
	if ok, _ := SendRegistration(rp, okT); ok || RegistrationDue(rp, time.Now()) {
		t.Error("wyłączona - nic nie wysyła")
	}
}

// TestLatestReleaseLive: prawdziwe najnowsze wydanie na GitHubie - pliki instalatora i wersji przenośnej z sumami,
// pobranie instalatora ze sprawdzeniem sumy SHA-256 i numeru wersji, jak przy aktualizacji z programu (go test -short
// go pomija; potrzebny internet). Uruchamiaj po każdym wydaniu.
func TestLatestReleaseLive(t *testing.T) {
	if testing.Short() {
		t.Skip("test z internetem")
	}
	r := LatestRelease(ProjectURL, 15*time.Second, "")
	if !r.Ok {
		t.Fatalf("najnowsze wydanie: %s", r.Error)
	}
	if r.Setup == "" || r.SetupSha == "" || r.Zip == "" || r.ZipSha == "" || r.Notes == "" {
		t.Fatalf("niekompletne wydanie %s: %+v", r.Latest, r)
	}
	setup, err := DownloadUpdate(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wydanie %s (masz %s, nowsze: %v), instalator pobrany i sprawdzony: %s; komunikat: %s", r.Latest, Version, r.Newer, filepath.Base(setup), LatestReleaseText(r))
}
