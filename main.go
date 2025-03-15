package main

import (
	"github.com/warmans/tvgif/cmd"
	"log/slog"
	"os"
)

func main() {

	logger := createLogger()
	if err := cmd.Execute(logger); err != nil {
		logger.Error("Command failed", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func createLogger() *slog.Logger {
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelInfo)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	}))

	if os.Getenv("DEV") == "true" {
		lvl.Set(slog.LevelDebug)
	}

	return logger
}
