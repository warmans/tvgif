package cmd

import (
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/cmd/bot"
	"github.com/warmans/tvgif/cmd/importer"
	"github.com/warmans/tvgif/cmd/search"
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
	rootCmd.AddCommand(importer.NewImporterCommand(logger))
	rootCmd.AddCommand(search.NewSearchCommand(logger))
	rootCmd.AddCommand(bot.NewBotCommand(logger))
	rootCmd.AddCommand(tools.NewToolsCommand(logger))
	return rootCmd.Execute()
}
