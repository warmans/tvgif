package bot

import (
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/bwmarrin/discordgo"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/discord"
	"github.com/warmans/tvgif/pkg/flag"
	"github.com/warmans/tvgif/pkg/importer"
	"github.com/warmans/tvgif/pkg/mediacache"
	"github.com/warmans/tvgif/pkg/search"
	"log"
	"log/slog"
	"os"
	"os/signal"
)

func NewBotCommand(logger *slog.Logger) *cobra.Command {

	var mediaPath string
	var cachePath string
	var discordToken string
	var botUsername string

	var populateIndexOnStart bool
	var indexPath string
	var metadataPath string

	cmd := &cobra.Command{
		Use:   "bot",
		Short: "start the discord bot",
		RunE: func(cmd *cobra.Command, args []string) error {

			if indexPath == "" {
				return fmt.Errorf("no INDEX_PATH specified")
			}
			if populateIndexOnStart {
				if metadataPath == "" {
					return fmt.Errorf("no METADATA_PATH specified")
				}
				logger.Info("Creating Metadata...", slog.String("path", metadataPath))
				if err := importer.CreateMetadataFromSRTs(mediaPath, metadataPath); err != nil {
					return err
				}
				logger.Info("Creating Index...", slog.String("path", indexPath))
				if err := importer.PopulateIndex(logger, metadataPath, indexPath); err != nil {
					return err
				}
			}

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
			bot, err := discord.NewBot(logger, session, search.NewBlugeSearch(reader), mediaCache, mediaPath, botUsername)
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

	flag.StringVarEnv(cmd.Flags(), &mediaPath, "", "media-path", "./var/media", "path to media files")
	flag.StringVarEnv(cmd.Flags(), &discordToken, "", "discord-token", "", "discord auth token")
	flag.StringVarEnv(cmd.Flags(), &cachePath, "", "cache-path", "", "path to cache dir")
	flag.StringVarEnv(cmd.Flags(), &botUsername, "", "bot-username", "tvgif", "bot username and differentiator, used to determine if a message belongs to the bot e.g. tvgif#213")

	flag.BoolVarEnv(cmd.Flags(), &populateIndexOnStart, "", "populate-index", true, "automatically create indexes and metadata from the media dir")
	flag.StringVarEnv(cmd.Flags(), &indexPath, "", "index-path", "./var/index/metadata.bluge", "path to index files")
	flag.StringVarEnv(cmd.Flags(), &metadataPath, "", "metadata-path", "./var/metadata", "path to metadata files")
	flag.Parse()

	return cmd
}
