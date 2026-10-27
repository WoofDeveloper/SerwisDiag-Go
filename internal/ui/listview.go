package ui

import (
	"unicode/utf16"
	"unsafe"
)

// listView - lista w widoku szczegółów (DataGrid w XAML) z danymi w programie (LVS_OWNERDATA): system pyta
// o tekst widocznych komórek. statuses != nil: pierwsza kolumna to plakietka statusu, wiersze krytyczne
// i ostrzeżenia mają lekko zabarwione tło (jak RowStyle w XAML).

type listView struct {
	h        uintptr
	dpi      int
	cols     []string
	rows     [][]string
	statuses []string
	il       uintptr
	onSelect func(i int)
	textBuf  []uint16
}

func newListView(parent uintptr, id int, dpi int) *listView {
	lv := &listView{dpi: dpi}
	lv.h = newControl(parent, "SysListView32", "", wsTabStop|lvsReport|lvsSingleSel|lvsShowSelAlways|lvsOwnerData|lvsNoSortHeader, 0, id)
	sendMsg(lv.h, lvmSetExStyle, lvsExFullRowSel|lvsExDoubleBuffer|lvsExLabelTip, lvsExFullRowSel|lvsExDoubleBuffer|lvsExLabelTip)
	explorerTheme(lv.h)
	lv.setRowHeight()
	return lv
}

// setRowHeight: wysokość wiersza ustawiana listą obrazków o tej wysokości (sposób znany z comctl32).
func (lv *listView) setRowHeight() {
	if lv.il != 0 {
		call(pImageListDestroy, lv.il)
	}
	h := sc(lv.dpi).px(28)
	lv.il = call(pImageListCreate, 1, uintptr(h), ilcColor32, 1, 1)
	sendMsg(lv.h, lvmSetImageList, lvsilSmall, lv.il)
}

// setColumns ustawia kolumny; widths w pikselach (0 - dopasuj do zawartości).
func (lv *listView) setColumns(cols []string, widths []int) {
	for sendMsg(lv.h, lvmDeleteColumn, 0, 0) != 0 {
	}
	lv.cols = cols
	for i, c := range cols {
		w := 100
		if i < len(widths) {
			w = widths[i]
		}
		lc := lvColumn{Mask: lvcfText | lvcfWidth | lvcfSubItem | lvcfFmt, Cx: int32(w), Text: u16(c), SubItem: int32(i)}
		sendMsg(lv.h, lvmInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&lc)))
	}
}

func (lv *listView) setRows(rows [][]string, statuses []string) {
	lv.rows, lv.statuses = rows, statuses
	sendMsg(lv.h, lvmSetItemCount, uintptr(len(rows)), 0)
	invalidate(lv.h)
}

func (lv *listView) selected() int {
	return int(int32(sendMsg(lv.h, lvmGetNextItem, ^uintptr(0), lvniSelected)))
}

func (lv *listView) selectRow(i int) {
	it := lvItem{Mask: lvifState, State: lvisSelected | lvisFocused, StateMask: lvisSelected | lvisFocused}
	sendMsg(lv.h, lvmSetItemState, uintptr(i), uintptr(unsafe.Pointer(&it)))
	sendMsg(lv.h, lvmEnsureVisible, uintptr(i), 0)
}

func (lv *listView) colWidth(i int) int { return int(sendMsg(lv.h, lvmGetColumnWidth, uintptr(i), 0)) }

func (lv *listView) setColWidth(i, w int) { sendMsg(lv.h, lvmSetColumnWidth, uintptr(i), uintptr(w)) }

// autoWidths: szerokości kolumn wg nagłówka i do 200 wierszy, najwyżej maxW (MaxWidth="520" w XAML).
func (lv *listView) autoWidths(f, hf *font, maxW int) []int {
	pad := sc(lv.dpi).px(22)
	out := make([]int, len(lv.cols))
	for i, c := range lv.cols {
		w := strWidth(hf, c)
		for r := 0; r < len(lv.rows) && r < 200; r++ {
			if i < len(lv.rows[r]) {
				if tw := strWidth(f, lv.rows[r][i]); tw > w {
					w = tw
				}
			}
		}
		out[i] = min(w+pad, maxW)
	}
	return out
}

func (lv *listView) cell(r, c int) string {
	if r < 0 || r >= len(lv.rows) || c < 0 || c >= len(lv.rows[r]) {
		return ""
	}
	if c == 0 && lv.statuses != nil {
		return "" // plakietkę rysuje customDraw
	}
	return lv.rows[r][c]
}

// notify obsługuje WM_NOTIFY tej listy (wywołuje okno nadrzędne).
func (lv *listView) notify(lp uintptr, badgeFont *font) (uintptr, bool) {
	hdr := (*nmhdr)(unsafe.Pointer(lp))
	if hdr.HwndFrom != lv.h {
		return 0, false
	}
	switch hdr.Code {
	case lvnGetDispInfoW:
		di := (*nmLVDispInfo)(unsafe.Pointer(lp))
		if di.Item.Mask&lvifText != 0 && di.Item.Text != 0 && di.Item.TextMax > 0 {
			s := lv.cell(int(di.Item.Item), int(di.Item.SubItem))
			u := utf16.Encode([]rune(s))
			buf := unsafe.Slice((*uint16)(unsafe.Pointer(di.Item.Text)), di.Item.TextMax)
			n := min(len(u), len(buf)-1)
			copy(buf, u[:n])
			buf[n] = 0
		}
		return 0, true
	case lvnItemChanged:
		nl := (*nmListView)(unsafe.Pointer(lp))
		if nl.Changed&lvifState != 0 && (nl.NewState^nl.OldState)&lvisSelected != 0 && lv.onSelect != nil {
			lv.onSelect(lv.selected())
		}
		return 0, true
	case nmCustomDrawN:
		if lv.statuses == nil {
			return cdrfDoDefault, true
		}
		cd := (*nmLVCustomDraw)(unsafe.Pointer(lp))
		switch cd.Nmcd.DrawStage {
		case cddsPrePaint:
			return cdrfNotifyItemDraw, true
		case cddsItemPrePaint:
			return cdrfNotifySubItemDraw, true
		case cddsItemPrePaint | cddsSubItem:
			row := int(cd.Nmcd.ItemSpec)
			st := ""
			if row < len(lv.statuses) {
				st = lv.statuses[row]
			}
			cd.ClrText = uint32(cText.cref())
			selected := lv.selected() == row
			switch {
			case selected:
			case st == "crit":
				cd.ClrTextBk = uint32(hexc("#FFF6F5").cref())
			case st == "warn":
				cd.ClrTextBk = uint32(hexc("#FFFBF0").cref())
			default:
				cd.ClrTextBk = uint32(cWhite.cref())
			}
			if cd.SubItem == 0 {
				return cdrfNewFont | cdrfNotifyPostPaint, true
			}
			return cdrfNewFont, true
		case cddsItemPostPaint | cddsSubItem:
			if cd.SubItem == 0 {
				lv.drawBadge(cd, badgeFont)
			}
			return cdrfDoDefault, true
		}
		return cdrfDoDefault, true
	}
	return 0, false
}

// drawBadge: plakietka statusu w pierwszej kolumnie (BadgeBorder/BadgeText w XAML).
func (lv *listView) drawBadge(cd *nmLVCustomDraw, f *font) {
	row := int(cd.Nmcd.ItemSpec)
	if row < 0 || row >= len(lv.statuses) || len(lv.rows[row]) == 0 {
		return
	}
	// prostokąt komórki: od lewej krawędzi wiersza do początku drugiej kolumny
	r1 := rect{Top: 1, Left: lvirBounds}
	sendMsg(lv.h, lvmGetSubItemRect, uintptr(row), uintptr(unsafe.Pointer(&r1)))
	x0 := int(r1.Left) - lv.colWidth(0)
	cell := rect{Left: int32(x0), Top: r1.Top, Right: r1.Left, Bottom: r1.Bottom}
	w, h := cell.w(), cell.h()
	if w <= 0 || h <= 0 {
		return
	}
	cv := newCanvas(w, h)
	if cv == nil {
		return
	}
	defer cv.free()
	call(pBitBlt, cv.dc, 0, 0, uintptr(w), uintptr(h), cd.Nmcd.Hdc, uintptr(cell.Left), uintptr(cell.Top), srcCopy)
	s := sc(lv.dpi)
	st := lv.statuses[row]
	clr := statusColor(st)
	label := lv.rows[row][0]
	bw := strWidth(f, label) + s.px(16)
	bh := f.height + s.px(4)
	bx, by := s.px(6), (h-bh)/2
	cv.fillRound(bx, by, bx+bw, by+bh, float64(s.px(5)), clr[0], cornersAll)
	cv.textBox(f, label, bx, by, bx+bw, by+bh, clr[1], dtCenter)
	cv.flush()
	call(pBitBlt, cd.Nmcd.Hdc, uintptr(cell.Left), uintptr(cell.Top), uintptr(w), uintptr(h), cv.dc, 0, 0, srcCopy)
}

func (lv *listView) destroy() {
	if lv.il != 0 {
		call(pImageListDestroy, lv.il)
		lv.il = 0
	}
}
