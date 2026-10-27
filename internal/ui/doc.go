package ui

import (
	"unsafe"
)

// doc - przewijana treść rysowana przez program (odpowiednik ScrollViewer ze StackPanel w XAML): karty, akapity,
// plakietki i klikalne kafelki. Treść układa funkcja layout dla bieżącej szerokości.

const none col = 0xFF000000 // brak wypełnienia

type itemKind int

const (
	kRect   itemKind = iota // prostokąt / karta z zaokrągleniem i obramowaniem
	kBlock                  // akapit (textBlock)
	kBox                    // napis w jednym wierszu z wielokropkiem
	kCircle                 // kropka
	kLine                   // pozioma linia 1 px
	kCheck                  // pole wyboru (zaznaczenie: on, wyszarzone: off)
)

type docItem struct {
	kind       itemKind
	x, y, w, h int
	fill       col
	border     col
	bw         int
	r          float64
	corners    int
	alpha      float64
	tb         textBlock
	f          *font
	s          string
	color      col
	align      uint32
	id         int // > 0: klikalny
	hoverFill  col
	hoverBd    col
	on, off    bool // kCheck: zaznaczone / niedostępne
}

type doc struct {
	h, parent uintptr
	dpi       int
	bg        col
	items     []docItem
	height    int
	scroll    int
	layout    func(d *doc, width int) int
	onClick   func(id int)
	onKey     func(vk int) bool
	hover     int
	down      int
	cv        *canvas
	sbShown   bool
	inLayout  bool
	dirty     bool
	focusable bool
}

const docClass = "SerwisDiagGoDoc"

var pShowScrollBar = user32.NewProc("ShowScrollBar")

func newDoc(parent uintptr, bg col, layout func(d *doc, width int) int) *doc {
	registerClass(docClass, idcArrow, csDblClks)
	d := &doc{parent: parent, bg: bg, layout: layout, dpi: dpiOf(parent)}
	d.h = createWindow(d, docClass, "", wsChild|wsVisible|wsVScroll|wsClipSiblings, 0, 0, 0, 10, 10, parent, 0)
	call(pShowScrollBar, d.h, sbVert, 0)
	return d
}

func (d *doc) px(v int) int { return sc(d.dpi).px(v) }

func (d *doc) font(size10, weight int) *font { return getFont(d.dpi, size10, weight) }

// add* - elementy treści (współrzędne treści, bez przewinięcia).
func (d *doc) rect(x, y, w, h int, fill, border col, bw int, r float64) *docItem {
	d.items = append(d.items, docItem{kind: kRect, x: x, y: y, w: w, h: h, fill: fill, border: border, bw: bw, r: r, corners: cornersAll, alpha: 1})
	return &d.items[len(d.items)-1]
}

func (d *doc) block(tb textBlock, x, y int) {
	d.items = append(d.items, docItem{kind: kBlock, x: x, y: y, w: tb.w, h: tb.h, tb: tb})
}

func (d *doc) box(f *font, s string, x, y, w, h int, c col, align uint32) *docItem {
	d.items = append(d.items, docItem{kind: kBox, x: x, y: y, w: w, h: h, f: f, s: s, color: c, align: align})
	return &d.items[len(d.items)-1]
}

func (d *doc) circle(cx, cy, r int, c col) {
	d.items = append(d.items, docItem{kind: kCircle, x: cx - r, y: cy - r, w: 2 * r, h: 2 * r, fill: c})
}

func (d *doc) hline(x, y, w int, c col) {
	d.items = append(d.items, docItem{kind: kLine, x: x, y: y, w: w, h: 1, fill: c})
}

// check: pole wyboru s×s; klikalny wiersz to osobny prostokąt z id (zaznaczenie zmienia właściciel listy).
func (d *doc) check(x, y, s int, on, disabled bool) {
	d.items = append(d.items, docItem{kind: kCheck, x: x, y: y, w: s, h: s, on: on, off: disabled})
}

// relayout układa treść od nowa (zmiana danych albo szerokości); przewinięcie zostaje, w granicach treści.
func (d *doc) relayout() {
	if d.h == 0 {
		return
	}
	if d.inLayout {
		d.dirty = true
		return
	}
	d.inLayout = true
	for i := 0; i < 3; i++ {
		d.dirty = false
		cr := clientRect(d.h)
		d.items = d.items[:0]
		d.height = d.layout(d, cr.w())
		need := d.height > cr.h()
		if need != d.sbShown {
			d.sbShown = need
			v := uintptr(0)
			if need {
				v = 1
			}
			call(pShowScrollBar, d.h, sbVert, v)
			continue
		}
		if !d.dirty {
			break
		}
	}
	d.inLayout = false
	d.updateScroll()
	invalidate(d.h)
}

func (d *doc) updateScroll() {
	cr := clientRect(d.h)
	maxScroll := max(d.height-cr.h(), 0)
	d.scroll = min(max(d.scroll, 0), maxScroll)
	si := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: sifRange | sifPage | sifPos,
		Min: 0, Max: int32(max(d.height-1, 0)), Page: uint32(cr.h()), Pos: int32(d.scroll)}
	call(pSetScrollInfo, d.h, sbVert, uintptr(unsafe.Pointer(&si)), 1)
}

func (d *doc) scrollTo(y int) {
	old := d.scroll
	d.scroll = y
	d.updateScroll()
	if d.scroll != old {
		d.updateHover()
		invalidate(d.h)
	}
}

func (d *doc) hitTest(x, y int) int {
	y += d.scroll
	for i := len(d.items) - 1; i >= 0; i-- {
		it := &d.items[i]
		if it.id > 0 && x >= it.x && x < it.x+it.w && y >= it.y && y < it.y+it.h {
			return it.id
		}
	}
	return 0
}

func (d *doc) updateHover() {
	p := screenToClient(d.h, cursorPos())
	cr := clientRect(d.h)
	id := 0
	if p.X >= 0 && p.Y >= 0 && int(p.X) < cr.w() && int(p.Y) < cr.h() {
		id = d.hitTest(int(p.X), int(p.Y))
	}
	if id != d.hover {
		d.hover = id
		invalidate(d.h)
	}
}

func (d *doc) paint() {
	var ps paintStruct
	hdc := call(pBeginPaint, d.h, uintptr(unsafe.Pointer(&ps)))
	cr := clientRect(d.h)
	d.cv = ensureCanvas(d.cv, cr.w(), cr.h())
	if d.cv != nil {
		cv := d.cv
		cv.oy = d.scroll
		cv.fillRect(0, d.scroll, cr.w(), d.scroll+cr.h(), d.bg)
		for i := range d.items {
			it := &d.items[i]
			if it.y+it.h < d.scroll || it.y > d.scroll+cr.h() {
				continue
			}
			drawItem(cv, it, it.id > 0 && it.id == d.hover)
		}
		cv.oy = 0
		cv.blit(hdc, cr.w(), cr.h())
	}
	call(pEndPaint, d.h, uintptr(unsafe.Pointer(&ps)))
}

func drawItem(cv *canvas, it *docItem, hot bool) {
	switch it.kind {
	case kRect:
		fill, bd := it.fill, it.border
		if hot && it.hoverFill != 0 {
			fill = it.hoverFill
		}
		if hot && it.hoverBd != 0 {
			bd = it.hoverBd
		}
		if fill == none && bd == none {
			return
		}
		if it.bw > 0 && bd != none {
			cv.fillRoundA(it.x, it.y, it.x+it.w, it.y+it.h, it.r, bd, it.corners, it.alpha)
			if fill != none {
				r := it.r - float64(it.bw)
				if r < 0 {
					r = 0
				}
				cv.fillRoundA(it.x+it.bw, it.y+it.bw, it.x+it.w-it.bw, it.y+it.h-it.bw, r, fill, it.corners, it.alpha)
			}
			return
		}
		if fill != none {
			cv.fillRoundA(it.x, it.y, it.x+it.w, it.y+it.h, it.r, fill, it.corners, it.alpha)
		}
	case kBlock:
		cv.block(it.tb, it.x, it.y)
	case kBox:
		cv.textBox(it.f, it.s, it.x, it.y, it.x+it.w, it.y+it.h, it.color, it.align)
	case kCircle:
		cv.fillCircle(float64(it.x)+float64(it.w)/2, float64(it.y)+float64(it.h)/2, float64(it.w)/2, it.fill)
	case kLine:
		cv.fillRect(it.x, it.y, it.x+it.w, it.y+it.h, it.fill)
	case kCheck:
		cv.checkBox(it.x, it.y, it.w, it.on, !it.off)
	}
}

func (d *doc) wheelLines() int {
	var n uint32 = 3
	call(pSystemParametersInfoW, spiGetWheelScroll, 0, uintptr(unsafe.Pointer(&n)), 0)
	return int(max(n, 1))
}

func (d *doc) proc(h uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	switch m {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		d.paint()
		return 0, true
	case wmSize:
		d.relayout()
		return 0, true
	case wmVScroll:
		cr := clientRect(h)
		line := d.px(40)
		switch int(wp & 0xFFFF) {
		case sbLineUp:
			d.scrollTo(d.scroll - line)
		case sbLineDown:
			d.scrollTo(d.scroll + line)
		case sbPageUp:
			d.scrollTo(d.scroll - cr.h())
		case sbPageDown:
			d.scrollTo(d.scroll + cr.h())
		case sbTop:
			d.scrollTo(0)
		case sbBottom:
			d.scrollTo(d.height)
		case sbThumbTrack, sbThumbPosition:
			si := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: sifTrackPos}
			call(pGetScrollInfo, h, sbVert, uintptr(unsafe.Pointer(&si)))
			d.scrollTo(int(si.TrackPos))
		}
		return 0, true
	case wmMouseWheel:
		delta := highWord(wp)
		d.scrollTo(d.scroll - delta*d.wheelLines()*d.px(20)/120)
		return 0, true
	case wmMouseMove:
		trackLeave(h)
		id := d.hitTest(lowWord(lp), highWord(lp))
		if id != d.hover {
			d.hover = id
			invalidate(h)
		}
		return 0, true
	case wmMouseLeave:
		if d.hover != 0 {
			d.hover = 0
			invalidate(h)
		}
		return 0, true
	case wmSetCursor:
		if lowWord(lp) == 1 && d.hover > 0 { // HTCLIENT
			call(pSetCursor, loadCursor(idcHand))
			return 1, true
		}
		return 0, false
	case wmLButtonDown:
		if d.focusable {
			call(pSetFocus, h)
		}
		d.down = d.hitTest(lowWord(lp), highWord(lp))
		if d.down > 0 {
			call(pSetCapture, h)
		}
		return 0, true
	case wmLButtonUp:
		if call(pGetCapture) == h {
			call(pReleaseCapture)
		}
		id := d.hitTest(lowWord(lp), highWord(lp))
		down := d.down
		d.down = 0
		if id > 0 && id == down && d.onClick != nil {
			d.onClick(id)
		}
		return 0, true
	case wmGetDlgCode:
		if d.onKey != nil {
			return dlgcWantArrows, true
		}
		return 0, false
	case wmKeyDown:
		vk := int(wp)
		if d.onKey != nil && d.onKey(vk) {
			return 0, true
		}
		cr := clientRect(h)
		switch vk {
		case vkUp:
			d.scrollTo(d.scroll - d.px(40))
		case vkDown:
			d.scrollTo(d.scroll + d.px(40))
		case vkPrior:
			d.scrollTo(d.scroll - cr.h())
		case vkNext:
			d.scrollTo(d.scroll + cr.h())
		case vkHome:
			d.scrollTo(0)
		case vkEnd:
			d.scrollTo(d.height)
		}
		return 0, true
	case wmSetFocus, wmKillFocus:
		invalidate(h)
		return 0, false
	case wmDestroy:
		d.cv.free()
		d.cv = nil
		return 0, false
	}
	return 0, false
}
