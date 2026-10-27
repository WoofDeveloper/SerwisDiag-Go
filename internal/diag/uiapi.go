package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"serwisdiag/internal/win"
)

// Funkcje dla okna programu i monitora przy zegarze (internal/ui).

// SystemCaption: nazwa systemu i architektura do nagłówka okna (Get-SdWinInfo: Caption, Arch).
func SystemCaption() (caption, arch string) {
	c := &Ctx{s: NewScan(Options{}), r: &checkRun{}}
	win.WithCOM(func() {
		w := winInfo(c)
		caption, arch = w.Caption, w.Arch
	})
	return caption, arch
}

// KnowledgeInfo odpowiada Get-SdKnowledgeInfo: opis aktualności bazy wiedzy i czy jest nieaktualna.
func KnowledgeInfo(now time.Time) (text string, stale bool) {
	k := knowledgeInfo(now)
	return k.Text, k.Stale
}

// CategoryLabel odpowiada Get-SdCategoryLabel.
func CategoryLabel(key string) string { return categoryLabel(key) }

// FmtDt odpowiada Format-Dt.
func FmtDt(t time.Time) string { return fmtDt(t) }

// FmtCount odpowiada Format-Count (1 problem, 2 problemy, 5 problemów).
func FmtCount(n int, one, few, many string) string { return fmtCount(n, one, few, many) }

// LimitText odpowiada Limit-Text.
func LimitText(s string, max int) string { return limitText(s, max) }

// FmtSize odpowiada Format-Size.
func FmtSize(b float64, digits int) string { return fmtSize(b, digits) }

// FmtNum odpowiada formatowaniu liczb "N" w Windows PowerShell (pl-PL).
func FmtNum(x float64, digits int) string { return fmtNum(x, digits) }

// CellText: wartość komórki tabeli albo metadanych jako tekst, jak [string] w PowerShellu.
func CellText(v any) string { return htmlString(v) }

// SaveSettings odpowiada Save-SdSettings: config\settings.json obok programu, UTF-8 bez BOM.
func SaveSettings(root string, s Settings) error {
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o644)
}
