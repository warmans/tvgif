package importer

import (
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/store"
	"log/slog"
)

func RefreshDB(logger *slog.Logger) *cobra.Command {
	var dbCfg = &store.Config{}
	cmd := &cobra.Command{
		Use:   "refresh-db",
		Short: "refresh the database from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := store.NewConn(dbCfg)
			if err != nil {
				return err
			}
			return store.InitDB(logger, metadataPath, conn)
		},
	}

	dbCfg.RegisterFlags(cmd.Flags(), "", "dialog")
	return cmd
}
