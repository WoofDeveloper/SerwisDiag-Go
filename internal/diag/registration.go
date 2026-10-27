package diag

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// Dobrowolna rejestracja użytkowników programu (lib\Registration.ps1): imię i nazwisko, e-mail i (opcjonalnie) telefon
// trafiają za zgodą użytkownika do Formularza Google autora programu. Dane, których nie udało się wysłać (brak
// internetu), czekają w %LOCALAPPDATA%\SerwisDiag\rejestracja.json - tym samym pliku co w wersji PowerShell, z tą samą
// blokadą wysyłania (jedna rejestracja użytkownika Windows dla obu wersji). Pusty adres formularza = rejestracja wyłączona.

// RegistrationConfig - $SdRegistration.
type RegistrationConfig struct {
	FormURL                     string            // https://docs.google.com/forms/d/e/<id>/formResponse
	Fields                      map[string]string // Name, Email, Phone, Program -> entry.<numer> pól formularza
	Controller, Contact, Clause string            // administrator danych (RODO), kontakt, data treści informacji
}

// Registration - ustawienia rejestracji (jak w wersji PowerShell: adres formularza jeszcze nie ustawiony).
var Registration = RegistrationConfig{
	Fields:     map[string]string{"Name": "", "Email": "", "Phone": "", "Program": ""},
	Controller: "Kacper Suliński - INDUSIP.PL",
	Contact:    "woofdeveloper@proton.me",
	Clause:     "2026-10-26",
}

const registrationRetryMin = 30

// RegistrationEnabled odpowiada Test-SdRegistrationEnabled.
func RegistrationEnabled() bool {
	if Registration.FormURL == "" {
		return false
	}
	for _, k := range []string{"Name", "Email", "Phone", "Program"} {
		if Registration.Fields[k] == "" {
			return false
		}
	}
	return true
}

// RegistrationPath: plik stanu rejestracji (wspólny z wersją PowerShell).
func RegistrationPath() string { return filepath.Join(UserDataDir(), "rejestracja.json") }

// RegistrationClause odpowiada Get-SdRegistrationClause: informacja o przetwarzaniu danych (art. 13 RODO).
func RegistrationClause() string {
	c := Registration
	return strings.Join([]string{
		"1. Administrator danych: " + c.Controller + ", kontakt: " + c.Contact + ".",
		"2. Zakres: imię i nazwisko, adres e-mail, numer telefonu (jeśli go podasz) oraz wersja programu i data zgody.",
		"3. Cel: rejestracja użytkowników programu SerwisDiag i kontakt w jego sprawie - informacje o nowych wersjach i pomoc techniczna. Dane nie służą do wysyłania reklam.",
		"4. Podstawa prawna: Twoja zgoda (art. 6 ust. 1 lit. a RODO). Podanie danych jest dobrowolne - program działa tak samo bez rejestracji.",
		"5. Odbiorca: dane są zapisywane w usłudze Formularze Google (Google Ireland Limited), która przechowuje je na zlecenie administratora. Google może przekazywać dane do USA na podstawie Ram ochrony danych UE-USA i standardowych klauzul umownych.",
		"6. Okres przechowywania: do wycofania zgody, nie dłużej niż do zakończenia rozwijania programu.",
		"7. Masz prawo dostępu do danych, ich sprostowania, usunięcia, ograniczenia przetwarzania i przeniesienia oraz do wycofania zgody w dowolnej chwili (bez wpływu na zgodność z prawem wcześniejszego przetwarzania) - napisz na " + c.Contact + ". Możesz też złożyć skargę do Prezesa Urzędu Ochrony Danych Osobowych.",
		"8. Dane nie są używane do zautomatyzowanego podejmowania decyzji ani profilowania.",
	}, "\n")
}

// RegistrationConsentText odpowiada Get-SdRegistrationConsentText.
func RegistrationConsentText() string {
	return "Wyrażam zgodę na przetwarzanie podanych danych przez autora programu (" + Registration.Controller + ") w celu rejestracji programu SerwisDiag i kontaktu w jego sprawie (nowe wersje, pomoc). Zgodę mogę wycofać w każdej chwili."
}

var (
	regLetterRx = regexp.MustCompile(`\p{L}`)
	regEmailRx  = regexp.MustCompile(`^[^@\s<>",;]+@[^@\s<>",;]+\.\p{L}{2,}$`)
	regPhoneRx  = regexp.MustCompile(`^\+?[0-9][0-9 ()\-]*$`)
	regNonDigit = regexp.MustCompile(`\D`)
	regSpacesRx = regexp.MustCompile(`\s+`)
)

// RegistrationInputErrors odpowiada Test-SdRegistrationInput: lista błędów po polsku (pusta = dane poprawne).
func RegistrationInputErrors(name, email, phone string, consent bool) []string {
	var errs []string
	n := strings.TrimSpace(name)
	if l := utf8.RuneCountInString(n); l < 3 || l > 100 || !regLetterRx.MatchString(n) {
		errs = append(errs, "Podaj imię i nazwisko.")
	}
	if e := strings.TrimSpace(email); utf8.RuneCountInString(e) > 254 || !regEmailRx.MatchString(e) {
		errs = append(errs, "Podaj poprawny adres e-mail.")
	}
	if p := strings.TrimSpace(phone); p != "" {
		if d := len(regNonDigit.ReplaceAllString(p, "")); !regPhoneRx.MatchString(p) || d < 7 || d > 15 {
			errs = append(errs, "Numer telefonu może zawierać tylko cyfry, spacje, myślniki, nawiasy i + na początku.")
		}
	}
	if !consent {
		errs = append(errs, "Zaznacz zgodę na przetwarzanie danych - bez niej rejestracja nie jest możliwa.")
	}
	return errs
}

// RegistrationRecord - zapisana rejestracja (pola w kolejności z ConvertTo-SdRegistrationRecord).
type RegistrationRecord struct {
	Status      string `json:"status"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	ConsentAt   string `json:"consentAt"`
	Clause      string `json:"clause"`
	Version     string `json:"version"`
	Mode        string `json:"mode"`
	Attempts    int    `json:"attempts"`
	LastAttempt string `json:"lastAttempt"`
	LastError   string `json:"lastError"`
	SentAt      string `json:"sentAt"`
}

// ReadRegistration odpowiada Get-SdRegistration: zapisana rejestracja albo nil.
func ReadRegistration(path string) *RegistrationRecord {
	if path == "" {
		path = RegistrationPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// wersja PowerShell zapisuje liczbę prób jako liczbę, ale pozostałe pola mogą być null
	var raw map[string]any
	if json.Unmarshal(decodeText(b), &raw) != nil {
		return nil
	}
	s := func(k string) string { return jsonStr(raw[k]) }
	r := &RegistrationRecord{Status: s("status"), Name: s("name"), Email: s("email"), Phone: s("phone"), ConsentAt: s("consentAt"), Clause: s("clause"),
		Version: s("version"), Mode: s("mode"), LastAttempt: s("lastAttempt"), LastError: s("lastError"), SentAt: s("sentAt")}
	if f, ok := raw["attempts"].(float64); ok {
		r.Attempts = int(f)
	}
	return r
}

func writeRegistration(path string, r RegistrationRecord) error {
	b, err := json.MarshalIndent(r, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// SaveRegistration odpowiada Save-SdRegistration: dane z okna rejestracji do wysłania (status pending) - zgoda z datą,
// wersja programu i rodzaj kopii.
func SaveRegistration(name, email, phone, root, path string) (RegistrationRecord, error) {
	if path == "" {
		path = RegistrationPath()
	}
	mode := "wersja przenośna"
	switch InstallMode(root) {
	case "allusers":
		mode = "instalacja dla wszystkich"
	case "currentuser":
		mode = "instalacja dla mnie"
	}
	r := RegistrationRecord{Status: "pending", Name: regSpacesRx.ReplaceAllString(strings.TrimSpace(name), " "), Email: strings.TrimSpace(email),
		Phone: regSpacesRx.ReplaceAllString(strings.TrimSpace(phone), " "), ConsentAt: time.Now().Format("2006-01-02T15:04:05"),
		Clause: Registration.Clause, Version: Version + " (Go)", Mode: mode}
	return r, writeRegistration(path, r)
}

// registrationBody odpowiada ConvertTo-SdRegistrationBody: treść zgłoszenia (application/x-www-form-urlencoded, UTF-8).
func registrationBody(r RegistrationRecord) string {
	when := ""
	if d := parseJSONDate(r.ConsentAt); !d.IsZero() {
		when = d.Format("2006-01-02 15:04")
	}
	program := "SerwisDiag " + r.Version
	if r.Mode != "" {
		program += ", " + r.Mode
	}
	program += "; zgoda " + when + " (informacja o danych z " + r.Clause + ")"
	f := Registration.Fields
	return strings.Join([]string{f["Name"] + "=" + escapeDataString(r.Name), f["Email"] + "=" + escapeDataString(r.Email),
		f["Phone"] + "=" + escapeDataString(r.Phone), f["Program"] + "=" + escapeDataString(program)}, "&")
}

// RegistrationDue odpowiada Test-SdRegistrationDue: rejestracja włączona, dane czekają, a od poprzedniej nieudanej
// próby minęło co najmniej 30 minut.
func RegistrationDue(path string, now time.Time) bool {
	if !RegistrationEnabled() {
		return false
	}
	r := ReadRegistration(path)
	if r == nil || r.Status != "pending" {
		return false
	}
	last := parseJSONDate(r.LastAttempt)
	return last.IsZero() || last.After(now) || now.Sub(last).Minutes() >= registrationRetryMin
}

// httpStatusError - odpowiedź serwera z błędem HTTP (jak WebException z kodem w wersji PowerShell).
type httpStatusError int

func (e httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

// postRegistration odpowiada Invoke-SdRegistrationPost: POST i adres po przekierowaniach.
func postRegistration(u, body string) (string, error) {
	req, err := http.NewRequest("POST", u, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", httpStatusError(resp.StatusCode)
	}
	return resp.Request.URL.String(), nil
}

// registrationError odpowiada Get-SdRegistrationError: opis błędu wysyłania dla użytkownika.
func registrationError(err error) string {
	var he httpStatusError
	var ne net.Error
	switch {
	case errors.As(err, &he):
		switch int(he) {
		case 404:
			return "Nie znaleziono formularza rejestracji (HTTP 404) - ta wersja programu ma nieaktualny adres."
		case 400:
			return "Formularz rejestracji odrzucił dane (HTTP 400)."
		}
		return fmt.Sprintf("Serwer formularza zwrócił błąd HTTP %d.", int(he))
	case errors.As(err, &ne):
		return "Brak połączenia z internetem (albo serwer formularza nie odpowiada)."
	}
	return "Nie udało się wysłać: " + err.Error()
}

var (
	closedFormRx = rx(`closedform`)
	googleAuthRx = rx(`accounts\.google\.com|ServiceLogin`)
)

// SendRegistration odpowiada Send-SdRegistration: wysyła zapisane dane (status pending), po sukcesie status sent.
// Okno programu, monitor i wersja PowerShell mogą próbować jednocześnie - blokada sprawia, że zgłoszenie wychodzi raz.
// transport (testy): zamiast wysyłania przez HTTP dostaje adres i treść, zwraca adres po przekierowaniach.
func SendRegistration(path string, transport func(u, body string) (string, error)) (bool, string) {
	if path == "" {
		path = RegistrationPath()
	}
	if !RegistrationEnabled() {
		return false, "Rejestracja jest wyłączona w tej wersji programu."
	}
	name, _ := windows.UTF16PtrFromString(`Local\SerwisDiag.Registration`)
	// blokada należy do wątku systemowego: gorutyna przeniesiona na inny wątek nie zwolniłaby jej (ReleaseMutex),
	// a inne procesy czekałyby na nią do zamknięcia programu
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// blokada otwarta już przez inny proces (np. wersję PowerShell): x/sys zwraca ERROR_ALREADY_EXISTS z poprawnym
	// uchwytem - wtedy czekamy na nią niżej
	mx, err := windows.CreateMutex(nil, false, name)
	if mx == 0 {
		return false, "Nie udało się wysłać: " + err.Error()
	}
	defer windows.CloseHandle(mx)
	if ev, _ := windows.WaitForSingleObject(mx, 30000); ev != windows.WAIT_OBJECT_0 && ev != windows.WAIT_ABANDONED {
		return false, "Rejestrację wysyła właśnie inne okno programu."
	}
	defer windows.ReleaseMutex(mx)
	rec := ReadRegistration(path)
	switch {
	case rec != nil && rec.Status == "sent":
		return true, "Rejestracja została już wysłana."
	case rec == nil || rec.Status != "pending":
		return false, "Brak danych rejestracji do wysłania."
	}
	if transport == nil {
		transport = postRegistration
	}
	msg := ""
	if final, err := transport(Registration.FormURL, registrationBody(*rec)); err != nil {
		msg = registrationError(err)
	} else if closedFormRx.MatchString(final) {
		msg = "Formularz rejestracji nie przyjmuje już zgłoszeń."
	} else if googleAuthRx.MatchString(final) {
		msg = "Formularz rejestracji wymaga logowania do konta Google."
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	if msg != "" {
		rec.Attempts++
		rec.LastAttempt, rec.LastError = now, msg
	} else {
		rec.Status, rec.SentAt, rec.LastError = "sent", now, ""
	}
	if err := writeRegistration(path, *rec); err != nil && msg == "" {
		msg = "Nie udało się zapisać stanu rejestracji: " + err.Error()
	}
	if msg != "" {
		return false, msg
	}
	return true, "Wysłano."
}
