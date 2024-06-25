package tools

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/util"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

var (
	metadataPath string
)

func NewToolsCommand(logger *slog.Logger) *cobra.Command {

	cmd := &cobra.Command{
		Use:   "tools",
		Short: "small tools for working with files",
	}

	cmd.Flags().StringVar(&metadataPath, "o", "./var/metadata", "output metadata to this path")

	cmd.AddCommand(NewFixNameCommand())

	return cmd
}

var nameWithShortSeasonAndEpisode = regexp.MustCompile(`^.*[sS](?P<series>\d+)(\s+)?[eE](?P<episode>\d+).*$`)
var nameWithLongSeasonAndEpisode = regexp.MustCompile(`^.*[sS]eason (?P<series>\d+) [eE]pisode (?P<episode>\d+).*$`)
var nameWithSplitSeasonAndEpisode = regexp.MustCompile(`^.*[sS](?P<series>\d+)\.[eE](?P<episode>\d+).*$`)
var shortSeasonAndEpisode = regexp.MustCompile(`^.*(?P<series>\d+)x(?P<episode>\d+).*$`)

func NewFixNameCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fix-name",
		Short: "extract a correctly formatted episode name from stdin",
		RunE: func(cmd *cobra.Command, args []string) error {

			rawName, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}

			strName := strings.TrimSpace(string(rawName))
			for _, re := range []*regexp.Regexp{nameWithShortSeasonAndEpisode, nameWithLongSeasonAndEpisode, nameWithSplitSeasonAndEpisode, shortSeasonAndEpisode} {
				if re.MatchString(strName) {
					series, episode, err := util.ParseSeriesAndEpisodeFromFileName(re, strName)
					if err != nil {
						return err
					}
					fixedName := fmt.Sprintf("S%02dE%02d", series, episode)
					if fixedName == strName {
						return fmt.Errorf("rename was a noop")
					}
					fmt.Fprintf(os.Stdout, "%s\n", fixedName)
					return nil
				}
			}
			return fmt.Errorf("no regex matched: %s", strName)
		},
	}

	return cmd
}
