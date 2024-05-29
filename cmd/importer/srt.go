package importer

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/srt"
	"log/slog"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

func NewImportSrtCommand(logger *slog.Logger) *cobra.Command {
	var fileNamePattern string
	var publication string
	var videoExtension string

	cmd := &cobra.Command{
		Use:   "srt",
		Short: "import all .srt files from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) != 1 {
				return fmt.Errorf("expecting exactly one argument: the directory to import")
			}
			mediaPath := args[0]

			if publication == "" {
				return fmt.Errorf("expecting --publication to be set")
			}

			filePatternRegex, err := regexp.Compile(fileNamePattern)
			if err != nil {
				return fmt.Errorf("failed to compile file pattern: %w", err)
			}
			dirEntries, err := os.ReadDir(mediaPath)
			if err != nil {
				return fmt.Errorf("failed to read dir %s: %w", mediaPath, err)
			}
			for _, entry := range dirEntries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".srt") {
					continue
				}

				meta := &model.Episode{
					SRTFile:     entry.Name(),
					VideoFile:   fmt.Sprintf("%s.%s", strings.TrimSuffix(path.Base(entry.Name()), ".srt"), videoExtension),
					Publication: publication,
				}

				var err error
				meta.Series, meta.Episode, err = parseFileName(filePatternRegex, entry.Name())
				if err != nil {
					return err
				}
				meta.Dialog, err = parseSRT(path.Join(mediaPath, entry.Name()))
				if err != nil {
					return err
				}
				if err := writeMetadata(path.Join(metadataPath, fmt.Sprintf("%s.json", meta.ID())), meta); err != nil {
					return fmt.Errorf("failed to write metadata: %w", err)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&fileNamePattern, "file-pattern", `.*-S(?P<series>\d+)E(?P<episode>\d+)\.srt`, "Filename pattern is used to extract the series info from the file name")
	cmd.Flags().StringVar(&publication, "publication", "", "Publication name given to all files")
	cmd.Flags().StringVar(&videoExtension, "video-extension", ".webm", "File extension for video files")

	return cmd
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

func parseFileName(filePatternRegex *regexp.Regexp, filename string) (int64, int64, error) {

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

func parseSRT(filePath string) ([]model.Dialog, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open srt file %s: %w", filePath, err)
	}
	defer f.Close()
	return srt.Read(f)
}
