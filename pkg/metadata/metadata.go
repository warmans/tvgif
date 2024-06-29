package metadata

import (
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"github.com/warmans/tvgif/pkg/model"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
)

const ManifestName = ".manifest.json"

func WithManifest(metadataDir string, fn func(manifest *model.Manifest) error) error {
	manifestPath := path.Join(metadataDir, ManifestName)
	fmt.Println("open file...")
	f, err := os.OpenFile(manifestPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		if errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("manifest is locked")
		}
		return fmt.Errorf("failed to open manifest: %w", err)
	}
	fmt.Println("awaiting lock...")
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		fmt.Println("awaiting unlock...")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			panic("failed to unlock file: " + err.Error())
		}
		fmt.Println("awaiting close...")
		f.Close()
	}()

	manifest := &model.Manifest{
		Episodes: map[string]*model.EpisodeMeta{},
		SrtIndex: map[string]string{},
	}
	fmt.Println("decoding file...")
	if err := json.NewDecoder(f).Decode(manifest); err != nil {
		if !errors.Is(err, io.EOF) {
			return err
		}
	}

	fmt.Println("truncate...")
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "    ")
	if err = fn(manifest); err != nil {
		if encodeErr := encoder.Encode(manifest); encodeErr != nil {
			return fmt.Errorf("failed write manifest after SRT improt failure: %w (%s)", encodeErr, err.Error())
		}
		return err
	}
	return encoder.Encode(manifest)
}

func CreateMetadataFromSRTs(logger *slog.Logger, srtDir string, metadataDir string) error {
	_, err := os.Stat(metadataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Info("Creating metadata dir...", slog.String("path", metadataDir))
			if err = os.MkdirAll(metadataDir, 0755); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	logger.Info("Updating metadata...")
	return WithManifest(metadataDir, func(manifest *model.Manifest) error {

		numConcurrentImports := 25
		wg := sync.WaitGroup{}
		work := make(chan struct{}, numConcurrentImports)

		logger.Info("Listing SRTs...")
		dirEntries, err := os.ReadDir(srtDir)
		if err != nil {
			return err
		}
		for _, dirEntry := range dirEntries {
			entryLogger := logger.With(slog.String("name", dirEntry.Name()))
			if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".srt") {
				continue
			}
			if manifest.SrtExists(dirEntry.Name()) {
				entryLogger.Debug("Meta exists, skipping.")
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					<-work
				}()
				srtPath := path.Join(srtDir, dirEntry.Name())
				logger.Info("Create metadata...", slog.String("srt", srtPath))
				fileName, err := CreateMetadataFromSRT(srtPath, metadataDir)
				if err != nil {
					logger.Error("Invalid SRT, skipping.", slog.String("err", err.Error()), slog.String("srt", srtPath))
					return
				}
				manifest.Add(fileName, &model.EpisodeMeta{
					SourceSRTName: dirEntry.Name(),
				})
			}()
			work <- struct{}{}
		}

		wg.Wait()
		return nil
	})
}

func Process(inputDir string, fn func(fileName string, ep model.Episode) error) error {
	dirEntries, err := os.ReadDir(inputDir)
	if err != nil {
		return err
	}
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".json") || strings.HasPrefix(dirEntry.Name(), ".") {
			continue
		}
		f, err := os.Open(path.Join(inputDir, dirEntry.Name()))
		if err != nil {
			return err
		}
		if err := func() error {
			defer f.Close()
			episode := &model.Episode{}
			if err := json.NewDecoder(f).Decode(episode); err != nil {
				return err
			}
			if err := fn(dirEntry.Name(), *episode); err != nil {
				return err
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}
