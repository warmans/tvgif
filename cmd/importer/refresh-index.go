package importer

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/search"
	"log/slog"
)

func PopulateBlugeIndex(logger *slog.Logger) *cobra.Command {

	var indexPath string

	cmd := &cobra.Command{
		Use:   "refresh-index",
		Short: "refresh the search index from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {

			fmt.Printf("Using index %s...\n", indexPath)

			return search.PopulateIndex(logger, metadataPath, indexPath)
		},
	}

	cmd.Flags().StringVarP(&indexPath, "index-path", "i", "./var/index/metadata.bluge", "Path to index file")

	return cmd
}
