package diag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"serwisdiag/internal/win"
)

// Nowa wersja programu z wydań na GitHubie (lib\AppInfo.ps1, lib\Update.ps1): sprawdzenie (w wersji zainstalowanej
// raz na dobę), pobranie instalatora, kontrola sumy SHA-256 i numeru wersji w pliku, uruchomienie instalatora
// w trybie aktualizacji. Wersji przenośnej program sam nie aktualizuje - pokazuje stronę wydania.
//
// Wersja Go ma własne pliki w wydaniu: SerwisDiag-Go-<wersja>-instalator.exe (+ .sha256). Wersja PowerShell bierze
// z wydania plik kończący się na „-setup.exe”, więc instalatorów obu wersji nie da się pomylić, nawet gdyby
// trafiły do jednego wydania.

// ProjectURL - strona projektu wersji Go na GitHubie (osobne repozytorium: wersja PowerShell czyta najnowsze wydanie
// swojego repozytorium, więc wspólne wydania musiałyby zawsze mieć pliki obu wersji). Pusty adres: sprawdzanie nowej
// wersji wyłączone.
var ProjectURL = "https://github.com/WoofDeveloper/SerwisDiag-Go"

const updateCheckHours = 24

var (
	githubRepoRx   = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)
	versionRx      = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)
	setupAssetRx   = rx(`^SerwisDiag-Go-.*-instalator\.exe$`)
	setupShaRx     = rx(`^SerwisDiag-Go-.*-instalator\.exe\.sha256$`)
	zipAssetRx     = rx(`^SerwisDiag-Go-.*\.zip$`)
	zipShaRx       = rx(`^SerwisDiag-Go-.*\.zip\.sha256$`)
	sha256LineRx   = regexp.MustCompile(`^([0-9a-fA-F]{64})\b`)
	unsafeFileRx   = regexp.MustCompile(`[\\/:*?"<>|]`)
	installModeRx1 = rx(`(?m)^\s*Tryb\s*=\s*wszyscy\s*$`)
	installModeRx2 = rx(`(?m)^\s*Tryb\s*=\s*biezacy\s*$`)
)

// GitHubRepo odpowiada Get-SdGitHubRepo: 'https://github.com/konto/repo' -> 'konto/repo'; inny adres -> ”.
func GitHubRepo(u string) string {
	if m := githubRepoRx.FindStringSubmatch(u); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

// AppVersion - numer wersji (major.minor.build).
type AppVersion [3]int

func (v AppVersion) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

// Compare: -1, 0 albo 1.
func (v AppVersion) Compare(o AppVersion) int {
	for i := range v {
		if r := cmpNum(v[i], o[i]); r != 0 {
			return r
		}
	}
	return 0
}

// ParseVersion odpowiada ConvertTo-SdVersion: 'v1.2.3', '1.2', 'SerwisDiag-1.2.0' -> wersja; ok=false, gdy w tekście
// nie ma numeru wersji.
func ParseVersion(text string) (AppVersion, bool) {
	m := versionRx.FindStringSubmatch(text)
	if m == nil {
		return AppVersion{}, false
	}
	var v AppVersion
	for i := 0; i < 3; i++ {
		if m[i+1] != "" {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return v, true
}

// Release - najnowsze wydanie (Get-SdLatestRelease): Latest - numer, Newer - nowsze od uruchomionej wersji,
// URL - strona wydania, Notes - opis; adresy plików wydania wersji Go.
type Release struct {
	Ok                         bool
	Latest, URL, Error, Notes  string
	Newer                      bool
	Setup, SetupName, SetupSha string
	Zip, ZipSha                string
}

var (
	userAgent = "SerwisDiag-Go/" + Version
	githubAPI = "https://api.github.com" // testy: lokalny serwer
)

// LatestRelease odpowiada Get-SdLatestRelease. jsonText - odpowiedź podana z zewnątrz (testy).
func LatestRelease(projectURL string, timeout time.Duration, jsonText string) Release {
	var r Release
	repo := GitHubRepo(projectURL)
	if repo == "" {
		r.Error = "Nie ustawiono adresu strony projektu na GitHubie."
		return r
	}
	if jsonText == "" {
		req, err := http.NewRequest("GET", githubAPI+"/repos/"+repo+"/releases/latest", nil)
		if err != nil {
			r.Error = "Nie udało się połączyć z GitHubem: " + err.Error()
			return r
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := (&http.Client{Timeout: timeout}).Do(req)
		if err != nil {
			r.Error = "Nie udało się połączyć z GitHubem: " + innermost(err).Error()
			return r
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == 404:
			r.Error = "Projekt nie ma jeszcze opublikowanych wydań."
			return r
		case resp.StatusCode != 200:
			r.Error = fmt.Sprintf("Nie udało się połączyć z GitHubem: serwer zwrócił błąd HTTP %d.", resp.StatusCode)
			return r
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			r.Error = "Nie udało się połączyć z GitHubem: " + err.Error()
			return r
		}
		jsonText = string(b)
	}
	var o struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(jsonText), &o); err != nil || o.TagName == "" {
		r.Error = "Niezrozumiała odpowiedź serwera."
		return r
	}
	latest, ok := ParseVersion(o.TagName)
	if !ok {
		r.Error = "Nie rozpoznano numeru wersji: " + o.TagName
		return r
	}
	cur, curOk := ParseVersion(Version)
	r.Ok, r.Latest, r.Newer = true, latest.String(), curOk && latest.Compare(cur) > 0
	r.URL = o.HTMLURL
	if r.URL == "" {
		r.URL = strings.TrimRight(projectURL, "/") + "/releases"
	}
	if o.Body != "" {
		r.Notes = limitText(o.Body, 1500)
	}
	for _, a := range o.Assets {
		switch {
		case setupAssetRx.MatchString(a.Name):
			r.SetupName, r.Setup = a.Name, a.URL
		case setupShaRx.MatchString(a.Name):
			r.SetupSha = a.URL
		case zipAssetRx.MatchString(a.Name):
			r.Zip = a.URL
		case zipShaRx.MatchString(a.Name):
			r.ZipSha = a.URL
		}
	}
	return r
}

func innermost(err error) error {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err
		}
		err = u
	}
}

// LatestReleaseText odpowiada Get-SdLatestReleaseText: komunikat dla użytkownika.
func LatestReleaseText(r Release) string {
	switch {
	case !r.Ok:
		return r.Error
	case r.Newer:
		return fmt.Sprintf("Dostępna jest nowsza wersja: %s (masz %s). Strona wydania: %s", r.Latest, Version, r.URL)
	}
	return fmt.Sprintf("Masz najnowszą wersję (%s).", Version)
}

// InstallMode odpowiada Get-SdInstallMode: "allusers" | "currentuser" dla kopii z instalatora, "" dla przenośnej.
// Instalator zapisuje tryb w instalacja.ini; bez wpisu decyduje folder (Program Files = dla wszystkich).
func InstallMode(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "instalacja.ini"))
	if err != nil {
		return ""
	}
	txt := string(decodeText(b))
	switch {
	case installModeRx1.MatchString(txt):
		return "allusers"
	case installModeRx2.MatchString(txt):
		return "currentuser"
	}
	full := strings.TrimRight(filepath.Clean(root), `\`) + `\`
	for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramW6432")} {
		if pf != "" && strings.HasPrefix(strings.ToLower(full), strings.ToLower(strings.TrimRight(filepath.Clean(pf), `\`)+`\`)) {
			return "allusers"
		}
	}
	return "currentuser"
}

// UserDataDir odpowiada Get-SdUserDataDir: %LOCALAPPDATA%\SerwisDiag (stan monitora, aktualizacji, rejestracji).
func UserDataDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	d := filepath.Join(base, "SerwisDiag")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// updateState - stan sprawdzania (aktualizacje-go.json - osobny plik niż wersji PowerShell, która sprawdza swoje wydania).
type updateState struct {
	LastCheck string `json:"lastCheck"`
	Latest    string `json:"latest"`
	Newer     bool   `json:"newer"`
	Notified  string `json:"notified"`
}

func updateStatePath() string { return filepath.Join(UserDataDir(), "aktualizacje-go.json") }

// UpdateCheckDue odpowiada Test-SdUpdateCheckDue: wersja zainstalowana, sprawdzanie włączone w ustawieniach, znany
// adres projektu i co najmniej doba od poprzedniej próby (także nieudanej).
func UpdateCheckDue(root string, s Settings, statePath string, now time.Time) bool {
	if !IsInstalledCopy(root) || !s.AutoUpdateCheck || GitHubRepo(ProjectURL) == "" {
		return false
	}
	if statePath == "" {
		statePath = updateStatePath()
	}
	var st updateState
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	last := parseJSONDate(st.LastCheck)
	// zegar cofnięty po poprzednim sprawdzeniu (data z przyszłości) - sprawdzamy od razu
	return last.IsZero() || last.After(now) || now.Sub(last).Hours() >= updateCheckHours
}

// SaveUpdateCheckState odpowiada Save-SdUpdateCheckState.
func SaveUpdateCheckState(r Release, statePath string) error {
	return saveUpdateState(r, statePath, nil)
}

// SaveUpdateNotified: jak SaveUpdateCheckState, a do tego monitor powiadomił już o tej wersji (-Notified).
func SaveUpdateNotified(r Release, statePath string) error {
	return saveUpdateState(r, statePath, &r.Latest)
}

// UpdateNotified: wersja, o której monitor ostatnio powiadomił (powiadomienie raz na wersję).
func UpdateNotified(statePath string) string {
	if statePath == "" {
		statePath = updateStatePath()
	}
	var st updateState
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st.Notified
}

func saveUpdateState(r Release, statePath string, notified *string) error {
	if statePath == "" {
		statePath = updateStatePath()
	}
	n := UpdateNotified(statePath)
	if notified != nil {
		n = *notified
	}
	b, err := json.MarshalIndent(updateState{LastCheck: time.Now().Format("2006-01-02T15:04:05"), Latest: r.Latest, Newer: r.Newer, Notified: n}, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, b, 0o644)
}

// downloadFile odpowiada Save-SdWebFile: https:// (wydania na GitHubie) albo file:// (testy).
func downloadFile(u, path string) error {
	if strings.HasPrefix(strings.ToLower(u), "file://") {
		pu, err := url.Parse(u)
		if err != nil {
			return err
		}
		src := filepath.FromSlash(strings.TrimPrefix(pu.Path, "/"))
		if pu.Host != "" {
			src = `\\` + pu.Host + filepath.FromSlash(pu.Path)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(path, b, 0o644)
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return innermost(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("serwer zwrócił błąd HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fileSha256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DownloadUpdate odpowiada Get-SdUpdatePackage: pobiera instalator nowej wersji i sprawdza go - suma SHA-256 z pliku
// .sha256 wydania i numer wersji zapisany w pliku. Niesprawdzony plik nie zostaje uruchomiony.
func DownloadUpdate(r Release, dir string) (string, error) {
	if r.Setup == "" || r.SetupSha == "" {
		return "", errText("Wydanie nie zawiera instalatora albo jego sumy kontrolnej - pobierz nową wersję ze strony projektu.")
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "SerwisDiag-Go-aktualizacja-"+r.Latest)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "SerwisDiag-Go-instalator.exe"
	if r.SetupName != "" {
		name = unsafeFileRx.ReplaceAllString(r.SetupName, "_")
	}
	setup := filepath.Join(dir, name)
	shaFile := setup + ".sha256"
	for _, d := range [][2]string{{r.Setup, setup}, {r.SetupSha, shaFile}} {
		if err := downloadFile(d[0], d[1]); err != nil {
			return "", errText("Nie udało się pobrać aktualizacji: " + err.Error())
		}
	}
	b, _ := os.ReadFile(shaFile)
	m := sha256LineRx.FindStringSubmatch(strings.TrimSpace(string(decodeText(b))))
	if m == nil {
		return "", errText("Plik sumy kontrolnej wydania jest nieprawidłowy - aktualizację przerwano.")
	}
	if fileSha256(setup) != strings.ToLower(m[1]) {
		_ = os.Remove(setup)
		return "", errText("Suma kontrolna pobranego instalatora się nie zgadza (plik uszkodzony albo podmieniony) - aktualizację przerwano.")
	}
	fv, fok := ParseVersion(win.FileVersion(setup))
	rv, rok := ParseVersion(r.Latest)
	if !fok || !rok || fv != rv {
		got := ""
		if fok {
			got = fv.String()
		}
		return "", errText(fmt.Sprintf("Pobrany instalator ma inny numer wersji (%s) niż wydanie (%s) - aktualizację przerwano.", got, r.Latest))
	}
	return setup, nil
}

// UpdateArguments odpowiada Get-SdUpdateArguments: okno postępu bez pytań, ten sam tryb instalacji co dotąd,
// na życzenie ponowne uruchomienie programu (/RELAUNCH=1 odczytuje skrypt instalatora).
func UpdateArguments(mode string, relaunch bool) string {
	a := "/SILENT /SUPPRESSMSGBOXES /NORESTART /NOCANCEL"
	switch mode {
	case "allusers":
		a += " /ALLUSERS"
	case "currentuser":
		a += " /CURRENTUSER"
	}
	if relaunch {
		a += " /RELAUNCH=1"
	}
	return a
}

// StartUpdateInstall odpowiada Start-SdUpdateInstall: uruchamia sprawdzony instalator (bez czekania - program
// powinien się zaraz zamknąć).
func StartUpdateInstall(setup, root string, relaunch bool) error {
	mode := InstallMode(root)
	if mode == "" {
		return errText("Ta kopia programu nie pochodzi z instalatora - pobierz nową wersję ze strony projektu.")
	}
	file, _ := windows.UTF16PtrFromString(setup)
	args, _ := windows.UTF16PtrFromString(UpdateArguments(mode, relaunch))
	verb, _ := windows.UTF16PtrFromString("open")
	return windows.ShellExecute(0, verb, file, args, nil, windows.SW_SHOWNORMAL)
}

// UpdateOutcome - wynik RunUpdate (Invoke-SdUpdate).
type UpdateOutcome struct {
	Started bool
	Release Release
	Message string
}

// RunUpdate odpowiada Invoke-SdUpdate: sprawdzenie, pobranie, kontrola i uruchomienie instalatora nowej wersji.
func RunUpdate(root string, relaunch bool, dir string) (UpdateOutcome, error) {
	rel := LatestRelease(ProjectURL, 8*time.Second, "")
	out := UpdateOutcome{Release: rel}
	switch {
	case !rel.Ok:
		out.Message = rel.Error
		return out, nil
	case !rel.Newer:
		out.Message = LatestReleaseText(rel)
		return out, nil
	case InstallMode(root) == "":
		out.Message = "Wersja przenośna nie aktualizuje się sama. " + LatestReleaseText(rel)
		return out, nil
	}
	setup, err := DownloadUpdate(rel, dir)
	if err != nil {
		return out, err
	}
	if err := StartUpdateInstall(setup, root, relaunch); err != nil {
		return out, err
	}
	out.Started, out.Message = true, fmt.Sprintf("Instalowanie wersji %s...", rel.Latest)
	return out, nil
}
