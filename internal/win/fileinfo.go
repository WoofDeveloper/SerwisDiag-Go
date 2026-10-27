package win

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Informacje o plikach wykonywalnych: producent i opis (zasób wersji) oraz wystawca podpisu osadzonego.

// FileVersion zwraca napis FileVersion z zasobu wersji (jak FileVersionInfo.FileVersion w .NET).
func FileVersion(path string) string {
	v, _ := fileVersionString(path, "FileVersion")
	return v
}

// FileVersionStrings zwraca CompanyName i FileDescription (jak FileVersionInfo w .NET).
func FileVersionStrings(path string) (company, description string) {
	return fileVersionString(path, "CompanyName", "FileDescription")
}

func fileVersionString(path, name1 string, name2 ...string) (string, string) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return "", ""
	}
	buf := make([]byte, size)
	block := unsafe.Pointer(&buf[0])
	if windows.GetFileVersionInfo(path, 0, size, block) != nil {
		return "", ""
	}
	var ptr unsafe.Pointer
	var n uint32
	var langs []string
	if windows.VerQueryValue(block, `\VarFileInfo\Translation`, unsafe.Pointer(&ptr), &n) == nil && n >= 4 {
		t := unsafe.Slice((*uint16)(ptr), n/2)
		langs = append(langs, fmt.Sprintf("%04x%04x", t[0], t[1]))
	}
	langs = append(langs, "040904b0", "040904e4", "04090000")
	get := func(name string) string {
		for _, l := range langs {
			if windows.VerQueryValue(block, `\StringFileInfo\`+l+`\`+name, unsafe.Pointer(&ptr), &n) == nil && n > 0 {
				return windows.UTF16PtrToString((*uint16)(ptr))
			}
		}
		return ""
	}
	second := ""
	if len(name2) > 0 {
		second = get(name2[0])
	}
	return get(name1), second
}

var (
	crypt32              = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetParam = crypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = crypt32.NewProc("CryptMsgClose")
)

// EmbeddedSigner zwraca nazwę (CN) wystawcy osadzonego podpisu Authenticode - jak
// X509Certificate.CreateFromSignedFile: bez sprawdzania ważności i bez podpisów katalogowych.
func EmbeddedSigner(path string) (string, bool) {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	var encoding, contentType, formatType uint32
	var store, msg windows.Handle
	err = windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(p16), windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED,
		windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0, &encoding, &contentType, &formatType, &store, &msg, nil)
	if err != nil {
		return "", false
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(msg))
	var size uint32
	procCryptMsgGetParam.Call(uintptr(msg), 6, 0, 0, uintptr(unsafe.Pointer(&size))) // CMSG_SIGNER_INFO_PARAM
	if size == 0 {
		return "", false
	}
	si := make([]byte, size)
	if r, _, _ := procCryptMsgGetParam.Call(uintptr(msg), 6, 0, uintptr(unsafe.Pointer(&si[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return "", false
	}
	// CMSG_SIGNER_INFO: dwVersion, Issuer (CERT_NAME_BLOB), SerialNumber (CRYPT_INTEGER_BLOB), ...
	type signerInfo struct {
		Version      uint32
		Issuer       windows.CertNameBlob
		SerialNumber windows.CryptIntegerBlob
	}
	s := (*signerInfo)(unsafe.Pointer(&si[0]))
	var ci windows.CertInfo
	ci.Issuer = s.Issuer
	ci.SerialNumber = s.SerialNumber
	cert, err := windows.CertFindCertificateInStore(store, encoding, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&ci), nil)
	if err != nil || cert == nil {
		return "", true
	}
	defer windows.CertFreeCertificateContext(cert)
	oid := []byte("2.5.4.3\x00") // szOID_COMMON_NAME
	name := make([]uint16, 512)
	n := windows.CertGetNameString(cert, windows.CERT_NAME_ATTR_TYPE, 0, unsafe.Pointer(&oid[0]), &name[0], uint32(len(name)))
	cn := ""
	if n > 1 {
		cn = windows.UTF16ToString(name[:n])
	}
	if cn == "" {
		n = windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &name[0], uint32(len(name)))
		if n > 1 {
			cn = windows.UTF16ToString(name[:n])
		}
	}
	return strings.Trim(cn, `"`), true
}

// ---------------------------------------------------------------- skróty .lnk

var (
	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// ShortcutTarget zwraca ścieżkę docelową skrótu (jak WScript.Shell CreateShortcut().TargetPath).
// Wymaga wątku z COM (WithCOM).
func ShortcutTarget(path string) (string, bool) {
	var link unsafe.Pointer
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link))); failed(hr) {
		return "", false
	}
	defer comRelease(link)
	var pf unsafe.Pointer
	if hr := comCall(link, 0, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf))); failed(hr) {
		return "", false
	}
	defer comRelease(pf)
	p16, _ := windows.UTF16PtrFromString(path)
	if hr := comCall(pf, 5, uintptr(unsafe.Pointer(p16)), 0); failed(hr) { // IPersistFile::Load(STGM_READ)
		return "", false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	if hr := comCall(link, 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); failed(hr) { // IShellLinkW::GetPath
		return "", false
	}
	return windows.UTF16ToString(buf), true
}
