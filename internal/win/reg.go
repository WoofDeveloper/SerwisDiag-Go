package win

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Rejestr z semantyką Get-ItemProperty: REG_EXPAND_SZ rozwinięte, REG_DWORD jako liczba ze znakiem (Int32),
// REG_MULTI_SZ jako []string, REG_BINARY jako []byte. Ścieżki: "HKLM\...", także w zapisie PowerShella
// ("HKLM:\...", "Registry::HKEY_USERS\...", PSPath "Microsoft.PowerShell.Core\Registry::...").

func splitRegPath(path string) (registry.Key, string, bool) {
	p := path
	if i := strings.Index(p, "::"); i >= 0 {
		p = p[i+2:]
	}
	i := strings.IndexByte(p, '\\')
	root, sub := p, ""
	if i >= 0 {
		root, sub = p[:i], p[i+1:]
	}
	root = strings.TrimSuffix(strings.ToUpper(root), ":")
	switch root {
	case "HKLM", "HKEY_LOCAL_MACHINE":
		return registry.LOCAL_MACHINE, sub, true
	case "HKCU", "HKEY_CURRENT_USER":
		return registry.CURRENT_USER, sub, true
	case "HKU", "HKEY_USERS":
		return registry.USERS, sub, true
	case "HKCR", "HKEY_CLASSES_ROOT":
		return registry.CLASSES_ROOT, sub, true
	}
	return 0, "", false
}

// OpenRegKey otwiera klucz do odczytu (ścieżka "HKLM\..." lub "HKEY_USERS\...").
func OpenRegKey(path string) (registry.Key, error) {
	root, sub, ok := splitRegPath(path)
	if !ok {
		return 0, registry.ErrNotExist
	}
	return registry.OpenKey(root, sub, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
}

// RegKeyExists: czy klucz istnieje (jak Test-Path dla ścieżki rejestru).
func RegKeyExists(path string) bool {
	k, err := OpenRegKey(path)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// RegValue odczytuje wartość; ok = false, gdy brak klucza lub wartości.
func RegValue(path, name string) (any, bool) {
	k, err := OpenRegKey(path)
	if err != nil {
		return nil, false
	}
	defer k.Close()
	return readRegValue(k, name)
}

func readRegValue(k registry.Key, name string) (any, bool) {
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return nil, false
	}
	switch typ {
	case registry.SZ:
		s, _, err := k.GetStringValue(name)
		return s, err == nil
	case registry.EXPAND_SZ:
		s, _, err := k.GetStringValue(name)
		if err != nil {
			return nil, false
		}
		if x, err := registry.ExpandString(s); err == nil {
			s = x
		}
		return s, true
	case registry.MULTI_SZ:
		s, _, err := k.GetStringsValue(name)
		return s, err == nil
	case registry.DWORD:
		n, _, err := k.GetIntegerValue(name)
		return int64(int32(uint32(n))), err == nil
	case registry.QWORD:
		n, _, err := k.GetIntegerValue(name)
		return int64(n), err == nil
	default:
		b, _, err := k.GetBinaryValue(name)
		return b, err == nil
	}
}

// RegValues odczytuje wszystkie wartości klucza w kolejności z rejestru.
func RegValues(path string) (names []string, values map[string]any, ok bool) {
	k, err := OpenRegKey(path)
	if err != nil {
		return nil, nil, false
	}
	defer k.Close()
	names, _ = k.ReadValueNames(-1)
	values = make(map[string]any, len(names))
	for _, n := range names {
		if v, ok := readRegValue(k, n); ok {
			values[strings.ToLower(n)] = v
		}
	}
	return names, values, true
}

// RegSubKeys zwraca nazwy podkluczy (puste, gdy klucz nie istnieje).
func RegSubKeys(path string) []string {
	k, err := OpenRegKey(path)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	return names
}

// ---------------------------------------------------------------- zapis (naprawy i ich cofanie)

// RegRawValue - wartość rejestru bez rozwijania zmiennych środowiskowych, z rodzajem - do przywrócenia jej 1:1.
// Value: DWord -> int64 (ze znakiem, jak Int32), QWord -> int64, teksty -> string, MultiString -> []string,
// pozostałe -> []byte. Kind jak RegistryValueKind w .NET: String, ExpandString, Binary, DWord, MultiString, QWord.
type RegRawValue struct {
	Exists bool
	Value  any
	Kind   string
}

func regKindName(typ uint32) string {
	switch typ {
	case registry.SZ:
		return "String"
	case registry.EXPAND_SZ:
		return "ExpandString"
	case registry.BINARY:
		return "Binary"
	case registry.DWORD:
		return "DWord"
	case registry.MULTI_SZ:
		return "MultiString"
	case registry.QWORD:
		return "QWord"
	case registry.NONE:
		return "None"
	}
	return "Unknown"
}

// RegRaw odczytuje wartość tak, jak jest zapisana (Get-SdRegRaw).
func RegRaw(path, name string) RegRawValue {
	k, err := OpenRegKey(path)
	if err != nil {
		return RegRawValue{}
	}
	defer k.Close()
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return RegRawValue{}
	}
	r := RegRawValue{Exists: true, Kind: regKindName(typ)}
	switch typ {
	case registry.SZ, registry.EXPAND_SZ:
		r.Value, _, _ = k.GetStringValue(name)
	case registry.MULTI_SZ:
		r.Value, _, _ = k.GetStringsValue(name)
	case registry.DWORD:
		n, _, _ := k.GetIntegerValue(name)
		r.Value = int64(int32(uint32(n)))
	case registry.QWORD:
		n, _, _ := k.GetIntegerValue(name)
		r.Value = int64(n)
	default:
		n, _, _ := k.GetValue(name, nil)
		buf := make([]byte, n)
		if n > 0 {
			n, _, _ = k.GetValue(name, buf)
			buf = buf[:n]
		}
		r.Value = buf
	}
	return r
}

// RegSet zapisuje wartość wskazanego rodzaju (New-ItemProperty -PropertyType); brakujący klucz tworzy, a istniejącego
// nie czyści.
func RegSet(path, name string, value any, kind string) error {
	root, sub, ok := splitRegPath(path)
	if !ok {
		return fmt.Errorf("nieobsługiwana ścieżka rejestru: %s", path)
	}
	k, _, err := registry.CreateKey(root, sub, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer k.Close()
	switch kind {
	case "DWord":
		err = k.SetDWordValue(name, uint32(AnyInt(value)))
	case "QWord":
		err = k.SetQWordValue(name, uint64(AnyInt(value)))
	case "String":
		err = k.SetStringValue(name, AnyString(value))
	case "ExpandString":
		err = k.SetExpandStringValue(name, AnyString(value))
	case "MultiString":
		s, _ := value.([]string)
		err = k.SetStringsValue(name, s)
	case "Binary":
		b, _ := value.([]byte)
		err = k.SetBinaryValue(name, b)
	default:
		return fmt.Errorf("nieobsługiwany rodzaj wartości rejestru: %s", kind)
	}
	if err != nil {
		return fmt.Errorf("%s [%s]: %w", path, name, err)
	}
	return nil
}

// RegDeleteValue usuwa wartość (Remove-ItemProperty).
func RegDeleteValue(path, name string) error {
	root, sub, ok := splitRegPath(path)
	if !ok {
		return fmt.Errorf("nieobsługiwana ścieżka rejestru: %s", path)
	}
	k, err := registry.OpenKey(root, sub, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil {
		return fmt.Errorf("%s [%s]: %w", path, name, err)
	}
	return nil
}

// PSRegPath zapisuje ścieżkę rejestru tak jak wersja PowerShell (HKLM:\..., Registry::HKEY_USERS\...) - dzienniki
// napraw obu wersji są wymienne.
func PSRegPath(path string) string {
	p := path
	if i := strings.Index(p, "::"); i >= 0 {
		p = p[i+2:]
	}
	root, sub, _ := strings.Cut(p, `\`)
	switch strings.TrimSuffix(strings.ToUpper(root), ":") {
	case "HKLM", "HKEY_LOCAL_MACHINE":
		return `HKLM:\` + sub
	case "HKCU", "HKEY_CURRENT_USER":
		return `HKCU:\` + sub
	case "HKU", "HKEY_USERS":
		return `Registry::HKEY_USERS\` + sub
	case "HKCR", "HKEY_CLASSES_ROOT":
		return `Registry::HKEY_CLASSES_ROOT\` + sub
	}
	return path
}
