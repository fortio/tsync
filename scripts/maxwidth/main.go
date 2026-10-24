// maxwidth reads screen text on stdin and exits 1 if any line is wider than the given number of
// columns (using the same width computation as the UI), for scripts/e2e_tmux.sh.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"

	"fortio.org/terminal/ansipixels"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: maxwidth columns < screen.txt")
		os.Exit(2)
	}
	maxCols, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ap := &ansipixels.AnsiPixels{} // ScreenWidth doesn't need an opened terminal.
	rc := 0
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if w := ap.ScreenWidth(sc.Text()); w > maxCols {
			fmt.Fprintf(os.Stderr, "width %d > %d: %q\n", w, maxCols, sc.Text())
			rc = 1
		}
	}
	os.Exit(rc)
}
