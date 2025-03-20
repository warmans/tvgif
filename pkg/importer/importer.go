package importer

import (
	"context"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/jmoiron/sqlx"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/store"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"
)

const filePollingInterval = time.Second * 10

type pendingFile struct {
	srtFilePath string
	modTime     time.Time
}

func NewIncrementalImporter(
	srtDir string,
	metadataDir string,
	varDir string,
	conn *store.Conn,
	searcher *search.BlugeSearch,
	logger *slog.Logger,
	useFilePolling bool,
) *Incremental {
	return &Incremental{
		srtDir:         srtDir,
		metadataDir:    metadataDir,
		varDir:         varDir,
		conn:           conn,
		searcher:       searcher,
		logger:         logger,
		useFilePolling: useFilePolling,
	}
}

type Incremental struct {
	srtDir         string
	metadataDir    string
	varDir         string
	conn           *store.Conn
	searcher       *search.BlugeSearch
	logger         *slog.Logger
	useFilePolling bool
}

func (i *Incremental) Start(ctx context.Context) error {

	i.logger.Info("Starting initial file sync...")
	if err := i.importAllNew(ctx); err != nil {
		return err
	}

	i.logger.Info("Starting incremental file sync...", slog.Bool("polling", i.useFilePolling))
	if i.useFilePolling {
		return i.startFilePolling(ctx)
	}
	return i.startFileWatch(ctx)
}

func (i *Incremental) startFilePolling(ctx context.Context) error {
	for {
		time.Sleep(filePollingInterval)
		if err := i.importAllNew(ctx); err != nil {
			return err
		}
	}
}

func (i *Incremental) startFileWatch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// since files are typically added in batches
	// group up as many pending files as are detected in a 2s period
	// instead of dispatching an import for each file.
	ticker := time.NewTicker(time.Second * 2)
	var pendingFiles []pendingFile

	// Start listening for events.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok || !strings.HasSuffix(event.Name, ".srt") {
					return
				}
				if event.Has(fsnotify.Create) {
					stat, err := os.Stat(event.Name)
					if err != nil {
						i.logger.Error("failed stat file", slog.String("err", err.Error()))
						continue
					}
					pendingFiles = append(pendingFiles, pendingFile{srtFilePath: event.Name, modTime: stat.ModTime()})
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				i.logger.Info("error", slog.String("err", err.Error()))
			case <-ticker.C:
				if len(pendingFiles) > 0 {
					if err := i.importNewSRT(ctx, pendingFiles); err != nil {
						i.logger.Error(
							"Failed to import pending files",
							slog.String("err", err.Error()),
						)
					}
					pendingFiles = []pendingFile{}
				}
			}
		}
	}()

	// Add a path.
	err = watcher.Add(i.srtDir)
	if err != nil {
		return err
	}

	<-ctx.Done()

	return nil
}

func (i *Incremental) importAllNew(ctx context.Context) error {
	manifest, err := store.NewSRTStore(i.conn.Db).GetManifest()
	if err != nil {
		return err
	}

	dirEntries, err := os.ReadDir(i.srtDir)
	if err != nil {
		return err
	}

	toImport := []pendingFile{}
	for _, v := range dirEntries {
		if !strings.HasSuffix(v.Name(), ".srt") {
			continue
		}
		inf, err := v.Info()
		if err != nil {
			return err
		}
		addToImport := false
		if oldModTime, ok := manifest[path.Join(i.srtDir, v.Name())]; ok {
			if inf.ModTime().After(oldModTime) {
				i.logger.Info("file older than existing",
					slog.String("path", path.Join(i.srtDir, v.Name())),
					slog.Time("old", oldModTime),
					slog.Time("new", inf.ModTime()),
				)
				addToImport = true
			}
		} else {
			addToImport = true
		}
		if addToImport {
			toImport = append(toImport, pendingFile{srtFilePath: path.Join(i.srtDir, v.Name()), modTime: inf.ModTime()})
		}
	}
	if len(toImport) == 0 {
		return nil
	}

	i.logger.Info("Importing files...", slog.Int("num_files", len(toImport)), slog.String("dir", i.srtDir))
	if err := i.importNewSRT(ctx, toImport); err != nil {
		i.logger.Error(
			"Failed to import pending files",
			slog.String("err", err.Error()),
		)
	}
	return nil
}

func (i *Incremental) importNewSRT(ctx context.Context, pendingFiles []pendingFile) error {

	for k, pending := range pendingFiles {
		err := i.conn.WithTx(func(tx *sqlx.Tx) error {
			meta, err := metadata.CreateMetadataFromSRT(pending.srtFilePath, i.metadataDir, i.varDir)
			if err != nil {
				return fmt.Errorf("failed to create metadata: %w", err)
			}
			logger := i.logger.With(slog.String("episode_id", meta.ID()), slog.Time("modtime", pending.modTime))

			s := store.NewSRTStore(tx)
			result, err := s.ManifestAdd(pending.srtFilePath, pending.modTime)
			if err != nil {
				return fmt.Errorf("failed to add to manifest: %w", err)
			}
			if result == store.UpsertResultNoop {
				// nothing to do
				logger.Info("File already processed, skipped")
				return nil
			}

			if err := s.ImportEpisode(*meta); err != nil {
				return err
			}

			logger.Info("Import to index...", slog.String("result", string(result)), slog.Float64("progress", float64(k)/float64(len(pendingFiles))*100))
			return i.searcher.Import(ctx, meta, result == store.UpsertResultUpdated)
		})
		if err != nil {
			return err
		}
		if k%100 == 0 {
			if err := i.searcher.RefreshIndex(); err != nil {
				return err
			}
		}
	}
	return i.searcher.RefreshIndex()
}
