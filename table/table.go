// Package table provides functions to render tables in terminal using ansipixels.
// TODO: move to fortio.org/terminal/ansipixels/table
package table

import (
	"strings"

	"fortio.org/terminal/ansipixels"
)

type Alignment int

const (
	Left Alignment = iota
	Center
	Right
)

func WriteTableBoxed(ap *ansipixels.AnsiPixels, y int, alignment []Alignment, columnSpacing int, table [][]string) int {
	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)
	var cursorY int
	leftX := (ap.W - width) / 2
	for i, l := range lines {
		cursorY = y + i
		ap.MoveCursor(leftX, cursorY)
		ap.WriteString(l)
	}
	ap.DrawRoundBox((ap.W-width)/2-1, y-1, width+2, len(lines)+2)
	return width
}

func CreateTableLines(ap *ansipixels.AnsiPixels, alignment []Alignment, columnSpacing int, table [][]string) ([]string, int) {
	nrows := len(table)
	ncols := len(alignment)
	// get the max width of each column
	colWidths := make([]int, ncols)
	allWidths := make([][]int, 0, nrows)
	for _, row := range table {
		if len(row) != ncols {
			panic("inconsistent number of columns in table")
		}
		allWidthsRow := make([]int, 0, ncols)
		for j, cell := range row {
			w := ap.ScreenWidth(cell)
			allWidthsRow = append(allWidthsRow, w)
			if w > colWidths[j] {
				colWidths[j] = w
			}
		}
		allWidths = append(allWidths, allWidthsRow)
	}
	maxw := 0
	for _, w := range colWidths {
		maxw += w
	}
	maxw += columnSpacing * (ncols - 1)
	lines := make([]string, nrows)
	var sb strings.Builder
	for i, row := range table {
		rowWidth := allWidths[i]
		// creat each line using width and specified alignment
		for j, cell := range row {
			w := rowWidth[j]
			delta := colWidths[j] - w
			if j > 0 {
				sb.WriteString(strings.Repeat(" ", columnSpacing))
			}
			switch alignment[j] {
			case Left:
				sb.WriteString(cell)
				sb.WriteString(strings.Repeat(" ", delta))
			case Center:
				sb.WriteString(strings.Repeat(" ", delta/2))
				sb.WriteString(cell)
				sb.WriteString(strings.Repeat(" ", delta/2+delta%2)) // if odd, add 1 more space on the right
			case Right:
				sb.WriteString(strings.Repeat(" ", delta))
				sb.WriteString(cell)
			}
		}
		lines[i] = sb.String()
		sb.Reset()
	}
	return lines, maxw
}
