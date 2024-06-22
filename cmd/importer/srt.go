package importer

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/metadata"
	"log/slog"
)

func NewImportSrtCommand(logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "srt",
		Short: "import all .srt files from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) != 1 {
				return fmt.Errorf("expecting exactly one argument: the directory to import")
			}
			mediaPath := args[0]
			return metadata.CreateMetadataFromSRTs(logger, mediaPath, metadataPath)
		},
	}
	return cmd
}
