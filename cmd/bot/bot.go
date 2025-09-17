package bot

import (
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/discord"
	"github.com/warmans/tvgif/pkg/docs"
	"github.com/warmans/tvgif/pkg/flag"
	"github.com/warmans/tvgif/pkg/importer"
	"github.com/warmans/tvgif/pkg/mediacache"
	"github.com/warmans/tvgif/pkg/render"
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

	var useFilePolling bool
	var indexPath string
	var dbCfg = &store.Config{}
	var metadataPath string
	var varPath string

	cmd := &cobra.Command{
		Use:   "bot",
		Short: "start the discord bot",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancelCtx := context.WithCancel(context.Background())
			defer cancelCtx()

			if metadataPath == "" {
				return fmt.Errorf("no METADATA_PATH specified")
			}

			logger.Info("Opening DB...", slog.String("dsn", dbCfg.DSN))
			conn, err := store.NewConn(dbCfg)
			if err != nil {
				return err
			}
			if err := conn.Migrate(); err != nil {
				panic(err.Error())
			}
			if indexPath == "" {
				return fmt.Errorf("no INDEX_PATH specified")
			}

			searcher, err := search.NewBlugeSearch(indexPath)
			if err != nil {
				return fmt.Errorf("failed to create searcher: %w", err)
			}

			importWorker := importer.NewIncrementalImporter(
				mediaPath,
				metadataPath,
				varPath,
				conn,
				searcher,
				logger,
				useFilePolling,
			)
			go func() {
				if err := importWorker.Start(ctx); err != nil {
					panic("importer failed " + err.Error())
				}
			}()

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

			docsRepo, err := docs.NewRepo()
			if err != nil {
				return fmt.Errorf("failed to create docs repo: %w", err)
			}

			logger.Info("Starting bot...")
			bot, err := discord.NewBot(
				logger,
				session,
				searcher,
				render.NewExecRenderer(mediaCache, mediaPath, logger),
				botUsername,
				store.NewSRTStore(conn.Db),
				docsRepo,
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

	flag.BoolVarEnv(cmd.Flags(), &useFilePolling, "", "use-file-polling", true, "instead of relying on filesystem events just poll for changes")
	flag.StringVarEnv(cmd.Flags(), &indexPath, "", "index-path", "./var/index/metadata.bluge", "path to index files")
	flag.StringVarEnv(cmd.Flags(), &metadataPath, "", "metadata-path", "./var/metadata", "path to metadata files")
	flag.StringVarEnv(cmd.Flags(), &varPath, "", "var-path", "./var", "path to var dir")

	dbCfg.RegisterFlags(cmd.Flags(), "", "dialog")
	flag.Parse()

	return cmd
}
