package search

import (
	"context"
	"fmt"
	"github.com/blugelabs/bluge"
	search2 "github.com/blugelabs/bluge/search"
	"github.com/warmans/tvgif/pkg/search/model"
	"github.com/warmans/tvgif/pkg/searchterms"
	"github.com/warmans/tvgif/pkg/searchterms/bluge_query"
	"github.com/warmans/tvgif/pkg/util"
	"sort"
	"strings"
	"sync"
)

const (
	DefaultPageSize = 10
)

type searchOverrides struct {
	pageSize *int
}

type Override func(overrides *searchOverrides)

func OverridePageSize(pageSize int) Override {
	return func(overrides *searchOverrides) {
		overrides.pageSize = util.ToPtr(pageSize)
	}
}

func resolveOverrides(opts []Override) *searchOverrides {
	overrides := &searchOverrides{}
	for _, v := range opts {
		v(overrides)
	}
	return overrides
}

type Searcher interface {
	Search(ctx context.Context, f []searchterms.Term, overrides ...Override) ([]model.DialogDocument, error)
	Get(ctx context.Context, id string) (*model.DialogDocument, error)
	ListTerms(ctx context.Context, field string) ([]string, error)
}

func NewBlugeSearch(indexPath string) (*BlugeSearch, error) {
	s := &BlugeSearch{
		indexReadLock: &sync.RWMutex{},
		indexPath:     indexPath,
	}
	if err := s.RefreshIndex(); err != nil {
		return nil, err
	}
	return s, nil
}

type BlugeSearch struct {
	indexReadLock *sync.RWMutex
	index         *bluge.Reader
	indexPath     string
}

func (b *BlugeSearch) RefreshIndex() error {
	b.indexReadLock.Lock()
	defer b.indexReadLock.Unlock()
	reader, err := bluge.OpenReader(bluge.DefaultConfig(b.indexPath))
	if err != nil {
		return fmt.Errorf("failed to open index: %w", err)
	}
	b.index = reader
	return nil

}

func (b *BlugeSearch) withSnapshot(fn func(r *bluge.Reader) error) error {
	b.indexReadLock.RLock()
	defer b.indexReadLock.RUnlock()
	return fn(b.index)
}

func (b *BlugeSearch) Get(ctx context.Context, id string) (*model.DialogDocument, error) {
	q, _, err := bluge_query.NewBlugeQuery([]searchterms.Term{{Field: "_id", Value: searchterms.String(id), Op: searchterms.CompOpEq}})
	if err != nil {
		return nil, fmt.Errorf("filter was invalid: %w", err)
	}
	var match *search2.DocumentMatch
	if err := b.withSnapshot(func(r *bluge.Reader) error {
		docs, err := b.index.Search(ctx, bluge.NewTopNSearch(1, q))
		if err != nil {
			return err
		}
		match, err = docs.Next()
		if err != nil {
			return err
		}
		if match == nil {
			return fmt.Errorf("no match found")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return scanDocument(match)
}

func (b *BlugeSearch) Search(ctx context.Context, f []searchterms.Term, overrides ...Override) ([]model.DialogDocument, error) {

	opts := resolveOverrides(overrides)

	query, offset, err := bluge_query.NewBlugeQuery(f)
	if err != nil {
		return nil, err
	}

	setFrom := 0
	if offset != nil {
		setFrom = int(*offset)
	}

	pageSize := DefaultPageSize
	if opts.pageSize != nil {
		pageSize = *opts.pageSize
	}

	req := bluge.NewTopNSearch(pageSize, query).SetFrom(setFrom)

	var results []model.DialogDocument
	if err := b.withSnapshot(func(r *bluge.Reader) error {
		dmi, err := b.index.Search(ctx, req)
		if err != nil {
			return err
		}
		match, err := dmi.Next()
		if err != nil {
			return err
		}

		for match != nil {
			res, err := scanDocument(match)
			if err != nil {
				return err
			}
			if res != nil {
				results = append(results, *res)
			}
			match, err = dmi.Next()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return results, err
}

func (b *BlugeSearch) ListTerms(ctx context.Context, fieldName string) ([]string, error) {

	terms := []string{}
	err := b.withSnapshot(func(r *bluge.Reader) error {
		fieldDict, err := b.index.DictionaryIterator(fieldName, nil, []byte{}, nil)
		if err != nil {
			return err
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
				return fmt.Errorf("too many terms for field '%s' returned", fieldName)
			}
			tfd, err = fieldDict.Next()
		}

		sort.Slice(terms, func(i, j int) bool {
			return terms[i] > terms[j]
		})

		return nil
	})

	return terms, err
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
		cur.SetNamedField(field, value)
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
