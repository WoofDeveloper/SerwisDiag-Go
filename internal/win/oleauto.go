package win

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OLE Automation (IDispatch) - obiekty skryptowe Windows: Windows Update (Microsoft.Update.Session),
// Harmonogram zadań (Schedule.Service) i inne, których PowerShell używa przez New-Object -ComObject.

var (
	iidIDispatch = windows.GUID{Data1: 0x00020400, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidNull      windows.GUID
)

const (
	dispatchMethod      = 1
	dispatchPropertyGet = 2
	dispatchPropertyPut = 4
	localeUserDefault   = 0x400
	dispidPropertyPut   = -3
	dispEException      = 0x80020009
)

// Dispatch to obiekt COM z interfejsem IDispatch. Po użyciu wywołaj Release.
type Dispatch struct{ p unsafe.Pointer }

// CreateObject tworzy obiekt po nazwie (ProgID), np. "Microsoft.Update.Session". Wymaga wątku z COM (WithCOM).
func CreateObject(progID string) (*Dispatch, error) {
	var clsid windows.GUID
	p16, err := windows.UTF16PtrFromString(progID)
	if err != nil {
		return nil, err
	}
	if hr, _, _ := procCLSIDFromProgID.Call(uintptr(unsafe.Pointer(p16)), uintptr(unsafe.Pointer(&clsid))); failed(hr) {
		return nil, hrErr(progID, hr)
	}
	var p unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer|clsctxLocalServer,
		uintptr(unsafe.Pointer(&iidIDispatch)), uintptr(unsafe.Pointer(&p)))
	if failed(hr) {
		return nil, hrErr(progID, hr)
	}
	return &Dispatch{p: p}, nil
}

// Release zwalnia obiekt (bezpieczne dla nil i wielokrotnego wywołania).
func (d *Dispatch) Release() {
	if d != nil && d.p != nil {
		comRelease(d.p)
		d.p = nil
	}
}

type dispParams struct {
	args   unsafe.Pointer
	named  unsafe.Pointer
	cArgs  uint32
	cNamed uint32
}

type excepInfo struct {
	wCode          uint16
	wReserved      uint16
	source         uintptr
	description    uintptr
	helpFile       uintptr
	helpContext    uint32
	reserved       uintptr
	deferredFillIn uintptr
	scode          int32
}

func (d *Dispatch) dispID(name string) (int32, error) {
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	var id int32
	// IDispatch::GetIDsOfNames(IID_NULL, nazwy, liczba, LCID, wynik)
	hr := comCall(d.p, 5, uintptr(unsafe.Pointer(&iidNull)), uintptr(unsafe.Pointer(&n16)), 1, localeUserDefault, uintptr(unsafe.Pointer(&id)))
	if failed(hr) {
		return 0, hrErr(name, hr)
	}
	return id, nil
}

func setArg(v *variant, a any) error {
	switch x := a.(type) {
	case int:
		v.vt = vtI4
		*(*int32)(v.ptr()) = int32(x)
	case int32:
		v.vt = vtI4
		*(*int32)(v.ptr()) = x
	case string:
		v.vt = vtBSTR
		*(*uintptr)(v.ptr()) = sysAllocString(x)
	case bool:
		v.vt = vtBool
		if x {
			*(*int16)(v.ptr()) = -1
		}
	case float64:
		v.vt = vtR8
		*(*float64)(v.ptr()) = x
	case *Dispatch: // obiekt jako argument (np. kolekcja aktualizacji); VariantClear zwolni dodaną referencję
		if x == nil || x.p == nil {
			v.vt = vtNull
			return nil
		}
		comAddRef(x.p)
		v.vt = vtDispatch
		*(*uintptr)(v.ptr()) = uintptr(x.p)
	default:
		return fmt.Errorf("nieobsługiwany typ argumentu %T", a)
	}
	return nil
}

func (d *Dispatch) invoke(name string, flags uint16, args []any) (any, error) {
	if d == nil || d.p == nil {
		return nil, fmt.Errorf("%s: brak obiektu", name)
	}
	id, err := d.dispID(name)
	if err != nil {
		return nil, err
	}
	vargs := make([]variant, len(args))
	defer func() {
		for i := range vargs {
			vargs[i].clear()
		}
	}()
	for i, a := range args { // IDispatch przyjmuje argumenty w odwrotnej kolejności
		if err := setArg(&vargs[len(args)-1-i], a); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	var dp dispParams
	if len(vargs) > 0 {
		dp.args = unsafe.Pointer(&vargs[0])
		dp.cArgs = uint32(len(vargs))
	}
	named := int32(dispidPropertyPut)
	if flags&dispatchPropertyPut != 0 {
		dp.named = unsafe.Pointer(&named)
		dp.cNamed = 1
	}
	var res variant
	var ex excepInfo
	hr := comCall(d.p, 6, uintptr(id), uintptr(unsafe.Pointer(&iidNull)), localeUserDefault, uintptr(flags),
		uintptr(unsafe.Pointer(&dp)), uintptr(unsafe.Pointer(&res)), uintptr(unsafe.Pointer(&ex)), 0)
	if failed(hr) {
		e := &HResultError{Op: name, Code: uint32(hr)}
		if uint32(hr) == dispEException {
			e.Desc = bstrToString(ex.description)
			if ex.scode != 0 {
				e.Code = uint32(ex.scode)
			}
		}
		sysFreeString(ex.source)
		sysFreeString(ex.description)
		sysFreeString(ex.helpFile)
		return nil, e
	}
	defer res.clear()
	return res.value(), nil
}

// Get odczytuje właściwość albo wywołuje metodę z argumentami (jak obj.Nazwa(...) w VBScript).
func (d *Dispatch) Get(name string, args ...any) (any, error) {
	return d.invoke(name, dispatchMethod|dispatchPropertyGet, args)
}

// Put ustawia właściwość.
func (d *Dispatch) Put(name string, value any) error {
	_, err := d.invoke(name, dispatchPropertyPut, []any{value})
	return err
}

// PutIdx ustawia właściwość z indeksem, np. FirewallEnabled(profil) = true: args - indeksy, a na końcu wartość.
func (d *Dispatch) PutIdx(name string, args ...any) error {
	_, err := d.invoke(name, dispatchPropertyPut, args)
	return err
}

// Obj zwraca właściwość lub wynik metody będący obiektem (do zwolnienia przez Release).
func (d *Dispatch) Obj(name string, args ...any) (*Dispatch, error) {
	v, err := d.Get(name, args...)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Dispatch)
	if !ok || o == nil {
		return nil, fmt.Errorf("%s: brak obiektu", name)
	}
	return o, nil
}

// Str, Int, Float, Bool - odczyt właściwości prostych (błąd odczytu daje wartość zerową).
func (d *Dispatch) Str(name string, args ...any) string {
	v, _ := d.Get(name, args...)
	return AnyString(v)
}

func (d *Dispatch) Int(name string, args ...any) int64 {
	v, _ := d.Get(name, args...)
	return AnyInt(v)
}

func (d *Dispatch) Float(name string, args ...any) float64 {
	v, _ := d.Get(name, args...)
	return AnyFloat(v)
}

func (d *Dispatch) Bool(name string, args ...any) bool {
	v, _ := d.Get(name, args...)
	return AnyBool(v)
}

// Value odczytuje dowolną właściwość (np. datę); nil, gdy się nie udało.
func (d *Dispatch) Value(name string, args ...any) any {
	v, _ := d.Get(name, args...)
	return v
}

// Items wywołuje f dla każdego elementu kolekcji (Count + Item(i), indeksy od 0).
func (d *Dispatch) Items(f func(item *Dispatch)) error {
	n, err := d.Get("Count")
	if err != nil {
		return err
	}
	for i := 0; i < int(AnyInt(n)); i++ {
		it, err := d.Obj("Item", i)
		if err != nil {
			return err
		}
		f(it)
		it.Release()
	}
	return nil
}
