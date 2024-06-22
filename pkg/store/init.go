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
		return metadata.WithManifest(metadataPath, func(manifest *model.Manifest) error {
			return metadata.Process(metadataPath, func(fileName string, ep model.Episode) error {
				if meta, ok := manifest.Episodes[fileName]; ok {
					if meta.ImportedDB {
						return nil
					}
				} else {
					logger.Warn("Manifest seems to be out of date, skipping unknown file...", slog.String("file", fileName))
					return nil
				}
				logger.Info("Importing file to DB...", slog.String("file", fileName))
				if err := srtStore.ImportEpisode(ep); err != nil {
					return err
				}
				manifest.Episodes[fileName].ImportedDB = true
				return nil
			})
		})
	})
}
