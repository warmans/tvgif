package util

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func TrimToN(line string, maxLength int) string {
	if len(line) <= maxLength {
		return line
	}
	return line[:maxLength-4] + "..."
}

func ToPtr[T any](v T) *T {
	return &v
}

func ParseSeriesAndEpisodeFromFileName(filePatternRegex *regexp.Regexp, filename string) (int64, int64, error) {

	match := filePatternRegex.FindStringSubmatch(filename)

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
			return 0, 0, fmt.Errorf("failed to parse matched series int %s: %w", seriesStr, err)
		}
	} else {
		return 0, 0, fmt.Errorf("file pattern did not match series in : %s", filename)
	}
	var episodeInt int64
	if episodeStr, ok := result["episode"]; ok && episodeStr != "" {
		episodeInt, err = strconv.ParseInt(strings.TrimLeft(episodeStr, "0"), 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to parse matched episode int %s: %w", episodeStr, err)
		}
	} else {
		return 0, 0, fmt.Errorf("file pattern did not match [episode]")
	}
	return seriesInt, episodeInt, nil
}
