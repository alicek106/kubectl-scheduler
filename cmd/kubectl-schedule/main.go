package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alicek106/kubectl-scheduler/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
