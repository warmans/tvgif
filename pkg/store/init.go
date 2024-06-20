package store

import (
	"github.com/jmoiron/sqlx"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/model"
	"log/slog"
)

func InitDB(logger *slog.Logger, metadataPath string, conn *Conn) error {
	if err := conn.Migrate(); err != nil {
		return err
	}
	return conn.WithTx(func(tx *sqlx.Tx) error {
		srtStore := NewSRTStore(tx)
		return metadata.Process(metadataPath, func(ep model.Episode) error {
			logger.Info("Processing episode...", slog.String("episode", ep.ID()))
			return srtStore.ImportEpisode(ep)
		})
	})
}
