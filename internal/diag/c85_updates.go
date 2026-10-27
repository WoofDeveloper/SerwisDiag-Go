package diag

import (
	"fmt"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Aktualizacje i licencja (checks\85-updates.ps1)

var updateChecks = []*Check{
	{ID: "up.history", Category: "updates", Title: "Historia i stan Windows Update", Level: "Quick", Run: checkWuHistory},
	{ID: "up.pending", Category: "updates", Title: "Oczekujący restart i zasady aktualizacji", Level: "Quick", Run: checkPendingReboot},
	{ID: "up.activation", Category: "updates", Title: "Aktywacja i licencja Windows", Level: "Quick", Run: checkActivation},
	{ID: "up.cbs", Category: "updates", Title: "Integralność magazynu składników Windows (DISM)", Level: "Quick", NeedsAdmin: true, Run: checkComponentStore},
	{ID: "up.pendingsearch", Category: "updates", Title: "Dostępne aktualizacje (wyszukiwanie online)", Level: "Full", NeedsInternet: true, Run: checkPendingUpdates},
	{ID: "up.dotnet", Category: "updates", Title: "Wersje .NET Framework", Level: "Quick", Run: checkDotNet},
}

var wuErrors = map[string]string{
	"0x80070002": "Brak wymaganego pliku - zwykle uszkodzona pamięć podręczna aktualizacji (SoftwareDistribution).",
	"0x80070003": "Nie znaleziono ścieżki - uszkodzone komponenty Windows Update.",
	"0x80070005": "Odmowa dostępu - blokada przez antywirus lub uszkodzone uprawnienia.",
	"0x8007000d": "Dane są nieprawidłowe - uszkodzony pobrany pakiet lub magazyn składników.",
	"0x80070070": "Brak miejsca na dysku.",
	"0x80070490": "Nie znaleziono elementu - uszkodzony magazyn składników (DISM /RestoreHealth).",
	"0x80070643": "Błąd instalacji MSI - uszkodzona instalacja pakietu (.NET / Defender).",
	"0x80070bc2": "Wymagany restart komputera przed dalszą instalacją.",
	"0x800705b4": "Przekroczono czas operacji - problem z siecią lub usługą.",
	"0x80073712": "Uszkodzony magazyn składników - uruchom DISM /Online /Cleanup-Image /RestoreHealth oraz sfc /scannow.",
	"0x800f081f": "Nie znaleziono plików źródłowych - uszkodzony magazyn składników lub brak dostępu do źródła.",
	"0x800f0831": "Brakuje poprzedniego pakietu aktualizacji - uszkodzony magazyn składników.",
	"0x800f0922": "Błąd CBS - za mało miejsca w partycji systemowej/odzyskiwania, aktywny VPN lub uszkodzone składniki.",
	"0x80240034": "Pobieranie aktualizacji nie powiodło się.",
	"0x80244022": "Serwer Windows Update zwrócił błąd HTTP 503 - spróbuj ponownie później.",
	"0x8024402c": "Brak połączenia z serwerem aktualizacji - problem z siecią, proxy, DNS lub zaporą.",
	"0x80248007": "Brakuje metadanych aktualizacji - zresetuj komponenty Windows Update.",
	"0x8024a105": "Usługa aktualizacji nie działa poprawnie - zresetuj komponenty Windows Update.",
	"0xc1900101": "Błąd sterownika podczas aktualizacji funkcji - odłącz urządzenia USB, zaktualizuj sterowniki (grafika, dysk).",
}

// fmtHr odpowiada Format-Hr: kod HRESULT jako 0x%08X.
func fmtHr(hr int64) string { return fmt.Sprintf("0x%08X", uint32(hr)) }

func checkWuHistory(c *Ctx) {
	hist := wuHistory(c)
	entries, errTxt := hist.Entries, hist.Error
	svcState, _ := win.ServiceState("wuauserv")
	startMode, hasMode := win.ServiceStartMode("wuauserv")
	if errTxt != "" {
		st := svcState
		if st == "" {
			st = "brak"
		}
		c.Add(F{Title: "Usługa Windows Update", Status: Warn, Value: "Nie można odczytać historii: " + limitText(errTxt, 120),
			Details: "Usługa wuauserv: " + st + ", tryb: " + startMode,
			Advice:  "Uruchom narzędzie do rozwiązywania problemów z Windows Update; sprawdź usługi wuauserv, bits, cryptsvc i uprawnienia."})
	}
	// Liczą się tylko poprawki systemu (lista QFE i wpisy KB dla Windows). Definicje Defendera, MSRT, sterowniki
	// i aplikacje ze Sklepu instalują się niezależnie i nie świadczą o łataniu systemu.
	last := lastOsPatch(c)
	var failedU []WuEntry
	limit60 := time.Now().AddDate(0, 0, -60)
	for _, e := range entries {
		if e.Result == 4 && !e.Date.Before(limit60) {
			failedU = append(failedU, e)
		}
	}
	psSort(failedU, func(a, b WuEntry) int { return b.Date.Compare(a.Date) })
	sup := supportInfo(c)
	noMorePatches := sup != nil && !sup.Result.Covered
	if last != nil {
		days := int(psRound(time.Since(last.Date).Hours() / 24))
		d := fmt.Sprintf("Ostatnia poprawka systemu: %s - %s", fmtDt(last.Date), limitText(last.Title, 110))
		if last.Source == "QFE" {
			d += "\nŹródło: lista zainstalowanych poprawek (data bez godziny)"
		} else {
			d += "\nŹródło: historia Windows Update"
		}
		if len(entries) > 0 {
			d += fmt.Sprintf("\nWpisów w historii Windows Update: %d", len(entries))
		}
		val := fmt.Sprintf("%s (%d dni temu)", fmtDate(last.Date), days)
		switch {
		case days > 45 && noMorePatches:
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: Info, Value: val, Details: d,
				Advice: "Ta wersja systemu nie otrzymuje już poprawek (zob. „Okres wsparcia systemu”) - brak nowych aktualizacji jest skutkiem końca wsparcia, a nie awarii Windows Update."})
		case days > 120:
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: Crit, Value: val, Details: d,
				Advice: "System nie był aktualizowany od miesięcy - zainstaluj wszystkie aktualizacje (a jeśli się nie instalują, zob. błędy poniżej)."})
		case days > 45:
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: Warn, Value: val, Details: d, Advice: "Sprawdź Windows Update - poprawki zabezpieczeń ukazują się co miesiąc."})
		default:
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: OK, Value: val, Details: d})
		}
	} else if errTxt == "" {
		if len(entries) == 0 {
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: Info, Value: "Historia aktualizacji jest pusta",
				Details: "Historia mogła zostać wyczyszczona lub system został niedawno zainstalowany.", Advice: "Sprawdź ręcznie w Ustawieniach, czy dostępne są aktualizacje."})
		} else {
			c.Add(F{Title: "Ostatnia aktualizacja systemu", Status: Warn, Value: "Brak zainstalowanych poprawek systemu w historii",
				Advice: "Sprawdź błędy Windows Update i uruchom narzędzie do rozwiązywania problemów."})
		}
	}
	if len(failedU) > 0 {
		var rows []Row
		for i, f := range failedU {
			if i >= 15 {
				break
			}
			code := fmtHr(f.Hr)
			rows = append(rows, row("Data", fmtDt(f.Date), "Aktualizacja", limitText(f.Title, 90), "Kod", code, "Znaczenie", wuErrors[strings.ToLower(code)]))
		}
		c.AddTable(T{Name: "Nieudane aktualizacje (60 dni)", Rows: rows})
		codes := groupBy(failedU, func(e WuEntry) string { return fmtHr(e.Hr) })
		sortByCountDesc(codes)
		first := codes[0].Name
		var lastF time.Time
		for _, f := range failedU {
			if f.Date.After(lastF) {
				lastF = f.Date
			}
		}
		c.Add(F{Title: "Nieudane aktualizacje", Status: Warn, Value: fmt.Sprintf("%s w ciągu 60 dni; najczęstszy kod %s", fmtCount(len(failedU), "niepowodzenie", "niepowodzenia", "niepowodzeń"), first),
			Details: wuErrors[strings.ToLower(first)], LastEvent: lastF,
			Advice: "Uruchom DISM /Online /Cleanup-Image /RestoreHealth i sfc /scannow, a następnie zresetuj komponenty Windows Update lub zainstaluj aktualizację ręcznie z Katalogu Microsoft Update."})
	} else if errTxt == "" && len(entries) > 0 {
		c.Add(F{Title: "Nieudane aktualizacje", Status: OK, Value: "Brak niepowodzeń w ciągu 60 dni"})
	}
	if hasMode && startMode == "Disabled" {
		c.Add(F{Title: "Usługa Windows Update (wuauserv)", Status: Warn, Value: "Wyłączona (tryb uruchamiania: Disabled)",
			Advice: "Usługa Windows Update jest wyłączona - przywróć tryb „Ręczny” lub „Automatyczny (opóźniony)”. Często wyłączają ją „optymalizatory”."})
	}
}

func checkPendingReboot(c *Ctx) {
	var reasons []string
	if win.RegKeyExists(`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		reasons = append(reasons, "oczekujące zmiany komponentów systemu (CBS)")
	}
	if win.RegKeyExists(`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		reasons = append(reasons, "Windows Update wymaga restartu")
	}
	pfrOnly := false
	if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager`, "PendingFileRenameOperations"); ok && win.AnyBool(v) {
		reasons = append(reasons, "oczekujące operacje na plikach (PendingFileRenameOperations)")
		if len(reasons) == 1 {
			pfrOnly = true
		}
	}
	a := regStr(`HKLM\SYSTEM\CurrentControlSet\Control\ComputerName\ActiveComputerName`, "ComputerName", "")
	b := regStr(`HKLM\SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`, "ComputerName", "")
	if a != "" && b != "" && !eqi(a, b) {
		reasons = append(reasons, "zmieniono nazwę komputera")
		pfrOnly = false
	}
	if len(reasons) > 0 {
		// sam wpis PendingFileRenameOperations bywa pozostałością po instalatorach - traktujemy go jako informację
		st := Warn
		if pfrOnly {
			st = Info
		}
		c.Add(F{Title: "Oczekujący restart", Status: st, Value: "Wymagane ponowne uruchomienie: " + strings.Join(reasons, "; "),
			Advice: "Uruchom ponownie komputer (opcja „Uruchom ponownie”, a nie „Zamknij”) przed dalszą diagnostyką i instalacją aktualizacji."})
	} else {
		c.Add(F{Title: "Oczekujący restart", Status: OK, Value: "Brak oczekujących restartów"})
	}

	var notes []string
	st := OK
	const au = `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
	if regInt(au+`\AU`, "NoAutoUpdate", 0) == 1 {
		notes, st = append(notes, "Zasady wyłączają automatyczne aktualizacje (NoAutoUpdate)"), Warn
	}
	if regInt(au, "DisableWindowsUpdateAccess", 0) == 1 {
		notes, st = append(notes, "Zasady blokują dostęp do Windows Update"), Warn
	}
	if wsus := regStr(au, "WUServer", ""); wsus != "" {
		notes = append(notes, "Aktualizacje pobierane z serwera WSUS: "+wsus+" (przy braku dostępu do serwera aktualizacje nie będą działać)")
		if st == OK {
			st = Info
		}
	}
	const ux = `HKLM\SOFTWARE\Microsoft\WindowsUpdate\UX\Settings`
	for _, n := range []string{"PauseUpdatesExpiryTime", "PauseFeatureUpdatesEndTime", "PauseQualityUpdatesEndTime"} {
		if v := regStr(ux, n, ""); v != "" {
			if t, ok := parseLooseDate(v); ok && t.After(time.Now()) {
				notes = append(notes, "Aktualizacje wstrzymane do "+fmtDate(t))
				if st == OK {
					st = Info
				}
				break
			}
		}
	}
	if len(notes) > 0 {
		c.Add(F{Title: "Zasady Windows Update", Status: st, Value: strings.Join(notes, "; "),
			Advice: "Sprawdź, czy ograniczenia są zamierzone. Zasady wyłączające aktualizacje (np. po „optymalizacji” systemu) narażają komputer na luki."})
	}
}

// parseLooseDate: daty z rejestru w różnych formatach (jak [datetime]::TryParse).
func parseLooseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", "02.01.2006 15:04:05", "1/2/2006 3:04:05 PM", "1/2/2006"} {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t.Local(), true
		}
	}
	return time.Time{}, false
}

var (
	gvlkRx = rx(`GVLK`)
	kmsRx  = rx(`VOLUME_KMSCLIENT`)
	makRx  = rx(`MAK`)
	vmakRx = rx(`VOLUME_MAK`)
)

func checkActivation(c *Ctx) {
	lic := c.Cim(Q{Class: "SoftwareLicensingProduct", Props: []string{"Name", "Description", "LicenseStatus", "ProductKeyChannel", "KeyManagementServiceMachine", "PartialProductKey"},
		Where: "PartialProductKey IS NOT NULL AND ApplicationId='55c92734-d682-4d71-983e-d6ec3f16059f'"})
	svc := c.cim1("SoftwareLicensingService", "OA3xOriginalProductKey")
	cs := csObj(c)
	if len(lic) == 0 {
		c.Add(F{Title: "Aktywacja Windows", Status: Warn, Value: "Nie wykryto zainstalowanego klucza produktu", Advice: "Sprawdź stan aktywacji (Ustawienia > System > Aktywacja)."})
	} else {
		l := lic[0]
		statusMap := map[int64]string{0: "Nieaktywowany", 1: "Aktywowany", 2: "Okres prolongaty (OOB)", 3: "Okres prolongaty (OOT)", 4: "Niezgodny z oryginałem (okres prolongaty)", 5: "Powiadomienie o braku aktywacji", 6: "Rozszerzony okres prolongaty"}
		ls := l.Int("LicenseStatus")
		channel, kms := l.Str("ProductKeyChannel"), l.Str("KeyManagementServiceMachine")
		d := []string{"Produkt: " + l.Str("Name"), "Stan: " + statusMap[ls]}
		if channel != "" {
			d = append(d, "Kanał licencji: "+channel)
		}
		if l.Str("Description") != "" {
			d = append(d, "Typ: "+l.Str("Description"))
		}
		if kms != "" {
			d = append(d, "Serwer KMS: "+kms)
		}
		d = append(d, "Końcówka klucza: "+l.Str("PartialProductKey"))
		det := strings.Join(d, "\n")
		if ls == 1 {
			domain := cs != nil && cs.Bool("PartOfDomain")
			isKms := gvlkRx.MatchString(channel) || kmsRx.MatchString(l.Str("Description"))
			isMak := makRx.MatchString(channel) || vmakRx.MatchString(l.Str("Description"))
			// Wątpliwości co do legalności licencji są tylko w raporcie technicznym - technik wyjaśnia je z klientem
			// osobiście; raport dla klienta podaje jedynie, że system jest aktywowany.
			switch {
			case isKms && !domain:
				v := "Aktywowany zbiorczo (KMS)"
				if kms != "" {
					v += " przez " + kms
				}
				c.Add(F{Title: "Aktywacja Windows", Status: Warn, Value: v, Details: det,
					Advice:         "Aktywacja zbiorcza (KMS) na komputerze spoza domeny jest częstym objawem nielegalnej aktywacji (zewnętrzne serwery KMS, aktywatory). Zweryfikuj legalność licencji z klientem.",
					CustomerStatus: Info, CustomerValue: "Aktywowany"})
			case isMak && !domain:
				c.Add(F{Title: "Aktywacja Windows", Status: Info, Value: "Aktywowany (licencja zbiorcza MAK)", Details: det,
					Advice:         "Licencja zbiorcza (MAK) pochodzi zwykle z umowy firmowej lub edukacyjnej. Upewnij się, że klient ma prawo jej używać.",
					CustomerStatus: Info, CustomerValue: "Aktywowany (licencja zbiorcza)"})
			default:
				v := "Aktywowany"
				if channel != "" {
					v += " (kanał: " + channel + ")"
				}
				c.Add(F{Title: "Aktywacja Windows", Status: OK, Value: v, Details: det})
			}
		} else {
			c.Add(F{Title: "Aktywacja Windows", Status: Crit, Value: statusMap[ls], Details: det,
				Advice: "Windows nie jest aktywowany: ogranicza personalizację i może wyświetlać komunikaty. Aktywuj kluczem cyfrowym/OEM z BIOS lub zakup licencję."})
		}
	}
	if svc != nil && svc.Str("OA3xOriginalProductKey") != "" {
		c.Add(F{Title: "Klucz OEM zapisany w BIOS/UEFI", Status: Info, Value: svc.Str("OA3xOriginalProductKey"), Private: true,
			Details: "Klucz fabryczny zapisany w firmware płyty - pozwala ponownie aktywować Windows po reinstalacji bez wpisywania klucza."})
	}
}

func checkComponentStore(c *Ctx) {
	s, err := win.ImageHealth(false)
	if err != nil {
		c.Log("WARN", "DISM CheckHealth: "+err.Error())
		c.Add(F{Title: "Magazyn składników (DISM CheckHealth)", Status: Skip, Value: "Nie udało się odczytać stanu (DISM niedostępny)."})
		return
	}
	switch s {
	case "Healthy":
		c.Add(F{Title: "Magazyn składników (DISM CheckHealth)", Status: OK, Value: "Zdrowy - brak wykrytych uszkodzeń"})
	case "Repairable":
		c.Add(F{Title: "Magazyn składników (DISM CheckHealth)", Status: Warn, Value: "Uszkodzony, ale możliwy do naprawy",
			Advice: "Uruchom jako administrator: DISM /Online /Cleanup-Image /RestoreHealth, a następnie sfc /scannow, po czym uruchom ponownie komputer."})
	default:
		c.Add(F{Title: "Magazyn składników (DISM CheckHealth)", Status: Crit, Value: "Stan: " + s,
			Advice: "Magazyn składników jest uszkodzony i nie można go naprawić online. Użyj naprawy za pomocą źródła (DISM /RestoreHealth /Source:...) lub zainstaluj system z zachowaniem plików."})
	}
}

type pendingUpdate struct {
	title, category string
	sizeMB          float64
	definition      bool
}

// pendingUpdateVerdict odpowiada Get-SdPendingUpdateVerdict: same definicje antywirusa nie są problemem.
func pendingUpdateVerdict(rows []pendingUpdate) (Status, string, string) {
	defN, other := 0, 0
	for _, r := range rows {
		if r.definition {
			defN++
		} else {
			other++
		}
	}
	defTxt := ""
	if defN > 0 {
		defTxt = fmt.Sprintf("definicje antywirusa Windows instaluje sam (oczekuje: %d)", defN)
	}
	if other == 0 {
		v := "Brak oczekujących aktualizacji systemu"
		if defTxt != "" {
			v += "; " + defTxt
		}
		return OK, v, ""
	}
	v := fmtCount(other, "aktualizacja oczekuje", "aktualizacje oczekują", "aktualizacji oczekuje")
	if defTxt != "" {
		v += "; " + defTxt
	}
	return Warn, v, "Zainstaluj oczekujące aktualizacje (po wykonaniu kopii danych), a następnie uruchom ponownie komputer."
}

func checkPendingUpdates(c *Ctx) {
	type result struct {
		rows []pendingUpdate
		err  string
	}
	done := make(chan result, 1)
	go win.WithCOM(func() {
		var r result
		defer func() { done <- r }()
		s, err := win.CreateObject("Microsoft.Update.Session")
		if err != nil {
			r.err = err.Error()
			return
		}
		defer s.Release()
		srch, err := s.Obj("CreateUpdateSearcher")
		if err != nil {
			r.err = err.Error()
			return
		}
		defer srch.Release()
		// sterowniki wyszukuje osobny test (kategoria Sterowniki) - tu tylko oprogramowanie
		res, err := srch.Obj("Search", "IsInstalled=0 and IsHidden=0 and Type='Software'")
		if err != nil {
			r.err = err.Error()
			return
		}
		defer res.Release()
		ups, err := res.Obj("Updates")
		if err != nil {
			r.err = err.Error()
			return
		}
		defer ups.Release()
		_ = ups.Items(func(u *win.Dispatch) {
			p := pendingUpdate{title: u.Str("Title"), sizeMB: psRound(u.Float("MaxDownloadSize") / (1 << 20))}
			if cats, err := u.Obj("Categories"); err == nil {
				_ = cats.Items(func(cat *win.Dispatch) {
					if p.category == "" {
						p.category = cat.Str("Name")
					}
					// klasyfikacja „Definition Updates” - stały identyfikator Windows Update, niezależny od języka
					if eqi(cat.Str("CategoryID"), "e0789628-ce08-4437-be74-2495b842f43b") {
						p.definition = true
					}
				})
				cats.Release()
			}
			r.rows = append(r.rows, p)
		})
	})
	var r result
	select {
	case r = <-done:
	case <-time.After(120 * time.Second):
		c.Add(F{Title: "Dostępne aktualizacje", Status: Info, Value: "Wyszukiwanie przerwane po 120 s (brak sieci lub bardzo wolne połączenie)"})
		return
	}
	if r.err != "" {
		c.Add(F{Title: "Dostępne aktualizacje", Status: Info, Value: "Nie udało się wyszukać: " + limitText(r.err, 120),
			Advice: "Brak połączenia z serwerem aktualizacji lub uszkodzone komponenty Windows Update."})
		return
	}
	if len(r.rows) == 0 {
		c.Add(F{Title: "Dostępne aktualizacje", Status: OK, Value: "Brak oczekujących aktualizacji"})
		return
	}
	var rows []Row
	for _, p := range r.rows {
		rows = append(rows, row("Aktualizacja", p.title, "Kategoria", p.category, "Rozmiar (MB)", fmtG(p.sizeMB)))
	}
	c.AddTable(T{Name: "Dostępne aktualizacje", Rows: rows})
	st, val, adv := pendingUpdateVerdict(r.rows)
	c.Add(F{Title: "Dostępne aktualizacje", Status: st, Value: val, Details: "Lista w tabeli „Dostępne aktualizacje”.", Advice: adv})
}

func checkDotNet(c *Ctx) {
	rel, ok := win.RegValue(`HKLM\SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full`, "Release")
	versions := []struct {
		release int64
		ver     string
	}{{533320, "4.8.1"}, {528040, "4.8"}, {461808, "4.7.2"}, {461308, "4.7.1"}, {460798, "4.7"}, {394802, "4.6.2"}, {394254, "4.6.1"}, {393295, "4.6"}, {379893, "4.5.2"}, {378675, "4.5.1"}, {378389, "4.5"}}
	ver := ""
	if ok {
		for _, v := range versions {
			if win.AnyInt(rel) >= v.release {
				ver = v.ver
				break
			}
		}
	}
	if ver == "" {
		return
	}
	net35 := regInt(`HKLM\SOFTWARE\Microsoft\NET Framework Setup\NDP\v3.5`, "Install", 0) == 1
	v := ".NET Framework " + ver
	if net35 {
		v += "; włączony .NET 3.5"
	} else {
		v += "; .NET 3.5 wyłączony"
	}
	c.Add(F{Title: ".NET Framework", Status: Info, Value: v, Details: "Starsze programy wymagają funkcji .NET Framework 3.5 (włączanie: Funkcje systemu Windows)."})
}
