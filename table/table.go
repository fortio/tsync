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

type BorderStyle int

const (
	BorderNone         BorderStyle = iota // No borders at all
	BorderColumns                         // Only vertical lines between columns (│)
	BorderOuter                           // Only outer box around the table
	BorderOuterColumns                    // Outer box + column separators
	BorderFull                            // Full grid with all cell borders
)

// WriteTable renders a table at the specified y position with the given border style.
// The table is centered horizontally on the screen.
// Returns the width of the table content (excluding borders).
func WriteTable(
	ap *ansipixels.AnsiPixels, y int, alignment []Alignment,
	columnSpacing int, table [][]string, borderStyle BorderStyle,
) int {
	lines, width := CreateTableLines(ap, alignment, columnSpacing, table, borderStyle)
	var cursorY int
	leftX := (ap.W - width) / 2
	for i, l := range lines {
		cursorY = y + i
		ap.MoveCursor(leftX, cursorY)
		ap.WriteString(l)
	}
	switch borderStyle {
	case BorderOuter:
		// Only BorderOuter needs an additional round box, as the table lines don't include borders
		ap.DrawRoundBox((ap.W-width)/2-1, y-1, width+2, len(lines)+2)
	case BorderNone, BorderColumns, BorderOuterColumns, BorderFull:
		// These styles either have no borders or already drew them in CreateTableLines
	}
	return width
}

//nolint:gocognit,gocyclo,funlen // it is indeed a bit complex.
func CreateTableLines(ap *ansipixels.AnsiPixels,
	alignment []Alignment,
	columnSpacing int,
	table [][]string,
	borderStyle BorderStyle,
) ([]string, int) {
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

	// Determine spacing between columns based on border style
	hasColumnBorders := borderStyle == BorderColumns || borderStyle == BorderOuterColumns || borderStyle == BorderFull
	hasOuterBorder := borderStyle == BorderOuterColumns || borderStyle == BorderFull

	// Calculate total width
	maxw := 0
	if hasColumnBorders {
		// With borders: width of each column + padding on both sides + separators
		for _, w := range colWidths {
			maxw += w + 2*columnSpacing
		}
		maxw += (ncols - 1) // separators between columns
		if hasOuterBorder {
			maxw += 2 // left and right borders
		}
	} else {
		// Without borders: just column widths + spacing between them
		for _, w := range colWidths {
			maxw += w
		}
		if ncols > 1 {
			maxw += columnSpacing * (ncols - 1)
		}
	}

	// Build table lines
	lines := make([]string, 0, nrows+3) // preallocate for data rows + potential border rows
	var sb strings.Builder

	// Add top border if needed
	if hasOuterBorder {
		sb.WriteString(ansipixels.SquareTopLeft)
		for j := range ncols {
			sb.WriteString(strings.Repeat(ansipixels.Horizontal, colWidths[j]+2*columnSpacing))
			if j < ncols-1 {
				sb.WriteString(ansipixels.TopT)
			}
		}
		sb.WriteString(ansipixels.SquareTopRight)
		lines = append(lines, sb.String())
		sb.Reset()
	}

	// Add data rows
	for i, row := range table {
		rowWidth := allWidths[i]

		// Add row separator for full borders (except before first row)
		if borderStyle == BorderFull && i > 0 {
			sb.WriteString(ansipixels.LeftT)
			for j := range ncols {
				sb.WriteString(strings.Repeat(ansipixels.Horizontal, colWidths[j]+2*columnSpacing))
				if j < ncols-1 {
					sb.WriteString(ansipixels.MiddleCross)
				}
			}
			sb.WriteString(ansipixels.RightT)
			lines = append(lines, sb.String())
			sb.Reset()
		}

		// Add left border if needed
		if hasOuterBorder {
			sb.WriteString(ansipixels.Vertical)
		}

		// Build the data row
		for j, cell := range row {
			w := rowWidth[j]
			delta := colWidths[j] - w

			// Add padding before content
			if hasColumnBorders {
				sb.WriteString(strings.Repeat(" ", columnSpacing))
			}

			// Add aligned content
			switch alignment[j] {
			case Left:
				sb.WriteString(cell)
				sb.WriteString(strings.Repeat(" ", delta))
			case Center:
				sb.WriteString(strings.Repeat(" ", delta/2))
				sb.WriteString(cell)
				sb.WriteString(strings.Repeat(" ", delta/2+delta%2))
			case Right:
				sb.WriteString(strings.Repeat(" ", delta))
				sb.WriteString(cell)
			}

			// Add padding after content
			if hasColumnBorders {
				sb.WriteString(strings.Repeat(" ", columnSpacing))
			}

			// Add column separator or spacing
			if j < ncols-1 {
				if hasColumnBorders {
					sb.WriteString(ansipixels.Vertical)
				} else {
					sb.WriteString(strings.Repeat(" ", columnSpacing))
				}
			}
		}

		// Add right border if needed
		if hasOuterBorder {
			sb.WriteString(ansipixels.Vertical)
		}

		lines = append(lines, sb.String())
		sb.Reset()
	}

	// Add bottom border if needed
	if hasOuterBorder {
		sb.WriteString(ansipixels.SquareBottomLeft)
		for j := range ncols {
			sb.WriteString(strings.Repeat(ansipixels.Horizontal, colWidths[j]+2*columnSpacing))
			if j < ncols-1 {
				sb.WriteString(ansipixels.BottomT)
			}
		}
		sb.WriteString(ansipixels.SquareBottomRight)
		lines = append(lines, sb.String())
		sb.Reset()
	}

	return lines, maxw
}
