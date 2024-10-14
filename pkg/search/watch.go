package search

import (
	"context"
	"errors"
	"fmt"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/store"
	"log/slog"
	"os"
	"sync"
	"time"
)

func NewBlugeRefresher(
	metadataPath string,
	mediaPath string,
	indexPath string,
	index *BlugeSearch,
	dbConn *store.Conn,
	logger *slog.Logger) *BlugeRefresher {
	return &BlugeRefresher{
		metadataPath: metadataPath,
		mediaPath:    mediaPath,
		indexPath:    indexPath,
		index:        index,
		dbConn:       dbConn,
		logger:       logger,
	}
}

type BlugeRefresher struct {
	metadataPath string
	mediaPath    string
	indexPath    string
	index        *BlugeSearch
	dbConn       *store.Conn
	logger       *slog.Logger

	refreshing sync.Mutex
}

func (w *BlugeRefresher) Refresh() error {
	if !w.refreshing.TryLock() {
		// refresh already in progress
		return nil
	}
	w.logger.Info("Refreshing...")

	updateFn := func() {
		defer func() {
			w.refreshing.Unlock()
			w.logger.Info("Refresh completed!")
		}()
		w.logger.Info("Updating Metadata...", slog.String("path", w.metadataPath))
		if err := metadata.CreateMetadataFromSRTs(w.logger, w.mediaPath, w.metadataPath); err != nil {
			w.logger.Error("failed to update metadata", slog.String("err", err.Error()))
			return
		}
		w.logger.Info("Updating Index...", slog.String("path", w.indexPath))
		if err := PopulateIndex(w.logger, w.metadataPath, w.indexPath); err != nil {
			w.logger.Error("failed to update index", slog.String("err", err.Error()))
		}
		w.logger.Info("Updating DB...")
		if err := store.InitDB(w.logger, w.metadataPath, w.dbConn); err != nil {
			w.logger.Error("failed to update db", slog.String("err", err.Error()))
		}
		w.logger.Info("Refresh index snapshot...")
		if err := w.index.RefreshIndex(); err != nil {
			w.logger.Error("failed to refresh index snapshot", slog.String("err", err.Error()))
		}
	}
	if _, err := os.Stat(w.indexPath); err == nil {
		w.logger.Info("Index exists, performing async update")
		go updateFn()
	} else {
		if errors.Is(err, os.ErrNotExist) {
			// it's not possible to open the index if it hasn't been created.
			// So if there is no index the first import must happen before the bot starts.
			w.logger.Info("Index does not exist, performing sync update")
			updateFn()
		} else {
			w.refreshing.Unlock()
			return fmt.Errorf("failed to stat index path: %w", err)
		}
	}

	return nil
}

func (w *BlugeRefresher) Schedule(ctx context.Context, interval time.Duration) {
	w.logger.Info("Starting refresh cycle", slog.Duration("interval", interval))
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Refresh(); err != nil {
				w.logger.Error("Refresh failed", slog.String("err", err.Error()))
			}
		}
	}
}
