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
	"strconv"
	"strings"
)

const (
	PageSize = 10
)

type Searcher interface {
	Search(ctx context.Context, f filter.Filter, page int32) ([]model.DialogDocument, error)
	Get(ctx context.Context, id string) (*model.DialogDocument, error)
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

// format is [publication]-S[series]E[episode]-[startTs]:[endTs]
func documentFromID(id string) (*model.DialogDocument, error) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid document id: %s", id)
	}
	doc := &model.DialogDocument{
		ID:        id,
		EpisodeID: strings.TrimSuffix(id, fmt.Sprintf("-%s", parts[2])),
	}
	doc.Publication = parts[0]
	episodeParts := strings.Split(parts[1], "E")
	if len(episodeParts) != 2 {
		return nil, fmt.Errorf("invalid episode specification: %s", parts[1])
	}
	series, err := strconv.Atoi(strings.TrimPrefix(episodeParts[0], "S"))
	if err != nil {
		return nil, fmt.Errorf("invalid series specification %s: %w", episodeParts[0], err)
	}
	doc.Series = int64(series)

	episode, err := strconv.Atoi(strings.TrimPrefix(episodeParts[1], "S"))
	if err != nil {
		return nil, fmt.Errorf("invalid episode specification %s: %w", episodeParts[1], err)
	}
	doc.Episode = int64(episode)

	timestamps := strings.Split(parts[2], ":")
	if len(timestamps) != 2 {
		return nil, fmt.Errorf("invalid timestamp specification: %s", parts[2])
	}
	doc.StartTimestamp = timestamps[0]
	doc.EndTimestamp = timestamps[1]

	return doc, nil
}

func scanDocument(match *search2.DocumentMatch) (*model.DialogDocument, error) {
	var innerErr error
	cur := &model.DialogDocument{}
	err := match.VisitStoredFields(func(field string, value []byte) bool {
		if field == "_id" {
			cur, innerErr = documentFromID(string(value))
			if innerErr != nil {
				return false
			}
		}
		if field == "content" {
			cur.Content = string(value)
		}
		if field == "video_file_name" {
			cur.VideoFileName = string(value)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if innerErr != nil {
		return nil, innerErr
	}
	return cur, nil
}
