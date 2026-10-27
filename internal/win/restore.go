package win

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	srclient               = windows.NewLazySystemDLL("srclient.dll")
	procSRSetRestorePointW = srclient.NewProc("SRSetRestorePointW")
)

// Rodzaje punktów przywracania (RestorePointType w Checkpoint-Computer).
const (
	RestoreDeviceDriverInstall = 10
	RestoreModifySettings      = 12
)

// CreateRestorePoint tworzy punkt przywracania systemu przez SRSetRestorePointW (jak Checkpoint-Computer).
// Windows tworzy najwyżej jeden punkt na 24 godziny - kolejny w tym czasie pomija bez błędu. Wymaga administratora.
func CreateRestorePoint(desc string, restoreType uint32) error {
	if err := procSRSetRestorePointW.Find(); err != nil {
		return fmt.Errorf("przywracanie systemu jest niedostępne (%v)", err)
	}
	type rpInfo struct {
		EventType   uint32
		RestoreType uint32
		Sequence    int64
		Desc        [256]uint16
	}
	set := func(event uint32, seq int64) (int64, error) {
		in := rpInfo{EventType: event, RestoreType: restoreType, Sequence: seq}
		copy(in.Desc[:255], windows.StringToUTF16(desc))
		var st [12]byte // STATEMGRSTATUS (spakowane): nStatus, llSequenceNumber
		ok, _, _ := procSRSetRestorePointW.Call(uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(&st[0])))
		status := binary.LittleEndian.Uint32(st[0:4])
		if ok == 0 {
			if status == 1058 { // ERROR_SERVICE_DISABLED
				return 0, fmt.Errorf("ochrona systemu (przywracanie systemu) jest wyłączona")
			}
			return 0, windows.Errno(status)
		}
		return int64(binary.LittleEndian.Uint64(st[4:12])), nil
	}
	var err error
	WithCOM(func() {
		var seq int64
		if seq, err = set(100 /*BEGIN_SYSTEM_CHANGE*/, 0); err == nil {
			_, err = set(101 /*END_SYSTEM_CHANGE*/, seq)
		}
	})
	return err
}
