package bot

import (
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/bwmarrin/discordgo"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/discord"
	"github.com/warmans/tvgif/pkg/flag"
	"github.com/warmans/tvgif/pkg/mediacache"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/store"
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

	var updateDataOnStartup bool
	var indexPath string
	var dbCfg = &store.Config{}
	var metadataPath string

	cmd := &cobra.Command{
		Use:   "bot",
		Short: "start the discord bot",
		RunE: func(cmd *cobra.Command, args []string) error {

			logger.Info("Opening DB...", slog.String("dsn", dbCfg.DSN))
			conn, err := store.NewConn(dbCfg)
			if err != nil {
				return err
			}
			if indexPath == "" {
				return fmt.Errorf("no INDEX_PATH specified")
			}
			if updateDataOnStartup {
				if metadataPath == "" {
					return fmt.Errorf("no METADATA_PATH specified")
				}
				logger.Info("Updating Metadata...", slog.String("path", metadataPath))
				if err := metadata.CreateMetadataFromSRTs(logger, mediaPath, metadataPath); err != nil {
					logger.Error("failed to update metadata", slog.String("err", err.Error()))
				}
				logger.Info("Updating Index...", slog.String("path", indexPath))
				if err := search.PopulateIndex(logger, metadataPath, indexPath); err != nil {
					logger.Error("failed to update index", slog.String("err", err.Error()))
				}
				logger.Info("Updating DB...", slog.String("dsn", dbCfg.DSN))
				if err := store.InitDB(logger, metadataPath, conn); err != nil {
					logger.Error("failed to update db", slog.String("err", err.Error()))
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
			bot, err := discord.NewBot(
				logger,
				session,
				search.NewBlugeSearch(reader),
				mediaCache,
				mediaPath,
				botUsername,
				store.NewSRTStore(conn.Db),
			)
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

	flag.BoolVarEnv(cmd.Flags(), &updateDataOnStartup, "", "update-data-on-startup", true, "automatically create indexes and metadata from the media dir when bot starts")
	flag.StringVarEnv(cmd.Flags(), &indexPath, "", "index-path", "./var/index/metadata.bluge", "path to index files")
	flag.StringVarEnv(cmd.Flags(), &metadataPath, "", "metadata-path", "./var/metadata", "path to metadata files")

	dbCfg.RegisterFlags(cmd.Flags(), "", "dialog")
	flag.Parse()

	return cmd
}
