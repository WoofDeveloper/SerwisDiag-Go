package ui

import (
	"math"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------- kolory

// col - kolor 0xRRGGBB (piksel DIB 32-bit w pamięci to BGRA, czyli ta sama liczba).
type col uint32

func hexc(s string) col {
	s = strings.TrimPrefix(s, "#")
	var v uint32
	for _, r := range s {
		v <<= 4
		switch {
		case r >= '0' && r <= '9':
			v |= uint32(r - '0')
		case r >= 'a' && r <= 'f':
			v |= uint32(r-'a') + 10
		case r >= 'A' && r <= 'F':
			v |= uint32(r-'A') + 10
		}
	}
	return col(v & 0xFFFFFF)
}

// cref: COLORREF dla GDI (0x00BBGGRR).
func (c col) cref() uintptr {
	return uintptr((uint32(c)>>16)&0xFF | uint32(c)&0xFF00 | (uint32(c)&0xFF)<<16)
}

func (c col) r() uint32 { return uint32(c) >> 16 & 0xFF }
func (c col) g() uint32 { return uint32(c) >> 8 & 0xFF }
func (c col) b() uint32 { return uint32(c) & 0xFF }

// mix: a*(1-t) + b*t.
func mix(a, b col, t float64) col {
	f := func(x, y uint32) uint32 { return uint32(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return col(f(a.r(), b.r())<<16 | f(a.g(), b.g())<<8 | f(a.b(), b.b()))
}

// Kolory okna WPF (lib\Gui.ps1).
var (
	cBg        = hexc("#F3F5F9")
	cWhite     = hexc("#FFFFFF")
	cText      = hexc("#1B2433")
	cDark      = hexc("#101828")
	cBorder    = hexc("#E4E7EC")
	cGray      = hexc("#667085")
	cGray2     = hexc("#475467")
	cGray3     = hexc("#344054")
	cBlue      = hexc("#0F6CBD")
	cBlueDark  = hexc("#0A3A66")
	cBlueHover = hexc("#0B5A9E")
	cBluePress = hexc("#094A82")
	cHover     = hexc("#F2F4F7")
	cSelected  = hexc("#E0EAFF")
	cAdviceBg  = hexc("#F4F8FF")
	cAdviceBd  = hexc("#D6E4FF")
	cBannerBg  = hexc("#FEF0C7")
	cBannerBd  = hexc("#F5D58B")
	cBannerFg  = hexc("#7A2E0E")
	cRed       = hexc("#D92D20")
	cOrange    = hexc("#F79009")
	cGreen     = hexc("#12B76A")
	cDotGray   = hexc("#D0D5DD")
	cBadgeNav  = hexc("#B54708")
	cBtnBorder = hexc("#D0D5DD")
	cBtnHover  = hexc("#EEF4FB")
)

// statusColors: tło i tekst plakietki statusu ($SdStatusColors).
var statusColors = map[string][2]col{
	"crit":  {hexc("#FEE4E2"), hexc("#B42318")},
	"warn":  {hexc("#FEF0C7"), hexc("#93370D")},
	"ok":    {hexc("#D1FADF"), hexc("#067647")},
	"info":  {hexc("#E0EAFF"), hexc("#175CD3")},
	"skip":  {hexc("#EAECF0"), hexc("#475467")},
	"error": {hexc("#EBE9FE"), hexc("#6941C6")},
}

func statusColor(st string) [2]col {
	if c, ok := statusColors[st]; ok {
		return c
	}
	return statusColors["skip"]
}

// ---------------------------------------------------------------- czcionki

type font struct {
	h       uintptr
	ascent  int
	descent int
	height  int
}

type fontKey struct {
	dpi, size10, weight int
}

var fontCache = map[fontKey]*font{}

// measureDC - kontekst urządzenia do pomiaru tekstu (wątek okien).
var measureDC uintptr

func mdc() uintptr {
	if measureDC == 0 {
		measureDC = call(pCreateCompatibleDC, 0)
	}
	return measureDC
}

// getFont: Segoe UI w rozmiarze size10/10 pikseli WPF (1/96 cala) przy danym DPI; weight 400, 600 (Semibold), 700.
func getFont(dpi, size10, weight int) *font {
	k := fontKey{dpi, size10, weight}
	if f, ok := fontCache[k]; ok {
		return f
	}
	face, w := "Segoe UI", weight
	if weight == fwSemiBold {
		face, w = "Segoe UI Semibold", fwNormal
	}
	h := -int(math.Round(float64(size10) * float64(dpi) / 960))
	hf := call(pCreateFontW, uintptr(h), 0, 0, 0, uintptr(w), 0, 0, 0, 1 /*DEFAULT_CHARSET*/, 0, 0, cleartypeQ, 0, u16p(face))
	f := &font{h: hf}
	dc := mdc()
	old := call(pSelectObject, dc, hf)
	var tm textMetric
	call(pGetTextMetricsW, dc, uintptr(unsafe.Pointer(&tm)))
	call(pSelectObject, dc, old)
	f.ascent, f.descent, f.height = int(tm.Ascent), int(tm.Descent), int(tm.Height+tm.ExternalLeading)
	fontCache[k] = f
	return f
}

var pGetTextExtentExPointW = gdi32.NewProc("GetTextExtentExPointW")

func textWidth(f *font, s []uint16) int {
	if len(s) == 0 {
		return 0
	}
	dc := mdc()
	old := call(pSelectObject, dc, f.h)
	var sz point
	call(pGetTextExtentPoint32W, dc, uintptr(unsafe.Pointer(&s[0])), uintptr(len(s)), uintptr(unsafe.Pointer(&sz)))
	call(pSelectObject, dc, old)
	return int(sz.X)
}

func strWidth(f *font, s string) int { return textWidth(f, utf16.Encode([]rune(s))) }

// fitChars: ile znaków s mieści się w szerokości w.
func fitChars(f *font, s []uint16, w int) int {
	if len(s) == 0 {
		return 0
	}
	dc := mdc()
	old := call(pSelectObject, dc, f.h)
	var fit int32
	var sz point
	call(pGetTextExtentExPointW, dc, uintptr(unsafe.Pointer(&s[0])), uintptr(len(s)), uintptr(max(w, 0)), uintptr(unsafe.Pointer(&fit)), 0, uintptr(unsafe.Pointer(&sz)))
	call(pSelectObject, dc, old)
	return int(fit)
}

// ---------------------------------------------------------------- układanie tekstu z zawijaniem (TextBlock z Run)

// span - fragment akapitu jednym krojem i kolorem; br: nowy wiersz przed fragmentem (LineBreak).
type span struct {
	text  string
	font  *font
	color col
	br    bool
}

type frag struct {
	x, y  int
	text  []uint16
	font  *font
	color col
}

// textBlock - akapit ułożony w danej szerokości: fragmenty względem lewego górnego rogu.
type textBlock struct {
	frags []frag
	w, h  int
}

// layoutText układa fragmenty w szerokości maxW (zawijanie na spacjach, zbyt długie słowa dzielone na znaki).
// lineH > 0: stała wysokość wiersza (LineHeight w XAML).
func layoutText(spans []span, maxW, lineH int) textBlock {
	var tb textBlock
	maxW = max(maxW, 1)
	x, top, lineStart := 0, 0, 0
	asc, desc := 0, 0
	gap := 0 // szerokość spacji przed następnym słowem
	var gapFont *font
	newLine := func(f *font) {
		if asc == 0 && desc == 0 && f != nil {
			asc, desc = f.ascent, f.descent
		}
		h, extra := asc+desc, 0
		if lineH > h {
			extra = (lineH - h) / 2
			h = lineH
		}
		for i := lineStart; i < len(tb.frags); i++ {
			fr := &tb.frags[i]
			fr.y = top + extra + asc - fr.font.ascent
		}
		top += h
		x, asc, desc, gap = 0, 0, 0, 0
		lineStart = len(tb.frags)
	}
	add := func(sp span, word []uint16, w int) {
		g := gap
		if x == 0 {
			g = 0
		}
		gap = 0
		// to samo pismo i jedna spacja: dopisz do poprzedniego fragmentu w wierszu (mniej wywołań GDI)
		if n := len(tb.frags); n > lineStart && g > 0 && gapFont == sp.font {
			p := &tb.frags[n-1]
			if p.font == sp.font && p.color == sp.color && g == strWidth(sp.font, " ") {
				p.text = append(append(p.text, ' '), word...)
				x += g + w
				tb.w = max(tb.w, x)
				return
			}
		}
		tb.frags = append(tb.frags, frag{x: x + g, text: append([]uint16(nil), word...), font: sp.font, color: sp.color})
		asc, desc = max(asc, sp.font.ascent), max(desc, sp.font.descent)
		x += g + w
		tb.w = max(tb.w, x)
	}
	for _, sp := range spans {
		if sp.br {
			newLine(sp.font)
		}
		sw := strWidth(sp.font, " ")
		for li, line := range strings.Split(strings.ReplaceAll(sp.text, "\r\n", "\n"), "\n") {
			if li > 0 {
				newLine(sp.font)
			}
			rs := []rune(line)
			for i := 0; i < len(rs); {
				if rs[i] == ' ' || rs[i] == '\t' {
					gap += sw
					gapFont = sp.font
					i++
					continue
				}
				j := i
				for j < len(rs) && rs[j] != ' ' && rs[j] != '\t' {
					j++
				}
				word := utf16.Encode(rs[i:j])
				i = j
				w := textWidth(sp.font, word)
				if x > 0 && x+gap+w > maxW {
					newLine(nil)
				}
				// słowo dłuższe niż wiersz (ścieżki, adresy): dzielimy na znaki
				for x == 0 && w > maxW && len(word) > 1 {
					n := max(fitChars(sp.font, word, maxW), 1)
					if n >= len(word) {
						break
					}
					add(sp, word[:n], textWidth(sp.font, word[:n]))
					newLine(nil)
					word = word[n:]
					w = textWidth(sp.font, word)
				}
				add(sp, word, w)
			}
		}
	}
	if len(tb.frags) > lineStart {
		newLine(nil)
	}
	tb.h = top
	return tb
}

// ---------------------------------------------------------------- płótno (DIB 32-bit z kontekstem GDI)

type canvas struct {
	dc, bmp, old uintptr
	pix          []uint32
	w, h         int
	ox, oy       int // przesunięcie: współrzędne treści - (ox, oy) = piksel płótna
}

func newCanvas(w, h int) *canvas {
	w, h = max(w, 1), max(h, 1)
	c := &canvas{w: w, h: h}
	bi := bitmapInfoHeader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	c.dc = call(pCreateCompatibleDC, 0)
	c.bmp = call(pCreateDIBSection, c.dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if c.bmp == 0 || bits == nil {
		call(pDeleteDC, c.dc)
		return nil
	}
	c.old = call(pSelectObject, c.dc, c.bmp)
	c.pix = unsafe.Slice((*uint32)(bits), w*h)
	call(pSetBkMode, c.dc, transparentBk)
	return c
}

func (c *canvas) free() {
	if c == nil {
		return
	}
	call(pSelectObject, c.dc, c.old)
	call(pDeleteObject, c.bmp)
	call(pDeleteDC, c.dc)
	c.pix = nil
}

// ensureCanvas: płótno co najmniej w×h (ponownie używane między malowaniami).
func ensureCanvas(c *canvas, w, h int) *canvas {
	if c != nil && c.w >= w && c.h >= h {
		c.ox, c.oy = 0, 0
		return c
	}
	c.free()
	return newCanvas(w+64, h+64)
}

func (c *canvas) flush() { call(pGdiFlush) }

func (c *canvas) set(x, y int, v col) {
	x -= c.ox
	y -= c.oy
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.pix[y*c.w+x] = uint32(v)
	}
}

func (c *canvas) blend(x, y int, v col, a float64) {
	x -= c.ox
	y -= c.oy
	if x < 0 || y < 0 || x >= c.w || y >= c.h || a <= 0 {
		return
	}
	i := y*c.w + x
	if a >= 1 {
		c.pix[i] = uint32(v)
		return
	}
	d := col(c.pix[i] & 0xFFFFFF)
	c.pix[i] = uint32(mix(d, v, a))
}

// clipRange zawęża zakres [a,b) do widocznej części płótna (oś x albo y).
func (c *canvas) clipX(a, b int) (int, int) { return max(a, c.ox), min(b, c.ox+c.w) }
func (c *canvas) clipY(a, b int) (int, int) { return max(a, c.oy), min(b, c.oy+c.h) }

func (c *canvas) fillRect(x0, y0, x1, y1 int, v col) {
	c.flush()
	ya, yb := c.clipY(y0, y1)
	xa, xb := c.clipX(x0, x1)
	for y := ya; y < yb; y++ {
		row := c.pix[(y-c.oy)*c.w:]
		for x := xa; x < xb; x++ {
			row[x-c.ox] = uint32(v)
		}
	}
}

// blendRect: prostokąt półprzezroczysty (np. #26FFFFFF na nagłówku).
func (c *canvas) blendRect(x0, y0, x1, y1 int, v col, a float64) {
	c.flush()
	ya, yb := c.clipY(y0, y1)
	xa, xb := c.clipX(x0, x1)
	for y := ya; y < yb; y++ {
		for x := xa; x < xb; x++ {
			c.blend(x, y, v, a)
		}
	}
}

// gradientH: gradient poziomy (nagłówek okna).
func (c *canvas) gradientH(x0, y0, x1, y1 int, a, b col) {
	c.flush()
	ya, yb := c.clipY(y0, y1)
	xa, xb := c.clipX(x0, x1)
	w := float64(max(x1-x0-1, 1))
	for x := xa; x < xb; x++ {
		v := uint32(mix(a, b, float64(x-x0)/w))
		for y := ya; y < yb; y++ {
			c.pix[(y-c.oy)*c.w+x-c.ox] = v
		}
	}
}

// Narożniki do zaokrąglenia.
const (
	cornerTL = 1 << iota
	cornerTR
	cornerBR
	cornerBL
	cornersAll  = cornerTL | cornerTR | cornerBR | cornerBL
	cornersLeft = cornerTL | cornerBL
)

// fillRound: prostokąt [x0,x1)×[y0,y1) z zaokrąglonymi narożnikami o promieniu r (wygładzone krawędzie),
// alpha < 1: półprzezroczysty.
func (c *canvas) fillRound(x0, y0, x1, y1 int, r float64, v col, corners int) {
	c.fillRoundA(x0, y0, x1, y1, r, v, corners, 1)
}

func (c *canvas) fillRoundA(x0, y0, x1, y1 int, r float64, v col, corners int, alpha float64) {
	if x1 <= x0 || y1 <= y0 {
		return
	}
	c.flush()
	r = math.Min(r, math.Min(float64(x1-x0), float64(y1-y0))/2)
	ri := int(math.Ceil(r))
	ya, yb := c.clipY(y0, y1)
	xa, xb := c.clipX(x0, x1)
	fx0, fy0, fx1, fy1 := float64(x0), float64(y0), float64(x1), float64(y1)
	for y := ya; y < yb; y++ {
		py := float64(y) + 0.5
		top, bottom := y < y0+ri, y >= y1-ri
		for x := xa; x < xb; x++ {
			left, right := x < x0+ri, x >= x1-ri
			cov := 1.0
			if (top || bottom) && (left || right) {
				px := float64(x) + 0.5
				var dx, dy float64
				corner := 0
				switch {
				case top && left:
					corner, dx, dy = cornerTL, math.Min(px-(fx0+r), 0), math.Min(py-(fy0+r), 0)
				case top && right:
					corner, dx, dy = cornerTR, math.Max(px-(fx1-r), 0), math.Min(py-(fy0+r), 0)
				case bottom && right:
					corner, dx, dy = cornerBR, math.Max(px-(fx1-r), 0), math.Max(py-(fy1-r), 0)
				default:
					corner, dx, dy = cornerBL, math.Min(px-(fx0+r), 0), math.Max(py-(fy1-r), 0)
				}
				if corners&corner != 0 {
					cov = math.Max(0, math.Min(1, 0.5-(math.Hypot(dx, dy)-r)))
				}
			}
			if a := cov * alpha; a > 0 {
				c.blend(x, y, v, a)
			}
		}
	}
}

// card: zaokrąglona karta z obramowaniem bw pikseli.
func (c *canvas) card(x0, y0, x1, y1 int, r float64, fill, border col, bw int) {
	if bw > 0 {
		c.fillRound(x0, y0, x1, y1, r, border, cornersAll)
		c.fillRound(x0+bw, y0+bw, x1-bw, y1-bw, math.Max(r-float64(bw), 0), fill, cornersAll)
		return
	}
	c.fillRound(x0, y0, x1, y1, r, fill, cornersAll)
}

// fillCircle: koło o środku (cx, cy) i promieniu rad.
func (c *canvas) fillCircle(cx, cy, rad float64, v col) {
	c.flush()
	ya, yb := c.clipY(int(math.Floor(cy-rad-1)), int(math.Ceil(cy+rad+1)))
	xa, xb := c.clipX(int(math.Floor(cx-rad-1)), int(math.Ceil(cx+rad+1)))
	for y := ya; y < yb; y++ {
		for x := xa; x < xb; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) - rad
			if a := math.Max(0, math.Min(1, 0.5-d)); a > 0 {
				c.blend(x, y, v, a)
			}
		}
	}
}

// stroke: odcinek o grubości w z wygładzonymi krawędziami i zaokrąglonymi końcami (znaczek pola wyboru).
func (c *canvas) stroke(x0, y0, x1, y1, w float64, v col) {
	c.flush()
	r := w / 2
	ya, yb := c.clipY(int(math.Floor(math.Min(y0, y1)-r-1)), int(math.Ceil(math.Max(y0, y1)+r+1)))
	xa, xb := c.clipX(int(math.Floor(math.Min(x0, x1)-r-1)), int(math.Ceil(math.Max(x0, x1)+r+1)))
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	for y := ya; y < yb; y++ {
		for x := xa; x < xb; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			t := 0.0
			if l2 > 0 {
				t = math.Max(0, math.Min(1, ((px-x0)*dx+(py-y0)*dy)/l2))
			}
			d := math.Hypot(px-(x0+t*dx), py-(y0+t*dy)) - r
			if a := math.Max(0, math.Min(1, 0.5-d)); a > 0 {
				c.blend(x, y, v, a)
			}
		}
	}
}

// checkBox: pole wyboru s×s od (x, y) - zaznaczone niebieskie z białym znaczkiem, niedostępne szare.
func (c *canvas) checkBox(x, y, s int, on, enabled bool) {
	fs := float64(s)
	bw := max(1, s/16)
	bd, fill := hexc("#8A94A6"), cWhite
	switch {
	case !enabled:
		bd, fill = hexc("#D0D5DD"), hexc("#F2F4F7")
	case on:
		bd, fill = cBlue, cBlue
	}
	c.fillRound(x, y, x+s, y+s, fs*0.2, bd, cornersAll)
	c.fillRound(x+bw, y+bw, x+s-bw, y+s-bw, math.Max(fs*0.2-float64(bw), 0), fill, cornersAll)
	if on {
		w := math.Max(1.5, fs*0.13)
		fx, fy := float64(x), float64(y)
		c.stroke(fx+fs*0.25, fy+fs*0.52, fx+fs*0.43, fy+fs*0.70, w, cWhite)
		c.stroke(fx+fs*0.43, fy+fs*0.70, fx+fs*0.76, fy+fs*0.32, w, cWhite)
	}
}

// text rysuje napis od (x, y) - lewy górny róg wiersza.
func (c *canvas) text(f *font, s string, x, y int, v col) {
	u := utf16.Encode([]rune(s))
	c.text16(f, u, x, y, v)
}

func (c *canvas) text16(f *font, u []uint16, x, y int, v col) {
	if len(u) == 0 {
		return
	}
	old := call(pSelectObject, c.dc, f.h)
	call(pSetTextColor, c.dc, v.cref())
	call(pExtTextOutW, c.dc, uintptr(x-c.ox), uintptr(y-c.oy), 0, 0, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), 0)
	call(pSelectObject, c.dc, old)
}

// textBox: napis w prostokącie (jeden wiersz, wielokropek); align: dtLeft, dtCenter, dtRight.
func (c *canvas) textBox(f *font, s string, x0, y0, x1, y1 int, v col, align uint32) {
	if s == "" || x1 <= x0 {
		return
	}
	u, _ := windows.UTF16FromString(s)
	r := rect{int32(x0 - c.ox), int32(y0 - c.oy), int32(x1 - c.ox), int32(y1 - c.oy)}
	old := call(pSelectObject, c.dc, f.h)
	call(pSetTextColor, c.dc, v.cref())
	call(pDrawTextW, c.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)),
		uintptr(align|dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis))
	call(pSelectObject, c.dc, old)
}

// block rysuje ułożony akapit od (x, y).
func (c *canvas) block(tb textBlock, x, y int) {
	if y+tb.h < c.oy || y > c.oy+c.h {
		return
	}
	for _, f := range tb.frags {
		c.text16(f.font, f.text, x+f.x, y+f.y, f.color)
	}
}

// blit kopiuje płótno na ekran (w×h pikseli od lewego górnego rogu).
func (c *canvas) blit(dst uintptr, w, h int) {
	c.flush()
	call(pBitBlt, dst, 0, 0, uintptr(w), uintptr(h), c.dc, 0, 0, srcCopy)
}
