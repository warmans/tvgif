package importer

import (
	"encoding/json"
	"fmt"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/srt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var filePatternRegex = regexp.MustCompile(`(?P<publication>[a-zA-Z0-9]+)-S(?P<series>\d+)E(?P<episode>\d+)\.srt`)

const videoExtension = ".webm"

func CreateMetadataFromSRTs(srtPath string, metadataPath string) error {

	dirEntries, err := os.ReadDir(srtPath)
	if err != nil {
		return fmt.Errorf("failed to read dir %s: %w", srtPath, err)
	}
	for _, entry := range dirEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".srt") {
			continue
		}

		meta := &model.Episode{
			SRTFile:   entry.Name(),
			VideoFile: fmt.Sprintf("%s.%s", strings.TrimSuffix(path.Base(entry.Name()), ".srt"), strings.TrimPrefix(videoExtension, ".")),
		}
		var err error
		meta.Publication, meta.Series, meta.Episode, err = parseFileName(filePatternRegex, entry.Name())
		if err != nil {
			return err
		}
		meta.Dialog, err = parseSRT(path.Join(srtPath, entry.Name()))
		if err != nil {
			return fmt.Errorf("failed to process SRT %s: %w", entry.Name(), err)
		}
		if err := writeMetadata(path.Join(metadataPath, fmt.Sprintf("%s.json", meta.ID())), meta); err != nil {
			return fmt.Errorf("failed to write metadata: %w", err)
		}
	}

	return nil
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

func parseFileName(filePatternRegex *regexp.Regexp, filename string) (string, int64, int64, error) {

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
		seriesInt, err = strconv.ParseInt(strings.TrimLeft(seriesStr, "0"), 10, 64)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to parse matched series int %s: %w", seriesStr, err)
		}
	} else {
		return "", 0, 0, fmt.Errorf("file pattern did not match series in : %s", filename)
	}
	var episodeInt int64
	if episodeStr, ok := result["episode"]; ok && episodeStr != "" {
		episodeInt, err = strconv.ParseInt(strings.TrimLeft(episodeStr, "0"), 10, 64)
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
	return publication, seriesInt, episodeInt, nil
}

func parseSRT(filePath string) ([]model.Dialog, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open srt file %s: %w", filePath, err)
	}
	defer f.Close()
	return srt.Read(f)
}
