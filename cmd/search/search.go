package search

import (
	"context"
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/davecgh/go-spew/spew"
	"github.com/spf13/cobra"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/searchterms"
	"log/slog"
)

func NewSearchCommand(logger *slog.Logger) *cobra.Command {

	var indexPath string

	cmd := &cobra.Command{
		Use:   "search",
		Short: "search the index",
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) == 0 {
				return cmd.Help()
			}

			filter := searchterms.TermsToFilter(searchterms.MustParse(args[0]))

			reader, err := bluge.OpenReader(bluge.DefaultConfig(indexPath))
			if err != nil {
				return fmt.Errorf("failed to open index: %w", err)
			}
			searcher := search.NewBlugeSearch(reader)
			res, err := searcher.Search(context.Background(), filter, 1)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}
			spew.Dump(res)
			return nil
		},
	}

	cmd.Flags().StringVar(&indexPath, "o", "./var/index/metadata.bluge", "path to index files")

	return cmd
}
