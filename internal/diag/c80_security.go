package diag

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"serwisdiag/internal/win"
)

// Testy kategorii: Bezpieczeństwo (checks\80-security.ps1)

var securityChecks = []*Check{
	{ID: "se.av", Category: "security", Title: "Ochrona antywirusowa", Level: "Quick", Run: checkAntivirus},
	{ID: "se.defexcl", Category: "security", Title: "Wykluczenia Microsoft Defender", Level: "Quick", NeedsAdmin: true, Run: checkDefenderExclusions},
	{ID: "se.firewall", Category: "security", Title: "Zapora systemu Windows", Level: "Quick", Run: checkFirewall},
	{ID: "se.uac", Category: "security", Title: "Kontrola konta użytkownika (UAC)", Level: "Quick", Run: checkUAC},
	{ID: "se.bitlocker", Category: "security", Title: "Szyfrowanie dysków (BitLocker)", Level: "Quick", NeedsAdmin: true, Run: checkBitLocker},
	{ID: "se.platform", Category: "security", Title: "Secure Boot, TPM i zabezpieczenia wirtualizacyjne", Level: "Quick", Run: checkPlatform},
	{ID: "se.accounts", Category: "security", Title: "Konta użytkowników i administratorzy", Level: "Quick", Run: checkAccounts},
	{ID: "se.exposure", Category: "security", Title: "Usługi zdalnego dostępu i udostępnianie", Level: "Quick", Run: checkExposure},
	{ID: "se.persistence", Category: "security", Title: "Ślady modyfikacji systemu (trwałość złośliwego oprogramowania)", Level: "Quick", Run: checkPersistence},
	{ID: "se.logons", Category: "security", Title: "Nieudane logowania (ostatnie 24 godziny)", Level: "Full", NeedsAdmin: true, Run: checkFailedLogons},
}

var (
	defenderRx = rx(`Defender|Windows Defender`)
	passiveRx  = rx(`Passive`)
)

func checkAntivirus(c *Ctx) {
	products := c.Cim(Q{NS: `root\SecurityCenter2`, Class: "AntivirusProduct", Props: []string{"displayName", "productState", "pathToSignedProductExe"}})
	var mp win.WmiObject
	if r := c.Cim(Q{NS: `root\Microsoft\Windows\Defender`, Class: "MSFT_MpComputerStatus"}); len(r) > 0 {
		mp = r[0]
	}
	var rows []Row
	var activeNames, thirdActive, outdated, names []string
	for _, p := range products {
		state := uint32(p.Int("productState"))
		on := (state >> 12) & 0xF
		sig := (state >> 4) & 0xF
		stateTxt := map[uint32]string{0: "wyłączony", 1: "włączony", 2: "wstrzymany", 3: "licencja wygasła"}[on]
		if stateTxt == "" {
			stateTxt = "nieznany"
		}
		sigTxt := "aktualne"
		if sig != 0 {
			sigTxt = "NIEAKTUALNE"
		}
		name := p.Str("displayName")
		rows = append(rows, row("Produkt", name, "Stan", stateTxt, "Definicje", sigTxt, "Ścieżka", p.Str("pathToSignedProductExe")))
		names = append(names, fmt.Sprintf("%s (%s, definicje %s)", name, stateTxt, sigTxt))
		if on == 1 {
			activeNames = append(activeNames, name)
			if sig != 0 {
				outdated = append(outdated, name)
			}
			if !defenderRx.MatchString(name) {
				thirdActive = append(thirdActive, name)
			}
		}
	}
	c.AddTable(T{Name: "Produkty antywirusowe (Centrum zabezpieczeń)", Rows: rows})
	namesTxt := strings.Join(names, "; ")
	switch {
	case len(products) == 0 && mp == nil:
		if winInfo(c).IsServer {
			c.Add(F{Title: "Program antywirusowy", Status: Info, Value: "Wersja serwerowa - Centrum zabezpieczeń nie jest dostępne"})
		} else {
			c.Add(F{Title: "Program antywirusowy", Status: Crit, Value: "Nie wykryto żadnego programu antywirusowego",
				Advice: "Komputer jest niechroniony. Włącz Microsoft Defender (Zabezpieczenia Windows) lub zainstaluj sprawdzony program antywirusowy; wykonaj pełne skanowanie."})
		}
	case len(activeNames) == 0:
		c.Add(F{Title: "Program antywirusowy", Status: Crit, Value: "Żaden program nie jest aktywny: " + namesTxt,
			Advice: "Ochrona antywirusowa jest wyłączona lub licencja wygasła. Włącz Microsoft Defender lub odnów licencję programu; wykonaj pełne skanowanie systemu."})
	case len(outdated) > 0:
		c.Add(F{Title: "Program antywirusowy", Status: Warn, Value: "Nieaktualne definicje: " + strings.Join(outdated, ", "), Details: namesTxt,
			Advice: "Zaktualizuj definicje wirusów (połączenie z internetem, usługa Windows Update / aktualizator programu)."})
	case len(thirdActive) > 1:
		c.Add(F{Title: "Program antywirusowy", Status: Warn, Value: "Aktywnych kilka programów antywirusowych: " + strings.Join(thirdActive, ", "), Details: namesTxt,
			Advice: "Dwa aktywne programy antywirusowe konfliktują ze sobą i spowalniają system. Pozostaw jeden, drugi odinstaluj (zwykle dedykowanym narzędziem producenta)."})
	default:
		c.Add(F{Title: "Program antywirusowy", Status: OK, Value: namesTxt})
	}
	if mp == nil {
		return
	}
	onOff := func(b bool, on, off string) string {
		if b {
			return on
		}
		return off
	}
	d := []string{fmt.Sprintf("Usługa Defender: %s; ochrona w czasie rzeczywistym: %s", onOff(mp.Bool("AMServiceEnabled"), "działa", "zatrzymana"), onOff(mp.Bool("RealTimeProtectionEnabled"), "włączona", "wyłączona"))}
	if mp.Str("AMRunningMode") != "" {
		d = append(d, "Tryb pracy: "+mp.Str("AMRunningMode"))
	}
	if t, ok := mp.Time("AntivirusSignatureLastUpdated"); ok {
		d = append(d, fmt.Sprintf("Definicje zaktualizowano: %s; wiek: %s dn.", fmtDt(t), mp.Str("AntivirusSignatureAge")))
	}
	if mp.Has("QuickScanAge") && mp.Int("QuickScanAge") < 4000 {
		d = append(d, fmt.Sprintf("Ostatnie szybkie skanowanie: %d dn. temu", mp.Int("QuickScanAge")))
	}
	neverFull := !mp.Has("FullScanAge") || mp.Int("FullScanAge") >= 4000
	if !neverFull {
		d = append(d, fmt.Sprintf("Ostatnie pełne skanowanie: %d dn. temu", mp.Int("FullScanAge")))
	} else {
		d = append(d, "Pełne skanowanie: nigdy nie wykonano")
	}
	if mp.Has("IsTamperProtected") {
		d = append(d, "Ochrona przed manipulacją: "+onOff(mp.Bool("IsTamperProtected"), "włączona", "wyłączona"))
	}
	passive := passiveRx.MatchString(mp.Str("AMRunningMode"))
	age := mp.Int("AntivirusSignatureAge")
	det := strings.Join(d, "\n")
	switch {
	case !passive && mp.Bool("AntivirusEnabled") && mp.Has("RealTimeProtectionEnabled") && !mp.Bool("RealTimeProtectionEnabled"):
		c.Add(F{Title: "Microsoft Defender", Status: Crit, Value: "Ochrona w czasie rzeczywistym wyłączona", Details: det,
			Advice: "Włącz ochronę w czasie rzeczywistym w Zabezpieczeniach Windows. Jeśli wyłącza się samoczynnie, sprawdź obecność złośliwego oprogramowania i zasady grupy."})
	case !passive && age > 30:
		c.Add(F{Title: "Microsoft Defender", Status: Crit, Value: fmt.Sprintf("Definicje nieaktualne od %d dni", age), Details: det,
			Advice: "Zaktualizuj definicje (Zabezpieczenia Windows > Ochrona przed wirusami > Aktualizacje) i sprawdź Windows Update."})
	case !passive && age > 7:
		c.Add(F{Title: "Microsoft Defender", Status: Warn, Value: fmt.Sprintf("Definicje mają %d dni", age), Details: det,
			Advice: "Zaktualizuj definicje (Zabezpieczenia Windows > Ochrona przed wirusami > Aktualizacje)."})
	case passive:
		c.Add(F{Title: "Microsoft Defender", Status: Info, Value: "Tryb pasywny (aktywny jest inny program antywirusowy)", Details: det})
	default:
		st, adv := OK, ""
		if neverFull {
			st, adv = Info, "Nie wykonano jeszcze pełnego skanowania - przy diagnostyce podejrzanego komputera uruchom pełne skanowanie."
		}
		c.Add(F{Title: "Microsoft Defender", Status: st, Value: fmt.Sprintf("Aktywny; definicje %d dn.; ochrona w czasie rzeczywistym %s", age, onOff(mp.Bool("RealTimeProtectionEnabled"), "włączona", "wyłączona")),
			Details: det, Advice: adv})
	}
}

var riskyExclRx = rx(`^[A-Za-z]:\\?$|\\Users\\?$|\\Temp\\?$|\\AppData\\?$|\\Windows\\?$|\\Downloads\\?$`)

func checkDefenderExclusions(c *Ctx) {
	r := c.Cim(Q{NS: `root\Microsoft\Windows\Defender`, Class: "MSFT_MpPreference", Props: []string{"ExclusionPath", "ExclusionExtension", "ExclusionProcess"}})
	if len(r) == 0 {
		return
	}
	nonEmpty := func(v []string) []string {
		var o []string
		for _, s := range v {
			if s != "" {
				o = append(o, s)
			}
		}
		return o
	}
	paths, exts, procs := nonEmpty(r[0].Strs("ExclusionPath")), nonEmpty(r[0].Strs("ExclusionExtension")), nonEmpty(r[0].Strs("ExclusionProcess"))
	total := len(paths) + len(exts) + len(procs)
	if total == 0 {
		c.Add(F{Title: "Wykluczenia Defender", Status: OK, Value: "Brak wykluczeń"})
		return
	}
	var rows []Row
	for _, x := range paths {
		rows = append(rows, row("Rodzaj", "Ścieżka", "Wartość", x))
	}
	for _, x := range exts {
		rows = append(rows, row("Rodzaj", "Rozszerzenie", "Wartość", x))
	}
	for _, x := range procs {
		rows = append(rows, row("Rodzaj", "Proces", "Wartość", x))
	}
	c.AddTable(T{Name: "Wykluczenia Microsoft Defender", Rows: rows, Private: true})
	var risky []string
	for _, p := range paths {
		if riskyExclRx.MatchString(p) {
			risky = append(risky, p)
		}
	}
	if len(risky) > 0 {
		c.Add(F{Title: "Wykluczenia Defender", Status: Warn, Value: "Szerokie wykluczenia: " + strings.Join(risky, "; "),
			Details: "Wykluczenie całego dysku lub katalogów użytkownika/temp wyłącza ochronę dla tych miejsc - często robią to cracki i złośliwe programy.",
			Advice:  "Sprawdź, kto i po co dodał wykluczenia; usuń zbędne (Zabezpieczenia Windows > Wykluczenia)."})
	} else {
		c.Add(F{Title: "Wykluczenia Defender", Status: Info, Value: fmtCount(total, "wykluczenie", "wykluczenia", "wykluczeń"), Details: "Lista w tabeli (widoczna tylko w raporcie technicznym).",
			Advice: "Upewnij się, że każde wykluczenie jest uzasadnione (np. katalog narzędzia deweloperskiego)."})
	}
}

func checkFirewall(c *Ctx) {
	profiles := []struct{ key, label string }{{"DomainProfile", "domenowy"}, {"StandardProfile", "prywatny"}, {"PublicProfile", "publiczny"}}
	const base = `HKLM\SYSTEM\CurrentControlSet\Services\SharedAccess\Parameters\FirewallPolicy`
	const pol = `HKLM\SOFTWARE\Policies\Microsoft\WindowsFirewall`
	var states, off []string
	for _, p := range profiles {
		v, ok := win.RegValue(base+`\`+p.key, "EnableFirewall")
		if pv, pok := win.RegValue(pol+`\`+p.key, "EnableFirewall"); pok {
			v, ok = pv, true
		}
		if !ok {
			continue
		}
		on := win.AnyInt(v) == 1
		st := "włączona"
		if !on {
			st = "WYŁĄCZONA"
			off = append(off, p.label)
		}
		states = append(states, p.label+": "+st)
	}
	if len(states) == 0 {
		return
	}
	var third []string
	for _, f := range c.Cim(Q{NS: `root\SecurityCenter2`, Class: "FirewallProduct", Props: []string{"displayName", "productState"}}) {
		if !rx(`Windows`).MatchString(f.Str("displayName")) && (uint32(f.Int("productState"))>>12)&0xF == 1 {
			third = append(third, f.Str("displayName"))
		}
	}
	val := strings.Join(states, "; ")
	switch {
	case len(off) == 0:
		c.Add(F{Title: "Zapora systemu Windows", Status: OK, Value: val})
	case len(third) > 0:
		c.Add(F{Title: "Zapora systemu Windows", Status: Info, Value: val, Details: "Aktywna zapora innego producenta: " + strings.Join(third, ", ")})
	case len(off) >= 3:
		c.Add(F{Title: "Zapora systemu Windows", Status: Crit, Value: val,
			Advice: "Zapora jest wyłączona we wszystkich profilach. Włącz ją (Zabezpieczenia Windows > Zapora i ochrona sieci), chyba że działa zapora innego producenta."})
	default:
		c.Add(F{Title: "Zapora systemu Windows", Status: Warn, Value: val, Advice: "Włącz zaporę we wszystkich profilach sieciowych, zwłaszcza w publicznym."})
	}
}

func checkUAC(c *Ctx) {
	const key = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	lua := regInt(key, "EnableLUA", 1)
	admin := regInt(key, "ConsentPromptBehaviorAdmin", 5)
	switch {
	case lua == 0:
		c.Add(F{Title: "Kontrola konta użytkownika (UAC)", Status: Crit, Value: "Wyłączona",
			Advice: "UAC jest wyłączone - programy mogą zmieniać system bez zgody użytkownika. Włącz UAC (Zmień ustawienia kontroli konta użytkownika) i uruchom ponownie komputer."})
	case admin == 0:
		c.Add(F{Title: "Kontrola konta użytkownika (UAC)", Status: Warn, Value: "Włączona, ale administrator nie otrzymuje powiadomień",
			Advice: "Ustaw poziom UAC co najmniej „Powiadamiaj tylko, gdy aplikacje próbują wprowadzić zmiany”."})
	default:
		c.Add(F{Title: "Kontrola konta użytkownika (UAC)", Status: OK, Value: "Włączona"})
	}
}

func checkBitLocker(c *Ctx) {
	vols := c.Cim(Q{NS: `root\cimv2\Security\MicrosoftVolumeEncryption`, Class: "Win32_EncryptableVolume", Props: []string{"DriveLetter", "ProtectionStatus"}})
	if len(vols) == 0 {
		c.Add(F{Title: "Szyfrowanie dysków (BitLocker)", Status: Info, Value: "Niedostępne w tej edycji systemu lub brak woluminów szyfrowanych"})
		return
	}
	var on []string
	var rows []Row
	for _, v := range vols {
		txt := map[int64]string{0: "wyłączone", 1: "WŁĄCZONE"}[v.Int("ProtectionStatus")]
		if txt == "" {
			txt = "nieznane"
		}
		rows = append(rows, row("Wolumin", v.Str("DriveLetter"), "Ochrona BitLocker", txt))
		if v.Int("ProtectionStatus") == 1 {
			on = append(on, v.Str("DriveLetter"))
		}
	}
	c.AddTable(T{Name: "BitLocker", Rows: rows})
	if len(on) > 0 {
		c.Add(F{Title: "Szyfrowanie dysków (BitLocker)", Status: Warn, Value: "Włączone na: " + strings.Join(on, ", "), Private: true,
			Details: "Dysk jest szyfrowany. Zmiana sprzętu (płyta główna, TPM), aktualizacja BIOS lub przeniesienie dysku może wymagać klucza odzyskiwania.",
			Advice:  "UWAGA SERWISOWA: przed wymianą podzespołów, aktualizacją BIOS lub naprawą rozruchu upewnij się, że klient ma klucz odzyskiwania (konto Microsoft: aka.ms/myrecoverykey)."})
	} else {
		c.Add(F{Title: "Szyfrowanie dysków (BitLocker)", Status: Info, Value: "Wyłączone",
			Details: "Dane na dysku nie są szyfrowane - w razie kradzieży laptopa są dostępne dla osoby trzeciej.",
			Advice:  "W laptopach rozważ włączenie szyfrowania (BitLocker / Szyfrowanie urządzenia), po zapisaniu klucza odzyskiwania."})
	}
}

func checkPlatform(c *Ctx) {
	w := winInfo(c)
	sb := secureBoot(c)
	tpm := tpmInfo(c)
	switch {
	case sb.Firmware != "UEFI":
		c.Add(F{Title: "Secure Boot", Status: Info, Value: fmt.Sprintf("Niedostępny (tryb rozruchu: %s)", sb.Firmware),
			Advice: "Secure Boot wymaga trybu UEFI. Windows 11 wymaga UEFI i Secure Boot; konwersja MBR→GPT (mbr2gpt) pozwala przejść z Legacy BIOS."})
	case sb.Known && sb.Enabled:
		c.Add(F{Title: "Secure Boot", Status: OK, Value: "Włączony"})
	case sb.Known:
		st := Info
		if w.Build >= 22000 {
			st = Warn
		}
		c.Add(F{Title: "Secure Boot", Status: st, Value: "Wyłączony w BIOS/UEFI",
			Details: "Secure Boot chroni przed złośliwym oprogramowaniem ładowanym przed systemem; wymagany przez Windows 11 oraz część gier z zabezpieczeniami anty-cheat.",
			Advice:  "Włącz Secure Boot w BIOS/UEFI (wymaga dysku GPT; jeśli system działa w trybie CSM/Legacy, najpierw konwersja)."})
	default:
		c.Add(F{Title: "Secure Boot", Status: Info, Value: "Nieobsługiwany przez firmware lub nie można ustalić"})
	}
	// Certyfikaty Secure Boot 2023 (zastępują certyfikaty Microsoft z 2011 r., które wygasają w 2026 r.)
	if sb.Firmware == "UEFI" && w.Build >= 9600 {
		const svc = `HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\Servicing`
		status := regStr(svc, "UEFICA2023Status", "")
		errV, hasErr := win.RegValue(svc, "UEFICA2023Error")
		d := "Certyfikaty Microsoft Secure Boot z 2011 r. wygasają w 2026 r. Komputer musi otrzymać certyfikaty z 2023 r. (aktualizacja Windows + często BIOS producenta), aby nadal otrzymywać poprawki menedżera rozruchu."
		switch {
		case eqi(status, "Updated"):
			c.Add(F{Title: "Certyfikaty Secure Boot (CA 2023)", Status: OK, Value: "Zaktualizowane", Details: d})
		case eqi(status, "InProgress"):
			c.Add(F{Title: "Certyfikaty Secure Boot (CA 2023)", Status: Info, Value: "Aktualizacja w toku (wymagany restart)", Details: d, Advice: "Uruchom ponownie komputer i sprawdź ponownie."})
		default:
			st := Info
			if sb.Known && sb.Enabled {
				st = Warn
			}
			val := "Brak informacji o aktualizacji certyfikatów"
			if status != "" {
				val = "Stan: " + status
			}
			if hasErr && win.AnyInt(errV) != 0 {
				val += "; błąd aktualizacji: " + win.AnyString(errV)
			}
			c.Add(F{Title: "Certyfikaty Secure Boot (CA 2023)", Status: st, Value: val, Details: d,
				Advice: "Zainstaluj wszystkie aktualizacje Windows i najnowszy BIOS od producenta; sprawdź komunikaty producenta komputera o aktualizacji certyfikatów Secure Boot."})
		}
	}
	// TPM
	if tpm.Present {
		en := "stan nieznany"
		switch tpm.Enabled {
		case No:
			en = "wyłączony"
		case Yes:
			en = "włączony"
		}
		ver := tpm.Version
		if ver == "" {
			ver = "wersja nieustalona"
		}
		mf := ""
		if tpm.Manufacturer != "" {
			mf = ", producent " + tpm.Manufacturer
		}
		st, adv := OK, ""
		if tpm.Enabled == No {
			st, adv = Warn, "Włącz TPM w BIOS/UEFI (fTPM / Intel PTT / AMD fTPM) - wymagany przez Windows 11 i BitLocker."
		}
		c.Add(F{Title: "TPM", Status: st, Value: fmt.Sprintf("TPM %s, %s%s", ver, en, mf), Advice: adv})
	} else {
		c.Add(F{Title: "TPM", Status: Info, Value: "Nie wykryto modułu TPM", Advice: "Sprawdź w BIOS/UEFI opcje fTPM / Intel PTT / AMD fTPM; Windows 11 wymaga TPM 2.0."})
	}
	// VBS / HVCI
	if dg := c.Cim(Q{NS: `root\Microsoft\Windows\DeviceGuard`, Class: "Win32_DeviceGuard", Props: []string{"SecurityServicesRunning", "VirtualizationBasedSecurityStatus"}}); len(dg) > 0 {
		running := dg[0].Ints("SecurityServicesRunning")
		var svcNames []string
		if slices.Contains(running, 1) {
			svcNames = append(svcNames, "Credential Guard")
		}
		if slices.Contains(running, 2) {
			svcNames = append(svcNames, "Izolacja rdzenia (HVCI)")
		}
		txt := map[int64]string{2: "działa", 1: "włączona, nieaktywna"}[dg[0].Int("VirtualizationBasedSecurityStatus")]
		if txt == "" {
			txt = "wyłączona"
		}
		if len(svcNames) > 0 {
			txt += ": " + strings.Join(svcNames, ", ")
		}
		c.Add(F{Title: "Zabezpieczenia oparte na wirtualizacji (VBS)", Status: Info, Value: txt})
	}
}

func checkAccounts(c *Ctx) {
	accts, err := win.LocalUsers()
	if err != nil {
		c.Log("WARN", "Konta lokalne: "+err.Error())
	}
	if len(accts) == 0 {
		return
	}
	admins, err := win.AdminGroupMembers()
	if err != nil {
		c.Log("WARN", "Administratorzy: "+err.Error())
	}
	var rows []Row
	var notes []string
	worstSt := OK
	note := func(s string) {
		notes = append(notes, s)
		if worstSt == OK {
			worstSt = Info
		}
	}
	yn := func(b bool, y, n string) string {
		if b {
			return y
		}
		return n
	}
	for _, a := range accts {
		isAdm := containsi(admins, a.Name)
		enabled := !a.Disabled
		rows = append(rows, row("Konto", a.Name, "Włączone", yn(enabled, "tak", "nie"), "Administrator", yn(isAdm, "tak", ""), "Wymaga hasła", yn(a.PasswordRequired, "tak", "NIE"), "Zablokowane", yn(a.Lockout, "tak", "")))
		// Konta powiązane z kontem Microsoft / logowaniem PIN-em często mają tu "hasło nie jest wymagane" - dlatego tylko informacja.
		if enabled && !a.PasswordRequired && isAdm {
			note("Konto administratora „" + a.Name + "” ma ustawione „hasło nie jest wymagane” (dla konta lokalnego ustaw hasło; konta Microsoft/PIN tak się czasem raportują)")
		}
		if enabled && a.RID == 501 {
			note("Konto Gość jest włączone")
		}
		if enabled && a.RID == 500 {
			note("Wbudowane konto Administrator jest włączone")
		}
	}
	c.AddTable(T{Name: "Konta lokalne", Rows: rows, Private: true})
	enabledAdmins := 0
	for _, n := range admins {
		for _, a := range accts {
			if eqi(a.Name, n) && !a.Disabled {
				enabledAdmins++
				break
			}
		}
	}
	if enabledAdmins > 3 {
		note(fmt.Sprintf("Wielu administratorów lokalnych: %d", enabledAdmins))
	}
	val := fmt.Sprintf("Konta lokalne: %d; administratorzy: %d", len(accts), len(admins))
	if len(notes) > 0 {
		c.Add(F{Title: "Konta użytkowników", Status: worstSt, Value: val, Details: strings.Join(notes, "\n"),
			Advice: "Ustaw hasła na kontach administratorów, wyłącz niepotrzebne konta (Gość, wbudowany Administrator) i ogranicz liczbę administratorów."})
	} else {
		c.Add(F{Title: "Konta użytkowników", Status: OK, Value: val})
	}
}

var (
	dollarEndRx = rx(`\$$`)
	wholeDiskRx = rx(`^[A-Za-z]:\\?$`)
)

func checkExposure(c *Ctx) {
	var notes []string
	worstSt := OK
	if regInt(`HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server`, "fDenyTSConnections", 1) == 0 {
		if regInt(`HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp`, "UserAuthentication", 1) == 0 {
			notes = append(notes, "Pulpit zdalny (RDP) włączony BEZ uwierzytelniania na poziomie sieci (NLA)")
			worstSt = Warn
		} else {
			notes = append(notes, "Pulpit zdalny (RDP) jest włączony")
			if worstSt == OK {
				worstSt = Info
			}
		}
	}
	smb1 := Unknown
	if c.IsAdmin() {
		if r := c.Cim(Q{NS: `root\Microsoft\Windows\SMB`, Class: "MSFT_SmbServerConfiguration", Props: []string{"EnableSMB1Protocol"}}); len(r) > 0 {
			smb1 = triOf(r[0].Bool("EnableSMB1Protocol"))
		}
	}
	if smb1 == Unknown {
		if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Services\LanmanServer\Parameters`, "SMB1"); ok {
			smb1 = triOf(win.AnyInt(v) != 0)
		}
	}
	if smb1 == Yes {
		notes = append(notes, "Włączony przestarzały protokół SMB1 (podatność wykorzystana przez WannaCry)")
		worstSt = Warn
	}
	if st, ok := win.ServiceState("RemoteRegistry"); ok && st == "Running" {
		notes = append(notes, "Działa usługa Zdalny rejestr")
		worstSt = Warn
	}
	var shares []win.WmiObject
	for _, s := range c.cim("Win32_Share", "Name", "Path", "Description", "Type") {
		if s.Int("Type") == 0 && !dollarEndRx.MatchString(s.Str("Name")) {
			shares = append(shares, s)
		}
	}
	if len(shares) > 0 {
		var rows []Row
		whole := false
		for _, s := range shares {
			rows = append(rows, row("Nazwa", s.Str("Name"), "Ścieżka", s.Str("Path"), "Opis", s.Str("Description")))
			if wholeDiskRx.MatchString(s.Str("Path")) {
				whole = true
			}
		}
		c.AddTable(T{Name: "Udostępnione foldery", Rows: rows, Private: true})
		notes = append(notes, fmt.Sprintf("Udostępnione foldery sieciowe: %d", len(shares)))
		if whole {
			notes = append(notes, "Udostępniony jest cały dysk")
			worstSt = Warn
		} else if worstSt == OK {
			worstSt = Info
		}
	}
	if len(notes) == 0 {
		c.Add(F{Title: "Zdalny dostęp i udostępnianie", Status: OK, Value: "Brak wykrytych ryzykownych ustawień"})
	} else {
		c.Add(F{Title: "Zdalny dostęp i udostępnianie", Status: worstSt, Value: strings.Join(notes, "; "),
			Advice: "Wyłącz funkcje, które nie są potrzebne (RDP, SMB1, Zdalny rejestr, udostępnione foldery). Jeśli RDP jest używany, wymagaj NLA i nie wystawiaj portu 3389 do internetu."})
	}
}

// defaultWinlogon odpowiada Test-SdDefaultWinlogon: czy Winlogon\Userinit lub \Shell ma wartość domyślną.
// Dopisanie po przecinku drugiego programu to typowy sposób na uruchamianie złośliwego kodu przy logowaniu.
func defaultWinlogon(value, kind, systemRoot string) bool {
	var items []string
	for _, s := range strings.Split(value, ",") {
		if t := strings.Trim(strings.TrimSpace(s), `"`); t != "" {
			items = append(items, t)
		}
	}
	if len(items) == 0 {
		return true
	}
	if len(items) > 1 {
		return false
	}
	v := items[0]
	if x, err := registry.ExpandString(v); err == nil {
		v = x
	}
	v = strings.ReplaceAll(v, "/", `\`)
	ok := []string{"explorer.exe", "explorer", systemRoot + `\explorer.exe`}
	if kind == "Userinit" {
		ok = []string{"userinit.exe", "userinit", systemRoot + `\system32\userinit.exe`, systemRoot + `\sysnative\userinit.exe`}
	}
	return containsi(ok, v)
}

var (
	accessibilityTools = []string{"sethc.exe", "utilman.exe", "osk.exe", "narrator.exe", "magnify.exe", "displayswitch.exe", "atbroker.exe"}
	redirectedRx       = rx(`MsMpEng|MpCmdRun|NisSrv|mbam|avast|avg|kaspersky|eset|bdagent|taskmgr|regedit|cmd\.exe|powershell|mmc\.exe`)
	bootExecRx         = rx(`^autocheck autochk \*$`)
)

func checkPersistence(c *Ctx) {
	var notes []string
	crit := false
	const wl = `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
	shell := regStr(wl, "Shell", "explorer.exe")
	uinit := regStr(wl, "Userinit", "")
	sr := getenv("SystemRoot")
	if !defaultWinlogon(shell, "Shell", sr) {
		notes, crit = append(notes, "Winlogon\\Shell ma niestandardową wartość: "+shell), true
	}
	if !defaultWinlogon(uinit, "Userinit", sr) {
		notes, crit = append(notes, "Winlogon\\Userinit ma niestandardową wartość: "+uinit), true
	}
	if regStr(wl, "AutoAdminLogon", "0") == "1" && win.AnyBool(func() any { v, _ := win.RegValue(wl, "DefaultPassword"); return v }()) {
		notes = append(notes, "Włączone automatyczne logowanie z hasłem zapisanym jawnym tekstem w rejestrze")
	}
	for _, k := range []string{`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Windows`, `HKLM\SOFTWARE\Wow6432Node\Microsoft\Windows NT\CurrentVersion\Windows`} {
		ai := regStr(k, "AppInit_DLLs", "")
		if strings.TrimSpace(ai) != "" && regInt(k, "LoadAppInit_DLLs", 0) == 1 {
			notes, crit = append(notes, "AppInit_DLLs ładuje biblioteki do każdego procesu: "+ai), true
		}
	}
	const ifeo = `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options`
	for _, k := range win.RegSubKeys(ifeo) {
		dbg := regStr(ifeo+`\`+k, "Debugger", "")
		if dbg == "" {
			continue
		}
		switch {
		case containsi(accessibilityTools, k):
			notes, crit = append(notes, fmt.Sprintf("Narzędzie ułatwień dostępu %s podmienione (Debugger = %s) - typowa tylna furtka", k, dbg)), true
		case redirectedRx.MatchString(k):
			notes, crit = append(notes, fmt.Sprintf("Uruchomienie %s jest przekierowane (Debugger = %s)", k, dbg)), true
		}
	}
	if regInt(`HKLM\SOFTWARE\Policies\Microsoft\Windows Defender`, "DisableAntiSpyware", 0) == 1 {
		notes = append(notes, "Microsoft Defender wyłączony zasadą (DisableAntiSpyware)")
	}
	if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager`, "BootExecute"); ok {
		var be []string
		switch x := v.(type) {
		case []string:
			be = x
		case string:
			be = []string{x}
		}
		if len(be) > 0 && !bootExecRx.MatchString(strings.TrimSpace(strings.Join(be, " "))) {
			notes = append(notes, "BootExecute ma niestandardową wartość: "+strings.Join(be, " "))
		}
	}
	if len(notes) == 0 {
		c.Add(F{Title: "Ślady modyfikacji systemu", Status: OK, Value: "Nie wykryto typowych podmian (Winlogon, AppInit, IFEO, BootExecute)"})
		return
	}
	st := Warn
	if crit {
		st = Crit
	}
	c.Add(F{Title: "Ślady modyfikacji systemu", Status: st, Value: fmtCount(len(notes), "podejrzana zmiana", "podejrzane zmiany", "podejrzanych zmian"), Details: strings.Join(notes, "\n"),
		Advice: "Takie wpisy w rejestrze są typowe dla złośliwego oprogramowania. Przeskanuj system (Defender offline, Malwarebytes, Autoruns) i przywróć domyślne wartości po upewnieniu się, że wpis nie pochodzi z legalnego programu."})
}

func checkFailedLogons(c *Ctx) {
	ev := c.events(win.EventFilter{Logs: []string{"Security"}, IDs: []int{4625}, Since: time.Now().Add(-24 * time.Hour)}, 3000, false)
	if len(ev) == 0 {
		c.Add(F{Title: "Nieudane logowania (24 h)", Status: OK, Value: "Brak nieudanych prób logowania"})
		return
	}
	type cnt struct {
		key string
		n   int
	}
	count := func(get func(e *win.Event) string) []cnt {
		idx := map[string]*cnt{}
		var out []*cnt
		for _, e := range ev {
			k := get(e)
			if k == "" {
				continue
			}
			if c := idx[strings.ToLower(k)]; c != nil {
				c.n++
			} else {
				c := &cnt{k, 1}
				idx[strings.ToLower(k)] = c
				out = append(out, c)
			}
		}
		psSort(out, func(a, b *cnt) int { return b.n - a.n })
		var r []cnt
		for i, c := range out {
			if i < 5 {
				r = append(r, *c)
			}
		}
		return r
	}
	fmtTop := func(l []cnt) string {
		var s []string
		for _, x := range l {
			s = append(s, fmt.Sprintf("%s ×%d", x.key, x.n))
		}
		return strings.Join(s, ", ")
	}
	ips := count(func(e *win.Event) string {
		if ip := e.Data["IpAddress"]; ip != "-" {
			return ip
		}
		return ""
	})
	users := count(func(e *win.Event) string { return e.Data["TargetUserName"] })
	d := []string{fmt.Sprintf("Nieudanych prób: %d", len(ev))}
	if len(ips) > 0 {
		d = append(d, "Główne źródła (IP): "+fmtTop(ips))
	}
	if len(users) > 0 {
		d = append(d, "Najczęściej atakowane konta: "+fmtTop(users))
	}
	st := Info
	if len(ev) >= 500 {
		st = Crit
	} else if len(ev) >= 50 {
		st = Warn
	}
	c.Add(F{Title: "Nieudane logowania (24 h)", Status: st, Value: fmtCount(len(ev), "próba", "próby", "prób") + " w ciągu 24 godzin", Details: strings.Join(d, "\n"),
		LastEvent: latestTime(ev), Private: true,
		Advice: "Duża liczba nieudanych logowań z obcych adresów oznacza atak siłowy (zwykle na RDP/SMB wystawione do internetu). Wyłącz ekspozycję usługi, ustaw silne hasła i rozważ blokadę konta."})
}
