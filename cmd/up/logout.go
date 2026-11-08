package main

import (
	"fmt"
	"github.com/heohlee/up/internal/connection"
	"io"
)

func logout(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "Usage: up logout")
		return 2
	}
	removed, err := connection.DeleteProfile()
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	if removed {
		fmt.Fprintln(stdout, "up: removed the saved remote")
	} else {
		fmt.Fprintln(stdout, "up: no saved remote")
	}
	return 0
}
