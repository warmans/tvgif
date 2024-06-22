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
	"syscall"
)

const ManifestName = ".manifest.json"

func WithManifest(metadataDir string, fn func(manifest *model.Manifest) error) error {
	manifestPath := path.Join(metadataDir, ManifestName)
	f, err := os.OpenFile(manifestPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		if errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("manifest is locked")
		}
		return fmt.Errorf("failed to open manifest: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			panic("failed to unlock file: " + err.Error())
		}
		f.Close()
	}()

	manifest := &model.Manifest{
		Episodes: map[string]*model.EpisodeMeta{},
		SrtIndex: map[string]string{},
	}
	if err := json.NewDecoder(f).Decode(manifest); err != nil {
		if !errors.Is(err, io.EOF) {
			return err
		}
	}

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
	return WithManifest(metadataDir, func(manifest *model.Manifest) error {
		dirEntries, err := os.ReadDir(srtDir)
		if err != nil {
			return err
		}
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".srt") {
				continue
			}
			if _, ok := manifest.SrtIndex[dirEntry.Name()]; ok {
				logger.Debug("Meta exists, skipping.", slog.String("name", dirEntry.Name()))
				continue
			}
			metaFilePath := path.Join(srtDir, dirEntry.Name())
			logger.Info("Create metadata...", slog.String("name", metaFilePath))
			fileName, err := CreateMetadataFromSRT(metaFilePath, metadataDir)
			if err != nil {
				return err
			}
			manifest.Add(fileName, &model.EpisodeMeta{
				SourceSRTName: dirEntry.Name(),
			})
		}
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
