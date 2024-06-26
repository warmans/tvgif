package metadata

import (
	"encoding/json"
	"fmt"
	"github.com/warmans/tvgif/pkg/limits"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/srt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var filePatternRegex = regexp.MustCompile(`(?P<publication>[a-zA-Z0-9]+)-S(?P<series>\d+)E(?P<episode>\d+)\.srt`)

const videoExtension = ".webm"

func CreateMetadataFromSRT(srtPath, metadataDir string) (string, time.Time, error) {

	srtName := path.Base(srtPath)

	meta := &model.Episode{
		SRTFile:   srtName,
		VideoFile: fmt.Sprintf("%s.%s", strings.TrimSuffix(path.Base(srtName), ".srt"), strings.TrimPrefix(videoExtension, ".")),
	}
	var err error
	meta.Publication, meta.Series, meta.Episode, err = parseFileName(filePatternRegex, srtName)
	if err != nil {
		return "", time.Time{}, err
	}
	var modTime time.Time
	meta.Dialog, modTime, err = parseSRT(srtPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to process SRT %s: %w", srtName, err)
	}
	fileName := fmt.Sprintf("%s.json", meta.ID())
	if err := writeMetadata(path.Join(metadataDir, fileName), meta); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to write metadata: %w", err)
	}
	return fileName, modTime, nil
}

func writeMetadata(path string, e *model.Episode) error {

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file for writing %s: %w", path, err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")

	return enc.Encode(e)
}

func parseFileName(filePatternRegex *regexp.Regexp, filename string) (string, int32, int32, error) {

	match := filePatternRegex.FindStringSubmatch(filename)
	if len(match) < 3 {
		return "", 0, 0, fmt.Errorf("failed to match file name %s", filename)
	}
	result := make(map[string]string)
	for i, name := range filePatternRegex.SubexpNames() {
		if i != 0 && name != "" {
			result[name] = match[i]
		}
	}

	var err error
	var seriesInt int64
	if seriesStr, ok := result["series"]; ok && seriesStr != "" {
		seriesInt, err = strconv.ParseInt(strings.TrimLeft(seriesStr, "0"), 10, 32)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to parse matched series int %s: %w", seriesStr, err)
		}
	} else {
		return "", 0, 0, fmt.Errorf("file pattern did not match series in : %s", filename)
	}
	var episodeInt int64
	if episodeStr, ok := result["episode"]; ok && episodeStr != "" {
		episodeInt, err = strconv.ParseInt(strings.TrimLeft(episodeStr, "0"), 10, 32)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to parse matched episode int %s: %w", episodeStr, err)
		}
	} else {
		return "", 0, 0, fmt.Errorf("file pattern did not match [episode]")
	}
	publication := ""
	if publicationStr, ok := result["publication"]; ok && publicationStr != "" {
		publication = publicationStr
	}
	return publication, int32(seriesInt), int32(episodeInt), nil
}

func parseSRT(filePath string) ([]model.Dialog, time.Time, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("failed to open srt file %s: %w", filePath, err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	dialog, err := srt.Read(f, true, limits.MaxGifDuration)
	if err != nil {
		return nil, time.Time{}, err
	}
	return dialog, fileInfo.ModTime(), nil
}
