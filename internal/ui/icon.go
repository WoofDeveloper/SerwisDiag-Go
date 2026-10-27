package ui

import (
	"image"
	"math"
	"unsafe"
)

// Ikona programu rysowana w docelowym rozmiarze, jak New-SdMonitorIconBitmap w wersji PowerShell 1.5.0:
// niebieska plakietka z gradientem i białą linią pulsu, a w stanie warn/crit - kółko z wykrzyknikiem w prawym
// dolnym rogu, oddzielone przezroczystym pierścieniem. Zamiast GDI+ liczymy pokrycie pikseli analitycznie
// (odległość od krawędzi kształtu) - wygładzanie jak przy SmoothingMode.AntiAlias, piksel = kwadrat od x do x+1.

type rgbaF struct{ r, g, b, a float64 }

type iconImg struct {
	s   int
	pix []rgbaF
}

// over: kolor (r,g,b) z kryciem a na wierzch piksela (alfa nieprzemnożona).
func (im *iconImg) over(x, y int, r, g, b, a float64) {
	if a <= 0 || x < 0 || y < 0 || x >= im.s || y >= im.s {
		return
	}
	d := &im.pix[y*im.s+x]
	oa := a + d.a*(1-a)
	if oa <= 0 {
		*d = rgbaF{}
		return
	}
	d.r = (r*a + d.r*d.a*(1-a)) / oa
	d.g = (g*a + d.g*d.a*(1-a)) / oa
	d.b = (b*a + d.b*d.a*(1-a)) / oa
	d.a = oa
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// segDist: odległość punktu od odcinka.
func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l := dx*dx + dy*dy
	t := 0.0
	if l > 0 {
		t = clamp01(((px-ax)*dx + (py-ay)*dy) / l)
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func rgbOf(c col) (float64, float64, float64) {
	return float64(c.r()) / 255, float64(c.g()) / 255, float64(c.b()) / 255
}

// drawAppIcon: ikona w rozmiarze size (co najmniej 16 px), state: ok | warn | crit.
func drawAppIcon(size int, state string) *iconImg {
	s := max(size, 16)
	fs := float64(s)
	im := &iconImg{s: s, pix: make([]rgbaF, s*s)}
	round := math.RoundToEven // [math]::Round w .NET

	// plakietka: prostokąt z zaokrąglonymi rogami, gradient od (0,0) do (s,s)
	m := math.Floor(fs * 0.04)
	e := fs - 2*m
	rad := math.Max(2.0, fs*0.11) // promień = połowa średnicy łuku
	cx, cy := m+e/2, m+e/2
	c0r, c0g, c0b := 10.0/255, 58.0/255, 102.0/255
	c1r, c1g, c1b := 15.0/255, 108.0/255, 189.0/255
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			qx := math.Abs(px-cx) - (e/2 - rad)
			qy := math.Abs(py-cy) - (e/2 - rad)
			d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rad
			cov := clamp01(0.5 - d)
			if cov <= 0 {
				continue
			}
			t := clamp01((px + py) / (2 * fs))
			im.over(x, y, c0r+(c1r-c0r)*t, c0g+(c1g-c0g)*t, c0b+(c1b-c0b)*t, cov)
		}
	}

	// linia pulsu: pełne piksele grubości, poziome odcinki na siatce pikseli
	pw := math.Max(2.0, round(fs*0.08))
	yb := fs * 0.52
	if int(pw)%2 == 1 {
		yb = math.Floor(yb) + 0.5
	} else {
		yb = round(yb)
	}
	xs := []float64{0.16, 0.34, 0.44, 0.56, 0.66, 0.74, 0.84}
	ys := []float64{yb, yb, fs * 0.26, fs * 0.76, fs * 0.44, yb, yb}
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			d := math.Inf(1)
			for i := 0; i+1 < len(xs); i++ {
				d = math.Min(d, segDist(px, py, xs[i]*fs, ys[i], xs[i+1]*fs, ys[i+1]))
			}
			if cov := clamp01(pw/2 - d + 0.5); cov > 0 {
				im.over(x, y, 1, 1, 1, cov)
			}
		}
	}

	if state == "warn" || state == "crit" {
		sw := math.Max(1.0, round(fs*0.066)) // grubość wykrzyknika
		bd := round(fs * 0.44)               // średnica plakietki
		if int(bd-sw)%2 != 0 {
			bd++ // wykrzyknik dokładnie na środku, bez półpikseli
		}
		x0 := fs - bd
		c := math.Max(1.0, round(fs/16.0))
		// przezroczysty pierścień pod plakietką (na jasnym i ciemnym pasku zadań)
		hc, hr := x0-c+(bd+2*c)/2, (bd+2*c)/2
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				d := math.Hypot(float64(x)+0.5-hc, float64(y)+0.5-hc) - hr
				if mk := clamp01(0.5 - d); mk > 0 {
					p := &im.pix[y*s+x]
					p.a = math.Floor(p.a*255*(1-mk)) / 255
				}
			}
		}
		fill, mark := cOrange, hexc("#1F1F1F")
		if state == "crit" {
			fill, mark = cRed, cWhite
		}
		fr, fg, fb := rgbOf(fill)
		bc, br := x0+bd/2, bd/2
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				d := math.Hypot(float64(x)+0.5-bc, float64(y)+0.5-bc) - br
				if cov := clamp01(0.5 - d); cov > 0 {
					im.over(x, y, fr, fg, fb, cov)
				}
			}
		}
		// wykrzyknik bez wygładzania
		mr, mg, mb := rgbOf(mark)
		mx := x0 + (bd-sw)/2
		top := x0 + round(bd*0.2)
		h := round(bd * 0.4)
		dot := top + h + math.Max(1.0, round(bd*0.08))
		fillPx := func(x0, y0, w, h float64) {
			for y := int(y0); y < int(y0+h); y++ {
				for x := int(x0); x < int(x0+w); x++ {
					im.over(x, y, mr, mg, mb, 1)
				}
			}
		}
		fillPx(mx, top, sw, h)
		fillPx(mx, dot, sw, sw)
	}
	return im
}

// nrgba: obraz do zapisu PNG (autotest monitora).
func (im *iconImg) nrgba() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, im.s, im.s))
	for i, p := range im.pix {
		out.Pix[i*4] = uint8(math.Round(p.r * 255))
		out.Pix[i*4+1] = uint8(math.Round(p.g * 255))
		out.Pix[i*4+2] = uint8(math.Round(p.b * 255))
		out.Pix[i*4+3] = uint8(math.Round(p.a * 255))
	}
	return out
}

// hicon tworzy uchwyt ikony (32 bity z kanałem alfa i maska) - zwalnia go destroyIcon.
func (im *iconImg) hicon() uintptr {
	s := im.s
	bi := bitmapInfoHeader{Size: 40, Width: int32(s), Height: -int32(s), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	hdc := call(pGetDC, 0)
	color := call(pCreateDIBSection, hdc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	call(pReleaseDC, 0, hdc)
	if color == 0 || bits == nil {
		return 0
	}
	px := unsafe.Slice((*uint32)(bits), s*s)
	stride := (s + 15) / 16 * 2 // wiersze maski wyrównane do słowa
	mask := make([]byte, stride*s)
	for i, p := range im.pix {
		a := uint32(math.Round(p.a * 255))
		px[i] = a<<24 | uint32(math.Round(p.r*255))<<16 | uint32(math.Round(p.g*255))<<8 | uint32(math.Round(p.b*255))
		if a == 0 {
			x, y := i%s, i/s
			mask[y*stride+x/8] |= 0x80 >> (x % 8)
		}
	}
	mbmp := call(pCreateBitmap, uintptr(s), uintptr(s), 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	ii := iconInfo{FIcon: 1, HbmMask: mbmp, HbmColor: color}
	h := call(pCreateIconIndirect, uintptr(unsafe.Pointer(&ii)))
	call(pDeleteObject, mbmp)
	call(pDeleteObject, color)
	return h
}

func destroyIcon(h uintptr) {
	if h != 0 {
		call(pDestroyIcon, h)
	}
}

// appIcon: ikona programu z zasobów pliku .exe (APP, jak assets\app.ico) w rozmiarze size.
func appIcon(size int) uintptr {
	return call(pLoadImageW, hinstance(), u16p("APP"), imageIcon, uintptr(size), uintptr(size), lrDefaultColor)
}
