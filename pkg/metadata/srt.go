package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/warmans/tvgif/pkg/limits"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/srt"
	"github.com/warmans/tvgif/pkg/util"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var filePatternRegex = regexp.MustCompile(`(?P<publication>[a-zA-Z0-9]+)-S(?P<series>\d+)E(?P<episode>\d+)\.srt`)

const videoExtension = ".webm"

const publicationAliasFile = "publications_aliases.json"

func CreateMetadataFromSRT(srtPath, metadataDir, varDir string) (*model.Episode, error) {

	srtName := path.Base(srtPath)

	publicationMapping, err := readPublicationMapping(varDir)
	if err != nil {
		return nil, err
	}

	meta := &model.Episode{
		SRTFile:   srtName,
		VideoFile: fmt.Sprintf("%s.%s", strings.TrimSuffix(path.Base(srtName), ".srt"), strings.TrimPrefix(videoExtension, ".")),
	}
	meta.Publication, meta.Series, meta.Episode, err = parseFileName(filePatternRegex, srtName)
	if err != nil {
		return nil, err
	}

	// allow a publication to be assigned a group for an aliases file
	if publicationGroup, ok := publicationMapping[meta.Publication]; ok {
		meta.PublicationGroup = publicationGroup
	}

	fileName := fmt.Sprintf("%s.json", meta.ID())
	metaPath := path.Join(metadataDir, fileName)

	meta.Dialog, err = parseSRT(srtPath)
	if err != nil {
		return nil, fmt.Errorf("failed to process SRT %s: %w", srtName, err)
	}

	if err := writeMetadata(metaPath, meta); err != nil {
		return nil, fmt.Errorf("failed to write metadata: %w", err)
	}
	return meta, nil
}

func readPublicationMapping(metadataDir string) (map[string]string, error) {
	data, err := os.ReadFile(path.Join(metadataDir, publicationAliasFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", publicationAliasFile, err)
	}
	var result map[string]string
	return result, json.Unmarshal(data, &result)
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
		seriesInt, err = strconv.ParseInt(util.NormaliseNumericIdentifier(seriesStr), 10, 32)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to parse matched series int %s: %w", seriesStr, err)
		}
	} else {
		return "", 0, 0, fmt.Errorf("file pattern did not match series in : %s", filename)
	}
	var episodeInt int64
	if episodeStr, ok := result["episode"]; ok && episodeStr != "" {
		episodeInt, err = strconv.ParseInt(util.NormaliseNumericIdentifier(episodeStr), 10, 32)
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

func parseSRT(filePath string) ([]model.Dialog, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open srt file %s: %w", filePath, err)
	}
	defer f.Close()

	dialog, err := srt.Read(f, true, limits.MaxGifDuration)
	if err != nil {
		return nil, err
	}
	return dialog, nil
}
