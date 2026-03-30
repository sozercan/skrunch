package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sozercan/skrunch/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			if msg := strings.TrimSpace(exitErr.Error()); msg != "" {
				fmt.Fprintln(os.Stderr, msg)
			}
			os.Exit(exitErr.Code)
		}

		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
