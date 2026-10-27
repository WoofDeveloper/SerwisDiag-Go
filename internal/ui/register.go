package ui

import (
	"os"
	"strings"
	"sync"
	"time"

	"serwisdiag/internal/diag"
)

// Okno dobrowolnej rejestracji programu (Show-SdUiRegistration): imię i nazwisko, e-mail, telefon i zgoda; dane
// zapisuje plik stanu wspólny z wersją PowerShell, wysyłanie w tle. Standalone (-Register, na końcu instalacji) -
// bez okna głównego programu.

const (
	wmRegBg  = wmApp + 23 // wynik wysyłania rejestracji
	timerReg = 10
)

const (
	ctlRegName = 781 + iota
	ctlRegEmail
	ctlRegPhone
	ctlRegConsent
	ctlRegMore
	ctlRegSend
	ctlRegCancel
)

type regDialog struct {
	d                          *dialog
	root                       string
	edits                      [3]uintptr
	consent, more, send, close uintptr
	clause                     *doc
	title, intro, status       string
	statusColor                col
	expanded, busy             bool
	consentRect                rect

	mu     sync.Mutex
	bgDone func()
}

// lastRegDialog: autotest wypełnia okno błędnymi danymi do drugiego zrzutu.
var lastRegDialog *regDialog

var regLabels = [3]string{"Imię i nazwisko", "E-mail", "Telefon (opcjonalnie)"}

// regLayout - położenie elementów okna (liczone dla szerokości obszaru klienta).
type regLayout struct {
	introY, gridY, consentY, moreY, clauseY, statusY, btnY, total int
	intro, consent, status                                        textBlock
}

func (rd *regDialog) layout(w int) regLayout {
	d := rd.d
	p := d.px(22)
	var L regLayout
	y := p + d.font(200, fwSemiBold).height + d.px(6)
	L.introY = y
	L.intro = layoutText([]span{{text: rd.intro, font: d.font(130, fwNormal), color: cGray2}}, w-2*p, 0)
	y += L.intro.h + d.px(14)
	L.gridY = y
	y += 3*d.px(38) + d.px(14)
	L.consentY = y
	box := d.px(20)
	L.consent = layoutText([]span{{text: diag.RegistrationConsentText(), font: d.font(130, fwNormal), color: cGray3}}, w-2*p-box, 0)
	y += max(L.consent.h, box) + d.px(12)
	L.moreY = y
	y += d.px(30)
	if rd.expanded {
		y += d.px(8)
		L.clauseY = y
		y += d.px(190)
	}
	if rd.status != "" {
		y += d.px(12)
		L.statusY = y
		L.status = layoutText([]span{{text: rd.status, font: d.font(130, fwNormal), color: rd.statusColor}}, w-2*p, 0)
		y += L.status.h
	}
	y += d.px(16)
	L.btnY = y
	L.total = y + d.px(32) + p
	return L
}

// fit: wysokość okna dopasowana do treści (SizeToContent), wyśrodkowana względem dotychczasowej.
func (rd *regDialog) fit() {
	h := rd.d.h
	cr, wr := clientRect(h), windowRect(h)
	need := rd.layout(cr.w()).total
	if need == cr.h() {
		return
	}
	nh := wr.h() + need - cr.h()
	y := int(wr.Top) - (need-cr.h())/2
	if wr.Left > -2000 {
		wa := workArea(h)
		y = min(max(y, int(wa.Top)), max(int(wa.Bottom)-nh, int(wa.Top)))
	}
	call(pSetWindowPos, h, 0, uintptr(int(wr.Left)), uintptr(y), uintptr(wr.w()), uintptr(nh), swpNoZOrder|swpNoActivate)
}

func (rd *regDialog) setStatus(text string, c col) {
	rd.status, rd.statusColor = text, c
	rd.fit()
	call(pInvalidateRect, rd.d.h, 0, 0)
}

// showRegistration odpowiada Show-SdUiRegistration; owner 0 - okno samodzielne (na końcu instalacji).
func showRegistration(owner uintptr, root string) {
	if !diag.RegistrationEnabled() {
		return
	}
	d := newDialog(owner, "Rejestracja programu SerwisDiag", 560, 520, false)
	rd := &regDialog{d: d, root: root, title: "Zarejestruj program",
		intro: "Rejestracja jest dobrowolna - program działa tak samo bez niej. Dane trafią tylko do autora programu, który może poinformować Cię o nowych wersjach i pomóc w razie problemów."}
	lastRegDialog = rd
	for i := range rd.edits {
		rd.edits[i] = newEdit(d.h, ctlRegName+i, "", 0)
		sendMsg(rd.edits[i], emLimitText, uintptr([]int{100, 254, 25}[i]), 0)
	}
	rd.consent = newCheck(d.h, ctlRegConsent, diag.RegistrationConsentText())
	rd.more = newButton(d.h, ctlRegMore, "Informacja o przetwarzaniu danych (RODO)")
	rd.send = newButton(d.h, ctlRegSend, "Zarejestruj")
	rd.close = newButton(d.h, ctlRegCancel, "Nie teraz")
	rd.clause = newDoc(d.h, cWhite, func(dc *doc, width int) int {
		y := dc.px(10)
		t := layoutText([]span{{text: diag.RegistrationClause(), font: dc.font(120, fwNormal), color: cGray3}}, width-2*dc.px(12), 0)
		dc.block(t, dc.px(12), y)
		return y + t.h + dc.px(10)
	})
	rd.clause.dpi = d.dpi
	showWin(rd.clause.h, false)
	if r := diag.ReadRegistration(""); r != nil && (r.Status == "sent" || r.Status == "pending") && !quietMessages {
		for i, v := range []string{r.Name, r.Email, r.Phone} {
			setText(rd.edits[i], v)
		}
		if r.Status == "sent" {
			rd.intro = "Program jest zarejestrowany"
			if t, err := time.ParseInLocation(sFormat, r.SentAt, time.Local); err == nil {
				rd.intro += " od " + t.Format("2006-01-02")
			}
			rd.intro += ". Możesz zaktualizować swoje dane - zostaną wysłane ponownie. Aby usunąć dane, napisz na " + diag.Registration.Contact + "."
			rd.title = "Program jest zarejestrowany"
			setText(rd.send, "Wyślij zmiany")
		} else {
			rd.intro = "Twoje dane czekają na wysłanie"
			if r.LastError != "" {
				rd.intro += " (ostatnia próba: " + r.LastError + ")"
			}
			rd.intro += ". Program wyśle je sam, gdy będzie internet - możesz też kliknąć „Wyślij teraz”."
			rd.title = "Rejestracja czeka na wysłanie"
			setText(rd.send, "Wyślij teraz")
		}
		setText(rd.close, "Zamknij")
	}
	fonts := func() {
		f := d.font(130, fwNormal)
		for _, c := range []uintptr{rd.edits[0], rd.edits[1], rd.edits[2], rd.consent, rd.more, rd.send, rd.close} {
			setFont(c, f)
		}
		for _, e := range rd.edits {
			setEditMargins(e, d.px(4))
		}
		rd.clause.dpi = d.dpi
	}
	fonts()
	d.fonts = fonts
	d.paint = func(cv *canvas, w, h int) {
		p := d.px(22)
		L := rd.layout(w)
		cv.text(d.font(200, fwSemiBold), rd.title, p, p, cDark)
		cv.block(L.intro, p, L.introY)
		f := d.font(130, fwNormal)
		for i, l := range regLabels {
			cv.text(f, l, p, L.gridY+i*d.px(38)+(d.px(38)-f.height)/2, cText)
		}
		cv.block(L.consent, p+d.px(20), L.consentY)
		if rd.expanded {
			cv.card(p, L.clauseY, w-p, L.clauseY+d.px(190), float64(d.px(6)), cWhite, cBorder, 1)
		}
		if rd.status != "" {
			cv.block(L.status, p, L.statusY)
		}
	}
	d.onSize = func(w, h int) {
		p := d.px(22)
		L := rd.layout(w)
		x := p + d.px(150)
		for i, e := range rd.edits {
			moveWin(e, x, L.gridY+i*d.px(38)+d.px(4), w-x-p, d.px(30))
		}
		// pole wyboru bez opisu (opis rysuje okno - zawija się w kilka wierszy; kliknięcie w niego też zaznacza)
		moveWin(rd.consent, p, L.consentY+d.px(1), d.px(15), d.px(18)) // sam kwadrat - bez skrawka opisu kontrolki
		rd.consentRect = rect{int32(p + d.px(20)), int32(L.consentY), int32(p + d.px(20) + L.consent.w), int32(L.consentY + L.consent.h)}
		moveWin(rd.more, p, L.moreY, buttonWidth(d.font(130, fwNormal), getText(rd.more), d.px(12)), d.px(30))
		if rd.expanded {
			moveWin(rd.clause.h, p+d.px(1), L.clauseY+d.px(1), w-2*p-d.px(2), d.px(188))
		}
		moveWin(rd.close, w-p-d.px(110), L.btnY, d.px(110), d.px(32))
		sw := buttonWidth(d.font(130, fwNormal), getText(rd.send), d.px(16))
		moveWin(rd.send, w-p-d.px(110)-d.px(8)-sw, L.btnY, sw, d.px(32))
	}
	d.canClose = func() bool { return !rd.busy } // najpierw wynik wysyłania
	d.onCmd = func(id, code int) {
		switch id {
		case ctlRegSend, idOK:
			rd.submit()
		case ctlRegCancel, idCancel:
			if !rd.busy {
				d.close()
			}
		case ctlRegMore:
			rd.expanded = !rd.expanded
			setText(rd.more, map[bool]string{false: "Informacja o przetwarzaniu danych (RODO)", true: "Ukryj informację o przetwarzaniu danych"}[rd.expanded])
			showWin(rd.clause.h, rd.expanded)
			rd.fit()
			cr := clientRect(d.h)
			d.onSize(cr.w(), cr.h())
			call(pInvalidateRect, d.h, 0, 0)
		}
	}
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		switch m {
		case wmLButtonUp:
			x, y := int(int16(lp&0xFFFF)), int(int16(lp>>16&0xFFFF))
			if r := rd.consentRect; x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom) && call(pIsWindowEnabled, rd.consent) != 0 {
				setChecked(rd.consent, !isChecked(rd.consent))
				call(pSetFocus, rd.consent)
				return 0, true
			}
		case wmRegBg:
			rd.mu.Lock()
			f := rd.bgDone
			rd.bgDone = nil
			rd.mu.Unlock()
			if f != nil {
				f()
			}
			return 0, true
		}
		return 0, false
	}
	rd.fit()
	shotStep := 0
	if regShotPrefix != "" {
		if owner == 0 {
			wr := windowRect(d.h)
			call(pSetWindowPos, d.h, 0, ^uintptr(3000-1), 40, uintptr(wr.w()), uintptr(wr.h()), swpNoZOrder|swpNoActivate)
		}
		call(pSetTimer, d.h, timerReg, 600, 0)
	}
	prev := d.onMsg
	d.onMsg = func(m uint32, wp, lp uintptr) (uintptr, bool) {
		if m == wmTimer && wp == timerReg {
			rd.regSelfTestTick(&shotStep)
			return 0, true
		}
		return prev(m, wp, lp)
	}
	d.show()
	call(pKillTimer, d.h, timerReg)
	lastRegDialog = nil
}

// submit: kontrola danych, zapis (status pending) i wysyłanie w tle; wynik w komunikacie, potem zamknięcie okna.
func (rd *regDialog) submit() {
	if rd.busy {
		return
	}
	name, email, phone := getText(rd.edits[0]), getText(rd.edits[1]), getText(rd.edits[2])
	if errs := diag.RegistrationInputErrors(name, email, phone, isChecked(rd.consent)); len(errs) > 0 {
		rd.setStatus("• "+strings.Join(errs, "\n• "), hexc("#B42318"))
		return
	}
	if _, err := diag.SaveRegistration(name, email, phone, rd.root, ""); err != nil {
		rd.setStatus("Nie zapisano danych: "+err.Error(), hexc("#B42318"))
		return
	}
	rd.busy = true
	for _, c := range []uintptr{rd.edits[0], rd.edits[1], rd.edits[2], rd.consent, rd.send} {
		enableWin(c, false)
	}
	rd.setStatus("Wysyłanie...", cGray2)
	h := rd.d.h
	go func() {
		ok, msg := diag.SendRegistration("", nil)
		rd.mu.Lock()
		rd.bgDone = func() {
			rd.busy = false
			if ok {
				msgBox(h, "Dziękujemy! Program SerwisDiag jest zarejestrowany.", "Rejestracja", mbIconInfo)
			} else {
				msgBox(h, "Nie udało się wysłać rejestracji: "+msg+"\n\nDane zostały zapisane na tym komputerze - program wyśle je sam przy następnym uruchomieniu.", "Rejestracja", mbIconWarning)
			}
			rd.d.close()
		}
		rd.mu.Unlock()
		postMsg(h, wmRegBg, 0, 0)
	}()
}

// selfTestErrors (autotest): błędne dane, rozwinięta informacja RODO i lista błędów - do drugiego zrzutu.
func (rd *regDialog) selfTestErrors() {
	for i, v := range []string{"Jan Kowalski", "jan.kowalski@example", "600 700 80x"} {
		setText(rd.edits[i], v)
	}
	if !rd.expanded {
		rd.d.onCmd(ctlRegMore, 0)
	}
	errs := diag.RegistrationInputErrors(getText(rd.edits[0]), getText(rd.edits[1]), getText(rd.edits[2]), false)
	rd.setStatus("• "+strings.Join(errs, "\n• "), hexc("#B42318"))
	cr := clientRect(rd.d.h)
	rd.d.onSize(cr.w(), cr.h())
}

// RunRegistration - parametr -Register: samo okno rejestracji (na końcu instalacji). screenshot (autotest): zrzuty
// okna pustego i z błędami w danych, bez zapisu i wysyłania; okno poza ekranem.
func RunRegistration(root, screenshot string) int {
	initUI()
	if screenshot != "" {
		quietMessages = true
		regShotPrefix = screenshot
		if !diag.RegistrationEnabled() && os.Getenv("SERWISDIAG_SELFTEST_REGISTER") != "" {
			// wygląd okna, zanim program dostanie adres formularza (autotest niczego nie wysyła)
			diag.Registration.FormURL = "https://docs.google.com/forms/d/e/TEST/formResponse"
			diag.Registration.Fields = map[string]string{"Name": "entry.1", "Email": "entry.2", "Phone": "entry.3", "Program": "entry.4"}
		}
	}
	showRegistration(0, root)
	return 0
}

// regShotPrefix: autotest okna rejestracji - prefiks plików zrzutów.
var regShotPrefix string

// regSelfTestTick: zrzut pustego okna, potem z błędami, potem zamknięcie.
func (rd *regDialog) regSelfTestTick(step *int) {
	switch *step {
	case 0:
		if err := saveWindowPNG(rd.d.h, regShotPrefix+"-rejestracja.png"); err != nil {
			logError("zrzut rejestracji: " + err.Error())
		}
		rd.selfTestErrors()
	case 1:
		// jeszcze jeden takt - okno po zmianie rozmiaru
	default:
		if err := saveWindowPNG(rd.d.h, regShotPrefix+"-rejestracja-bledy.png"); err != nil {
			logError("zrzut rejestracji: " + err.Error())
		}
		call(pKillTimer, rd.d.h, timerReg)
		rd.d.close()
	}
	*step++
}
