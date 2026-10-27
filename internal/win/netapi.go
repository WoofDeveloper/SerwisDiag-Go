package win

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Konta lokalne i grupa Administratorzy przez NetAPI (zamiast Win32_UserAccount i ADSI WinNT://).

var (
	netapi32                    = windows.NewLazySystemDLL("netapi32.dll")
	procNetUserEnum             = netapi32.NewProc("NetUserEnum")
	procNetLocalGroupGetMembers = netapi32.NewProc("NetLocalGroupGetMembers")
)

// LocalUser - konto lokalne (pola jak w Win32_UserAccount).
type LocalUser struct {
	Name             string
	RID              uint32
	Disabled         bool
	PasswordRequired bool
	Lockout          bool
}

type userInfo20 struct {
	name     *uint16
	fullName *uint16
	comment  *uint16
	flags    uint32
	userID   uint32
}

const (
	ufAccountDisable = 0x0002
	ufLockout        = 0x0010
	ufPasswdNotReqd  = 0x0020
)

// LocalUsers zwraca konta lokalne komputera.
func LocalUsers() ([]LocalUser, error) {
	var out []LocalUser
	var resume uint32
	for {
		var buf *byte
		var read, total uint32
		// NetUserEnum(serwer, poziom 20, FILTER_NORMAL_ACCOUNT, bufor, MAX_PREFERRED_LENGTH, ...)
		r, _, _ := procNetUserEnum.Call(0, 20, 2, uintptr(unsafe.Pointer(&buf)), 0xFFFFFFFF,
			uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
		if r != 0 && r != 234 { // ERROR_MORE_DATA
			return out, windows.Errno(r)
		}
		if buf != nil {
			for _, u := range unsafe.Slice((*userInfo20)(unsafe.Pointer(buf)), read) {
				out = append(out, LocalUser{Name: windows.UTF16PtrToString(u.name), RID: u.userID,
					Disabled: u.flags&ufAccountDisable != 0, PasswordRequired: u.flags&ufPasswdNotReqd == 0, Lockout: u.flags&ufLockout != 0})
			}
			windows.NetApiBufferFree(buf)
		}
		if r == 0 {
			return out, nil
		}
	}
}

type localGroupMembersInfo1 struct {
	sid    *windows.SID
	sidUse uint32
	name   *uint16
}

// AdminGroupMembers zwraca nazwy członków grupy Administratorzy (nazwa grupy ustalana z SID S-1-5-32-544,
// więc działa w każdej wersji językowej).
func AdminGroupMembers() ([]string, error) {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	group, _, _, err := sid.LookupAccount("")
	if err != nil {
		return nil, err
	}
	g16, _ := windows.UTF16PtrFromString(group)
	var out []string
	var resume uintptr
	for {
		var buf *byte
		var read, total uint32
		r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(g16)), 1, uintptr(unsafe.Pointer(&buf)), 0xFFFFFFFF,
			uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
		if r != 0 && r != 234 {
			return out, windows.Errno(r)
		}
		if buf != nil {
			for _, m := range unsafe.Slice((*localGroupMembersInfo1)(unsafe.Pointer(buf)), read) {
				out = append(out, windows.UTF16PtrToString(m.name))
			}
			windows.NetApiBufferFree(buf)
		}
		if r == 0 {
			return out, nil
		}
	}
}

// SIDAccountName: "S-1-5-21-..." -> "KOMPUTER\Jan"; pusty napis, gdy SID nieznany.
func SIDAccountName(s string) string {
	sid, err := windows.StringToSid(s)
	if err != nil {
		return ""
	}
	acc, dom, _, err := sid.LookupAccount("")
	if err != nil {
		return ""
	}
	if dom != "" {
		return dom + `\` + acc
	}
	return acc
}
