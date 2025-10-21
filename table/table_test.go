package table

import (
	"strings"
	"testing"

	"fortio.org/terminal/ansipixels"
)

func TestCreateTableLines_LeftAlignment(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Left, Left}
	columnSpacing := 2
	table := [][]string{
		{"Name", "Age", "City"},
		{"Alice", "30", "NYC"},
		{"Bob", "25", "LA"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// Check that all lines have the same width (padded properly)
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}

	// Check left alignment - content should be at the start of each column
	if !strings.HasPrefix(lines[0], "Name") {
		t.Errorf("First column should start with 'Name', got: %q", lines[0])
	}
}

func TestCreateTableLines_RightAlignment(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Right, Right, Right}
	columnSpacing := 2
	table := [][]string{
		{"Name", "Age", "City"},
		{"Alice", "30", "NYC"},
		{"Bob", "25", "LA"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// Check that all lines have the same width
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}

	// With right alignment, shorter values should be padded on the left
	// "Bob" should have more leading spaces than "Alice" in the first column
	bobLine := lines[2]
	aliceLine := lines[1]

	// Find where content starts (after leading spaces)
	bobStart := strings.Index(bobLine, "Bob")
	aliceStart := strings.Index(aliceLine, "Alice")

	if bobStart <= aliceStart {
		t.Errorf("Bob should have more leading spaces than Alice with right alignment")
	}
}

func TestCreateTableLines_CenterAlignment(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Center, Center, Center}
	columnSpacing := 2
	table := [][]string{
		{"Name", "Age", "City"},
		{"Alice", "30", "NYC"},
		{"Bob", "25", "LA"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// Check that all lines have the same width
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}

	// With center alignment, content should be roughly centered
	// "Bob" (3 chars) in a column sized for "Alice" (5 chars) should have 1 space before
	bobLine := lines[2]
	bobStart := strings.Index(bobLine, "Bob")

	// Should have at least some leading space (centered)
	if bobStart == 0 {
		t.Errorf("Bob should be centered with leading space, got line: %q", bobLine)
	}
}

func TestCreateTableLines_MixedAlignment(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Right, Center}
	columnSpacing := 3
	table := [][]string{
		{"Product", "Price", "Stock"},
		{"Apple", "1.50", "100"},
		{"Banana", "0.75", "50"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// Verify all lines have consistent width
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}

	// Check that lines are not empty
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("Line %d is empty or whitespace only", i)
		}
	}
}

func TestCreateTableLines_DifferentColumnSpacing(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Left}
	table := [][]string{
		{"A", "B"},
		{"C", "D"},
	}

	testCases := []int{0, 1, 2, 5, 10}

	for _, spacing := range testCases {
		lines, width := CreateTableLines(ap, alignment, spacing, table)

		// Check that spacing is correctly applied
		// Width should be: max_col0_width + spacing + max_col1_width
		expectedWidth := 1 + spacing + 1 // Both columns have width 1
		if width != expectedWidth {
			t.Errorf("With spacing %d, expected width %d, got %d", spacing, expectedWidth, width)
		}

		// Verify lines have the correct width
		for i, line := range lines {
			lineWidth := ap.ScreenWidth(line)
			if lineWidth != width {
				t.Errorf("Spacing %d, line %d has width %d, expected %d: %q", spacing, i, lineWidth, width, line)
			}
		}
	}
}

func TestCreateTableLines_SingleColumn(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Center}
	columnSpacing := 2
	table := [][]string{
		{"Header"},
		{"Row1"},
		{"Row2"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// Width should be the max width of all cells
	expectedWidth := 6 // "Header" is 6 chars
	if width != expectedWidth {
		t.Errorf("Expected width %d, got %d", expectedWidth, width)
	}
}

func TestCreateTableLines_UnevenColumnWidths(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Left, Left}
	columnSpacing := 2
	table := [][]string{
		{"Short", "Medium Length", "X"},
		{"A", "B", "Very Long Column"},
		{"Test", "Data", "C"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	// All lines should have the same width
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}

	// The width should accommodate the longest value in each column
	// Col 0: "Short" (5), Col 1: "Medium Length" (13), Col 2: "Very Long Column" (16)
	expectedWidth := 5 + 2 + 13 + 2 + 16
	if width != expectedWidth {
		t.Errorf("Expected width %d, got %d", expectedWidth, width)
	}
}

func TestCreateTableLines_EmptyTable(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left}
	columnSpacing := 2
	table := [][]string{}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 0 {
		t.Errorf("Expected 0 lines for empty table, got %d", len(lines))
	}

	if width != 0 {
		t.Errorf("Expected width 0 for empty table, got %d", width)
	}
}

func TestCreateTableLines_InconsistentColumns(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Left}
	columnSpacing := 2
	table := [][]string{
		{"A", "B"},
		{"C", "D", "E"}, // Extra column - should panic
	}

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic for inconsistent number of columns")
		}
	}()

	CreateTableLines(ap, alignment, columnSpacing, table)
}

func TestCreateTableLines_CenterAlignmentOddEven(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Center}
	columnSpacing := 0

	// Test odd difference (5 char column, 3 char content = 2 char delta)
	tableOdd := [][]string{
		{"ABCDE"},
		{"ABC"},
	}

	linesOdd, _ := CreateTableLines(ap, alignment, columnSpacing, tableOdd)

	// "ABC" centered in 5 chars should be " ABC " (1 space left, 1 space right)
	abcLine := linesOdd[1]
	if !strings.HasPrefix(abcLine, " ABC ") {
		t.Errorf("Center alignment with even delta failed: expected ' ABC ', got %q", abcLine)
	}

	// Test even difference (6 char column, 3 char content = 3 char delta)
	tableEven := [][]string{
		{"ABCDEF"},
		{"ABC"},
	}

	linesEven, _ := CreateTableLines(ap, alignment, columnSpacing, tableEven)

	// "ABC" centered in 6 chars should be " ABC  " (1 space left, 2 spaces right due to odd delta)
	abcLineEven := linesEven[1]
	if !strings.HasPrefix(abcLineEven, " ABC  ") {
		t.Errorf("Center alignment with odd delta failed: expected ' ABC  ', got %q", abcLineEven)
	}
}

func TestCreateTableLines_ZeroColumnSpacing(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Left, Left}
	columnSpacing := 0
	table := [][]string{
		{"A", "B", "C"},
		{"X", "Y", "Z"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	// Width should be sum of column widths with no spacing
	expectedWidth := 3 // 1 + 1 + 1
	if width != expectedWidth {
		t.Errorf("Expected width %d, got %d", expectedWidth, width)
	}

	// Lines should have adjacent columns with no spaces
	if lines[0] != "ABC" {
		t.Errorf("Expected 'ABC', got %q", lines[0])
	}
	if lines[1] != "XYZ" {
		t.Errorf("Expected 'XYZ', got %q", lines[1])
	}
}

func TestCreateTableLines_WithEmojisAndUnicode(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	alignment := []Alignment{Left, Center, Right}
	columnSpacing := 2
	table := [][]string{
		{"Name", "Icon", "Score"},
		{"Alice", "🎉", "100"},
		{"Bob", "🚀", "95"},
		{"Charlie", "✨", "98"},
	}

	lines, width := CreateTableLines(ap, alignment, columnSpacing, table)

	if len(lines) != 4 {
		t.Errorf("Expected 4 lines, got %d", len(lines))
	}

	// All lines should have consistent width
	for i, line := range lines {
		lineWidth := ap.ScreenWidth(line)
		if lineWidth != width {
			t.Errorf("Line %d has width %d, expected %d: %q", i, lineWidth, width, line)
		}
	}
}

func TestCreateTableLines_VisualAlignment(t *testing.T) {
	ap := &ansipixels.AnsiPixels{}
	
	tests := []struct {
		name          string
		alignment     []Alignment
		columnSpacing int
		table         [][]string
		expected      string
	}{
		{
			name:          "Left alignment",
			alignment:     []Alignment{Left, Left, Left},
			columnSpacing: 2,
			table: [][]string{
				{"Name", "Age", "City"},
				{"Alice", "30", "NYC"},
				{"Bob", "25", "LA"},
			},
			expected: `
Name   Age  City
Alice  30   NYC 
Bob    25   LA  `,
		},
		{
			name:          "Right alignment",
			alignment:     []Alignment{Right, Right, Right},
			columnSpacing: 2,
			table: [][]string{
				{"Name", "Age", "City"},
				{"Alice", "30", "NYC"},
				{"Bob", "25", "LA"},
			},
			expected: `
 Name  Age  City
Alice   30   NYC
  Bob   25    LA`,
		},
		{
			name:          "Center alignment",
			alignment:     []Alignment{Center, Center, Center},
			columnSpacing: 2,
			table: [][]string{
				{"Name", "Age", "City"},
				{"Alice", "30", "NYC"},
				{"Bob", "25", "LA"},
			},
			expected: `
Name   Age  City
Alice  30   NYC 
 Bob   25    LA `,
		},
		{
			name:          "Mixed alignment",
			alignment:     []Alignment{Left, Right, Center},
			columnSpacing: 3,
			table: [][]string{
				{"Product", "Price", "Stock"},
				{"Apple", "1.50", "100"},
				{"Banana", "0.75", "50"},
			},
			expected: `
Product   Price   Stock
Apple      1.50    100 
Banana     0.75    50  `,
		},
		{
			name:          "No spacing",
			alignment:     []Alignment{Left, Center, Right},
			columnSpacing: 0,
			table: [][]string{
				{"A", "BB", "CCC"},
				{"X", "Y", "Z"},
			},
			expected: `
ABBCCC
XY   Z`,
		},
		{
			name:          "Wide spacing",
			alignment:     []Alignment{Left, Right},
			columnSpacing: 5,
			table: [][]string{
				{"Foo", "Bar"},
				{"A", "B"},
			},
			expected: `
Foo     Bar
A         B`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, _ := CreateTableLines(ap, tt.alignment, tt.columnSpacing, tt.table)
			result := "\n" + strings.Join(lines, "\n")
			
			if result != tt.expected {
				t.Errorf("\nExpected:\n%s\n\nGot:\n%s", tt.expected, result)
			}
		})
	}
}
