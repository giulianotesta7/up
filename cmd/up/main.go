// Command up is a server-local uptime monitor with a terminal UI.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Stdout))
}

func run(stdout io.Writer) int {
	fmt.Fprintln(stdout, "up: uptime monitor TUI (not implemented yet)")
	return 0
}
