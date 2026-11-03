package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/giulianotesta7/up/internal/remote"
)

const tokenHelp = `Usage: up token reset [--db PATH]

Reset the single API access token. Only its hash is stored in <db>.token
(mode 0600); the new token is shown once on stdout. The old token stops working.
`

func tokenCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, tokenHelp)
		return 0
	}
	if len(args) == 0 || args[0] != "reset" {
		fmt.Fprint(stderr, tokenHelp)
		return 2
	}
	fs := flag.NewFlagSet("token reset", flag.ContinueOnError)
	path := databaseFlag(fs)
	if code, ok := parseFlags(fs, tokenHelp+"\nFlags:\n", args[1:], stdout, stderr); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, tokenHelp)
		return 2
	}
	if err := prepareDatabase(fs, path); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	token, err := remote.GenerateToken()
	if err == nil {
		err = remote.SaveToken(*path+".token", token)
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "up: access token shown once; store it securely")
	fmt.Fprintln(stdout, token)
	return 0
}
