package tables

import (
	"encoding/csv"
	"fmt"
	"os"
	"unicode/utf8"

	"golang.org/x/term"
	"iter"
)

// Render prints rows as a formatted table when stdout is a TTY.
// If stdout is not a TTY (e.g., piped), it prints CSV.
// columns defines the header names. rowIter yields each row of values.
func Render(columns []string, rowIter iter.Seq[[]string]) {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		renderCSV(columns, rowIter)
		return
	}

	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		// Fallback to CSV if we cannot determine size
		renderCSV(columns, rowIter)
		return
	}

	// Buffer rows to compute column widths
	rows := make([][]string, 0, 64)
	for r := range rowIter {
		// Ensure we don't mutate caller's backing arrays later
		cp := make([]string, len(r))
		copy(cp, r)
		rows = append(rows, cp)
	}

	// Derive widths and render
	printTable(width, columns, rows)
}

func renderCSV(columns []string, rowIter iter.Seq[[]string]) {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write(columns)
	for r := range rowIter {
		_ = w.Write(r)
	}
	w.Flush()
}

func printTable(ttyWidth int, headers []string, rows [][]string) {
	const gap = 2 // spaces between columns
	n := len(headers)
	if n == 0 {
		return
	}

	// Normalize rows to header length
	norm := make([][]string, len(rows))
	for i, r := range rows {
		if len(r) < n {
			rr := make([]string, n)
			copy(rr, r)
			norm[i] = rr
		} else {
			norm[i] = r[:n]
		}
	}

	// Desired width per column based on content
	widths := make([]int, n)
	for i := 0; i < n; i++ {
		widths[i] = displayWidth(headers[i])
	}
	for _, r := range norm {
		for i := 0; i < n; i++ {
			if w := displayWidth(r[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Cap very wide columns so table fits; then shrink if still overflowing.
	// Start with soft caps to avoid a single column dominating.
	minCol := 4
	softCap := max(10, ttyWidth/3) // cap any single column around ~1/3 of width
	for i := 0; i < n; i++ {
		if widths[i] > softCap {
			widths[i] = softCap
		}
		if widths[i] < minCol {
			widths[i] = minCol
		}
	}

	total := sum(widths) + (n-1)*gap
	// If table too wide, iteratively shave the largest columns.
	for total > ttyWidth {
		// Find the widest column that can still shrink
		idx := -1
		maxw := 0
		for i := 0; i < n; i++ {
			if widths[i] > minCol && widths[i] > maxw {
				maxw = widths[i]
				idx = i
			}
		}
		if idx == -1 { // nothing can shrink further
			break
		}
		widths[idx]--
		total = sum(widths) + (n-1)*gap
	}

	// Render header
	for i := 0; i < n; i++ {
		if i > 0 {
			fmt.Print(spaces(gap))
		}
		fmt.Print(formatCell(headers[i], widths[i]))
	}
	fmt.Println()
	// Separator
	for i := 0; i < n; i++ {
		if i > 0 {
			fmt.Print(spaces(gap))
		}
		fmt.Print(repeat('-', widths[i]))
	}
	fmt.Println()

	// Render rows
	for _, r := range norm {
		for i := 0; i < n; i++ {
			if i > 0 {
				fmt.Print(spaces(gap))
			}
			fmt.Print(formatCell(r[i], widths[i]))
		}
		fmt.Println()
	}
}

func displayWidth(s string) int {
	// Approximate display width by rune count.
	// This ignores combining/wide characters, good enough for CLI defaults.
	return utf8.RuneCountInString(s)
}

func formatCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	rw := displayWidth(s)
	if rw <= width { // pad right
		if pad := width - rw; pad > 0 {
			return s + spaces(pad)
		}
		return s
	}
	// Truncate and add ellipsis if space allows
	if width == 1 {
		return "…"
	}
	// Keep width-1 runes, then ellipsis
	keep := width - 1
	// Walk runes to slice precisely
	i := 0
	for idx := range s {
		if i == keep {
			return s[:idx] + "…"
		}
		i++
	}
	// Fallback (shouldn't hit): string shorter in bytes than runes computed
	return s
}

func sum(v []int) int {
	t := 0
	for _, x := range v {
		t += x
	}
	return t
}

func spaces(n int) string { return repeat(' ', n) }

func repeat(ch rune, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]rune, n)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
