package win

import (
	"encoding/binary"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetFirmwareType      = kernel32.NewProc("GetFirmwareType")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// IsAdmin: proces ma aktywną grupę Administratorzy (jak WindowsPrincipal.IsInRole w wersji PowerShell).
func IsAdmin() bool {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	ok, err := windows.Token(0).IsMember(sid)
	return err == nil && ok
}

// FirmwareType: 1 = BIOS (Legacy), 2 = UEFI, 0 = nie udało się ustalić.
func FirmwareType() int {
	var t uint32
	if r, _, _ := procGetFirmwareType.Call(uintptr(unsafe.Pointer(&t))); r == 0 {
		return 0
	}
	return int(t)
}

var procGetFirmwareEnvironmentVariableW = kernel32.NewProc("GetFirmwareEnvironmentVariableW")

// SecureBootUEFI odczytuje zmienną UEFI SecureBoot (jak Confirm-SecureBootUEFI; wymaga administratora).
func SecureBootUEFI() (enabled, ok bool) {
	enablePrivilege("SeSystemEnvironmentPrivilege")
	name, _ := windows.UTF16PtrFromString("SecureBoot")
	guid, _ := windows.UTF16PtrFromString("{8be4df61-93ca-11d2-aa0d-00e098032b8c}")
	var b [1]byte
	n, _, _ := procGetFirmwareEnvironmentVariableW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(guid)), uintptr(unsafe.Pointer(&b[0])), 1)
	if n == 0 {
		return false, false
	}
	return b[0] == 1, true
}

func enablePrivilege(name string) {
	var tok windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok) != nil {
		return
	}
	defer tok.Close()
	var luid windows.LUID
	n16, _ := windows.UTF16PtrFromString(name)
	if windows.LookupPrivilegeValue(nil, n16, &luid) != nil {
		return
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}

var (
	procGetDynamicTimeZoneInformation = kernel32.NewProc("GetDynamicTimeZoneInformation")
	advapi32                          = windows.NewLazySystemDLL("advapi32.dll")
	procRegLoadMUIStringW             = advapi32.NewProc("RegLoadMUIStringW")
)

// TimeZoneDisplayName: nazwa strefy czasowej jak TimeZoneInfo.Local.DisplayName, np.
// "(UTC+01:00) Sarajewo, Skopie, Warszawa, Zagrzeb".
func TimeZoneDisplayName() string {
	var tz [432]byte // DYNAMIC_TIME_ZONE_INFORMATION
	procGetDynamicTimeZoneInformation.Call(uintptr(unsafe.Pointer(&tz[0])))
	key := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&tz[172])), 128))
	if key == "" {
		return ""
	}
	path := `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Time Zones\` + key
	var h windows.Handle
	p16, _ := windows.UTF16PtrFromString(path)
	if windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, p16, 0, windows.KEY_READ, &h) == nil {
		defer windows.RegCloseKey(h)
		buf := make([]uint16, 256)
		var n uint32
		val, _ := windows.UTF16PtrFromString("MUI_Display")
		if r, _, _ := procRegLoadMUIStringW.Call(uintptr(h), uintptr(unsafe.Pointer(val)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n)), 0, 0); r == 0 {
			return windows.UTF16ToString(buf)
		}
	}
	if v, ok := RegValue(`HKLM\`+path, "Display"); ok {
		return AnyString(v)
	}
	return key
}

// UILanguage: język interfejsu użytkownika, np. "pl-PL" (jak CultureInfo.CurrentUICulture.Name).
func UILanguage() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}

// DosDevice: "C:" -> "\Device\HarddiskVolume3"; pusty napis, gdy nazwa nie istnieje.
func DosDevice(name string) string {
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n, err := windows.QueryDosDevice(n16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// MemoryStatus - stan pamięci (GlobalMemoryStatusEx).
type MemoryStatus struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func GetMemoryStatus() (MemoryStatus, bool) {
	var m MemoryStatus
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	return m, r != 0
}

// NvmeHealthLog odczytuje dziennik zdrowia NVMe (Log Page 02h, 512 bajtów) przez IOCTL_STORAGE_QUERY_PROPERTY.
// Działa bez uprawnień administratora (uchwyt bez praw dostępu). Zwraca nil i kod błędu Win32.
func NvmeHealthLog(disk int) ([]byte, uint32) {
	name, _ := windows.UTF16PtrFromString(`\\.\PhysicalDrive` + itoa(disk))
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, errCode(err)
	}
	defer windows.CloseHandle(h)
	in := make([]byte, 8+40)
	binary.LittleEndian.PutUint32(in[0:], 50)   // StorageDeviceProtocolSpecificProperty
	binary.LittleEndian.PutUint32(in[4:], 0)    // PropertyStandardQuery
	binary.LittleEndian.PutUint32(in[8:], 3)    // ProtocolTypeNvme
	binary.LittleEndian.PutUint32(in[12:], 2)   // NVMeDataTypeLogPage
	binary.LittleEndian.PutUint32(in[16:], 2)   // NVME_LOG_PAGE_HEALTH_INFO
	binary.LittleEndian.PutUint32(in[20:], 0)   // podwartość
	binary.LittleEndian.PutUint32(in[24:], 40)  // ProtocolDataOffset
	binary.LittleEndian.PutUint32(in[28:], 512) // ProtocolDataLength
	out := make([]byte, 8+40+512)
	var ret uint32
	if err := windows.DeviceIoControl(h, 0x002D1400, &in[0], uint32(len(in)), &out[0], uint32(len(out)), &ret, nil); err != nil {
		return nil, errCode(err)
	}
	off := binary.LittleEndian.Uint32(out[8+16:])
	if int(off)+8+512 > len(out) {
		return nil, 87
	}
	log := make([]byte, 512)
	copy(log, out[8+off:8+off+512])
	return log, 0
}

func errCode(err error) uint32 {
	if e, ok := err.(windows.Errno); ok {
		return uint32(e)
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DirSize zwraca rozmiar katalogu (bajty) i liczbę plików; pomija punkty reparse i niedostępne katalogi
// (jak SdNative.DirSize). FindFirstFileEx z FindExInfoBasic i dużym buforem - bez zbędnych danych.
func DirSize(path string) (bytes int64, files int64) {
	walkDir(path, 0, &bytes, &files)
	return
}

var (
	procFindFirstFileExW = kernel32.NewProc("FindFirstFileExW")
)

func walkDir(dir string, depth int, total, files *int64) {
	if depth > 40 {
		return
	}
	pattern, err := windows.UTF16PtrFromString(strings.TrimRight(dir, `\`) + `\*`)
	if err != nil {
		return
	}
	var fd windows.Win32finddata
	// FindExInfoBasic (1), FindExSearchNameMatch (0), FIND_FIRST_EX_LARGE_FETCH (2)
	r, _, _ := procFindFirstFileExW.Call(uintptr(unsafe.Pointer(pattern)), 1, uintptr(unsafe.Pointer(&fd)), 0, 0, 2)
	h := windows.Handle(r)
	if h == windows.InvalidHandle {
		return
	}
	defer windows.FindClose(h)
	var subdirs []string
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if fd.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			if name != "." && name != ".." && fd.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
				subdirs = append(subdirs, dir+`\`+name)
			}
		} else {
			*total += int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow)
			*files++
		}
		if windows.FindNextFile(h, &fd) != nil {
			break
		}
	}
	for _, d := range subdirs {
		walkDir(d, depth+1, total, files)
	}
}

// DocumentsDir: folder Dokumenty bieżącego użytkownika (także przeniesiony, np. do OneDrive).
func DocumentsDir(home string) string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil && p != "" {
		return p
	}
	return home + `\Documents`
}

// FileSize: rozmiar pliku (także pliku systemowego w użyciu, np. hiberfil.sys).
func FileSize(path string) (int64, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		var fd windows.Win32FileAttributeData
		p16, err2 := windows.UTF16PtrFromString(path)
		if err2 != nil || windows.GetFileAttributesEx(p16, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&fd))) != nil {
			return 0, false
		}
		return int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow), true
	}
	return fi.Size(), true
}
