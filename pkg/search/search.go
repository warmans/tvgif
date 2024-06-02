package search

import (
	"context"
	"fmt"
	"github.com/blugelabs/bluge"
	search2 "github.com/blugelabs/bluge/search"
	"github.com/blugelabs/bluge/search/aggregations"
	"github.com/warmans/tvgif/pkg/filter"
	"github.com/warmans/tvgif/pkg/filter/bluge_query"
	"github.com/warmans/tvgif/pkg/search/model"
	"github.com/warmans/tvgif/pkg/util"
	"sort"
	"strings"
)

const (
	PageSize = 10
)

type Searcher interface {
	Search(ctx context.Context, f filter.Filter, page int32) ([]model.DialogDocument, error)
	Get(ctx context.Context, id string) (*model.DialogDocument, error)
	ListTerms(ctx context.Context, field string) ([]string, error)
}

func NewBlugeSearch(index *bluge.Reader) *BlugeSearch {
	return &BlugeSearch{index: index}
}

type BlugeSearch struct {
	index *bluge.Reader
}

func (b *BlugeSearch) Get(ctx context.Context, id string) (*model.DialogDocument, error) {
	q, err := bluge_query.FilterToQuery(filter.Eq("_id", filter.String(id)))
	if err != nil {
		return nil, fmt.Errorf("filter was invalid: %w", err)
	}
	docs, err := b.index.Search(ctx, bluge.NewTopNSearch(1, q))
	if err != nil {
		return nil, err
	}
	match, err := docs.Next()
	if err != nil {
		return nil, err
	}
	if match == nil {
		return nil, fmt.Errorf("no match found")
	}
	return scanDocument(match)
}

func (b *BlugeSearch) Search(ctx context.Context, f filter.Filter, page int32) ([]model.DialogDocument, error) {

	query, err := bluge_query.FilterToQuery(f)
	if err != nil {
		return nil, err
	}

	agg := aggregations.NewTermsAggregation(search2.Field("actor"), 25)
	agg.AddAggregation("transcript_id", aggregations.NewTermsAggregation(search2.Field("transcript_id"), 150))

	req := bluge.NewTopNSearch(PageSize, query).SetFrom(PageSize * int(page))
	req.AddAggregation("actor_count_over_time", agg)

	dmi, err := b.index.Search(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	match, err := dmi.Next()
	if err != nil {
		return nil, err
	}
	var results []model.DialogDocument
	for match != nil {
		res, err := scanDocument(match)
		if err != nil {
			return nil, err
		}
		if res != nil {
			results = append(results, *res)
		}
		match, err = dmi.Next()
		if err != nil {
			return nil, err
		}
	}
	return results, err
}

func (b *BlugeSearch) ListTerms(ctx context.Context, fieldName string) ([]string, error) {

	fieldDict, err := b.index.DictionaryIterator(fieldName, nil, []byte{}, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := fieldDict.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	tfd, err := fieldDict.Next()
	terms := []string{}
	for err == nil && tfd != nil && strings.TrimSpace(tfd.Term()) != "" {
		terms = append(terms, tfd.Term())
		if len(terms) > 100 {
			return nil, fmt.Errorf("too many terms for field '%s' returned", fieldName)
		}
		tfd, err = fieldDict.Next()
	}

	sort.Slice(terms, func(i, j int) bool {
		return terms[i] > terms[j]
	})

	return terms, nil
}

func scanDocument(match *search2.DocumentMatch) (*model.DialogDocument, error) {
	cur := &model.DialogDocument{}
	var innerErr error
	err := match.VisitStoredFields(func(field string, value []byte) bool {
		if field == "_id" {
			parts := strings.Split(string(value), "-")
			var err error
			cur.Series, cur.Episode, err = util.ExtractSeriesAndEpisode(parts[1])
			if err != nil {
				innerErr = fmt.Errorf("failed to scan details from id %s: %w", string(value), err)
				return false
			}
		}
		cur.SetNamedField(field, string(value))
		return true
	})
	if innerErr != nil {
		return nil, innerErr
	}
	if err != nil {
		return nil, err
	}
	return cur, nil
}
