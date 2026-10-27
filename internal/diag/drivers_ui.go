package diag

import (
	"fmt"
)

// Sterowniki w oknach narzędzi serwisowych (Show-SdUiDrivers, Show-SdUiMissingDrivers, Invoke-SdUiVendorPage):
// dane z WMI poza diagnostyką.

// DriverInfo: opis sterownika pod nazwą na liście okna sterowników (klasa, data wersji, rozmiar, uwagi).
func DriverInfo(d WuDriver) string {
	prob := ""
	if d.DeviceProblem != 0 {
		prob = fmt.Sprintf("naprawia urządzenie z błędem (kod %d)", d.DeviceProblem)
	}
	opt := ""
	if d.Optional {
		opt = "opcjonalny"
	}
	return joinNonEmpty(" · ", d.Class, "wersja z "+fmtDate(d.Date), fmtSize(d.SizeBytes, 1), prob, opt)
}

// MissingDevice - urządzenie bez sterownika lub z błędem.
type MissingDevice struct {
	Name, Problem, Vendor, HardwareID string
	Code                              int
}

// MissingDevices: urządzenia z błędem w Menedżerze urządzeń (bez wyłączonych ręcznie - kod 22), najpierw te
// bez sterownika (kod 28), potem według nazwy.
func MissingDevices() []MissingDevice {
	c := standaloneCtx()
	objs := c.Cim(Q{Class: "Win32_PnPEntity", Props: []string{"Name", "PNPDeviceID", "ConfigManagerErrorCode", "HardwareID"}, Where: "ConfigManagerErrorCode <> 0"})
	type dev struct {
		name string // nazwa z WMI (sortowanie jak Sort-Object Name)
		d    MissingDevice
	}
	var list []dev
	for _, o := range objs {
		code := int(o.Int("ConfigManagerErrorCode"))
		if code == 22 {
			continue
		}
		hw := o.Str("PNPDeviceID")
		if ids := o.Strs("HardwareID"); len(ids) > 0 {
			hw = ids[0]
		}
		name := o.Str("Name")
		d := MissingDevice{Name: name, Code: code, Problem: pnpErrorText(code), Vendor: vendorName(hw), HardwareID: hw}
		if d.Name == "" {
			d.Name = "Nieznane urządzenie"
		}
		list = append(list, dev{name, d})
	}
	psSort(list, func(a, b dev) int {
		ka, kb := 1, 1
		if a.d.Code == 28 {
			ka = 0
		}
		if b.d.Code == 28 {
			kb = 0
		}
		if ka != kb {
			return ka - kb
		}
		return cmpText(a.name, b.name)
	})
	out := make([]MissingDevice, len(list))
	for i, x := range list {
		out[i] = x.d
	}
	return out
}

// CatalogURL odpowiada Get-SdCatalogUrl: wyszukiwanie sterownika w Katalogu Microsoft Update po identyfikatorze sprzętu.
func CatalogURL(hwid string) string { return catalogURL(hwid) }

// ComputerSupportPage odpowiada Get-SdComputerSupportInfo: strona ze sterownikami producenta komputera
// (albo płyty głównej) i to, co trzeba w niej wpisać.
func ComputerSupportPage() SupportPage { return computerSupportInfo(standaloneCtx()) }

// IsVirtualMachine odpowiada Test-SdVm.
func IsVirtualMachine() bool { return isVM(standaloneCtx()) }
