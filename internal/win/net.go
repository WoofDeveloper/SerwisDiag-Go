package win

import (
	"encoding/binary"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Sieć przez IP Helper API (iphlpapi.dll): trasy, ping ICMP bez uprawnień administratora, porty nasłuchujące.

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile      = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle     = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho        = iphlpapi.NewProc("IcmpSendEcho")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// Route - trasa domyślna IPv4 z łączną metryką (trasa + interfejs).
type Route struct {
	IfIndex uint32
	NextHop string
	Metric  uint32
}

// DefaultRoutes zwraca trasy 0.0.0.0/0 (jak Get-NetRoute -DestinationPrefix 0.0.0.0/0 z Get-NetIPInterface).
func DefaultRoutes() ([]Route, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	rows := table.Rows()
	var out []Route
	for i := range rows {
		r := &rows[i]
		if r.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		dst := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.DestinationPrefix.Prefix))
		if dst.Family != windows.AF_INET || dst.Addr != [4]byte{} {
			continue
		}
		nh := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.NextHop))
		metric := r.Metric
		ifr := windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceIndex: r.InterfaceIndex}
		if windows.GetIpInterfaceEntry(&ifr) == nil {
			metric += ifr.Metric
		}
		out = append(out, Route{IfIndex: r.InterfaceIndex, NextHop: net.IP(nh.Addr[:]).String(), Metric: metric})
	}
	return out, nil
}

// Ping wysyła jedno żądanie echa ICMP (IcmpSendEcho - bez uprawnień administratora). Zwraca czas w ms.
func Ping(addr string, timeoutMs uint32) (float64, bool) {
	ip := net.ParseIP(addr).To4()
	if ip == nil {
		ips, err := net.LookupIP(addr)
		if err != nil {
			return 0, false
		}
		for _, x := range ips {
			if ip = x.To4(); ip != nil {
				break
			}
		}
		if ip == nil {
			return 0, false
		}
	}
	h, _, _ := procIcmpCreateFile.Call()
	if h == 0 || h == uintptr(windows.InvalidHandle) {
		return 0, false
	}
	defer procIcmpCloseHandle.Call(h)
	data := []byte("SerwisDiag ping 0123456789abcdef")
	reply := make([]byte, 256+len(data))
	dst := binary.LittleEndian.Uint32(ip) // IPAddr w kolejności sieciowej
	n, _, _ := procIcmpSendEcho.Call(h, uintptr(dst), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeoutMs))
	if n == 0 {
		return 0, false
	}
	status := binary.LittleEndian.Uint32(reply[4:])
	rtt := binary.LittleEndian.Uint32(reply[8:])
	return float64(rtt), status == 0
}

// Listener - port TCP w stanie nasłuchiwania.
type Listener struct {
	Address string
	Port    uint16
	PID     uint32
}

// TCPListeners zwraca porty nasłuchujące IPv4 i IPv6 (jak Get-NetTCPConnection -State Listen).
func TCPListeners() ([]Listener, error) {
	var out []Listener
	for _, af := range []uint32{windows.AF_INET, windows.AF_INET6} {
		var size uint32
		procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), 3, 0) // TCP_TABLE_OWNER_PID_LISTENER
		if size == 0 {
			continue
		}
		buf := make([]byte, size+1024)
		size = uint32(len(buf))
		if r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), 3, 0); r != 0 {
			return out, windows.Errno(r)
		}
		n := binary.LittleEndian.Uint32(buf)
		if af == windows.AF_INET {
			// MIB_TCPROW_OWNER_PID: stan, adres lokalny, port lokalny, adres zdalny, port zdalny, PID (po 4 bajty)
			for i := uint32(0); i < n; i++ {
				r := buf[4+i*24:]
				out = append(out, Listener{Address: net.IP(r[4:8]).String(), Port: uint16(r[8])<<8 | uint16(r[9]), PID: binary.LittleEndian.Uint32(r[20:])})
			}
		} else {
			// MIB_TCP6ROW_OWNER_PID: adres lokalny (16), zakres (4), port (4), adres zdalny (16), zakres (4), port (4), stan (4), PID (4)
			for i := uint32(0); i < n; i++ {
				r := buf[4+i*56:]
				out = append(out, Listener{Address: net.IP(r[0:16]).String(), Port: uint16(r[20])<<8 | uint16(r[21]), PID: binary.LittleEndian.Uint32(r[52:])})
			}
		}
	}
	return out, nil
}
