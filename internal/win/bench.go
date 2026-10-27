package win

import (
	"math/rand"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DiskBenchResult - wynik testu dysku.
type DiskBenchResult struct {
	SeqWriteMBs, SeqReadMBs         float64
	Rand4kReadIops, Rand4kWriteIops float64
	Err                             string
}

// DiskBench: test dysku bez buforowania systemowego (FILE_FLAG_NO_BUFFERING | WRITE_THROUGH) - mierzy
// urządzenie, a nie pamięć podręczną; plik usuwa się sam po zamknięciu (DELETE_ON_CLOSE).
func DiskBench(path string, sizeMB int, randSeconds int) (r DiskBenchResult) {
	const chunk = 1 << 20
	buf, err := windows.VirtualAlloc(0, chunk, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		r.Err = "VirtualAlloc " + err.Error()
		return
	}
	defer windows.VirtualFree(buf, 0, windows.MEM_RELEASE)
	data := unsafe.Slice((*byte)(unsafe.Pointer(buf)), chunk)
	rand.New(rand.NewSource(12345)).Read(data) // dane losowe - nie do skompresowania przez kontroler SSD

	p16, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.CREATE_ALWAYS, windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		r.Err = "CreateFile " + err.Error()
		return
	}
	defer windows.CloseHandle(h)
	var n uint32
	start := time.Now()
	for i := 0; i < sizeMB; i++ {
		if err := windows.WriteFile(h, data, &n, nil); err != nil {
			r.Err = "WriteFile " + err.Error()
			return
		}
	}
	r.SeqWriteMBs = float64(sizeMB) / max(time.Since(start).Seconds(), 0.001)

	windows.Seek(h, 0, 0)
	start = time.Now()
	for i := 0; i < sizeMB; i++ {
		if err := windows.ReadFile(h, data, &n, nil); err != nil {
			r.Err = "ReadFile " + err.Error()
			return
		}
	}
	r.SeqReadMBs = float64(sizeMB) / max(time.Since(start).Seconds(), 0.001)

	blocks := int64(sizeMB) * (1 << 20) / 4096
	rr := rand.New(rand.NewSource(777))
	small := data[:4096]
	ops := 0
	start = time.Now()
	for time.Since(start).Seconds() < float64(randSeconds) {
		windows.Seek(h, int64(rr.Float64()*float64(blocks))*4096, 0)
		if windows.ReadFile(h, small, &n, nil) != nil {
			break
		}
		ops++
	}
	r.Rand4kReadIops = float64(ops) / max(time.Since(start).Seconds(), 0.001)
	ops = 0
	start = time.Now()
	for time.Since(start).Seconds() < float64(randSeconds) {
		windows.Seek(h, int64(rr.Float64()*float64(blocks))*4096, 0)
		if windows.WriteFile(h, small, &n, nil) != nil {
			break
		}
		ops++
	}
	r.Rand4kWriteIops = float64(ops) / max(time.Since(start).Seconds(), 0.001)
	return
}
