package diag

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"serwisdiag/internal/win"
)

// Katalog napraw (Register-Fix w lib\Fixes.ps1) - te same warunki, teksty i kolejność.

var (
	noRestorePointsRx = rx(`Brak punktów`)
	definitionsRx     = rx(`Definicje`)
	disabledValueRx   = rx(`Wyłączony`)
	ssdRx             = rx(`SSD`)
	noneValueRx       = rx(`^Brak`)
	defenderNameRx    = rx(`Defender`)
	windowsNameRx     = rx(`Windows`)
)

// productOn: stan produktu z Centrum zabezpieczeń (productState) - bity 12-15 = 1: włączony.
func productOn(o win.WmiObject) bool { return (uint32(o.Int("productState"))>>12)&0xF == 1 }

func partOfDomain(c *Ctx) bool {
	cs := csObj(c)
	return cs != nil && cs.Bool("PartOfDomain")
}

// regNameVal - wartość rejestru do usunięcia albo zmiany (Data wielu napraw).
type regNameVal struct{ Path, Name string }

var fixCatalog = []*Fix{
	// ---------------------------------------------------------------- system
	{ID: "fx.restoreon", Group: "System", Order: 5, NeedsAdmin: true, Covers: []string{"sys.restore"},
		Title:       "Włącz ochronę systemu (punkty przywracania)",
		Description: "Włącza tworzenie punktów przywracania na dysku systemowym - pozwala cofnąć zmiany sterowników i ustawień (także te napraw).",
		Detect: func(fc *FixContext) []FixItem {
			if _, ok := fc.first("sys.restore", "", func(f Finding) bool { return noRestorePointsRx.MatchString(f.Value) }); !ok {
				return nil
			}
			if regInt(`HKLM\SOFTWARE\Policies\Microsoft\Windows NT\SystemRestore`, "DisableSR", 0) == 1 {
				return nil
			}
			return []FixItem{{Problem: "Brak punktów przywracania - ochrona systemu jest prawdopodobnie wyłączona."}}
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := wmiCall(`root\default`, "SystemRestore", "Enable", map[string]any{"Drive": sysDrive() + `\`, "WaitTillEnabled": true}); err != nil {
				return FixOutcome{}, err
			}
			r.Log("Ochrona systemu włączona dla " + sysDrive())
			return done("Ochrona systemu włączona.")
		}},

	{ID: "fx.wmi", Group: "System", Order: 10, NeedsAdmin: true, Risk: "medium", Covers: []string{"sys.wmi"},
		Title:       "Napraw repozytorium WMI",
		Description: "winmgmt /salvagerepository - sprawdza spójność repozytorium WMI i odbudowuje je, gdy jest uszkodzone.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.problem("sys.wmi", "") {
				return []FixItem{{Problem: "WMI nie odpowiadało albo odpowiadało bardzo wolno podczas diagnostyki."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			res := runNative(15*time.Minute, sysPath(`System32\wbem\winmgmt.exe`), "/salvagerepository")
			r.Log(fmt.Sprintf("winmgmt /salvagerepository: kod %d", res.ExitCode))
			if res.ExitCode != 0 {
				return FixOutcome{}, fmt.Errorf("winmgmt zakończył się kodem %d: %s", res.ExitCode, lastLine(res.Out+res.Err))
			}
			if t := lastLine(res.Out); t != "" {
				return done(t)
			}
			return done("Repozytorium WMI sprawdzone i naprawione.")
		}},

	{ID: "fx.dism", Group: "System", Order: 20, NeedsAdmin: true, NeedsInternet: true, Long: true, Covers: []string{"up.cbs", "tt.dism"},
		Title:       "Napraw magazyn składników Windows (DISM)",
		Description: "DISM /Online /Cleanup-Image /RestoreHealth - pobiera z Windows Update i podmienia uszkodzone składniki systemu (15-30 min). Bez internetu użyj „DISM RestoreHealth z płyty/ISO” w Narzędziach serwisowych.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.problem("up.cbs", "") || fc.problem("tt.dism", "") {
				return []FixItem{{Problem: "Diagnostyka DISM wykryła uszkodzenia magazynu składników Windows."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			res := runNative(time.Hour, sysPath(`System32\Dism.exe`), "/Online", "/Cleanup-Image", "/RestoreHealth")
			code := fmtHr(int64(res.ExitCode))
			r.Log("DISM /RestoreHealth: kod " + code)
			if res.TimedOut {
				return FixOutcome{}, errors.New("DISM nie zakończył pracy w ciągu 60 minut.")
			}
			if res.ExitCode != 0 {
				why, ok := wuErrors[strings.ToLower(code)]
				if !ok {
					why = lastLine(res.Out)
				}
				return FixOutcome{}, fmt.Errorf("DISM zakończył się kodem %s: %s", code, why)
			}
			return done("Magazyn składników naprawiony.")
		}},

	{ID: "fx.sfc", Group: "System", Order: 21, NeedsAdmin: true, Long: true, Covers: []string{"up.cbs"},
		Title:       "Sprawdź i napraw pliki systemowe (SFC)",
		Description: "sfc /scannow - porównuje pliki systemowe z wzorcem i przywraca uszkodzone (10-20 min). Wykonywane po naprawie DISM.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.problem("up.cbs", "") || fc.problem("tt.dism", "") {
				return []FixItem{{Problem: "Uszkodzony magazyn składników - po jego naprawie trzeba sprawdzić pliki systemowe."}}
			}
			if fc.problem("ev.crash", "^Niebieskie ekrany") || fc.problem("ev.apps", "") {
				return []FixItem{{Problem: "Awarie systemu lub aplikacji - jedną z przyczyn bywają uszkodzone pliki systemowe.", Default: boolp(false)}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			res := win.Run(time.Hour, sysPath(`System32\sfc.exe`), "/scannow")
			if res.TimedOut {
				return FixOutcome{}, errors.New("SFC nie zakończył pracy w ciągu 60 minut.")
			}
			t := lastLine(utf16Text(res.Stdout))
			r.Log("sfc /scannow: " + t)
			if t != "" {
				return done(t)
			}
			return done(fmt.Sprintf("Sprawdzanie zakończone (kod %d).", res.ExitCode))
		}},

	// ---------------------------------------------------------------- Windows Update
	{ID: "fx.wusvc", Group: "Windows Update", Order: 30, NeedsAdmin: true, Covers: []string{`up.history|^Usługa Windows Update \(wuauserv\)$`},
		Title:       "Włącz usługę Windows Update",
		Description: "Przywraca uruchamianie usługi wuauserv na żądanie (tryb „Ręczny”, jak domyślnie w Windows 10/11).",
		Detect: func(fc *FixContext) []FixItem {
			if m, ok := win.ServiceStartMode("wuauserv"); ok && m == "Disabled" {
				return []FixItem{{Problem: "Usługa Windows Update jest wyłączona - system nie otrzymuje poprawek."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := r.SetServiceStart("wuauserv", "Manual"); err != nil {
				return FixOutcome{}, err
			}
			return done("Usługa Windows Update będzie uruchamiana na żądanie.")
		}},

	{ID: "fx.wupolicy", Group: "Windows Update", Order: 31, NeedsAdmin: true, Risk: "medium", Covers: []string{`up.pending|^Zasady Windows Update$`},
		Title:       "Usuń zasady blokujące Windows Update",
		Description: "Usuwa z rejestru zasady NoAutoUpdate / DisableWindowsUpdateAccess (często ustawiane przez „optymalizatory”). Na komputerach w domenie nie jest proponowane.",
		Detect: func(fc *FixContext) []FixItem {
			if partOfDomain(fc.c) {
				return nil
			}
			au := `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
			var vals []regNameVal
			if regInt(au+`\AU`, "NoAutoUpdate", 0) == 1 {
				vals = append(vals, regNameVal{au + `\AU`, "NoAutoUpdate"})
			}
			if regInt(au, "DisableWindowsUpdateAccess", 0) == 1 {
				vals = append(vals, regNameVal{au, "DisableWindowsUpdateAccess"})
			}
			if len(vals) == 0 {
				return nil
			}
			var names []string
			for _, v := range vals {
				names = append(names, v.Name)
			}
			return []FixItem{{Problem: "Zasady wyłączają aktualizacje: " + strings.Join(names, ", ") + ".", Data: vals}}
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			for _, v := range data.([]regNameVal) {
				if err := r.RemoveReg(v.Path, v.Name); err != nil {
					return FixOutcome{}, err
				}
			}
			return done("Usunięto zasady blokujące aktualizacje.")
		}},

	{ID: "fx.wureset", Group: "Windows Update", Order: 32, NeedsAdmin: true, Covers: []string{`up.history|^Nieudane aktualizacje$`, `up.history|^Usługa Windows Update$`},
		Title:       "Zresetuj składniki Windows Update",
		Description: "Zatrzymuje usługi aktualizacji, zmienia nazwy folderów SoftwareDistribution i catroot2 na .old (pamięć podręczna pobierze się od nowa) i uruchamia usługi ponownie. Historia aktualizacji w Ustawieniach może się wyczyścić.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.problem("up.history", "^Nieudane aktualizacje$") || fc.problem("up.history", "^Usługa Windows Update$") {
				return []FixItem{{Problem: "Aktualizacje kończą się błędami albo usługa Windows Update nie odpowiada."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			svcs := []string{"wuauserv", "bits", "cryptsvc"}
			defer func() {
				for _, s := range svcs {
					_ = win.StartService(s, 30*time.Second)
				}
			}()
			for _, s := range svcs {
				_ = win.StopService(s, true, 30*time.Second)
			}
			for _, p := range [][2]string{{sysPath("SoftwareDistribution"), "SoftwareDistribution.old"}, {sysPath(`System32\catroot2`), "catroot2.old"}} {
				old := filepath.Join(filepath.Dir(p[0]), p[1])
				if _, err := os.Stat(old); err == nil {
					_ = os.RemoveAll(old)
				}
				if _, err := os.Stat(p[0]); err == nil {
					if err := os.Rename(p[0], old); err != nil {
						return FixOutcome{}, fmt.Errorf("zmiana nazwy %s: %w", p[0], err)
					}
					r.Log(p[0] + " -> " + p[1])
				}
			}
			return done("Składniki Windows Update zresetowane.")
		}},

	{ID: "fx.drivers", Group: "Sterowniki", Order: 35, NeedsAdmin: true, NeedsInternet: true, Long: true, Covers: []string{"dr.wu"},
		Title:       "Zainstaluj sterowniki z Windows Update",
		Description: "Pobiera i instaluje zalecane sterowniki oraz te, które naprawiają urządzenia z błędami (opcjonalne wybierzesz w Narzędziach > Sterowniki z Windows Update).",
		Detect: func(fc *FixContext) []FixItem {
			f, ok := fc.first("dr.wu", "", func(f Finding) bool { return f.Status == Warn || f.Status == Info })
			if !ok {
				return nil
			}
			return []FixItem{{Problem: f.Value, Default: boolp(f.Status == Warn)}}
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			r.say("Wyszukiwanie sterowników w Windows Update...")
			found := WuDriverSearch()
			defer found.Release()
			if found.Error != "" {
				return FixOutcome{}, errors.New(found.Error)
			}
			sel := SelectDriversToInstall(found.Items)
			if len(sel) == 0 {
				return done("Brak zalecanych sterowników do zainstalowania.")
			}
			var ids []string
			for _, d := range sel {
				ids = append(ids, d.UpdateID)
			}
			ins := found.Install(ids, r.say)
			if ins.Error != "" {
				return FixOutcome{}, errors.New(ins.Error)
			}
			ok := 0
			for _, x := range ins.Results {
				r.Log(fmt.Sprintf("%s: %s", x.Title, WuResultText(x.ResultCode)))
				if x.ResultCode == 2 {
					ok++
				}
			}
			res := "ok"
			if ok != len(ins.Results) {
				res = "warn"
			}
			return FixOutcome{Result: res, Message: fmt.Sprintf("Zainstalowano %d z %d.", ok, len(ins.Results)), Reboot: boolp(ins.RebootRequired)}, nil
		}},

	// ---------------------------------------------------------------- bezpieczeństwo
	{ID: "fx.defender", Group: "Bezpieczeństwo", Order: 40, NeedsAdmin: true, Covers: []string{"se.av|^Microsoft Defender$", "se.av|^Program antywirusowy$"},
		Title:       "Włącz ochronę Microsoft Defender",
		Description: "Usuwa zasady wyłączające Defendera i włącza ochronę w czasie rzeczywistym. Nie jest proponowane, gdy działa inny antywirus.",
		Detect: func(fc *FixContext) []FixItem {
			for _, av := range fc.c.Cim(Q{NS: `root\SecurityCenter2`, Class: "AntivirusProduct", Props: []string{"displayName", "productState"}}) {
				if !defenderNameRx.MatchString(av.Str("displayName")) && productOn(av) {
					return nil // działa inny antywirus
				}
			}
			pol := `HKLM\SOFTWARE\Policies\Microsoft\Windows Defender`
			var vals []regNameVal
			if regInt(pol, "DisableAntiSpyware", 0) == 1 {
				vals = append(vals, regNameVal{pol, "DisableAntiSpyware"})
			}
			if regInt(pol+`\Real-Time Protection`, "DisableRealtimeMonitoring", 0) == 1 {
				vals = append(vals, regNameVal{pol + `\Real-Time Protection`, "DisableRealtimeMonitoring"})
			}
			rtOff := false
			if mp := fc.c.Cim(Q{NS: `root\Microsoft\Windows\Defender`, Class: "MSFT_MpComputerStatus", Props: []string{"RealTimeProtectionEnabled"}}); len(mp) > 0 {
				if v, ok := mp[0].Raw("RealTimeProtectionEnabled").(bool); ok && !v {
					rtOff = true
				}
			}
			if len(vals) == 0 && !rtOff {
				return nil
			}
			how := ""
			if len(vals) > 0 {
				var n []string
				for _, v := range vals {
					n = append(n, v.Name)
				}
				how = " (zasada w rejestrze: " + strings.Join(n, ", ") + ")"
			}
			return []FixItem{{Problem: "Ochrona Microsoft Defender jest wyłączona" + how + ".", Data: vals}}
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			vals, _ := data.([]regNameVal)
			for _, v := range vals {
				if err := r.RemoveReg(v.Path, v.Name); err != nil {
					return FixOutcome{}, err
				}
			}
			if wmiCall(`root\Microsoft\Windows\Defender`, "MSFT_MpPreference", "Set", map[string]any{"DisableRealtimeMonitoring": false}) == nil {
				r.Log("Defender: ochrona w czasie rzeczywistym -> włączona")
			}
			time.Sleep(2 * time.Second)
			if mp, err := win.WmiQuery(`root\Microsoft\Windows\Defender`, "SELECT RealTimeProtectionEnabled FROM MSFT_MpComputerStatus", []string{"RealTimeProtectionEnabled"}, 30*time.Second); err == nil && len(mp) > 0 {
				if v, ok := mp[0].Raw("RealTimeProtectionEnabled").(bool); ok && !v {
					return FixOutcome{}, errors.New("Ochrona nadal jest wyłączona - blokuje to ochrona przed manipulacją (Tamper Protection) albo złośliwe oprogramowanie. Włącz ją ręcznie w Zabezpieczeniach Windows i przeskanuj komputer.")
				}
			}
			return done("Ochrona w czasie rzeczywistym włączona.")
		}},

	{ID: "fx.defsig", Group: "Bezpieczeństwo", Order: 41, NeedsAdmin: true, NeedsInternet: true, Covers: []string{"se.av|^Microsoft Defender$"},
		Title:       "Zaktualizuj definicje Microsoft Defender",
		Description: "Pobiera najnowsze definicje wirusów (Update-MpSignature).",
		Detect: func(fc *FixContext) []FixItem {
			f, ok := fc.first("se.av", "^Microsoft Defender$", func(f Finding) bool { return isProblem(f) && definitionsRx.MatchString(f.Value) })
			if !ok {
				return nil
			}
			return []FixItem{{Problem: f.Value}}
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := wmiCall(`root\Microsoft\Windows\Defender`, "MSFT_MpSignature", "Update", nil); err != nil {
				return FixOutcome{}, err
			}
			r.Log("Update-MpSignature: wykonano")
			age := ""
			if mp, err := win.WmiQuery(`root\Microsoft\Windows\Defender`, "SELECT AntivirusSignatureAge FROM MSFT_MpComputerStatus", []string{"AntivirusSignatureAge"}, 30*time.Second); err == nil && len(mp) > 0 && mp[0].Has("AntivirusSignatureAge") {
				age = fmt.Sprintf(" (wiek: %d dn.)", mp[0].Int("AntivirusSignatureAge"))
			}
			return done("Definicje zaktualizowane" + age + ".")
		}},

	{ID: "fx.firewall", Group: "Bezpieczeństwo", Order: 42, NeedsAdmin: true, Covers: []string{"se.firewall"},
		Title:       "Włącz zaporę systemu Windows",
		Description: "Włącza zaporę w profilach, w których jest wyłączona (i usuwa zasady, które ją wyłączają). Nie jest proponowane, gdy działa zapora innego producenta albo wyłącza ją zasada domeny.",
		Detect: func(fc *FixContext) []FixItem {
			for _, fw := range fc.c.Cim(Q{NS: `root\SecurityCenter2`, Class: "FirewallProduct", Props: []string{"displayName", "productState"}}) {
				if !windowsNameRx.MatchString(fw.Str("displayName")) && productOn(fw) {
					return nil // działa zapora innego producenta
				}
			}
			domain := partOfDomain(fc.c)
			base := `HKLM\SYSTEM\CurrentControlSet\Services\SharedAccess\Parameters\FirewallPolicy`
			pol := `HKLM\SOFTWARE\Policies\Microsoft\WindowsFirewall`
			var off []fwProfile
			for _, p := range [][3]string{{"DomainProfile", "Domain", "domenowy"}, {"StandardProfile", "Private", "prywatny"}, {"PublicProfile", "Public", "publiczny"}} {
				v, hasV := win.RegValue(base+`\`+p[0], "EnableFirewall")
				pv, hasPv := win.RegValue(pol+`\`+p[0], "EnableFirewall")
				eff, has := v, hasV
				if hasPv {
					eff, has = pv, true
				}
				if !has || win.AnyInt(eff) != 0 {
					continue
				}
				if hasPv && domain {
					continue
				}
				fp := fwProfile{Profile: p[1], Label: p[2]}
				if hasPv {
					fp.PolicyPath = pol + `\` + p[0]
				}
				off = append(off, fp)
			}
			if len(off) == 0 {
				return nil
			}
			var labels []string
			for _, o := range off {
				labels = append(labels, o.Label)
			}
			return []FixItem{{Problem: "Zapora jest wyłączona w profilach: " + strings.Join(labels, ", ") + ".", Data: off}}
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			off := data.([]fwProfile)
			for _, o := range off {
				if o.PolicyPath != "" {
					if err := r.RemoveReg(o.PolicyPath, "EnableFirewall"); err != nil {
						return FixOutcome{}, err
					}
				}
			}
			var names, labels []string
			for _, o := range off {
				names = append(names, o.Profile)
				labels = append(labels, o.Label)
			}
			if err := setFirewallProfiles(names, true); err != nil {
				return FixOutcome{}, err
			}
			r.Log("Zapora: włączono profile " + strings.Join(names, ", "))
			r.addUndo(UndoRecord{Kind: "firewall", Profiles: names})
			return done("Zapora włączona (" + strings.Join(labels, ", ") + ").")
		}},

	{ID: "fx.uac", Group: "Bezpieczeństwo", Order: 43, NeedsAdmin: true, Covers: []string{"se.uac"},
		Title:       "Włącz Kontrolę konta użytkownika (UAC)",
		Description: "Przywraca domyślny poziom UAC (powiadomienia o zmianach wprowadzanych przez programy). Po całkowitym wyłączeniu UAC potrzebny jest restart.",
		Detect: func(fc *FixContext) []FixItem {
			k := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
			if regInt(k, "EnableLUA", 1) == 0 {
				return []FixItem{{Problem: "UAC jest całkowicie wyłączone."}}
			}
			if regInt(k, "ConsentPromptBehaviorAdmin", 5) == 0 {
				return []FixItem{{Problem: "UAC nie pyta administratora o zgodę na zmiany."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			k := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
			wasOff := regInt(k, "EnableLUA", 1) == 0
			for _, v := range []struct {
				name string
				val  int
			}{{"EnableLUA", 1}, {"ConsentPromptBehaviorAdmin", 5}, {"PromptOnSecureDesktop", 1}} {
				if err := r.SetReg(k, v.name, v.val, "DWord"); err != nil {
					return FixOutcome{}, err
				}
			}
			return FixOutcome{Message: "Przywrócono domyślny poziom UAC.", Reboot: boolp(wasOff)}, nil
		}},

	{ID: "fx.smb1", Group: "Bezpieczeństwo", Order: 44, NeedsAdmin: true, DefaultOff: true, Risk: "medium",
		Title:       "Wyłącz przestarzały protokół SMB1",
		Description: "Wyłącza serwer SMB1 (podatność wykorzystana przez WannaCry). Uwaga: stare urządzenia (NAS, drukarki skanujące do folderu) mogą stracić dostęp do udostępnionych folderów tego komputera.",
		Detect: func(fc *FixContext) []FixItem {
			on, known := false, false
			if fc.IsAdmin {
				if cfg := fc.c.Cim(Q{NS: `root\Microsoft\Windows\SMB`, Class: "MSFT_SmbServerConfiguration", Props: []string{"EnableSMB1Protocol"}}); len(cfg) > 0 {
					if v, ok := cfg[0].Raw("EnableSMB1Protocol").(bool); ok {
						on, known = v, true
					}
				}
			}
			if !known {
				if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Services\LanmanServer\Parameters`, "SMB1"); ok {
					on = win.AnyInt(v) != 0
				}
			}
			if on {
				return []FixItem{{Problem: "Włączony jest serwer SMB1."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := wmiCall(`root\Microsoft\Windows\SMB`, "MSFT_SmbServerConfiguration", "SetConfiguration", map[string]any{"EnableSMB1Protocol": false}); err != nil {
				return FixOutcome{}, err
			}
			r.Log("SMB1 (serwer): włączony -> wyłączony")
			r.addUndo(UndoRecord{Kind: "smb1"})
			return done("Serwer SMB1 wyłączony.")
		}},

	{ID: "fx.remotereg", Group: "Bezpieczeństwo", Order: 45, NeedsAdmin: true,
		Title:       "Zatrzymaj usługę Zdalny rejestr",
		Description: "Zatrzymuje i wyłącza usługę RemoteRegistry, która pozwala zmieniać rejestr tego komputera przez sieć. Na komputerach w domenie nie jest proponowane.",
		Detect: func(fc *FixContext) []FixItem {
			if partOfDomain(fc.c) {
				return nil
			}
			if st, ok := win.ServiceState("RemoteRegistry"); ok && st == "Running" {
				return []FixItem{{Problem: "Działa usługa Zdalny rejestr."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := win.StopService("RemoteRegistry", true, 30*time.Second); err != nil {
				return FixOutcome{}, err
			}
			if err := r.SetServiceStart("RemoteRegistry", "Disabled"); err != nil {
				return FixOutcome{}, err
			}
			return done("Usługa Zdalny rejestr zatrzymana i wyłączona.")
		}},

	{ID: "fx.winlogon", Group: "Bezpieczeństwo", Order: 46, NeedsAdmin: true, Risk: "medium",
		Title:       "Przywróć domyślne ustawienia logowania (Winlogon)",
		Description: "Przywraca wartości Shell = explorer.exe i Userinit = userinit.exe - złośliwe programy dopisują tam siebie, żeby uruchamiać się przy każdym logowaniu.",
		Detect: func(fc *FixContext) []FixItem {
			wl := `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
			sr := getenv("SystemRoot")
			var bad []string
			shell, uinit := regStr(wl, "Shell", "explorer.exe"), regStr(wl, "Userinit", "")
			if !defaultWinlogon(shell, "Shell", sr) {
				bad = append(bad, "Shell = "+shell)
			}
			if !defaultWinlogon(uinit, "Userinit", sr) {
				bad = append(bad, "Userinit = "+uinit)
			}
			if len(bad) > 0 {
				return []FixItem{{Problem: "Niestandardowe wartości: " + strings.Join(bad, "; ")}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			wl := `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`
			sr := getenv("SystemRoot")
			if !defaultWinlogon(regStr(wl, "Shell", "explorer.exe"), "Shell", sr) {
				if err := r.SetReg(wl, "Shell", "explorer.exe", "String"); err != nil {
					return FixOutcome{}, err
				}
			}
			if !defaultWinlogon(regStr(wl, "Userinit", ""), "Userinit", sr) {
				if err := r.SetReg(wl, "Userinit", sr+`\system32\userinit.exe,`, "String"); err != nil {
					return FixOutcome{}, err
				}
			}
			return done("Przywrócono domyślne wartości Winlogon - przeskanuj komputer programem antywirusowym.")
		}},

	{ID: "fx.ifeo", Group: "Bezpieczeństwo", Order: 47, NeedsAdmin: true,
		Title:       "Usuń przekierowanie programu (IFEO)",
		Description: "Usuwa wartość Debugger, która zamiast właściwego programu uruchamia inny (typowa tylna furtka przez narzędzia ułatwień dostępu albo blokada antywirusa).",
		Detect: func(fc *FixContext) []FixItem {
			ifeo := `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options`
			var out []FixItem
			for _, nm := range win.RegSubKeys(ifeo) {
				dbg := regStr(ifeo+`\`+nm, "Debugger", "")
				if dbg == "" {
					continue
				}
				if containsi(accessibilityTools, nm) || redirectedRx.MatchString(nm) {
					out = append(out, FixItem{Key: nm, Title: "Usuń przekierowanie programu: " + nm, Problem: fmt.Sprintf("Uruchomienie %s jest przekierowane do: %s", nm, dbg),
						Data: regNameVal{ifeo + `\` + nm, "Debugger"}})
				}
			}
			return out
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			v := data.(regNameVal)
			if err := r.RemoveReg(v.Path, v.Name); err != nil {
				return FixOutcome{}, err
			}
			return done("Usunięto przekierowanie.")
		}},

	{ID: "fx.appinit", Group: "Bezpieczeństwo", Order: 48, NeedsAdmin: true,
		Title:       "Wyłącz ładowanie bibliotek AppInit_DLLs",
		Description: "Wyłącza mechanizm wstrzykiwania bibliotek do każdego programu (LoadAppInit_DLLs = 0); lista bibliotek zostaje w rejestrze do wglądu.",
		Detect: func(fc *FixContext) []FixItem {
			var keys []regNameVal // Name = lista bibliotek
			for _, k := range []string{`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Windows`, `HKLM\SOFTWARE\Wow6432Node\Microsoft\Windows NT\CurrentVersion\Windows`} {
				ai := regStr(k, "AppInit_DLLs", "")
				if strings.TrimSpace(ai) != "" && regInt(k, "LoadAppInit_DLLs", 0) == 1 {
					keys = append(keys, regNameVal{k, ai})
				}
			}
			if len(keys) == 0 {
				return nil
			}
			var dlls []string
			for _, k := range keys {
				dlls = append(dlls, k.Name)
			}
			return []FixItem{{Problem: "AppInit_DLLs ładuje do każdego programu: " + strings.Join(dlls, "; "), Data: keys}}
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			for _, k := range data.([]regNameVal) {
				if err := r.SetReg(k.Path, "LoadAppInit_DLLs", 0, "DWord"); err != nil {
					return FixOutcome{}, err
				}
			}
			return done("Ładowanie AppInit_DLLs wyłączone - przeskanuj komputer programem antywirusowym.")
		}},

	// ---------------------------------------------------------------- dysk i system
	{ID: "fx.dumps", Group: "Dysk i system", Order: 50, NeedsAdmin: true, Reboot: true, Covers: []string{"ev.crash|^Zrzuty pamięci$"},
		Title:       "Włącz zapisywanie zrzutów pamięci po awarii",
		Description: "Ustawia automatyczny zrzut pamięci - przy kolejnym niebieskim ekranie powstanie plik do analizy przyczyny.",
		Detect: func(fc *FixContext) []FixItem {
			if v, ok := win.RegValue(`HKLM\SYSTEM\CurrentControlSet\Control\CrashControl`, "CrashDumpEnabled"); ok && win.AnyInt(v) == 0 {
				return []FixItem{{Problem: "Zapis zrzutów pamięci jest wyłączony - przyczyny niebieskich ekranów nie da się przeanalizować."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if err := r.SetReg(`HKLM\SYSTEM\CurrentControlSet\Control\CrashControl`, "CrashDumpEnabled", 7, "DWord"); err != nil {
				return FixOutcome{}, err
			}
			return done("Włączono automatyczny zrzut pamięci (działa po ponownym uruchomieniu).")
		}},

	{ID: "fx.trim", Group: "Dysk i system", Order: 51, NeedsAdmin: true, Covers: []string{"st.layout|^TRIM"},
		Title:       "Włącz TRIM dla dysków SSD",
		Description: "fsutil behavior set DisableDeleteNotify 0 - SSD dowiaduje się o usuniętych plikach, co utrzymuje jego wydajność i trwałość.",
		Detect: func(fc *FixContext) []FixItem {
			fs := `HKLM\SYSTEM\CurrentControlSet\Control\FileSystem`
			if regInt(fs, "DisableDeleteNotification", 0) != 1 && regInt(fs, "NtfsDisableDeleteNotification", 0) != 1 {
				return nil
			}
			ssd := false
			for _, d := range diskInventory(fc.c) {
				if ssdRx.MatchString(diskTypeName(d)) {
					ssd = true
				}
			}
			if !ssd {
				return nil
			}
			return []FixItem{{Problem: "TRIM jest wyłączony, a komputer ma dysk SSD."}}
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			fsKey := `HKLM\SYSTEM\CurrentControlSet\Control\FileSystem`
			before := regSnapshot(fsKey, "DisableDeleteNotification", "NtfsDisableDeleteNotification", "RefsDisableDeleteNotification")
			res := runNative(30*time.Second, sysPath(`System32\fsutil.exe`), "behavior", "set", "DisableDeleteNotify", "0")
			r.snapshotUndo(fsKey, before)
			if res.ExitCode != 0 {
				return FixOutcome{}, fmt.Errorf("fsutil zakończył się kodem %d: %s", res.ExitCode, lastLine(res.Out+res.Err))
			}
			r.Log("fsutil behavior set DisableDeleteNotify 0")
			return done("TRIM włączony.")
		}},

	{ID: "fx.pagefile", Group: "Dysk i system", Order: 52, NeedsAdmin: true, Reboot: true, Covers: []string{"st.pagefile|^Plik wymiany"},
		Title:       "Przywróć automatyczny plik wymiany",
		Description: "Włącza plik wymiany zarządzany przez system - jego brak powoduje błędy braku pamięci i uniemożliwia zrzuty awaryjne.",
		Detect: func(fc *FixContext) []FixItem {
			if _, ok := fc.first("st.pagefile", "^Plik wymiany", func(f Finding) bool { return f.Status == Warn && disabledValueRx.MatchString(f.Value) }); ok {
				return []FixItem{{Problem: "Plik wymiany jest wyłączony."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			mmKey := `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management`
			before := regSnapshot(mmKey, "PagingFiles")
			_ = win.EnablePrivilege("SeCreatePagefilePrivilege")
			err := win.WmiSetProperties(`root\cimv2`, fmt.Sprintf(`Win32_ComputerSystem.Name="%s"`, getenv("COMPUTERNAME")), map[string]any{"AutomaticManagedPagefile": true})
			r.snapshotUndo(mmKey, before)
			if err != nil {
				return FixOutcome{}, err
			}
			r.Log("Win32_ComputerSystem.AutomaticManagedPagefile: False -> True")
			return done("Plik wymiany będzie zarządzany przez system (po ponownym uruchomieniu).")
		}},

	{ID: "fx.cleanup", Group: "Dysk i system", Order: 55, NeedsAdmin: true, Covers: []string{"st.volumes|^Wolumin " + regexp.QuoteMeta(sysDrive()) + "$", "st.cleanup"},
		Title:       "Usuń pliki tymczasowe",
		Description: "Czyści foldery Temp (system i użytkownicy), raporty błędów Windows i pobrane pakiety Windows Update. Pliki używane przez programy zostaną pominięte.",
		Detect: func(fc *FixContext) []FixItem {
			sys := strings.ToUpper(sysDrive())
			if f, ok := fc.first("st.volumes", "^Wolumin "+regexp.QuoteMeta(sys)+"$", isProblem); ok {
				return []FixItem{{Problem: "Mało miejsca na dysku systemowym: " + f.Value}}
			}
			if f, ok := fc.first("st.cleanup", "", func(f Finding) bool { return f.Status == Info }); ok {
				return []FixItem{{Problem: f.Value, Default: boolp(false)}}
			}
			return []FixItem{{Problem: "Rutynowe porządki: pliki tymczasowe, raporty błędów i pamięć podręczna aktualizacji.", Default: boolp(false)}}
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			dirs := []string{sysPath("Temp")}
			for _, up := range userProfiles(NewFixContext(nil, true, false).c) {
				dirs = append(dirs, filepath.Join(up.LocalPath, `AppData\Local\Temp`))
			}
			wer := filepath.Join(getenv("ProgramData"), `Microsoft\Windows\WER`)
			dirs = append(dirs, filepath.Join(wer, "ReportArchive"), filepath.Join(wer, "ReportQueue"))
			swd := sysPath(`SoftwareDistribution\Download`)
			measure := func() int64 {
				var t int64
				for _, d := range append(dirs, swd) {
					b, _ := win.DirSize(d)
					t += b
				}
				return t
			}
			clean := func(d string) {
				ents, _ := os.ReadDir(d)
				for _, e := range ents {
					_ = os.RemoveAll(filepath.Join(d, e.Name()))
				}
			}
			before := measure()
			for _, d := range dirs {
				clean(d)
			}
			st, _ := win.ServiceState("wuauserv")
			_ = win.StopService("wuauserv", true, 30*time.Second)
			clean(swd)
			if st == "Running" {
				_ = win.StartService("wuauserv", 30*time.Second)
			}
			freed := float64(max(before-measure(), 0))
			r.Log("Usunięto pliki tymczasowe: " + fmtSize(freed, 1))
			return done("Zwolniono " + fmtSize(freed, 1) + ".")
		}},

	// ---------------------------------------------------------------- sieć
	{ID: "fx.dns", Group: "Sieć", Order: 60, Covers: []string{"nw.connectivity|DNS"},
		Title:       "Wyczyść pamięć podręczną DNS",
		Description: "ipconfig /flushdns - usuwa zapamiętane (także błędne) odpowiedzi serwerów nazw.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.problem("nw.connectivity", "DNS") {
				return []FixItem{{Problem: "Problemy z rozpoznawaniem nazw (DNS)."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			res := runNative(30*time.Second, sysPath(`System32\ipconfig.exe`), "/flushdns")
			if res.ExitCode != 0 {
				return FixOutcome{}, fmt.Errorf("ipconfig zakończył się kodem %d", res.ExitCode)
			}
			r.Log("ipconfig /flushdns")
			return done("Pamięć podręczna DNS wyczyszczona.")
		}},

	{ID: "fx.time", Group: "Sieć", Order: 61, NeedsAdmin: true, NeedsInternet: true, Covers: []string{"nw.connectivity|^Zegar systemowy$"},
		Title:       "Zsynchronizuj zegar systemowy",
		Description: "Uruchamia usługę Czas systemu Windows i synchronizuje zegar z serwerem czasu (błędny czas psuje HTTPS, logowanie i aktualizacje).",
		Detect: func(fc *FixContext) []FixItem {
			if f, ok := fc.first("nw.connectivity", "^Zegar systemowy$", isProblem); ok {
				return []FixItem{{Problem: f.Value}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			if m, ok := win.ServiceStartMode("w32time"); ok && m == "Disabled" {
				if err := r.SetServiceStart("w32time", "Manual"); err != nil {
					return FixOutcome{}, err
				}
			}
			_ = win.StartService("w32time", 30*time.Second)
			w := sysPath(`System32\w32tm.exe`)
			res := runNative(time.Minute, w, "/resync")
			if res.ExitCode != 0 {
				// komputer bez skonfigurowanego źródła czasu - ustawiamy serwer Microsoft i próbujemy ponownie
				runNative(30*time.Second, w, "/config", "/manualpeerlist:time.windows.com,0x9", "/syncfromflags:manual", "/update")
				r.Log("w32tm: źródło czasu -> time.windows.com")
				res = runNative(time.Minute, w, "/resync", "/rediscover")
			}
			if res.ExitCode != 0 {
				return FixOutcome{}, errors.New("Synchronizacja nie powiodła się: " + lastLine(res.Out))
			}
			return done("Zegar zsynchronizowany z serwerem czasu.")
		}},

	{ID: "fx.proxy", Group: "Sieć", Order: 62, Covers: []string{"nw.config|[Pp]roxy"},
		Title:       "Wyłącz ręczne ustawienie serwera proxy",
		Description: "Wyłącza serwer proxy w ustawieniach internetowych użytkowników (adres zostaje zapisany w dzienniku). Proponowane tylko, gdy strony internetowe nie działają.",
		Detect: func(fc *FixContext) []FixItem {
			if !fc.problem("nw.connectivity", "HTTP") {
				return nil
			}
			sub := `\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
			targets := []string{`HKCU` + sub}
			for _, h := range userHives(fc.c) {
				targets = append(targets, h.Path+sub)
			}
			var on []string
			for _, t := range targets {
				if regInt(t, "ProxyEnable", 0) == 1 {
					on = append(on, t)
				}
			}
			if len(on) == 0 {
				return nil
			}
			return []FixItem{{Problem: "Strony internetowe nie działają, a ustawiono serwer proxy: " + regStr(on[0], "ProxyServer", "") + ".", Data: on}}
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			for _, p := range data.([]string) {
				if err := r.SetReg(p, "ProxyEnable", 0, "DWord"); err != nil {
					return FixOutcome{}, err
				}
			}
			return done("Serwer proxy wyłączony.")
		}},

	{ID: "fx.winsock", Group: "Sieć", Order: 65, NeedsAdmin: true, Reboot: true, DefaultOff: true, Risk: "medium",
		Covers:      []string{"nw.connectivity|^Ping do internetu", "nw.connectivity|^Dostęp do internetu"},
		Title:       "Zresetuj Winsock i stos TCP/IP",
		Description: "netsh winsock reset oraz netsh int ip reset - naprawia uszkodzone ustawienia sieci. Uwaga: usuwa ręcznie ustawione adresy IP i może wymagać ponownej instalacji klienta VPN; potrzebny restart.",
		Detect: func(fc *FixContext) []FixItem {
			if fc.Offline {
				return nil
			}
			// tylko przy faktycznym braku internetu (utrata części pakietów to zwykle jakość Wi-Fi, nie uszkodzony stos sieciowy)
			crit := func(f Finding) bool { return f.Status == Crit }
			_, ping := fc.first("nw.connectivity", "^Ping do internetu", crit)
			_, http := fc.first("nw.connectivity", `^Dostęp do internetu \(HTTP\)$`, crit)
			_, https := fc.first("nw.connectivity", `^Dostęp do internetu \(HTTPS\)$`, func(f Finding) bool { return noneValueRx.MatchString(f.Value) })
			if ping || http || https {
				return []FixItem{{Problem: "Brak łączności z internetem mimo aktywnego połączenia sieciowego."}}
			}
			return nil
		},
		Apply: func(r *FixRun, _ any) (FixOutcome, error) {
			n := sysPath(`System32\netsh.exe`)
			a := runNative(time.Minute, n, "winsock", "reset")
			b := runNative(time.Minute, n, "int", "ip", "reset")
			r.Log(fmt.Sprintf("netsh winsock reset: kod %d", a.ExitCode))
			r.Log(fmt.Sprintf("netsh int ip reset: kod %d", b.ExitCode))
			if a.ExitCode != 0 {
				return FixOutcome{}, errors.New("netsh winsock reset: " + lastLine(a.Out))
			}
			return done("Zresetowano Winsock i TCP/IP - uruchom komputer ponownie.")
		}},

	// ---------------------------------------------------------------- autostart i programy
	{ID: "fx.startup", Group: "Autostart", Order: 70, NeedsAdmin: true, Covers: []string{"sw.startup|^Autostart$"},
		Title:       "Wyłącz podejrzany wpis autostartu",
		Description: "Wyłącza wpis tak jak Menedżer zadań > Autostart (nie usuwa plików; można go włączyć z powrotem).",
		Detect: func(fc *FixContext) []FixItem {
			var out []FixItem
			for _, e := range startupEntries(fc.c) {
				if !e.Enabled || e.Kind == "RunOnce" || e.Approved == "" {
					continue
				}
				if why := startupSuspicion(e); why != "" {
					out = append(out, FixItem{Key: e.Approved + "|" + e.ApprovedName, Title: "Wyłącz z autostartu: " + e.Name,
						Problem: why + " (" + e.Source + ", " + e.Owner + ")", Data: e})
				}
			}
			return out
		},
		Apply: disableStartupEntry},

	{ID: "fx.startupopt", Group: "Autostart (przyspieszenie startu)", Order: 71, NeedsAdmin: true, DefaultOff: true, Covers: []string{"sw.startup|^Autostart$"},
		Title:       "Wyłącz program z autostartu",
		Description: "Program nie będzie uruchamiał się razem z Windows (wyłączenie jak w Menedżerze zadań > Autostart; można cofnąć). Zaznacz te, których klient nie potrzebuje od razu po starcie.",
		Detect: func(fc *FixContext) []FixItem {
			if _, ok := fc.first("sw.startup", "^Autostart$", func(f Finding) bool { return f.Status == Warn || f.Status == Info }); !ok {
				return nil
			}
			sr := strings.ToLower(getenv("SystemRoot"))
			var out []FixItem
			for _, e := range startupEntries(fc.c) {
				if !e.Enabled || e.Kind == "RunOnce" || e.Approved == "" || startupSuspicion(e) != "" {
					continue
				}
				exe := resolveCommandPath(e.Command)
				if exe != "" && sr != "" && strings.HasPrefix(strings.ToLower(exe), sr) {
					continue
				}
				fi := fileInfo(fc.c, exe, false)
				out = append(out, FixItem{Key: e.Approved + "|" + e.ApprovedName, Title: "Wyłącz z autostartu: " + e.Name,
					Problem: joinNonEmpty(" · ", fi.Company, limitText(e.Command, 90)), Data: e, Default: boolp(false)})
			}
			return out
		},
		Apply: disableStartupEntry},

	{ID: "fx.uninstall", Group: "Oprogramowanie", Order: 90, NeedsAdmin: true, DefaultOff: true, Interactive: true, Risk: "medium",
		Covers:      []string{"sw.installed|^Programy potencjalnie niechciane", "sw.installed|^Optymalizatory", "sw.installed|^Narzędzia do obchodzenia aktywacji"},
		Title:       "Odinstaluj program",
		Description: "Uruchamia deinstalator programu - jego okno trzeba przejść ręcznie. Deinstalacje wykonywane są na końcu, po pozostałych naprawach.",
		Detect: func(fc *FixContext) []FixItem {
			types := map[string]string{"pup": "program potencjalnie niechciany", "optimizer": "optymalizator lub aktualizator sterowników", "crack": "narzędzie do obchodzenia aktywacji"}
			seen := map[string]bool{}
			var out []FixItem
			for _, app := range uninstallEntries(fc.c) {
				if seen[strings.ToLower(app.KeyPath)] {
					continue
				}
				for _, f := range data().flags {
					label, ok := types[f.Type]
					if !ok || !f.rx.MatchString(app.Name) {
						continue
					}
					seen[strings.ToLower(app.KeyPath)] = true
					if app.UninstallString != "" || app.QuietUninstallString != "" {
						problem := label
						if f.Note != "" {
							problem += " - " + f.Note
						}
						it := FixItem{Key: app.KeyPath, Title: "Odinstaluj: " + app.Name, Problem: problem, Data: app, Default: boolp(false)}
						if f.Type == "crack" {
							it.CustomerTitle = "Usunięcie programu wysokiego ryzyka"
						}
						out = append(out, it)
					}
					break
				}
			}
			return out
		},
		Apply: func(r *FixRun, data any) (FixOutcome, error) {
			app := data.(uninstallEntry)
			src := app.UninstallString
			if src == "" {
				src = app.QuietUninstallString
			}
			file, args, ok := uninstallCommand(src)
			if !ok {
				return FixOutcome{}, errors.New("Brak polecenia deinstalacji - usuń program w Programach i funkcjach.")
			}
			r.Log(strings.TrimSpace("Deinstalator: " + file + " " + args))
			code, finished, err := win.RunVisibleWait(file, args, 30*time.Minute)
			if err != nil {
				return FixOutcome{}, err
			}
			if !finished {
				return FixOutcome{Result: "warn", Message: "Deinstalator nadal działa - dokończ go ręcznie."}, nil
			}
			time.Sleep(2 * time.Second)
			if win.RegKeyExists(app.KeyPath) {
				return FixOutcome{Result: "warn", Message: fmt.Sprintf("Deinstalator zakończył się (kod %d), ale program nadal jest na liście zainstalowanych - sprawdź ręcznie.", code)}, nil
			}
			return done("Program odinstalowany.")
		}},
}

// fwProfile - profil zapory do włączenia (Data naprawy fx.firewall).
type fwProfile struct{ Profile, Label, PolicyPath string }

// setFirewallProfiles włącza albo wyłącza zaporę w profilach (Set-NetFirewallProfile -Profile ... -Enabled):
// Domain, Private, Public - przez HNetCfg.FwPolicy2, jak ustawienia zapory w Panelu sterowania.
func setFirewallProfiles(profiles []string, enabled bool) error {
	types := map[string]int{"Domain": 1, "Private": 2, "Public": 4}
	var err error
	win.WithCOM(func() {
		var fw *win.Dispatch
		if fw, err = win.CreateObject("HNetCfg.FwPolicy2"); err != nil {
			return
		}
		defer fw.Release()
		for _, p := range profiles {
			t, ok := types[p]
			if !ok {
				err = fmt.Errorf("nieznany profil zapory: %s", p)
				return
			}
			if err = fw.PutIdx("FirewallEnabled", t, enabled); err != nil {
				return
			}
		}
	})
	return err
}

// ---------------------------------------------------------------- autostart

// startupSuspicion odpowiada Get-SdStartupSuspicion: powód, dla którego wpis autostartu wygląda na złośliwy (te same
// reguły co test sw.startup); pusty napis - brak.
func startupSuspicion(e StartupEntry) string {
	exe := resolveCommandPath(e.Command)
	if suspiciousPathRx.MatchString(exe) {
		return "uruchamia plik z podejrzanej lokalizacji: " + exe
	}
	if suspiciousCmdRx.MatchString(e.Command) {
		return "polecenie typowe dla złośliwego oprogramowania (zakodowany lub pobierany kod)"
	}
	for _, pl := range hostPayload(e.Command, exe) {
		if suspiciousPathRx.MatchString(pl) || appDataRx.MatchString(pl) {
			return fmt.Sprintf("%s uruchamia plik z podejrzanej lokalizacji: %s", filepath.Base(exe), pl)
		}
	}
	return ""
}

// startupDisabledValue odpowiada New-SdStartupDisabledValue: 12 bajtów jak w Menedżerze zadań - 03 00 00 00 i czas
// wyłączenia (FILETIME UTC).
func startupDisabledValue(when time.Time) []byte {
	b := make([]byte, 12)
	b[0] = 3
	ft := uint64(when.UTC().UnixNano()/100) + 116444736000000000
	binary.LittleEndian.PutUint64(b[4:], ft)
	return b
}

func disableStartupEntry(r *FixRun, data any) (FixOutcome, error) {
	e := data.(StartupEntry)
	if err := r.SetReg(e.Approved, e.ApprovedName, startupDisabledValue(time.Now()), "Binary"); err != nil {
		return FixOutcome{}, err
	}
	return done("Wyłączono z autostartu (można przywrócić w Menedżerze zadań > Autostart).")
}

// ---------------------------------------------------------------- deinstalacja programów

// uninstallEntry - program z poleceniem deinstalacji (Get-SdUninstallEntries).
type uninstallEntry struct {
	Name, UninstallString, QuietUninstallString, KeyPath string
}

// uninstallEntries odpowiada Get-SdUninstallEntries: programy z poleceniem deinstalacji (komputer, 32-bit,
// załadowane profile użytkowników).
func uninstallEntries(c *Ctx) []uninstallEntry {
	roots := []string{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, `HKLM\SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\Uninstall`}
	for _, h := range userHives(c) {
		roots = append(roots, h.Path+`\Software\Microsoft\Windows\CurrentVersion\Uninstall`)
	}
	var out []uninstallEntry
	for _, root := range roots {
		for _, k := range win.RegSubKeys(root) {
			_, p, ok := win.RegValues(root + `\` + k)
			if !ok {
				continue
			}
			name := win.AnyString(p["displayname"])
			if name == "" {
				continue
			}
			if v, ok := p["systemcomponent"]; ok && win.AnyInt(v) == 1 {
				continue
			}
			out = append(out, uninstallEntry{Name: name, UninstallString: win.AnyString(p["uninstallstring"]),
				QuietUninstallString: win.AnyString(p["quietuninstallstring"]), KeyPath: root + `\` + k})
		}
	}
	return out
}

var (
	msiexecRx  = rx(`^"?(?:[^"]*\\)?msiexec(?:\.exe)?"?\s+(.*)$`)
	msiSlashIx = rx(`(^|\s)/I\s*(\{)`)
)

// uninstallCommand odpowiada ConvertTo-SdUninstallCommand: polecenie deinstalacji z rejestru -> program i parametry.
// MsiExec /I (zmiana/naprawa) zamieniamy na /X (usunięcie).
func uninstallCommand(command string) (file, args string, ok bool) {
	c := strings.TrimSpace(command)
	if c == "" {
		return "", "", false
	}
	c = expandEnv(c)
	if m := msiexecRx.FindStringSubmatch(c); m != nil {
		return "msiexec.exe", strings.TrimSpace(msiSlashIx.ReplaceAllString(m[1], "${1}/X ${2}")), true
	}
	if strings.HasPrefix(c, `"`) {
		if e := strings.Index(c[1:], `"`); e > 0 {
			return c[1 : e+1], strings.TrimSpace(c[e+2:]), true
		}
	}
	if exe := resolveCommandPath(c); exe != "" && len(c) >= len(exe) && strings.EqualFold(c[:len(exe)], exe) {
		return exe, strings.TrimSpace(c[len(exe):]), true
	}
	f, a, _ := strings.Cut(c, " ")
	return f, strings.TrimSpace(a), true
}
