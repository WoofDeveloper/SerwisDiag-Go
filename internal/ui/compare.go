package ui

import (
	"os"
	"path/filepath"
	"strings"

	"serwisdiag/internal/diag"
)

// Porównanie przed i po naprawie (Show-SdUiCompare): okno podpowiada raport bieżącej diagnostyki jako „po”
// i poprzedni raport tego komputera jako „przed”; naprawy wykonane w programie wpisują się w opis prac.

const (
	ctlCmpBefore = 801 + iota
	ctlCmpBeforePick
	ctlCmpAfter
	ctlCmpAfterPick
	ctlCmpWork
	ctlCmpMake
	ctlCmpCancel
)

const (
	cmpHeader    = "Raport z naprawy: porównanie przed i po"
	cmpInfo      = "Wskaż raporty JSON zapisane przez program (folder raportów). Powstanie raport techniczny i raport dla klienta z listą rozwiązanych i pozostałych problemów."
	cmpWorkLabel = "Wykonane czynności (trafią do obu raportów, np. wymiana dysku, czyszczenie, aktualizacja sterowników)"
)

// toCRLF: znaki nowego wiersza dla pola tekstowego Windows.
func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func (mw *mainWin) showCompare() {
	d := newDialog(mw.h, "Porównanie przed i po naprawie", 660, 450, false)
	dir := diag.ResolveReportDir(mw.opt.Root, mw.settings)
	ticket := strings.TrimSpace(getText(mw.edTicket))
	computer := os.Getenv("COMPUTERNAME")
	after := ""
	if mw.paths != nil && mw.paths.JSON != "" {
		after = mw.paths.JSON
	} else {
		after = diag.FindReportJSON(dir, computer, ticket)
	}
	work := ""
	if len(mw.lastRepair) > 0 {
		work = diag.RepairWorkText(mw.lastRepair)
	}
	edBefore := newEdit(d.h, ctlCmpBefore, diag.FindReportJSON(dir, computer, ticket, after), 0)
	btnBefore := newButton(d.h, ctlCmpBeforePick, "Wybierz...")
	edAfter := newEdit(d.h, ctlCmpAfter, after, 0)
	btnAfter := newButton(d.h, ctlCmpAfterPick, "Wybierz...")
	// bez ES_AUTOHSCROLL - tekst zawija się w polu (TextWrapping="Wrap", AcceptsReturn="True")
	edWork := newControl(d.h, "EDIT", toCRLF(work), wsTabStop|wsVScroll|esMultiline|esAutoVScroll|esWantReturn, wsExClientEdge, ctlCmpWork)
	btnMake := newButton(d.h, ctlCmpMake, "Utwórz porównanie")
	btnCancel := newButton(d.h, ctlCmpCancel, "Anuluj")
	fonts := func() {
		f := d.font(130, fwNormal)
		for _, c := range []uintptr{edBefore, btnBefore, edAfter, btnAfter, edWork, btnMake, btnCancel} {
			setFont(c, f)
		}
		for _, e := range []uintptr{edBefore, edAfter, edWork} {
			setEditMargins(e, d.px(4))
		}
	}
	fonts()
	d.fonts = fonts

	// układ jak w XAML: nagłówek z opisem, dwa wiersze po 38 px (opis 130 px, pole, „Wybierz...”), opis prac
	// z polem do dołu okna, przyciski po prawej
	p := func() int { return d.px(22) }
	info := func(w int) textBlock {
		return layoutText([]span{{text: cmpInfo, font: d.font(130, fwNormal), color: cGray}}, w-2*p(), 0)
	}
	rowsTop := func(w int) int {
		return p() + d.font(160, fwSemiBold).height + d.px(4) + info(w).h + d.px(12)
	}
	workLabel := func(w int) textBlock {
		return layoutText([]span{{text: cmpWorkLabel, font: d.font(130, fwNormal), color: cText}}, w-2*p(), 0)
	}
	d.paint = func(cv *canvas, w, h int) {
		cv.text(d.font(160, fwSemiBold), cmpHeader, p(), p(), cText)
		cv.block(info(w), p(), p()+d.font(160, fwSemiBold).height+d.px(4))
		f := d.font(130, fwNormal)
		top := rowsTop(w)
		for i, l := range []string{"Przed naprawą", "Po naprawie"} {
			cv.text(f, l, p(), top+i*d.px(38)+(d.px(38)-f.height)/2, cText)
		}
		cv.block(workLabel(w), p(), top+2*d.px(38)+d.px(12))
	}
	d.onSize = func(w, h int) {
		f := d.font(130, fwNormal)
		top := rowsTop(w)
		pw := buttonWidth(f, "Wybierz...", d.px(10))
		x := p() + d.px(130)
		for i, c := range [][2]uintptr{{edBefore, btnBefore}, {edAfter, btnAfter}} {
			y := top + i*d.px(38) + d.px(4)
			moveWin(c[0], x, y, w-p()-pw-d.px(8)-x, d.px(30))
			moveWin(c[1], w-p()-pw, y, pw, d.px(30))
		}
		btnY := h - p() - d.px(32)
		workTop := top + 2*d.px(38) + d.px(12) + workLabel(w).h + d.px(6)
		moveWin(edWork, p(), workTop, w-2*p(), max(btnY-d.px(14)-workTop, d.px(40)))
		moveWin(btnCancel, w-p()-d.px(100), btnY, d.px(100), d.px(32))
		cw := buttonWidth(f, "Utwórz porównanie", d.px(14))
		moveWin(btnMake, w-p()-d.px(100)-d.px(8)-cw, btnY, cw, d.px(32))
	}

	var paths *diag.ComparisonPaths
	summary := ""
	pick := func(box uintptr) {
		cur := strings.TrimSpace(getText(box))
		start := dir
		if cur != "" && fileExists(cur) {
			start = cur
		}
		if f := pickPath(d.h, "Wybierz raport SerwisDiag", "Raport SerwisDiag (*.json)", "*.json", false, start); f != "" {
			setText(box, f)
		}
	}
	create := func() {
		bp, ap := strings.TrimSpace(getText(edBefore)), strings.TrimSpace(getText(edAfter))
		if bp == "" || ap == "" {
			msgBox(d.h, "Wskaż oba raporty JSON: sprzed naprawy i po naprawie.", "Porównanie", mbIconInfo)
			return
		}
		workDone := strings.TrimSpace(strings.ReplaceAll(getText(edWork), "\r\n", "\n"))
		var err error
		withWaitCursor(func() {
			var before, after *diag.Result
			if before, err = diag.ImportReportJSON(bp); err != nil {
				return
			}
			if after, err = diag.ImportReportJSON(ap); err != nil {
				return
			}
			cmp := diag.NewComparison(before, after, workDone)
			var cp diag.ComparisonPaths
			if cp, err = diag.SaveComparisonReports(cmp, mw.settings, filepath.Dir(ap)); err != nil {
				return
			}
			paths = &cp
			summary = diag.ComparisonSummaryText(cmp.Counts)
			if len(cmp.Notes) > 0 {
				summary += "\n\nUwagi:\n" + strings.Join(cmp.Notes, "\n")
			}
		})
		if err != nil {
			msgBox(d.h, "Nie udało się utworzyć porównania:\n"+err.Error(), "Porównanie", mbIconError)
			return
		}
		d.close()
	}
	d.onCmd = func(id, code int) {
		switch id {
		case ctlCmpBeforePick:
			pick(edBefore)
		case ctlCmpAfterPick:
			pick(edAfter)
		case ctlCmpMake:
			create()
		case ctlCmpCancel:
			d.close()
		}
	}
	d.show()
	if paths != nil {
		mw.setFooter("Porównanie zapisano w: " + filepath.Dir(paths.Customer))
		if msgBox(mw.h, "Utworzono raport z naprawy ("+summary+").\n\nOtworzyć wersję dla klienta?", "Porównanie", mbIconQuestion|mbYesNo) == idYes {
			openPath(paths.Customer)
		}
	}
}
