package bluge_query

import (
	"github.com/warmans/tvgif/pkg/searchterms"
	"github.com/warmans/tvgif/pkg/util"
	"reflect"
	"testing"
)

func Test_extractOffset(t *testing.T) {
	tests := []struct {
		name  string
		terms []searchterms.Term
		want  []searchterms.Term
		want1 *int64
	}{
		{
			name:  "empty terms returns empty, nil",
			terms: make([]searchterms.Term, 0),
			want:  make([]searchterms.Term, 0),
			want1: nil,
		},
		{
			name: "no offset returns original terms",
			terms: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
				{Field: "series", Value: searchterms.Int(1), Op: searchterms.CompOpEq},
			},
			want: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
				{Field: "series", Value: searchterms.Int(1), Op: searchterms.CompOpEq},
			},
			want1: nil,
		}, {
			name: "no offset returns original terms",
			terms: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
				{Field: "series", Value: searchterms.Int(1), Op: searchterms.CompOpEq},
			},
			want: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
				{Field: "series", Value: searchterms.Int(1), Op: searchterms.CompOpEq},
			},
			want1: nil,
		}, {
			name: "offset is extracted from last position",
			terms: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
				{Field: "offset", Value: searchterms.Int(10), Op: searchterms.CompOpEq},
			},
			want: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
			},
			want1: util.ToPtr(int64(10)),
		}, {
			name: "offset is extracted from first position",
			terms: []searchterms.Term{
				{Field: "offset", Value: searchterms.Int(10), Op: searchterms.CompOpEq},
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
			},
			want: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
			},
			want1: util.ToPtr(int64(10)),
		}, {
			name: "offset is extracted from middle position",
			terms: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "offset", Value: searchterms.Int(10), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
			},
			want: []searchterms.Term{
				{Field: "actor", Value: searchterms.String("steve"), Op: searchterms.CompOpEq},
				{Field: "publication", Value: searchterms.String("xfm"), Op: searchterms.CompOpEq},
			},
			want1: util.ToPtr(int64(10)),
		}, {
			name: "offset is only filter",
			terms: []searchterms.Term{
				{Field: "offset", Value: searchterms.Int(10), Op: searchterms.CompOpEq},
			},
			want:  []searchterms.Term{},
			want1: util.ToPtr(int64(10)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := extractOffset(tt.terms)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractOffset() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("extractOffset() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
