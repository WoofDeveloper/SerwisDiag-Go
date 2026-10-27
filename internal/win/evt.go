package win

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Dzienniki zdarzeń przez Windows Event Log API (wevtapi.dll) - to samo API, którego używa Get-WinEvent.
// Get-WinEvent formatuje opis (Message) każdego zdarzenia, co przy tysiącach zdarzeń trwa długo; tutaj opis
// powstaje tylko wtedy, gdy test go potrzebuje.

var (
	wevtapi                      = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery                 = wevtapi.NewProc("EvtQuery")
	procEvtNext                  = wevtapi.NewProc("EvtNext")
	procEvtRender                = wevtapi.NewProc("EvtRender")
	procEvtClose                 = wevtapi.NewProc("EvtClose")
	procEvtOpenPublisherMetadata = wevtapi.NewProc("EvtOpenPublisherMetadata")
	procEvtFormatMessage         = wevtapi.NewProc("EvtFormatMessage")
)

const (
	evtQueryChannelPath      = 0x1
	evtQueryReverseDirection = 0x200
	evtRenderEventXML        = 1
	evtFormatMessageEvent    = 1
	errorNoMoreItems         = 259
	errorInsufficientBuffer  = 122
	errorEvtChannelNotFound  = 15007
)

// Event to zdarzenie z dziennika: pola systemowe i dane (EventData / UserData).
type Event struct {
	Provider string
	ID       int
	Level    int
	Time     time.Time // czas lokalny
	Log      string
	RecordID uint64
	Data     map[string]string // EventData: nazwa -> wartość
	Props    []string          // wartości w kolejności (jak $e.Properties)

	msgOnce sync.Once
	msg     string
	hasMsg  bool
}

// EventFilter odpowiada parametrowi -FilterHashtable (dziennik, dostawcy, identyfikatory, poziomy, od kiedy).
type EventFilter struct {
	Logs      []string
	Providers []string
	IDs       []int
	Levels    []int
	Since     time.Time
}

// XPath buduje filtr jak Get-SdEvents: warunki na dostawcę, identyfikator, poziom i czas.
func (f EventFilter) XPath() string {
	var conds []string
	or := func(items []string) string { return "(" + strings.Join(items, " or ") + ")" }
	if len(f.Providers) > 0 {
		var p []string
		for _, n := range f.Providers {
			p = append(p, "Provider[@Name='"+n+"']")
		}
		conds = append(conds, or(p))
	}
	if len(f.IDs) > 0 {
		var p []string
		for _, n := range f.IDs {
			p = append(p, "EventID="+strconv.Itoa(n))
		}
		conds = append(conds, or(p))
	}
	if len(f.Levels) > 0 {
		var p []string
		for _, n := range f.Levels {
			p = append(p, "Level="+strconv.Itoa(n))
		}
		conds = append(conds, or(p))
	}
	if !f.Since.IsZero() {
		conds = append(conds, "TimeCreated[@SystemTime>='"+f.Since.UTC().Format("2006-01-02T15:04:05.000Z")+"']")
	}
	if len(conds) == 0 {
		return "*"
	}
	return "*[System[" + strings.Join(conds, " and ") + "]]"
}

// QueryEvents zwraca do max zdarzeń z dziennika (od najnowszych). withMsg: od razu sformatuj opisy.
func QueryEvents(log, xpath string, max int, withMsg bool) ([]*Event, error) {
	p16, err := windows.UTF16PtrFromString(log)
	if err != nil {
		return nil, err
	}
	q16, err := windows.UTF16PtrFromString(xpath)
	if err != nil {
		return nil, err
	}
	h, _, e1 := procEvtQuery.Call(0, uintptr(unsafe.Pointer(p16)), uintptr(unsafe.Pointer(q16)), evtQueryChannelPath|evtQueryReverseDirection)
	if h == 0 {
		return nil, fmt.Errorf("dziennik %s: %w", log, e1)
	}
	defer procEvtClose.Call(h)

	var out []*Event
	var handles [64]uintptr
	buf := make([]uint16, 4096)
	for len(out) < max {
		want := min(len(handles), max-len(out))
		var n uint32
		r, _, e := procEvtNext.Call(h, uintptr(want), uintptr(unsafe.Pointer(&handles[0])), 10000, 0, uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			if e == syscall.Errno(errorNoMoreItems) {
				break
			}
			return out, fmt.Errorf("dziennik %s: %w", log, e)
		}
		for i := 0; i < int(n); i++ {
			ev, b := renderEvent(handles[i], buf)
			buf = b
			if ev != nil {
				if withMsg {
					ev.msgOnce.Do(func() { ev.msg, ev.hasMsg = formatEventMessage(handles[i], ev.Provider), true })
				}
				out = append(out, ev)
			}
			procEvtClose.Call(handles[i])
		}
	}
	return out, nil
}

// Message zwraca opis zdarzenia w języku systemu (pusty, gdy dostawca nie ma opisu). Gdy nie sformatowano go
// przy odczycie, zdarzenie jest pobierane ponownie po numerze rekordu.
func (e *Event) Message() string {
	e.msgOnce.Do(func() {
		q := fmt.Sprintf("*[System[EventRecordID=%d]]", e.RecordID)
		p16, _ := windows.UTF16PtrFromString(e.Log)
		q16, _ := windows.UTF16PtrFromString(q)
		h, _, _ := procEvtQuery.Call(0, uintptr(unsafe.Pointer(p16)), uintptr(unsafe.Pointer(q16)), evtQueryChannelPath)
		if h == 0 {
			return
		}
		defer procEvtClose.Call(h)
		var eh uintptr
		var n uint32
		if r, _, _ := procEvtNext.Call(h, 1, uintptr(unsafe.Pointer(&eh)), 5000, 0, uintptr(unsafe.Pointer(&n))); r == 0 || n == 0 {
			return
		}
		e.msg = formatEventMessage(eh, e.Provider)
		e.hasMsg = true
		procEvtClose.Call(eh)
	})
	return e.msg
}

func renderEvent(h uintptr, buf []uint16) (*Event, []uint16) {
	for {
		var used, count uint32
		r, _, e := procEvtRender.Call(0, h, evtRenderEventXML, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&count)))
		if r == 0 {
			if e == syscall.Errno(errorInsufficientBuffer) && int(used/2) > len(buf) {
				buf = make([]uint16, used/2+64)
				continue
			}
			return nil, buf
		}
		return parseEventXML(windows.UTF16ToString(buf[:used/2])), buf
	}
}

// parseEventXML odczytuje pola systemowe oraz EventData/UserData z XML zdarzenia.
func parseEventXML(s string) *Event {
	ev := &Event{Data: map[string]string{}}
	dec := xml.NewDecoder(strings.NewReader(s))
	var path []string
	var text bytes.Buffer
	dataName := ""
	inUserData := 0
	hasChild := []bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(hasChild) > 0 {
				hasChild[len(hasChild)-1] = true
			}
			path = append(path, t.Name.Local)
			hasChild = append(hasChild, false)
			text.Reset()
			switch t.Name.Local {
			case "Provider":
				for _, a := range t.Attr {
					if a.Name.Local == "Name" {
						ev.Provider = a.Value
					}
				}
			case "TimeCreated":
				for _, a := range t.Attr {
					if a.Name.Local == "SystemTime" {
						if tm, err := time.Parse(time.RFC3339Nano, a.Value); err == nil {
							ev.Time = tm.Local()
						}
					}
				}
			case "Data":
				dataName = ""
				for _, a := range t.Attr {
					if a.Name.Local == "Name" {
						dataName = a.Value
					}
				}
			case "UserData":
				inUserData = len(path)
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			name := t.Name.Local
			val := text.String()
			leaf := len(hasChild) > 0 && !hasChild[len(hasChild)-1]
			parent := ""
			if len(path) >= 2 {
				parent = path[len(path)-2]
			}
			switch {
			case name == "EventID" && parent == "System":
				ev.ID, _ = strconv.Atoi(strings.TrimSpace(val))
			case name == "Level" && parent == "System":
				ev.Level, _ = strconv.Atoi(strings.TrimSpace(val))
			case name == "EventRecordID" && parent == "System":
				ev.RecordID, _ = strconv.ParseUint(strings.TrimSpace(val), 10, 64)
			case name == "Channel" && parent == "System":
				ev.Log = val
			case name == "Data" && parent == "EventData":
				if dataName != "" {
					ev.Data[dataName] = val
				}
				ev.Props = append(ev.Props, val)
			case inUserData > 0 && len(path) > inUserData+1 && leaf:
				ev.Props = append(ev.Props, val)
				ev.Data[name] = val
			case name == "UserData":
				inUserData = 0
			}
			path = path[:len(path)-1]
			hasChild = hasChild[:len(hasChild)-1]
			text.Reset()
		}
	}
	return ev
}

var (
	pubMu    sync.Mutex
	pubCache = map[string]uintptr{}
)

func publisherMetadata(provider string) uintptr {
	pubMu.Lock()
	defer pubMu.Unlock()
	if h, ok := pubCache[provider]; ok {
		return h
	}
	var h uintptr
	if p16, err := windows.UTF16PtrFromString(provider); err == nil {
		h, _, _ = procEvtOpenPublisherMetadata.Call(0, uintptr(unsafe.Pointer(p16)), 0, 0, 0)
	}
	pubCache[provider] = h
	return h
}

func formatEventMessage(event uintptr, provider string) string {
	pub := publisherMetadata(provider)
	if pub == 0 {
		return ""
	}
	buf := make([]uint16, 1024)
	for {
		var used uint32
		r, _, e := procEvtFormatMessage.Call(pub, event, 0, 0, 0, evtFormatMessageEvent, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)))
		if r == 0 {
			if e == syscall.Errno(errorInsufficientBuffer) && int(used) > len(buf) {
				buf = make([]uint16, used+16)
				continue
			}
			return ""
		}
		return windows.UTF16ToString(buf[:used])
	}
}
