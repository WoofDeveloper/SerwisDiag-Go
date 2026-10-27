package diag

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"serwisdiag/internal/win"
)

// Testy kategorii: Sieć (checks\90-network.ps1)

var networkChecks = []*Check{
	{ID: "nw.adapters", Category: "network", Title: "Karty sieciowe i konfiguracja IP", Level: "Quick", Run: checkAdapters},
	{ID: "nw.wifi", Category: "network", Title: "Połączenie Wi-Fi", Level: "Quick", Run: checkWifi},
	{ID: "nw.connectivity", Category: "network", Title: "Łączność z internetem, DNS i czas", Level: "Quick", Run: checkConnectivity},
	{ID: "nw.config", Category: "network", Title: "Proxy, DNS i plik hosts", Level: "Quick", Run: checkNetConfig},
	{ID: "nw.ports", Category: "network", Title: "Porty nasłuchujące", Level: "Full", Run: checkPorts},
}

// NetAdapter - fizyczna karta sieciowa (Get-SdNetAdapters).
type NetAdapter struct {
	Index, IfIndex int64
	Name           string
	Connection     string
	Wifi, Virtual  bool
	StatusCode     int64
	Status         string
	SpeedBps       float64
	Mac            string
	IP, Gateway    []string
	DNS            []string
	DHCP           bool
	Manufacturer   string
}

var (
	wifiNameRx    = rx(`Wi-?Fi|Wireless|802\.11|WLAN|Bezprzewod`)
	wifiConnRx    = rx(`Wi-?Fi|WLAN|Wireless|Bezprzewod`)
	virtualNicRx  = rx(`Virtual|VPN|TAP|TUN|WireGuard|Hyper-V|VMware|VirtualBox|Bluetooth|Loopback|Proton|vEthernet|Miniport|AnyConnect|Fortinet|PANGP|Juniper|SonicWall|OpenVPN|ZeroTier|Tailscale|NordLynx|Wintun|Npcap`)
	ipv4Rx        = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)
	netStatusText = map[int64]string{0: "rozłączony", 1: "łączenie", 2: "połączony", 3: "rozłączanie", 4: "brak sprzętu", 5: "wyłączony", 6: "awaria sprzętu", 7: "brak kabla / sieci", 8: "uwierzytelnianie", 9: "uwierzytelniony", 10: "błąd uwierzytelniania", 11: "nieprawidłowy adres", 12: "wymagane poświadczenia"}
)

func netAdapters(c *Ctx) []*NetAdapter {
	return cached(c, "NetAdapters", func() []*NetAdapter {
		ad := c.Cim(Q{Class: "Win32_NetworkAdapter", Props: []string{"Index", "InterfaceIndex", "Name", "NetConnectionID", "NetConnectionStatus", "Speed", "MACAddress", "Manufacturer"}, Where: "PhysicalAdapter=True"})
		cfg := c.cim("Win32_NetworkAdapterConfiguration", "Index", "IPAddress", "DefaultIPGateway", "DNSServerSearchOrder", "DHCPEnabled")
		var out []*NetAdapter
		for _, a := range ad {
			var cf map[string]any
			for _, x := range cfg {
				if x.Int("Index") == a.Int("Index") {
					cf = x
					break
				}
			}
			name, conn := a.Str("Name"), a.Str("NetConnectionID")
			n := &NetAdapter{Index: a.Int("Index"), IfIndex: a.Int("InterfaceIndex"), Name: name, Connection: conn,
				Wifi: wifiNameRx.MatchString(name) || wifiConnRx.MatchString(conn), Virtual: virtualNicRx.MatchString(name),
				StatusCode: -1, Status: "nieznany", SpeedBps: a.Float("Speed"), Mac: a.Str("MACAddress"), Manufacturer: a.Str("Manufacturer")}
			if a.Has("NetConnectionStatus") {
				n.StatusCode = a.Int("NetConnectionStatus")
			}
			if t, ok := netStatusText[n.StatusCode]; ok {
				n.Status = t
			}
			if cf != nil {
				o := win.WmiObject(cf)
				for _, ip := range o.Strs("IPAddress") {
					if ipv4Rx.MatchString(ip) {
						n.IP = append(n.IP, ip)
					}
				}
				// 0.0.0.0 to trasa "na łączu" (np. VPN), a nie adres routera
				for _, g := range o.Strs("DefaultIPGateway") {
					if ipv4Rx.MatchString(g) && g != "0.0.0.0" {
						n.Gateway = append(n.Gateway, g)
					}
				}
				n.DNS = o.Strs("DNSServerSearchOrder")
				n.DHCP = o.Bool("DHCPEnabled")
			}
			out = append(out, n)
		}
		return out
	})
}

// selectGateway odpowiada Select-SdGateway: brama routera - trasa domyślna o najniższej metryce na połączonej
// karcie fizycznej (VPN to nie router klienta).
func selectGateway(adapters []*NetAdapter, routes []win.Route) string {
	var phys []*NetAdapter
	for _, a := range adapters {
		if a.StatusCode == 2 && !a.Virtual {
			phys = append(phys, a)
		}
	}
	var cand []win.Route
	for _, r := range routes {
		if r.NextHop == "" || r.NextHop == "0.0.0.0" || !ipv4Rx.MatchString(r.NextHop) {
			continue
		}
		for _, a := range phys {
			if a.IfIndex == int64(r.IfIndex) {
				cand = append(cand, r)
				break
			}
		}
	}
	if len(cand) > 0 {
		psSort(cand, func(a, b win.Route) int { return cmpNum(int64(a.Metric), int64(b.Metric)) })
		return cand[0].NextHop
	}
	for _, a := range phys {
		for _, g := range a.Gateway {
			if g != "" && g != "0.0.0.0" {
				return g
			}
		}
	}
	return ""
}

type pingStats struct {
	Sent, Received        int
	LossPct               float64
	Avg, Min, Max, Jitter float64
}

// pingBurst odpowiada Test-SdPingBurst: count rund, w każdej równoległe pingi do wszystkich celów.
func pingBurst(targets []string, count int, timeoutMs uint32) map[string]pingStats {
	rtt := map[string][]float64{}
	for i := 0; i < count; i++ {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, t := range targets {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				if ms, ok := win.Ping(t, timeoutMs); ok {
					mu.Lock()
					rtt[t] = append(rtt[t], ms)
					mu.Unlock()
				}
			}(t)
		}
		wg.Wait()
		if i < count-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	res := map[string]pingStats{}
	for _, t := range targets {
		l := rtt[t]
		s := pingStats{Sent: count, Received: len(l), LossPct: 100 * float64(count-len(l)) / float64(max(count, 1))}
		if len(l) > 0 {
			s.Avg, s.Min, s.Max = avg(l), slices.Min(l), slices.Max(l)
			if len(l) > 1 {
				d := 0.0
				for k := 1; k < len(l); k++ {
					d += math.Abs(l[k] - l[k-1])
				}
				s.Jitter = d / float64(len(l)-1)
			}
		}
		res[t] = s
	}
	return res
}

func checkAdapters(c *Ctx) {
	ad := netAdapters(c)
	if len(ad) == 0 {
		c.Add(F{Title: "Karty sieciowe", Status: Warn, Value: "Nie wykryto fizycznej karty sieciowej",
			Advice: "Sprawdź w Menedżerze urządzeń i BIOS, czy karta sieciowa jest włączona i ma zainstalowany sterownik."})
		return
	}
	var rows []Row
	for _, a := range ad {
		typ := "Ethernet"
		if a.Wifi {
			typ = "Wi-Fi"
		} else if a.Virtual {
			typ = "wirtualna/VPN"
		}
		spd := "-"
		if a.SpeedBps > 0 {
			spd = fmtNum(a.SpeedBps/1e6, 0) + " Mb/s"
		}
		dhcp := "nie"
		if a.DHCP {
			dhcp = "tak"
		}
		rows = append(rows, row("Karta", a.Connection, "Opis", a.Name, "Typ", typ, "Stan", a.Status, "Prędkość", spd,
			"IPv4", strings.Join(a.IP, ", "), "Brama", strings.Join(a.Gateway, ", "), "DNS", strings.Join(a.DNS, ", "), "DHCP", dhcp, "MAC", a.Mac))
	}
	c.AddTable(T{Name: "Karty sieciowe", Rows: rows})

	var connected []*NetAdapter
	for _, a := range ad {
		if a.StatusCode == 2 {
			connected = append(connected, a)
		}
	}
	offline := c.Offline()
	if len(connected) == 0 {
		var d []string
		for _, a := range ad {
			d = append(d, a.Connection+": "+a.Status)
		}
		st, val, adv := Warn, "Brak aktywnego połączenia sieciowego", "Sprawdź kabel / włącznik Wi-Fi, tryb samolotowy, sterownik karty oraz router."
		if offline {
			st, val, adv = Info, val+" (diagnostyka w trybie bez internetu)", ""
		}
		c.Add(F{Title: "Połączenie sieciowe", Status: st, Value: val, Details: strings.Join(d, "\n"), Advice: adv})
	} else {
		var d, names []string
		for _, a := range connected {
			gw := ""
			if len(a.Gateway) > 0 {
				gw = " brama " + strings.Join(a.Gateway, ", ")
			}
			d = append(d, fmt.Sprintf("%s (%s): %s%s", a.Connection, a.Name, strings.Join(a.IP, ", "), gw))
			names = append(names, a.Connection)
		}
		c.Add(F{Title: "Połączenie sieciowe", Status: OK, Value: "Aktywne: " + strings.Join(names, ", "), Details: strings.Join(d, "\n")})
	}
	gigRx := rx(`Gigabit|GbE|1000|Gb`)
	for _, a := range connected {
		var apipa []string
		for _, ip := range a.IP {
			if strings.HasPrefix(ip, "169.254.") {
				apipa = append(apipa, ip)
			}
		}
		switch {
		case len(apipa) > 0:
			c.Add(F{Title: "Adres IP karty " + a.Connection, Status: Crit, Value: "Adres APIPA: " + strings.Join(apipa, ", "),
				Details: "Karta nie uzyskała adresu z serwera DHCP, więc system nadał sobie adres awaryjny 169.254.x.x.",
				Advice:  "Sprawdź router/serwer DHCP, kabel i port; wykonaj „ipconfig /release” i „ipconfig /renew”; zrestartuj router."})
		case !a.Virtual && len(a.IP) > 0 && len(a.Gateway) == 0:
			st := Warn
			if offline {
				st = Info
			}
			c.Add(F{Title: "Brama domyślna karty " + a.Connection, Status: st, Value: "Brak bramy domyślnej", Advice: "Bez bramy domyślnej komputer nie wyjdzie poza sieć lokalną - sprawdź konfigurację IP/DHCP."})
		}
		if !a.Wifi && !a.Virtual && a.SpeedBps > 0 && a.SpeedBps <= 100e6 && gigRx.MatchString(a.Name) {
			c.Add(F{Title: "Prędkość łącza " + a.Connection, Status: Warn, Value: fmtNum(a.SpeedBps/1e6, 0) + " Mb/s przy karcie gigabitowej",
				Advice: "Łącze działa z prędkością 100 Mb/s - wymień kabel (uszkodzone żyły / kategoria poniżej 5e), sprawdź gniazdo i port routera."})
		}
	}
	for _, a := range ad {
		if a.StatusCode == 5 || a.StatusCode == 6 {
			c.Add(F{Title: "Karta " + a.Connection, Status: Info, Value: a.Status,
				Advice: "Karta jest wyłączona lub zgłasza awarię sprzętu - włącz ją w Połączeniach sieciowych (ncpa.cpl) lub sprawdź sterownik."})
		}
	}
}

var (
	kvLineRx   = regexp.MustCompile(`^\s*(.+?)\s*:\s+(.+?)\s*$`)
	percentRx  = regexp.MustCompile(`^\d{1,3}%$`)
	radioRx    = rx(`^802\.11[a-z/ ]*$`)
	channelKey = rx(`^(Channel|Kana)`)
	rateKey    = rx(`\((Mbps|Mb/s)\)`)
)

func checkWifi(c *Ctx) {
	connected := false
	for _, a := range netAdapters(c) {
		if a.Wifi && a.StatusCode == 2 {
			connected = true
		}
	}
	if !connected {
		return
	}
	r := win.Run(15*time.Second, filepath.Join(getenv("SystemRoot"), `System32\netsh.exe`), "wlan", "show", "interfaces")
	type kv struct{ key, val string }
	var pairs []kv
	for _, line := range strings.Split(strings.ReplaceAll(win.OEMString(r.Stdout), "\r", ""), "\n") {
		if m := kvLineRx.FindStringSubmatch(line); m != nil {
			pairs = append(pairs, kv{m[1], m[2]})
		}
	}
	find := func(f func(p kv) bool) string {
		for _, p := range pairs {
			if f(p) {
				return p.val
			}
		}
		return ""
	}
	ssid := find(func(p kv) bool { return eqi(p.key, "SSID") })
	signal := find(func(p kv) bool { return percentRx.MatchString(p.val) })
	radio := find(func(p kv) bool { return radioRx.MatchString(p.val) })
	channel := find(func(p kv) bool { return channelKey.MatchString(p.key) })
	var rates []string
	for _, p := range pairs {
		if rateKey.MatchString(p.key) {
			rates = append(rates, p.val)
		}
	}
	if signal == "" {
		c.Add(F{Title: "Wi-Fi: sygnał", Status: Info, Value: "Nie udało się odczytać parametrów (np. brak dostępu do lokalizacji dla netsh w nowych wersjach Windows)"})
		return
	}
	pct, _ := strconv.Atoi(strings.TrimSuffix(signal, "%"))
	band := ""
	if ch, err := strconv.Atoi(channel); err == nil && channel != "" {
		if ch <= 14 {
			band = "2,4 GHz"
		} else {
			band = "5/6 GHz"
		}
	}
	d := []string{"Sieć (SSID): " + ssid, "Sygnał: " + signal}
	if radio != "" {
		d = append(d, "Standard: "+radio)
	}
	if channel != "" {
		d = append(d, fmt.Sprintf("Kanał: %s (%s)", channel, band))
	}
	if len(rates) >= 2 {
		d = append(d, fmt.Sprintf("Prędkość odbioru / transmisji: %s / %s Mb/s", rates[0], rates[1]))
	}
	rxRate := 0.0
	if len(rates) >= 1 {
		rxRate, _ = strconv.ParseFloat(strings.ReplaceAll(rates[0], ",", "."), 64)
	}
	val := fmt.Sprintf("sygnał %d%%", pct)
	if radio != "" {
		val += ", " + radio
	}
	if band != "" {
		val += ", " + band
	}
	switch {
	case pct < 40:
		c.Add(F{Title: "Wi-Fi: jakość połączenia", Status: Warn, Value: val, Details: strings.Join(d, "\n"),
			Advice: "Słaby sygnał Wi-Fi: zbliż się do routera, zmień kanał/pasmo (5 GHz), usuń przeszkody, rozważ wzmacniacz lub kabel. W laptopach sprawdź też antenę i sterownik karty."})
	case pct < 60 || (rxRate > 0 && rxRate < 54):
		c.Add(F{Title: "Wi-Fi: jakość połączenia", Status: Info, Value: val, Details: strings.Join(d, "\n"), Advice: "Średnia jakość łącza - przy problemach z prędkością rozważ pasmo 5 GHz lub lepsze ustawienie routera."})
	default:
		c.Add(F{Title: "Wi-Fi: jakość połączenia", Status: OK, Value: val, Details: strings.Join(d, "\n")})
	}
}

type probeResult struct {
	ok   bool
	code int
	body string
	date string
	err  string
}

var httpClient = &http.Client{
	Timeout:       6 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS10},
		TLSHandshakeTimeout: 6 * time.Second, DisableKeepAlives: true},
}

// probe: zapytanie HTTP bez przekierowań; każda odpowiedź serwera (także błąd HTTP) oznacza łączność.
func probe(url string) probeResult {
	resp, err := httpClient.Get(url)
	if err != nil {
		return probeResult{err: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return probeResult{ok: true, code: resp.StatusCode, body: string(b), date: resp.Header.Get("Date")}
}

var hostOfRx = regexp.MustCompile(`^https://([^/]+)/.*$`)

func checkConnectivity(c *Ctx) {
	var conn []*NetAdapter
	for _, a := range netAdapters(c) {
		if a.StatusCode == 2 {
			conn = append(conn, a)
		}
	}
	if len(conn) == 0 {
		c.Add(F{Title: "Test łączności", Status: Skip, Value: "Brak aktywnego połączenia sieciowego - testy łączności pominięto."})
		return
	}
	routes, err := win.DefaultRoutes()
	if err != nil {
		c.Log("INFO", "Trasy domyślne: "+err.Error())
	}
	gw := selectGateway(conn, routes)
	offline := c.Offline()
	var targets []string
	if gw != "" {
		targets = append(targets, gw)
	}
	if !offline {
		targets = append(targets, "1.1.1.1", "8.8.8.8")
	}
	p := map[string]pingStats{}
	if len(targets) > 0 {
		p = pingBurst(targets, 4, 1500)
	}
	if gw != "" {
		g := p[gw]
		if g.Received == 0 {
			c.Add(F{Title: "Ping do bramy (routera)", Status: Warn, Value: "Brak odpowiedzi od " + gw, Details: "Router może blokować ICMP - sprawdź wynik testu internetu poniżej.",
				Advice: "Jeśli internet też nie działa: sprawdź kabel/Wi-Fi, zrestartuj router i kartę sieciową."})
		} else {
			st, adv := OK, ""
			if g.LossPct >= 25 {
				st, adv = Warn, "Utrata pakietów już do routera wskazuje na problem lokalny: kabel, sygnał Wi-Fi lub zakłócenia."
			} else if g.Avg > 30 {
				st, adv = Info, "Wysokie opóźnienie do routera - zła jakość sygnału Wi-Fi lub przeciążona sieć lokalna."
			}
			c.Add(F{Title: "Ping do bramy (routera)", Status: st, Value: fmt.Sprintf("%s: śr. %s ms, utrata pakietów %s%%, jitter %s ms", gw, fmtNum(g.Avg, 0), fmtNum(g.LossPct, 0), fmtNum(g.Jitter, 0)), Advice: adv})
		}
	}
	if offline {
		// sieć lokalna sprawdzona powyżej; testy internetu nie mają sensu na stanowisku celowo odłączonym od internetu
		c.Add(F{Title: "Łączność z internetem", Status: Skip, Value: offlineSkipText + " Nie sprawdzano: ping do internetu, DNS, HTTP/HTTPS, zegar."})
		return
	}
	var okTargets []string
	for _, t := range []string{"1.1.1.1", "8.8.8.8"} {
		if p[t].Received > 0 {
			okTargets = append(okTargets, t)
		}
	}
	internetUp := len(okTargets) > 0
	if !internetUp {
		c.Add(F{Title: "Ping do internetu (1.1.1.1, 8.8.8.8)", Status: Crit, Value: "Brak odpowiedzi",
			Details: "Zapora lub operator mogą blokować ICMP - sprawdź wyniki testów HTTP i DNS poniżej.", Advice: "Brak łączności z internetem: sprawdź router, kabel/Wi-Fi, ustawienia IP i DNS, proxy oraz zaporę."})
	} else {
		best := p[okTargets[0]]
		lossMax := 0.0
		for _, t := range okTargets {
			lossMax = math.Max(lossMax, p[t].LossPct)
		}
		st, adv := OK, ""
		if lossMax >= 25 || best.Avg > 120 {
			st, adv = Warn, "Wysokie opóźnienie lub utrata pakietów do internetu - sprawdź jakość łącza, Wi-Fi, obciążenie sieci i usługodawcę."
		} else if best.Avg > 60 {
			st = Info
		}
		c.Add(F{Title: "Ping do internetu (1.1.1.1, 8.8.8.8)", Status: st, Value: fmt.Sprintf("śr. %s ms, utrata pakietów do %s%%, jitter %s ms", fmtNum(best.Avg, 0), fmtNum(lossMax, 0), fmtNum(best.Jitter, 0)), Advice: adv})
	}

	// DNS (z limitem czasu)
	type dnsRes struct {
		host string
		ok   bool
		ms   int64
	}
	var dns []dnsRes
	for _, h := range []string{"www.microsoft.com", "www.google.com"} {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addrs, err := net.DefaultResolver.LookupHost(ctx, h)
		cancel()
		dns = append(dns, dnsRes{h, err == nil && len(addrs) > 0, time.Since(start).Milliseconds()})
	}
	dnsOk, slow := 0, 0
	var parts []string
	for _, d := range dns {
		if d.ok {
			dnsOk++
			parts = append(parts, fmt.Sprintf("%s: %d ms", d.host, d.ms))
			if d.ms > 800 {
				slow++
			}
		} else {
			parts = append(parts, d.host+": BŁĄD")
		}
	}
	txt := strings.Join(parts, "; ")
	switch {
	case dnsOk == 0:
		st, det := Warn, ""
		if internetUp {
			st, det = Crit, "Ping po adresie IP działa, a nazwy nie są rozpoznawane - problem z DNS."
		}
		c.Add(F{Title: "Rozpoznawanie nazw (DNS)", Status: st, Value: "Nie działa: " + txt, Details: det,
			Advice: "Sprawdź serwery DNS w ustawieniach karty (ustaw 1.1.1.1 / 8.8.8.8 jako test), wykonaj „ipconfig /flushdns”; sprawdź plik hosts, VPN i proxy."})
	case dnsOk < len(dns):
		c.Add(F{Title: "Rozpoznawanie nazw (DNS)", Status: Warn, Value: txt, Advice: "Część nazw nie jest rozpoznawana - sprawdź serwery DNS i plik hosts."})
	default:
		st, adv := OK, ""
		if slow > 0 {
			st, adv = Info, "Wolne odpowiedzi DNS - rozważ szybszy serwer DNS (np. 1.1.1.1)."
		}
		c.Add(F{Title: "Rozpoznawanie nazw (DNS)", Status: st, Value: txt, Advice: adv})
	}

	// sonda HTTP (NCSI) i HTTPS + różnica czasu
	httpR := probe("http://www.msftconnecttest.com/connecttest.txt")
	switch {
	case !httpR.ok:
		st := Crit
		if internetUp {
			st = Warn
		}
		c.Add(F{Title: "Dostęp do internetu (HTTP)", Status: st, Value: "Brak odpowiedzi: " + limitText(httpR.err, 90),
			Advice: "Brak dostępu HTTP: sprawdź proxy (Ustawienia > Sieć > Serwer proxy), zaporę, VPN oraz DNS."})
	case strings.TrimSpace(httpR.body) == "Microsoft Connect Test":
		c.Add(F{Title: "Dostęp do internetu (HTTP)", Status: OK, Value: "Test Microsoft Connect: poprawny"})
	default:
		c.Add(F{Title: "Dostęp do internetu (HTTP)", Status: Warn, Value: fmt.Sprintf("Nieoczekiwana odpowiedź (HTTP %d)", httpR.code),
			Details: "Odpowiedź różni się od oczekiwanej - możliwy portal logowania (hotspot), proxy lub filtrowanie treści.",
			Advice:  "Otwórz przeglądarkę i zaloguj się do sieci (portal przechwytujący) albo sprawdź ustawienia proxy/filtra."})
	}
	// HTTPS: kilka niezależnych celów - pojedyncze niepowodzenie nie świadczy o awarii internetu.
	var httpsOk *probeResult
	var failedT []string
	for _, u := range []string{"https://www.cloudflare.com/", "https://www.google.com/generate_204", "https://www.microsoft.com/"} {
		r := probe(u)
		if r.ok {
			httpsOk = &r
			break
		}
		failedT = append(failedT, hostOfRx.ReplaceAllString(u, "$1")+": "+limitText(r.err, 60))
	}
	if httpsOk != nil {
		if t, err := http.ParseTime(httpsOk.date); err == nil {
			skew := time.Since(t).Minutes()
			if math.Abs(skew) > 5 {
				c.Add(F{Title: "Zegar systemowy", Status: Warn, Value: "Różnica względem serwera: " + fmtNum(skew, 0) + " min",
					Details: "Błędny czas psuje działanie certyfikatów HTTPS, logowania do usług i aktualizacji.",
					Advice:  "Ustaw poprawną datę/strefę czasową i zsynchronizuj czas (Ustawienia > Czas i język > Synchronizuj teraz). Sprawdź baterię CMOS na płycie głównej, jeśli zegar się resetuje."})
			} else {
				c.Add(F{Title: "Zegar systemowy", Status: OK, Value: "Różnica względem serwera: " + fmtNum(skew, 1) + " min"})
			}
		}
		det := ""
		if len(failedT) > 0 {
			det = "Nie odpowiedziały (przy poprawnej łączności z pozostałymi): " + strings.Join(failedT, "; ") + "\nPojedyncze witryny mogą nie działać np. przez uszkodzoną trasę IPv6 lub VPN."
		}
		c.Add(F{Title: "Dostęp do internetu (HTTPS)", Status: OK, Value: "Połączenie szyfrowane działa", Details: det})
	} else {
		st := Info
		if httpR.ok {
			st = Warn
		}
		c.Add(F{Title: "Dostęp do internetu (HTTPS)", Status: st, Value: "Brak połączenia szyfrowanego z żadnym z testowanych serwerów", Details: strings.Join(failedT, "\n"),
			Advice: "Jeśli HTTP działa, a HTTPS nie: sprawdź datę i czas, certyfikaty główne, antywirus filtrujący HTTPS oraz obsługę TLS 1.2 w systemie."})
	}
}

var (
	privateDNSRx  = regexp.MustCompile(`^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)`)
	hostsEntryRx  = regexp.MustCompile(`^\s*[^#\s]`)
	hostsLocalRx  = rx(`^\s*(127\.0\.0\.1|::1)\s+localhost\s*$`)
	hostsSuspRx   = rx(`microsoft|windowsupdate|google|facebook|paypal|bank|mbank|pekao|ing\.|santander|kaspersky|avast|eset|bitdefender|malwarebytes|norton|mcafee`)
	knownPublicNS = []string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9", "149.112.112.112", "208.67.222.222", "208.67.220.220", "94.140.14.14", "94.140.15.15"}
)

func checkNetConfig(c *Ctx) {
	const is = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	pe, ps, pac := regInt(is, "ProxyEnable", 0), regStr(is, "ProxyServer", ""), regStr(is, "AutoConfigURL", "")
	switch {
	case pe == 1 && ps != "":
		c.Add(F{Title: "Serwer proxy", Status: Warn, Value: "Włączony: " + ps, Details: "Ręcznie skonfigurowany proxy bywa pozostałością po VPN/oprogramowaniu lub działaniem złośliwego programu.",
			Advice: "Jeśli proxy nie jest wymagane, wyłącz je (Ustawienia > Sieć i internet > Serwer proxy); sprawdź także przeglądarki."})
	case pac != "":
		c.Add(F{Title: "Serwer proxy", Status: Info, Value: "Skrypt automatycznej konfiguracji: " + pac, Advice: "Upewnij się, że skrypt PAC jest zamierzony (sieć firmowa)."})
	default:
		c.Add(F{Title: "Serwer proxy", Status: OK, Value: "Brak ręcznego proxy"})
	}
	var dnsAll []string
	for _, a := range netAdapters(c) {
		if a.StatusCode != 2 {
			continue
		}
		for _, d := range a.DNS {
			if !slices.Contains(dnsAll, d) {
				dnsAll = append(dnsAll, d)
			}
		}
	}
	if len(dnsAll) > 0 {
		var odd []string
		for _, d := range dnsAll {
			if !privateDNSRx.MatchString(d) && !slices.Contains(knownPublicNS, d) {
				odd = append(odd, d)
			}
		}
		det := ""
		if len(odd) > 0 {
			det = "Serwery spoza sieci lokalnej i popularnych publicznych usług: " + strings.Join(odd, ", ") + " (zwykle DNS operatora - jeśli nie, zweryfikuj)."
		}
		c.Add(F{Title: "Serwery DNS", Status: Info, Value: strings.Join(dnsAll, ", "), Details: det})
	}
	hostsPath := filepath.Join(getenv("SystemRoot"), `System32\drivers\etc\hosts`)
	b, err := os.ReadFile(hostsPath)
	if err != nil {
		return
	}
	var custom []string
	for _, l := range strings.Split(strings.ReplaceAll(string(decodeText(b)), "\r", ""), "\n") {
		if hostsEntryRx.MatchString(l) && !hostsLocalRx.MatchString(l) {
			custom = append(custom, l)
		}
	}
	if len(custom) == 0 {
		c.Add(F{Title: "Plik hosts", Status: OK, Value: "Domyślny (bez własnych wpisów)"})
		return
	}
	susp := 0
	for _, l := range custom {
		if hostsSuspRx.MatchString(l) {
			susp++
		}
	}
	st := Info
	if susp > 0 {
		st = Warn
	}
	d := []string{fmt.Sprintf("Własne wpisy: %d", len(custom))}
	for i, l := range custom {
		if i < 12 {
			d = append(d, "  "+limitText(strings.TrimSpace(l), 110))
		}
	}
	if len(custom) > 12 {
		d = append(d, "  ...")
	}
	adv := `Przejrzyj plik hosts (C:\Windows\System32\drivers\etc\hosts). Wpisy przekierowujące strony systemu, banków lub antywirusów są typowe dla złośliwego oprogramowania.`
	if len(custom) > 200 {
		adv += " Bardzo długi plik hosts (listy blokujące) potrafi spowalniać usługę DNS."
	}
	suspTxt := ""
	if susp > 0 {
		suspTxt = fmt.Sprintf("; podejrzane przekierowania: %d", susp)
	}
	c.Add(F{Title: "Plik hosts", Status: st, Value: fmt.Sprintf("Własne wpisy: %d%s", len(custom), suspTxt), Details: strings.Join(d, "\n"), Advice: adv})
}

func checkPorts(c *Ctx) {
	// To samo źródło co Get-NetTCPConnection: gniazdo dwustosowe (np. SMB na :: i 0.0.0.0) jest tu jednym
	// wpisem, a tablice GetExtendedTcpTable pokazują je dla IPv4 i IPv6 osobno.
	var ls []win.Listener
	objs := c.Cim(Q{NS: `root\StandardCimv2`, Class: "MSFT_NetTCPConnection", Props: []string{"LocalAddress", "LocalPort", "OwningProcess"}, Where: "State=2"})
	for _, o := range objs {
		ls = append(ls, win.Listener{Address: o.Str("LocalAddress"), Port: uint16(o.Int("LocalPort")), PID: uint32(o.Int("OwningProcess"))})
	}
	if len(objs) == 0 {
		var err error
		if ls, err = win.TCPListeners(); err != nil && len(ls) == 0 {
			return
		}
	}
	names := map[uint32]string{}
	if procs, err := win.Processes(); err == nil {
		for _, p := range procs {
			names[p.PID] = strings.TrimSuffix(strings.TrimSuffix(p.Name, ".exe"), ".EXE")
		}
	}
	psSort(ls, func(a, b win.Listener) int { return int(a.Port) - int(b.Port) })
	var rows []Row
	var risky []string
	ext := 0
	for _, l := range ls {
		rows = append(rows, row("Port", int64(l.Port), "Adres", l.Address, "Proces", names[l.PID], "PID", int64(l.PID)))
		if l.Address == "0.0.0.0" || l.Address == "::" {
			ext++
			switch l.Port {
			case 21, 23, 3389, 5900, 5985, 445, 139:
				risky = append(risky, fmt.Sprintf("port %d (%s)", l.Port, names[l.PID]))
			}
		}
	}
	c.AddTable(T{Name: "Porty nasłuchujące (TCP)", Rows: rows})
	if len(risky) > 0 {
		c.Add(F{Title: "Porty nasłuchujące", Status: Info, Value: fmt.Sprintf("Porty dostępne z sieci: %d; wrażliwe: %s", ext, strings.Join(risky, ", ")),
			Advice: "Upewnij się, że usługi na wrażliwych portach (RDP 3389, SMB 445, VNC 5900, Telnet 23, FTP 21) są potrzebne i niedostępne z internetu."})
	} else {
		c.Add(F{Title: "Porty nasłuchujące", Status: OK, Value: fmt.Sprintf("Porty nasłuchujące na wszystkich interfejsach: %d", ext)})
	}
}
