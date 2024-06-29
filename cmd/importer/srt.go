package importer

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/srt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

func NewImportSrtCommand(logger *slog.Logger) *cobra.Command {
	var clean bool
	cmd := &cobra.Command{
		Use:   "srt",
		Short: "import all .srt files from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("expecting exactly one argument: the directory to import")
			}
			mediaPath := args[0]
			if clean {
				logger.Info("Cleaning existing metadata...")
				if err := exec.Command("rm", "-rf", metadataPath).Run(); err != nil {
					return fmt.Errorf("failed to clean metadata: %w", err)
				}
			}
			return metadata.CreateMetadataFromSRTs(logger, mediaPath, metadataPath)
		},
	}

	cmd.Flags().BoolVar(&clean, "clean", false, "delete metadata first")
	return cmd
}

func NewValidateSrtCommand(logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate-srt",
		Short: "validate all .srt files from the given directory",
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) != 1 {
				return fmt.Errorf("expecting exactly one argument: the directory to validate")
			}
			mediaPath := args[0]

			dirEntries, err := os.ReadDir(mediaPath)
			if err != nil {
				return err
			}
			for _, dirEntry := range dirEntries {
				if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".srt") {
					continue
				}
				logger.Info("Validating SRT...", slog.String("name", dirEntry.Name()))
				if err := func() error {
					f, err := os.Open(path.Join(metadataPath, dirEntry.Name()))
					if err != nil {
						return err
					}
					defer f.Close()

					_, err = srt.Read(f, true, time.Second*15)
					return err
				}(); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return cmd
}
