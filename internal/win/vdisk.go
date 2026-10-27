package win

import (
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Obraz ISO jako napęd (Mount-DiskImage / Dismount-DiskImage w wersji PowerShell) - przez funkcje dysków
// wirtualnych Windows (virtdisk.dll), z których korzysta też Eksplorator. Podłączenie na stałe (do odłączenia
// albo ponownego uruchomienia) wymaga uprawnień administratora.

var (
	virtdisk                       = windows.NewLazySystemDLL("virtdisk.dll")
	procOpenVirtualDisk            = virtdisk.NewProc("OpenVirtualDisk")
	procAttachVirtualDisk          = virtdisk.NewProc("AttachVirtualDisk")
	procDetachVirtualDisk          = virtdisk.NewProc("DetachVirtualDisk")
	procGetVirtualDiskPhysicalPath = virtdisk.NewProc("GetVirtualDiskPhysicalPath")
	procGetSystemDefaultUILanguage = kernel32.NewProc("GetSystemDefaultUILanguage")
	procLCIDToLocaleName           = kernel32.NewProc("LCIDToLocaleName")
	virtualStorageVendorMicrosoft  = windows.GUID{Data1: 0xEC984AEC, Data2: 0xA0F9, Data3: 0x47E9, Data4: [8]byte{0x90, 0x1F, 0x71, 0x41, 0x5A, 0x66, 0x34, 0x5B}}
)

const (
	virtualStorageTypeDeviceISO = 1
	virtualDiskAccessRead       = 0x000D0000
	attachReadOnly              = 0x1
	attachPermanentLifetime     = 0x4
)

type virtualStorageType struct {
	DeviceID uint32
	VendorID windows.GUID
}

func openISO(path string) (windows.Handle, error) {
	st := virtualStorageType{DeviceID: virtualStorageTypeDeviceISO, VendorID: virtualStorageVendorMicrosoft}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h windows.Handle
	if r, _, _ := procOpenVirtualDisk.Call(uintptr(unsafe.Pointer(&st)), uintptr(unsafe.Pointer(p16)), virtualDiskAccessRead, 0, 0, uintptr(unsafe.Pointer(&h))); r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

// isoPhysicalPath: urządzenie podłączonego obrazu (\\.\CDROMn); pusty napis - obraz nie jest podłączony.
func isoPhysicalPath(h windows.Handle) string {
	buf := make([]uint16, 260)
	size := uint32(len(buf) * 2)
	if r, _, _ := procGetVirtualDiskPhysicalPath.Call(uintptr(h), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// cdromLetter: litera dysku napędu \\.\CDROMn (QueryDosDevice "E:" -> \Device\CdRomN); pusty napis - brak litery.
func cdromLetter(phys string) string {
	up := strings.ToUpper(phys)
	num := strings.TrimPrefix(up, `\\.\CDROM`)
	if num == up || num == "" {
		return ""
	}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return ""
	}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		l := string(rune('A' + i))
		if strings.EqualFold(DosDevice(l+":"), `\Device\CdRom`+num) {
			return l
		}
	}
	return ""
}

// MountISO podłącza obraz ISO jako napęd i zwraca jego literę. mounted: obraz podłączył program (wcześniej nie był
// podłączony) - do odłączenia po naprawie. Litera bywa nadawana z opóźnieniem - czekamy do 6 s (jak wersja PowerShell).
func MountISO(path string) (letter string, mounted bool, err error) {
	h, err := openISO(path)
	if err != nil {
		return "", false, err
	}
	defer windows.CloseHandle(h)
	phys := isoPhysicalPath(h)
	if phys == "" {
		if r, _, _ := procAttachVirtualDisk.Call(uintptr(h), 0, attachReadOnly|attachPermanentLifetime, 0, 0, 0); r != 0 {
			return "", false, syscall.Errno(r)
		}
		mounted = true
		phys = isoPhysicalPath(h)
	}
	for i := 0; i < 20; i++ {
		if letter = cdromLetter(phys); letter != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	return letter, mounted, nil
}

// DismountISO odłącza obraz ISO (Dismount-DiskImage).
func DismountISO(path string) error {
	h, err := openISO(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if r, _, _ := procDetachVirtualDisk.Call(uintptr(h), 0, 0); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// InstalledUILanguage: język instalacji systemu, np. "pl-PL" (jak CultureInfo.InstalledUICulture.Name).
func InstalledUILanguage() string {
	lid, _, _ := procGetSystemDefaultUILanguage.Call()
	buf := make([]uint16, 85)
	if n, _, _ := procLCIDToLocaleName.Call(lid&0xFFFF, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0); n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
