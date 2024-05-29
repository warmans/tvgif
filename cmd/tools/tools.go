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

var nameWithShortSeasonAndEpisode = regexp.MustCompile(`^.*[sS](?P<series>\d+)[eE](?P<episode>\d+).*$`)
var nameWithLongSeasonAndEpisode = regexp.MustCompile(`^.*[sS]eason (?P<series>\d+) [eE]pisode (?P<episode>\d+).*$`)

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
			for _, re := range []*regexp.Regexp{nameWithShortSeasonAndEpisode, nameWithLongSeasonAndEpisode} {
				if re.MatchString(strName) {
					series, episode, err := util.ParseSeriesAndEpisodeFromFileName(re, strName)
					if err != nil {
						return err
					}
					fmt.Fprintf(os.Stdout, "S%02dE%02d\n", series, episode)
					return nil
				}
			}
			return fmt.Errorf("no regex matched: %s", strName)
		},
	}

	return cmd
}
