// Command up is a server-local HTTP uptime monitor with a terminal UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `Usage: up <command> [flags]

Commands:
  serve   Run the HTTP checks and store their results (keep it running, for example under systemd)
  tui     Show the stored results in the terminal

Run 'up <command> -h' for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		return serve(ctx, args[1:], stdout, stderr)
	case "tui":
		return runTUI(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "up: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// parseFlags parses a subcommand's flags. -h prints the help to stdout; any
// other parse error prints it with the help to stderr. ok is false when the
// command must exit with code.
func parseFlags(fs *flag.FlagSet, help string, args []string, stdout, stderr io.Writer) (code int, ok bool) {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if err == nil {
		return 0, true
	}
	printHelp := func(w io.Writer) {
		fmt.Fprint(w, help)
		fs.SetOutput(w)
		fs.PrintDefaults()
	}
	if errors.Is(err, flag.ErrHelp) {
		printHelp(stdout)
		return 0, false
	}
	fmt.Fprintf(stderr, "up %s: %v\n\n", fs.Name(), err)
	printHelp(stderr)
	return 2, false
}
