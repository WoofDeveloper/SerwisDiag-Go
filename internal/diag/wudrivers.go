package diag

import (
	"fmt"
	"slices"

	"serwisdiag/internal/win"
)

// Sterowniki z Windows Update - wyszukiwanie z zachowaną sesją i instalacja wybranych ($global:SdWuDriverSearch
// -KeepResult i $global:SdWuDriverInstall w lib\Drivers.ps1).

// WuSearch - wynik wyszukiwania sterowników z sesją Windows Update do instalacji wybranych. Po użyciu: Release.
type WuSearch struct {
	Items   []WuDriver
	Error   string
	session *win.Dispatch
	result  *win.Dispatch
}

// WuDriverSearch wyszukuje sterowniki dla urządzeń komputera i zachowuje wynik do instalacji.
func WuDriverSearch() *WuSearch {
	ws := &WuSearch{}
	win.WithCOM(func() {
		s, err := win.CreateObject("Microsoft.Update.Session")
		if err != nil {
			ws.Error = err.Error()
			return
		}
		_ = s.Put("ClientApplicationID", "SerwisDiag")
		srch, err := s.Obj("CreateUpdateSearcher")
		if err != nil {
			s.Release()
			ws.Error = err.Error()
			return
		}
		defer srch.Release()
		res, err := srch.Obj("Search", "IsInstalled=0 and Type='Driver' and IsHidden=0")
		if err != nil {
			s.Release()
			ws.Error = err.Error()
			return
		}
		ws.session, ws.result = s, res
		ups, err := res.Obj("Updates")
		if err != nil {
			ws.Error = err.Error()
			return
		}
		defer ups.Release()
		ws.Items = wuDriverItems(ups)
	})
	return ws
}

// Release zwalnia sesję Windows Update.
func (ws *WuSearch) Release() {
	if ws == nil {
		return
	}
	win.WithCOM(func() {
		ws.result.Release()
		ws.session.Release()
	})
}

// WuInstallItem - wynik instalacji jednego sterownika (ResultCode jak OperationResultCode).
type WuInstallItem struct {
	Title      string
	ResultCode int
	HResult    int64
}

// WuInstall - wynik instalacji sterowników.
type WuInstall struct {
	Results        []WuInstallItem
	RebootRequired bool
	Error          string
}

// Install pobiera i instaluje wskazane sterowniki z wyniku wyszukiwania. Wymaga uprawnień administratora.
// say (może być nil) dostaje opis bieżącego kroku.
func (ws *WuSearch) Install(ids []string, say func(string)) WuInstall {
	var out WuInstall
	if say == nil {
		say = func(string) {}
	}
	if ws == nil || ws.session == nil || ws.result == nil {
		out.Error = "Wybrane sterowniki nie są już dostępne w Windows Update - wyszukaj ponownie."
		return out
	}
	win.WithCOM(func() {
		coll, err := win.CreateObject("Microsoft.Update.UpdateColl")
		if err != nil {
			out.Error = err.Error()
			return
		}
		defer coll.Release()
		ups, err := ws.result.Obj("Updates")
		if err != nil {
			out.Error = err.Error()
			return
		}
		defer ups.Release()
		_ = ups.Items(func(u *win.Dispatch) {
			id := ""
			if o, err := u.Obj("Identity"); err == nil {
				id = o.Str("UpdateID")
				o.Release()
			}
			if !slices.Contains(ids, id) {
				return
			}
			if !u.Bool("EulaAccepted") {
				_, _ = u.Get("AcceptEula")
			}
			_, _ = coll.Get("Add", u)
		})
		n := int(coll.Int("Count"))
		if n == 0 {
			out.Error = "Wybrane sterowniki nie są już dostępne w Windows Update - wyszukaj ponownie."
			return
		}
		say(fmt.Sprintf("Pobieranie sterowników (%d)...", n))
		dl, err := ws.session.Obj("CreateUpdateDownloader")
		if err != nil {
			out.Error = err.Error()
			return
		}
		defer dl.Release()
		if err := dl.Put("Updates", coll); err != nil {
			out.Error = err.Error()
			return
		}
		v, err := dl.Get("Download")
		if err != nil {
			out.Error = err.Error()
			return
		}
		if d, ok := v.(*win.Dispatch); ok {
			d.Release()
		}
		say(fmt.Sprintf("Instalowanie sterowników (%d)...", n))
		inst, err := ws.session.Obj("CreateUpdateInstaller")
		if err != nil {
			out.Error = err.Error()
			return
		}
		defer inst.Release()
		if err := inst.Put("Updates", coll); err != nil {
			out.Error = err.Error()
			return
		}
		ir, err := inst.Obj("Install")
		if err != nil {
			out.Error = err.Error()
			return
		}
		defer ir.Release()
		for i := 0; i < n; i++ {
			it := WuInstallItem{}
			if u, err := coll.Obj("Item", i); err == nil {
				it.Title = u.Str("Title")
				u.Release()
			}
			if ur, err := ir.Obj("GetUpdateResult", i); err == nil {
				it.ResultCode, it.HResult = int(ur.Int("ResultCode")), ur.Int("HResult")
				ur.Release()
			}
			out.Results = append(out.Results, it)
		}
		out.RebootRequired = ir.Bool("RebootRequired")
	})
	return out
}

// SelectDriversToInstall odpowiada Select-SdDriversToInstall: domyślny wybór - sterowniki zalecane oraz każdy,
// który naprawia urządzenie z błędem. Opcjonalne (BrowseOnly) technik zaznacza sam.
func SelectDriversToInstall(items []WuDriver) []WuDriver {
	var out []WuDriver
	for _, d := range items {
		if !d.Optional || d.DeviceProblem != 0 {
			out = append(out, d)
		}
	}
	return out
}

// WuResultText odpowiada Get-SdWuResultText: OperationResultCode z Windows Update -> opis.
func WuResultText(code int) string {
	switch code {
	case 2:
		return "zainstalowano"
	case 3:
		return "zainstalowano z błędami"
	case 4:
		return "niepowodzenie"
	case 5:
		return "przerwano"
	}
	return "nie rozpoczęto"
}
