package importer

import (
	"github.com/spf13/cobra"
	"log/slog"
)

var (
	metadataPath string
)

func NewImporterCommand(logger *slog.Logger) *cobra.Command {

	cmd := &cobra.Command{
		Use:   "importer",
		Short: "commands related to imports",
	}

	cmd.Flags().StringVar(&metadataPath, "o", "./var/metadata", "output metadata to this path")

	cmd.AddCommand(NewImportSrtCommand())
	cmd.AddCommand(PopulateBlugeIndex(logger))

	return cmd
}
