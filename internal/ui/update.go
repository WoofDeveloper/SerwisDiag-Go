package ui

import (
	"fmt"
	"time"

	"serwisdiag/internal/diag"
)

// Aktualizacje programu z okna (Show-SdUiUpdateBanner, Start-SdUiUpdateCheck, Invoke-SdUiUpdate): pasek „Dostępna
// nowa wersja”, sprawdzanie raz na dobę w tle i aktualizacja - instalator nowej wersji pobrany i sprawdzony w tle,
// potem uruchomiony, a program się zamyka (instalator uruchomi go ponownie).

// startupBackground: przy starcie okna - sprawdzenie nowej wersji (wersja zainstalowana, raz na dobę) i dane
// rejestracji czekające na wysłanie (np. instalacja bez internetu); w tle, bez komunikatów.
func (mw *mainWin) startupBackground() {
	if mw.opt.SelfTest {
		return // autotest nie łączy się z internetem
	}
	cleanupUpdates()
	now := time.Now()
	if diag.UpdateCheckDue(mw.opt.Root, mw.settings, "", now) {
		mw.background(func() func() {
			rel := diag.LatestRelease(diag.ProjectURL, 8*time.Second, "")
			_ = diag.SaveUpdateCheckState(rel, "")
			return func() {
				if rel.Ok && rel.Newer {
					mw.showUpdateBanner(rel)
				}
			}
		})
	}
	if diag.RegistrationDue("", now) {
		mw.background(func() func() {
			diag.SendRegistration("", nil)
			return nil
		})
	}
}

// cleanupUpdates: w tle usuwa z %TEMP% instalatory pobrane przez udaną aktualizację (diag.CleanupUpdateDownloads).
// Program uruchomiony ponownie przez instalator startuje, zanim ten skończy pracę i zwolni swój plik - stąd kolejne
// próby co 15 s przez 2 minuty.
func cleanupUpdates() {
	go func() {
		for i := 0; i < 9; i++ {
			if _, left := diag.CleanupUpdateDownloads(""); left == 0 {
				return
			}
			time.Sleep(15 * time.Second)
		}
	}()
}

// showUpdateBanner: wersja zainstalowana aktualizuje się sama, przenośna pokazuje stronę wydania.
func (mw *mainWin) showUpdateBanner(rel diag.Release) {
	mw.updRel = &rel
	installed := diag.InstallMode(mw.opt.Root) != ""
	mw.updText = fmt.Sprintf("Dostępna jest nowa wersja SerwisDiag %s (masz %s).", rel.Latest, diag.Version)
	btn := "Zaktualizuj"
	if !installed {
		mw.updText += " Wersję przenośną pobierz ze strony projektu."
		btn = "Pobierz"
	}
	setText(mw.btnUpdate, btn)
	enableWin(mw.btnUpdate, true)
	mw.layout()
	mw.redraw()
}

// checkUpdateNow: „Sprawdź nową wersję” w oknie O programie (owner - to okno).
func (mw *mainWin) checkUpdateNow(owner uintptr, closeOwner func()) {
	var rel diag.Release
	withWaitCursor(func() { rel = diag.LatestRelease(diag.ProjectURL, 8*time.Second, "") })
	_ = diag.SaveUpdateCheckState(rel, "")
	txt := diag.LatestReleaseText(rel)
	if !rel.Ok || !rel.Newer {
		msgBox(owner, txt, "Nowa wersja", mbIconInfo)
		return
	}
	mw.showUpdateBanner(rel)
	if diag.InstallMode(mw.opt.Root) != "" {
		if msgBox(owner, fmt.Sprintf("Dostępna jest nowa wersja: %s (masz %s).\n\nZaktualizować teraz?", rel.Latest, diag.Version), "Nowa wersja", mbIconQuestion|mbYesNo) == idYes {
			closeOwner()
			mw.update(true)
		}
		return
	}
	if msgBox(owner, txt+"\n\nOtworzyć stronę wydania?", "Nowa wersja", mbIconQuestion|mbYesNo) == idYes {
		openPath(rel.URL)
	}
}

// update odpowiada Invoke-SdUiUpdate.
func (mw *mainWin) update(confirmed bool) {
	rel := mw.updRel
	if rel == nil {
		return
	}
	if diag.InstallMode(mw.opt.Root) == "" {
		openPath(rel.URL)
		return
	}
	if mw.scanning {
		msgBox(mw.h, "Poczekaj na zakończenie diagnostyki - aktualizacja zamknie program.", "Aktualizacja", mbIconInfo)
		return
	}
	if !confirmed {
		q := fmt.Sprintf("Zainstalować SerwisDiag %s (masz %s)?\n\nProgram pobierze instalator z GitHuba, sprawdzi jego sumę kontrolną, zamknie się na czas aktualizacji i uruchomi ponownie. Ustawienia i raporty zostaną.", rel.Latest, diag.Version)
		if msgBox(mw.h, q, "Aktualizacja", mbIconQuestion|mbYesNo) != idYes {
			return
		}
	}
	mw.updText = fmt.Sprintf("Pobieranie i sprawdzanie wersji %s...", rel.Latest)
	enableWin(mw.btnUpdate, false)
	mw.layout()
	mw.redraw()
	r := *rel
	mw.background(func() func() {
		setup, err := diag.DownloadUpdate(r, "")
		return func() {
			if err == nil {
				err = diag.StartUpdateInstall(setup, mw.opt.Root, true)
				if err == nil {
					postMsg(mw.h, wmClose, 0, 0)
					return
				}
				mw.showUpdateBanner(r)
				msgBox(mw.h, "Nie uruchomiono instalatora: "+err.Error(), "Aktualizacja", mbIconWarning)
				return
			}
			mw.showUpdateBanner(r)
			msgBox(mw.h, "Aktualizacja nie powiodła się:\n"+err.Error(), "Aktualizacja", mbIconWarning)
		}
	})
}
