package diag

import (
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"
)

func getenv(k string) string { return os.Getenv(k) }

// fileCreationTime: data utworzenia pliku lub folderu (CreationTime w .NET).
func fileCreationTime(fi os.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return fi.ModTime()
}

// decodeText zamienia plik tekstowy w UTF-16 (z BOM) na UTF-8 i usuwa BOM UTF-8.
func decodeText(b []byte) []byte {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = uint16(b[2+2*i]) | uint16(b[3+2*i])<<8
		}
		s := string(utf16.Decode(u))
		s = strings.Replace(s, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
		s = strings.Replace(s, `encoding="utf-16"`, `encoding="utf-8"`, 1)
		return []byte(s)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return b[3:]
	}
	return b
}

// polishOrder: kolejność liter jak w sortowaniu Sort-Object przy polskich ustawieniach (ą po a, ć po c...).
var polishOrder = func() map[rune]int {
	m := map[rune]int{}
	letters := "aąbcćdeęfghijklłmnńoóprsśtuvwxyzźż"
	for i, r := range letters {
		m[r] = (i + 1) * 4
	}
	return m
}()

func runeKey(r rune) (int, rune) {
	l := unicode.ToLower(r)
	if k, ok := polishOrder[l]; ok {
		return 1000 + k, l
	}
	if l == 'q' {
		return 1000 + polishOrder['p']*1 + 2, l
	}
	if unicode.IsDigit(l) {
		return 500 + int(l-'0'), l
	}
	return int(l), l
}

// cmpText porównuje napisy jak Sort-Object w PowerShellu (bez rozróżniania wielkości liter, polskie litery
// we właściwych miejscach). Przybliżenie porównania kulturowego .NET - wystarcza do kolejności w tabelach.
func cmpText(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		ka, _ := runeKey(ra[i])
		kb, _ := runeKey(rb[i])
		if ka != kb {
			if ka < kb {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ra) < len(rb):
		return -1
	case len(ra) > len(rb):
		return 1
	}
	return strings.Compare(a, b)
}

// sortStable sortuje stabilnie (tam, gdzie wersja PowerShell nie używa Sort-Object).
func sortStable[T any](s []T, cmp func(a, b T) int) { slices.SortStableFunc(s, cmp) }

// psSort sortuje dokładnie jak Sort-Object w Windows PowerShell 5.1: algorytm DepthLimitedQuickSort z .NET 4.0.
// Nie jest stabilny - elementy równe zmieniają kolejność - więc przy remisach (np. „najczęstszy rodzaj
// zdarzenia”) obie wersje programu wybierają to samo. -Descending to odwrócony wynik porównania.
func psSort[T any](s []T, cmp func(a, b T) int) {
	if len(s) > 1 {
		quickSort(s, 0, len(s)-1, cmp, 32)
	}
}

func swapIfGreater[T any](s []T, cmp func(a, b T) int, a, b int) {
	if a != b && cmp(s[a], s[b]) > 0 {
		s[a], s[b] = s[b], s[a]
	}
}

func quickSort[T any](s []T, left, right int, cmp func(a, b T) int, depth int) {
	for {
		if depth == 0 {
			heapSort(s, left, right, cmp)
			return
		}
		i, j := left, right
		middle := i + ((j - i) >> 1)
		swapIfGreater(s, cmp, i, middle)
		swapIfGreater(s, cmp, i, j)
		swapIfGreater(s, cmp, middle, j)
		x := s[middle]
		for {
			for cmp(s[i], x) < 0 {
				i++
			}
			for cmp(x, s[j]) < 0 {
				j--
			}
			if i > j {
				break
			}
			if i < j {
				s[i], s[j] = s[j], s[i]
			}
			i++
			j--
			if i > j {
				break
			}
		}
		depth--
		if j-left <= right-i {
			if left < j {
				quickSort(s, left, j, cmp, depth)
			}
			left = i
		} else {
			if i < right {
				quickSort(s, i, right, cmp, depth)
			}
			right = j
		}
		if left >= right {
			return
		}
	}
}

func heapSort[T any](s []T, lo, hi int, cmp func(a, b T) int) {
	n := hi - lo + 1
	for i := n / 2; i >= 1; i-- {
		downHeap(s, i, n, lo, cmp)
	}
	for i := n; i > 1; i-- {
		s[lo], s[lo+i-1] = s[lo+i-1], s[lo]
		downHeap(s, 1, i-1, lo, cmp)
	}
}

func downHeap[T any](s []T, i, n, lo int, cmp func(a, b T) int) {
	d := s[lo+i-1]
	for i <= n/2 {
		child := 2 * i
		if child < n && cmp(s[lo+child-1], s[lo+child]) < 0 {
			child++
		}
		if !(cmp(d, s[lo+child-1]) < 0) {
			break
		}
		s[lo+i-1] = s[lo+child-1]
		i = child
	}
	s[lo+i-1] = d
}

func cmpNum[N int | int64 | float64](a, b N) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
