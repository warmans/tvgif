package bot

import (
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/bwmarrin/discordgo"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/discord"
	"github.com/warmans/tvgif/pkg/mediacache"
	"github.com/warmans/tvgif/pkg/search"
	"log"
	"log/slog"
	"os"
	"os/signal"
)

func NewBotCommand(logger *slog.Logger) *cobra.Command {

	var indexPath string
	var mediaPath string
	var cachePath string
	var discordToken string

	cmd := &cobra.Command{
		Use:   "bot",
		Short: "start the discord bot",
		RunE: func(cmd *cobra.Command, args []string) error {

			reader, err := bluge.OpenReader(bluge.DefaultConfig(indexPath))
			if err != nil {
				return fmt.Errorf("failed to open index: %w", err)
			}

			logger.Info("Creating discord session...")
			if discordToken == "" {
				return fmt.Errorf("discord token is required")
			}
			session, err := discordgo.New("Bot " + discordToken)
			if err != nil {
				return fmt.Errorf("failed to create discord session: %w", err)
			}

			if cachePath == "" {
				logger.Info("No cache dir specified, using OS temp dir")
				cachePath = os.TempDir()
			}
			mediaCache, err := mediacache.NewCache(cachePath, logger)
			if err != nil {
				return fmt.Errorf("failed to create media cache: %w", err)
			}

			if mediaPath == "" {
				return fmt.Errorf("no media dir specified")
			}

			logger.Info("Starting bot...")
			bot, err := discord.NewBot(logger, session, search.NewBlugeSearch(reader), mediaCache, mediaPath)
			if err != nil {
				return fmt.Errorf("failed to create bot: %w", err)
			}
			if err = bot.Start(); err != nil {
				return fmt.Errorf("failed to start bot: %w", err)
			}

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt)
			<-stop

			log.Println("Gracefully shutting down")
			if err = bot.Close(); err != nil {
				return fmt.Errorf("failed to gracefully shutdown bot: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&indexPath, "o", "./var/index/metadata.bluge", "path to index files")
	cmd.Flags().StringVar(&mediaPath, "i", os.Getenv("MEDIA_PATH"), "path to media files (i.e. video)")
	cmd.Flags().StringVar(&discordToken, "t", os.Getenv("DISCORD_TOKEN"), "discord auth token")
	cmd.Flags().StringVar(&cachePath, "c", os.Getenv("CACHE_DIR"), "cache dir")

	return cmd
}
