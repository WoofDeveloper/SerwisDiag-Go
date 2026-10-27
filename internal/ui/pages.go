package ui

import (
	"fmt"
	"slices"
	"strings"

	"serwisdiag/internal/diag"
)

// ---------------------------------------------------------------- menu boczne (NavList)

func (mw *mainWin) initNav() {
	mw.navItems = []navItem{{key: "summary", label: "Podsumowanie", dot: cDotGray}}
	for _, c := range diag.Categories {
		if c.Key != "tests" {
			mw.navItems = append(mw.navItems, navItem{key: c.Key, label: c.Label, dot: cDotGray})
		}
	}
	mw.navItems = append(mw.navItems, navItem{key: "tools", label: "Narzędzia serwisowe", dot: cDotGray})
}

// updateNav: kropka (kolor najgorszego wyniku) i liczba problemów przy kategoriach (Update-SdUiNav).
func (mw *mainWin) updateNav() {
	fs, _ := mw.snapshot()
	hasTests := slices.ContainsFunc(fs, func(f diag.Finding) bool { return f.Category == "tests" })
	present := slices.ContainsFunc(mw.navItems, func(n navItem) bool { return n.key == "tests" })
	if hasTests && !present {
		mw.navItems = slices.Insert(mw.navItems, len(mw.navItems)-1, navItem{key: "tests", label: diag.CategoryLabel("tests"), dot: cDotGray})
	}
	allCrit, allWarn := 0, 0
	for i := range mw.navItems {
		it := &mw.navItems[i]
		if it.key == "summary" || it.key == "tools" {
			continue
		}
		total, crit, warn := 0, 0, 0
		for _, f := range fs {
			if f.Category == it.key {
				total++
				switch f.Status {
				case diag.Crit:
					crit++
				case diag.Warn:
					warn++
				}
			}
		}
		allCrit, allWarn = allCrit+crit, allWarn+warn
		it.dot = cDotGray
		if total > 0 {
			it.dot = cGreen
		}
		if warn > 0 {
			it.dot = cOrange
		}
		if crit > 0 {
			it.dot = cRed
		}
		it.badge = ""
		if crit+warn > 0 {
			it.badge = fmt.Sprint(crit + warn)
		}
	}
	s := &mw.navItems[0]
	switch {
	case allCrit > 0:
		s.dot = cRed
	case allWarn > 0:
		s.dot = cOrange
	case len(fs) > 0:
		s.dot = cGreen
	default:
		s.dot = cDotGray
	}
	s.badge = ""
	if allCrit+allWarn > 0 {
		s.badge = fmt.Sprint(allCrit + allWarn)
	}
	mw.nav.relayout()
}

func (mw *mainWin) layoutNav(d *doc, width int) int {
	f := d.font(130, fwNormal)
	bf := d.font(130, fwBold)
	rowH := d.px(33)
	y := 0
	for i, it := range mw.navItems {
		sel := it.key == mw.selected
		r := d.rect(0, y+d.px(1), width, rowH-d.px(2), none, none, 0, float64(d.px(8)))
		r.id = i + 1
		if sel {
			r.fill = cSelected
		} else {
			r.hoverFill = cHover
		}
		cy := y + rowH/2
		dot := d.px(9)
		d.circle(d.px(10)+dot/2, cy, dot/2, it.dot)
		bw := 0
		if it.badge != "" {
			bw = strWidth(bf, it.badge) + d.px(8)
			d.box(bf, it.badge, width-d.px(10)-bw, y, bw, rowH, cBadgeNav, dtRight)
		}
		lx := d.px(10) + dot + d.px(10)
		d.box(f, it.label, lx, y, width-d.px(10)-bw-lx, rowH, cText, dtLeft)
		y += rowH
	}
	return y
}

func (mw *mainWin) navClick(id int) {
	if id >= 1 && id <= len(mw.navItems) {
		mw.selectPage(mw.navItems[id-1].key)
	}
}

func (mw *mainWin) navKey(vk int) bool {
	i := slices.IndexFunc(mw.navItems, func(n navItem) bool { return n.key == mw.selected })
	switch vk {
	case vkUp:
		i--
	case vkDown:
		i++
	case vkHome:
		i = 0
	case vkEnd:
		i = len(mw.navItems) - 1
	default:
		return false
	}
	if i >= 0 && i < len(mw.navItems) {
		mw.selectPage(mw.navItems[i].key)
	}
	return true
}

func (mw *mainWin) selectPage(key string) {
	if mw.selected != key {
		mw.detail, mw.detailKey = -1, ""
	}
	mw.selected = key
	mw.nav.relayout()
	mw.showPage(key)
}

// showPage pokazuje stronę: podsumowanie, kategorię albo narzędzia (Show-SdUiPage).
func (mw *mainWin) showPage(key string) {
	mw.selected = key
	switch key {
	case "summary":
		mw.sumDoc.relayout()
	case "tools":
		mw.toolsDoc.relayout()
	default:
		mw.updateCategory()
	}
	mw.layout()
	mw.redraw()
}

func (mw *mainWin) updatePageVisibility() {
	cat := mw.selected != "summary" && mw.selected != "tools"
	showWin(mw.sumDoc.h, mw.selected == "summary")
	showWin(mw.toolsDoc.h, mw.selected == "tools")
	showWin(mw.lvFind.h, cat)
	showWin(mw.detDoc.h, cat && mw.L.detTop >= 0)
	showWin(mw.cmbTables, cat && len(mw.catTables) > 0)
	showWin(mw.lvTable.h, cat && len(mw.catTables) > 0)
}

// refreshAll odświeża menu i bieżącą stronę (Refresh-SdUiPage).
func (mw *mainWin) refreshAll() {
	mw.updateNav()
	switch mw.selected {
	case "summary":
		mw.sumDoc.relayout()
	case "tools":
	default:
		mw.updateCategory()
		mw.layout()
	}
}

// snapshot: wyniki bieżącej albo ostatniej diagnostyki (w trakcie - kopia na tę chwilę).
func (mw *mainWin) snapshot() ([]diag.Finding, []diag.Table) {
	if mw.result != nil && !mw.scanning {
		return mw.result.Findings, mw.result.Tables
	}
	if mw.run != nil {
		return mw.run.scan.Snapshot()
	}
	return nil, nil
}

func (mw *mainWin) currentResult() *diag.Result {
	if mw.result != nil && !mw.scanning {
		return mw.result
	}
	if mw.run != nil {
		return mw.run.scan.Result()
	}
	return nil
}

// ---------------------------------------------------------------- podsumowanie (PageSummary)

func (mw *mainWin) layoutSummary(d *doc, width int) int {
	w := width - d.px(8)
	f := d.font(130, fwNormal)
	res := mw.currentResult()
	if res == nil || len(res.Findings) == 0 {
		// karta powitalna
		pad, padY := d.px(26), d.px(22)
		tw := w - 2*pad
		title := layoutText([]span{{text: "Witaj w SerwisDiag", font: d.font(220, fwSemiBold), color: cText}}, tw, 0)
		p1 := layoutText([]span{{text: "Program sprawdza komputer z systemem Windows 10 lub 11: sprzęt, dyski (SMART), baterię, awarie i błędy systemu, sterowniki, zabezpieczenia, aktualizacje, sieć oraz oprogramowanie i autostart.", font: f, color: cGray2}}, tw, d.px(21))
		p2 := layoutText([]span{{text: "Wpisz numer zlecenia i dane klienta (opcjonalnie), wybierz tryb i kliknij „Rozpocznij diagnostykę”. Po zakończeniu raport techniczny i raport dla klienta zapiszą się automatycznie.", font: f, color: cGray2}}, tw, d.px(21))
		p3 := layoutText([]span{{text: "Program pracuje lokalnie i nie wysyła danych o komputerze (łączy się z internetem tylko w testach łączności i wyszukiwaniu sterowników w Windows Update). Testy dodatkowe i narzędzia serwisowe uruchamiasz osobno, świadomie. To wersja Go programu SerwisDiag - jeden plik .exe, bez PowerShella.", font: d.font(125, fwNormal), color: cGray}}, tw, d.px(20))
		h := padY + title.h + d.px(8) + p1.h + d.px(10) + p2.h + d.px(14) + p3.h + padY
		d.rect(0, 0, w, h, cWhite, cBorder, 1, float64(d.px(12)))
		y := padY
		d.block(title, pad, y)
		y += title.h + d.px(8)
		d.block(p1, pad, y)
		y += p1.h + d.px(10)
		d.block(p2, pad, y)
		y += p2.h + d.px(14)
		d.block(p3, pad, y)
		return h + d.px(10)
	}

	// werdykt i liczniki
	v := res.Verdict
	barC, titleC := cGreen, hexc("#067647")
	switch v.Level {
	case diag.Crit:
		barC, titleC = cRed, hexc("#B42318")
	case diag.Warn:
		barC, titleC = cOrange, hexc("#B54708")
	}
	vtext := v.Text
	if mw.scanning {
		vtext = "Diagnostyka w toku - wyniki uzupełniają się na bieżąco..."
	}
	chipF := d.font(120, fwSemiBold)
	type chip struct {
		text string
		bg   [2]col
		w    int
	}
	var chips []chip
	for _, p := range [][2]string{{"crit", "krytyczne"}, {"warn", "ostrzeżenia"}, {"ok", "w porządku"}, {"info", "informacje"}, {"skip", "pominięte"}, {"error", "błędy testów"}} {
		if n := res.Counts[diag.Status(p[0])]; n > 0 {
			t := fmt.Sprintf("%d %s", n, p[1])
			chips = append(chips, chip{t, statusColor(p[0]), strWidth(chipF, t) + d.px(22)})
		}
	}
	inner := w - d.px(9) - d.px(40)
	maxChips := min(d.px(420), inner/2)
	// liczniki zawijane w prawej części (WrapPanel MaxWidth=420)
	chipH := chipF.height + d.px(8)
	var rows [][]chip
	rowW := 0
	cw := 0
	for _, c := range chips {
		need := c.w + d.px(6)
		if len(rows) == 0 || rowW+need > maxChips {
			rows = append(rows, nil)
			rowW = 0
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], c)
		rowW += need
		cw = max(cw, rowW)
	}
	chipsH := len(rows) * (chipH + d.px(6))
	leftW := inner - cw - d.px(10)
	vt := layoutText([]span{{text: v.Title, font: d.font(220, fwBold), color: titleC}}, leftW, 0)
	vx := layoutText([]span{{text: vtext, font: f, color: cGray}}, leftW, 0)
	leftH := vt.h + d.px(3) + vx.h
	ch := max(leftH, chipsH) + 2*d.px(16)
	d.rect(0, 0, w, ch, cWhite, cBorder, 1, float64(d.px(12)))
	bar := d.rect(1, 1, d.px(9), ch-2, barC, none, 0, float64(d.px(11)))
	bar.corners = cornersLeft
	lx := d.px(9) + d.px(20)
	ly := (ch - leftH) / 2
	d.block(vt, lx, ly)
	d.block(vx, lx, ly+vt.h+d.px(3))
	cy := (ch - chipsH) / 2
	for _, row := range rows {
		rw := 0
		for _, c := range row {
			rw += c.w + d.px(6)
		}
		x := w - d.px(20) - rw
		for _, c := range row {
			x += d.px(6)
			cr := d.rect(x, cy+d.px(3), c.w, chipH, c.bg[0], none, 0, float64(chipH)/2)
			_ = cr
			d.box(chipF, c.text, x, cy+d.px(3), c.w, chipH, c.bg[1], dtCenter)
			x += c.w
		}
		cy += chipH + d.px(6)
	}
	y := ch + d.px(12)

	// dane komputera (dwie kolumny)
	m := res.Meta
	var facts [][2]string
	for _, p := range [][2]string{{"Komputer", "Model"}, {"System", "OS"}, {"Procesor", "Cpu"}, {"Pamięć RAM", "Ram"}, {"Dyski", "Disks"}, {"Grafika", "Gpu"}, {"BIOS / UEFI", "Bios"}, {"Nazwa komputera", "ComputerName"}, {"Numer seryjny", "Serial"}, {"Typ obudowy", "Chassis"}} {
		if s := metaText(m, p[1]); s != "" {
			facts = append(facts, [2]string{p[0], s})
		}
	}
	if s := metaText(m, "BatteryHealth"); s != "" && s != "0" {
		facts = append(facts, [2]string{"Kondycja baterii", s + "% pojemności fabrycznej"})
	}
	if s := metaText(m, "Stability"); s != "" {
		facts = append(facts, [2]string{"Indeks stabilności", s + " / 10"})
	}
	if len(facts) > 0 {
		pad := d.px(20)
		head := layoutText([]span{{text: "Dane komputera", font: d.font(150, fwSemiBold), color: cText}}, w-2*pad, 0)
		colW := (w - 2*pad) / 2
		titleW := d.px(118)
		type cellL struct {
			t, v textBlock
		}
		cells := make([]cellL, len(facts))
		for i, fc := range facts {
			cells[i].t = layoutText([]span{{text: fc[0], font: f, color: cGray}}, titleW-d.px(4), 0)
			cells[i].v = layoutText([]span{{text: fc[1], font: f, color: cText}}, colW-titleW-d.px(18), 0)
		}
		gy := d.px(14) + head.h + d.px(8)
		type rowL struct{ y, h int }
		var rl []rowL
		yy := gy
		for i := 0; i < len(cells); i += 2 {
			h := max(cells[i].t.h, cells[i].v.h)
			if i+1 < len(cells) {
				h = max(h, max(cells[i+1].t.h, cells[i+1].v.h))
			}
			h += 2 * d.px(4)
			rl = append(rl, rowL{yy, h})
			yy += h
		}
		cardH := yy + d.px(14)
		d.rect(0, y, w, cardH, cWhite, cBorder, 1, float64(d.px(12)))
		d.block(head, pad, y+d.px(14))
		for i, c := range cells {
			r := rl[i/2]
			x := pad + (i%2)*colW
			d.block(c.t, x, y+r.y+d.px(4))
			d.block(c.v, x+titleW, y+r.y+d.px(4))
		}
		y += cardH
	}

	// problemy i zalecenia
	y += d.px(16)
	head := layoutText([]span{{text: "Wykryte problemy i zalecenia", font: d.font(150, fwSemiBold), color: cText}}, w, 0)
	d.block(head, d.px(4), y)
	y += head.h + d.px(8)
	order := map[string]int{}
	for i, c := range diag.Categories {
		order[c.Key] = i
	}
	var issues []diag.Finding
	for _, fd := range res.Findings {
		if fd.Status == diag.Crit || fd.Status == diag.Warn {
			issues = append(issues, fd)
		}
	}
	slices.SortStableFunc(issues, func(a, b diag.Finding) int {
		ra, rb := 1, 1
		if a.Status == diag.Crit {
			ra = 0
		}
		if b.Status == diag.Crit {
			rb = 0
		}
		if ra != rb {
			return ra - rb
		}
		oa, ok := order[a.Category]
		if !ok {
			oa = 99
		}
		ob, ok := order[b.Category]
		if !ok {
			ob = 99
		}
		return oa - ob
	})
	if len(issues) == 0 && !mw.scanning {
		t := layoutText([]span{{text: "Nie wykryto problemów wymagających działania.", font: f, color: hexc("#067647")}}, w, 0)
		d.block(t, d.px(4), y)
		y += t.h
	}
	for _, is := range issues {
		y += mw.issueCard(d, is, y, w) + d.px(10)
	}
	return y + d.px(10)
}

// issueCard: karta problemu (IssueCard w XAML); zwraca wysokość.
func (mw *mainWin) issueCard(d *doc, fd diag.Finding, y, w int) int {
	f := d.font(130, fwNormal)
	bf := d.font(110, fwBold)
	catF := d.font(120, fwNormal)
	titleF := d.font(140, fwSemiBold)
	barC := hexc("#98A2B3")
	switch fd.Status {
	case diag.Crit:
		barC = cRed
	case diag.Warn:
		barC = cOrange
	}
	clr := statusColor(string(fd.Status))
	x0 := d.px(7) + d.px(14)
	inner := w - x0 - d.px(14)
	label := fd.Status.Label()
	badgeW := strWidth(bf, label) + d.px(16)
	badgeH := bf.height + d.px(4)
	cat := diag.CategoryLabel(fd.Category)
	catW := strWidth(catF, cat)
	titleW := inner - badgeW - d.px(10) - catW - d.px(10)
	title := layoutText([]span{{text: fd.Title, font: titleF, color: cText}}, titleW, 0)
	headH := max(title.h, badgeH)
	val := layoutText([]span{{text: fd.Value, font: f, color: cGray2}}, inner, 0)
	h := d.px(10) + headH + d.px(5) + val.h
	var adv textBlock
	if fd.Advice != "" {
		adv = layoutText([]span{{text: "Zalecenie: ", font: d.font(130, fwSemiBold), color: cText}, {text: fd.Advice, font: f, color: cText}}, inner-2*d.px(10), 0)
		h += d.px(8) + adv.h + 2*d.px(7)
	}
	h += d.px(12)
	d.rect(0, y, w, h, cWhite, cBorder, 1, float64(d.px(9)))
	bar := d.rect(1, y+1, d.px(7), h-2, barC, none, 0, float64(d.px(8)))
	bar.corners = cornersLeft
	yy := y + d.px(10)
	by := yy + (headH-badgeH)/2
	d.rect(x0, by, badgeW, badgeH, clr[0], none, 0, float64(d.px(5)))
	d.box(bf, label, x0, by, badgeW, badgeH, clr[1], dtCenter)
	d.block(title, x0+badgeW+d.px(10), yy+(headH-title.h)/2)
	d.box(catF, cat, w-d.px(14)-catW, yy, catW, headH, cGray, dtRight)
	yy += headH + d.px(5)
	d.block(val, x0, yy)
	yy += val.h
	if fd.Advice != "" {
		yy += d.px(8)
		bh := adv.h + 2*d.px(7)
		d.rect(x0, yy, inner, bh, cAdviceBg, cAdviceBd, 1, float64(d.px(6)))
		d.block(adv, x0+d.px(10), yy+d.px(7))
	}
	return h
}

// metaText: wartość metadanych jako tekst ([string] w PowerShellu); pusta dla braku wartości.
func metaText(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s := strings.TrimSpace(diag.CellText(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

// ---------------------------------------------------------------- strona kategorii (PageCategory)

func (mw *mainWin) updateCategory() {
	key := mw.selected
	fs, ts := mw.snapshot()
	mw.catFind = mw.catFind[:0]
	for _, f := range fs {
		if f.Category == key {
			mw.catFind = append(mw.catFind, f)
		}
	}
	prevTables := len(mw.catTables)
	mw.catTables = mw.catTables[:0]
	for _, t := range ts {
		if t.Category == key {
			mw.catTables = append(mw.catTables, t)
		}
	}
	rows := make([][]string, len(mw.catFind))
	st := make([]string, len(mw.catFind))
	for i, f := range mw.catFind {
		rows[i] = []string{f.Status.Label(), f.Title, oneLine(f.Value)}
		st[i] = string(f.Status)
	}
	mw.lvFind.setRows(rows, st)
	// wybrany wynik zostaje wybrany także po dopisaniu nowych wyników (szukamy go po teście i tytule)
	mw.detail = -1
	if mw.detailKey != "" {
		for i, f := range mw.catFind {
			if f.CheckID+"|"+f.Title == mw.detailKey {
				mw.detail = i
				if mw.lvFind.selected() != i {
					mw.lvFind.selectRow(i)
				}
				break
			}
		}
	}

	names := make([]string, len(mw.catTables))
	for i, t := range mw.catTables {
		names[i] = fmt.Sprintf("%s (%d)", t.Name, len(t.Rows))
	}
	prev := comboSel(mw.cmbTables)
	comboReset(mw.cmbTables, names)
	if len(names) > 0 {
		setComboSel(mw.cmbTables, min(max(prev, 0), len(names)-1))
	}
	mw.updateTable()
	mw.detDoc.relayout()
	if (prevTables > 0) != (len(mw.catTables) > 0) {
		mw.layout()
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

// updateTable pokazuje wybraną tabelę danych szczegółowych (Update-SdUiTable).
func (mw *mainWin) updateTable() {
	idx := comboSel(mw.cmbTables)
	if idx < 0 || idx >= len(mw.catTables) {
		mw.lvTable.setColumns(nil, nil)
		mw.lvTable.setRows(nil, nil)
		mw.tableKey = ""
		return
	}
	t := mw.catTables[idx]
	rows := make([][]string, len(t.Rows))
	for i, r := range t.Rows {
		cells := make([]string, len(t.Columns))
		for j, c := range t.Columns {
			if v := r.Get(c); v != nil {
				cells[j] = oneLine(diag.CellText(v))
			}
		}
		rows[i] = cells
	}
	key := t.Category + "|" + t.Name + "|" + strings.Join(t.Columns, ",")
	if key != mw.tableKey {
		mw.lvTable.setColumns(nil, nil)
		mw.lvTable.rows = rows
		mw.lvTable.cols = t.Columns
		widths := mw.lvTable.autoWidths(mw.ui(), mw.ui(), mw.px(520))
		mw.lvTable.setColumns(t.Columns, widths)
		mw.tableKey = key
	}
	mw.lvTable.setRows(rows, nil)
}

func (mw *mainWin) emptyText() string {
	switch {
	case len(mw.catFind) > 0:
		return "Kliknij wiersz, aby zobaczyć szczegóły i zalecenia"
	case mw.run == nil && mw.result == nil:
		return "Uruchom diagnostykę, aby zobaczyć wyniki"
	}
	return "Brak wyników w tej kategorii"
}

// layoutDetail: szczegóły i zalecenie wybranego wyniku (RowDetailsTemplate w XAML).
func (mw *mainWin) layoutDetail(d *doc, width int) int {
	pad := d.px(16)
	w := width - 2*pad
	y := d.px(10)
	f := d.font(130, fwNormal)
	if mw.detail < 0 || mw.detail >= len(mw.catFind) {
		if len(mw.catFind) > 0 {
			t := layoutText([]span{{text: "Zaznacz wiersz na liście powyżej, aby zobaczyć szczegóły i zalecenia.", font: f, color: cGray}}, w, 0)
			d.block(t, pad, y)
			y += t.h
		}
		return y + d.px(10)
	}
	fd := mw.catFind[mw.detail]
	mw.detailKey = fd.CheckID + "|" + fd.Title
	clr := statusColor(string(fd.Status))
	bf := d.font(110, fwBold)
	label := fd.Status.Label()
	bw, bh := strWidth(bf, label)+d.px(16), bf.height+d.px(4)
	title := layoutText([]span{{text: fd.Title, font: d.font(140, fwSemiBold), color: cText}}, w-bw-d.px(10), 0)
	hh := max(title.h, bh)
	d.rect(pad, y+(hh-bh)/2, bw, bh, clr[0], none, 0, float64(d.px(5)))
	d.box(bf, label, pad, y+(hh-bh)/2, bw, bh, clr[1], dtCenter)
	d.block(title, pad+bw+d.px(10), y+(hh-title.h)/2)
	y += hh + d.px(6)
	if fd.Value != "" {
		v := layoutText([]span{{text: fd.Value, font: f, color: cGray2}}, w, 0)
		d.block(v, pad, y)
		y += v.h + d.px(6)
	}
	if fd.Details != "" {
		t := layoutText([]span{{text: fd.Details, font: d.font(125, fwNormal), color: cGray3}}, w, 0)
		d.block(t, pad, y)
		y += t.h + d.px(6)
	}
	if fd.Advice != "" {
		adv := layoutText([]span{{text: "Zalecenie: ", font: d.font(130, fwSemiBold), color: cText}, {text: fd.Advice, font: f, color: cText}}, w-2*d.px(10), 0)
		bh := adv.h + 2*d.px(7)
		d.rect(pad, y, w, bh, cAdviceBg, cAdviceBd, 1, float64(d.px(6)))
		d.block(adv, pad+d.px(10), y+d.px(7))
		y += bh
	}
	return y + d.px(10)
}

// ---------------------------------------------------------------- narzędzia serwisowe (PageTools)

func (mw *mainWin) layoutTools(d *doc, width int) int {
	w := width - d.px(8)
	f := d.font(130, fwNormal)
	small := d.font(120, fwNormal)
	note := layoutText([]span{{text: "Narzędzia otwierają odpowiednie okna systemu Windows lub konsolę. Operacje oznaczone jako naprawcze zmieniają system - wykonuj je świadomie, najlepiej po kopii danych i punkcie przywracania. Część wymaga uprawnień administratora.", font: f, color: cGray}}, w-d.px(8), 0)
	d.block(note, d.px(4), d.px(2))
	y := d.px(2) + note.h + d.px(10)
	var groups []string
	for _, t := range mw.tools {
		if !slices.Contains(groups, t.group) {
			groups = append(groups, t.group)
		}
	}
	for _, g := range groups {
		padX, padT := d.px(18), d.px(14)
		inner := w - 2*padX
		cols := max(1, (inner+d.px(8))/(d.px(250)+d.px(8)))
		tileW := (inner - (cols-1)*d.px(8)) / cols
		head := layoutText([]span{{text: g, font: d.font(150, fwSemiBold), color: cText}}, inner, 0)
		type tileL struct {
			idx    int
			t1, t2 textBlock
			h      int
		}
		var tiles []tileL
		for i, t := range mw.tools {
			if t.group != g {
				continue
			}
			desc := t.desc
			if t.admin {
				desc += " (wymaga administratora)"
			}
			t1 := layoutText([]span{{text: t.title, font: d.font(130, fwSemiBold), color: cText}}, tileW-2*d.px(12), 0)
			t2 := layoutText([]span{{text: desc, font: small, color: cGray}}, tileW-2*d.px(12), 0)
			tiles = append(tiles, tileL{i, t1, t2, d.px(9) + t1.h + d.px(3) + t2.h + d.px(10)})
		}
		// wiersze kafelków o wysokości najwyższego w wierszu
		cardTop := y
		yy := padT + head.h + d.px(10)
		type placed struct {
			tileL
			x, y, h int
		}
		var pl []placed
		for i := 0; i < len(tiles); i += cols {
			rh := 0
			for j := i; j < min(i+cols, len(tiles)); j++ {
				rh = max(rh, tiles[j].h)
			}
			for j := i; j < min(i+cols, len(tiles)); j++ {
				pl = append(pl, placed{tiles[j], padX + (j-i)*(tileW+d.px(8)), yy, rh})
			}
			yy += rh + d.px(8)
		}
		cardH := yy + d.px(8) - d.px(8) + d.px(8)
		d.rect(0, cardTop, w, cardH, cWhite, cBorder, 1, float64(d.px(12)))
		d.block(head, padX, cardTop+padT)
		for _, p := range pl {
			r := d.rect(p.x, cardTop+p.y, tileW, p.h, cWhite, cBtnBorder, 1, float64(d.px(7)))
			r.id = p.idx + 1
			r.hoverFill, r.hoverBd = cBtnHover, cBlue
			d.block(p.t1, p.x+d.px(12), cardTop+p.y+d.px(9))
			d.block(p.t2, p.x+d.px(12), cardTop+p.y+d.px(9)+p.t1.h+d.px(3))
		}
		y = cardTop + cardH + d.px(12)
	}
	return y
}

func (mw *mainWin) toolClick(id int) {
	if id >= 1 && id <= len(mw.tools) {
		mw.runTool(mw.tools[id-1])
	}
}
