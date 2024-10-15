package cmd

import (
	"github.com/spf13/cobra"
	transcribe "github.com/warmans/tvgif/cmd/aisrt"
	"github.com/warmans/tvgif/cmd/bot"
	"github.com/warmans/tvgif/cmd/tools"
	"log/slog"
)

var (
	rootCmd = &cobra.Command{
		Use:   "tvgif",
		Short: "Discord bot for posting TV show gifs",
	}
)

func init() {

}

// Execute executes the root command.
func Execute(logger *slog.Logger) error {
	rootCmd.AddCommand(bot.NewBotCommand(logger))
	rootCmd.AddCommand(tools.NewToolsCommand(logger))
	rootCmd.AddCommand(transcribe.NewRootCommand(logger))
	return rootCmd.Execute()
}
