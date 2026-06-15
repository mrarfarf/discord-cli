package main

import (
	"log/slog"
	"os"

	"github.com/chrischapin/discord-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		slog.Error("failed to execute command", "err", err)
		os.Exit(1)
	}
}
