package searchterms

import (
	"github.com/warmans/tvgif/pkg/filter"
	"reflect"
	"testing"
)

func TestMustParse(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name string
		args args
		want []Term
	}{
		{
			name: "parse word",
			args: args{s: "foo"},
			want: []Term{{Field: "content", Value: String("foo"), Op: filter.CompOpFuzzyLike}},
		},
		{
			name: "parse words",
			args: args{s: "foo bar baz"},
			want: []Term{{Field: "content", Value: String("foo bar baz"), Op: filter.CompOpFuzzyLike}},
		},
		{
			name: "parse quoted string",
			args: args{s: `"foo bar"`},
			want: []Term{{Field: "content", Value: String("foo bar"), Op: filter.CompOpEq}},
		},
		{
			name: "parse quoted strings",
			args: args{s: `"foo bar" "baz"`},
			want: []Term{
				{Field: "content", Value: String("foo bar"), Op: filter.CompOpEq},
				{Field: "content", Value: String("baz"), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse publication",
			args: args{s: `~xfm`},
			want: []Term{
				{Field: "publication", Value: String("xfm"), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse mention",
			args: args{s: `@steve`},
			want: []Term{
				{Field: "actor", Value: String("steve"), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse id",
			args: args{s: `#s01e05`},
			want: []Term{
				{Field: "series", Value: Int(1), Op: filter.CompOpEq},
				{Field: "episode", Value: Int(5), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse id",
			args: args{s: `#E05`},
			want: []Term{
				{Field: "episode", Value: Int(5), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse id",
			args: args{s: `#S2`},
			want: []Term{
				{Field: "series", Value: Int(2), Op: filter.CompOpEq},
			},
		},
		{
			name: "parse all",
			args: args{s: `@steve ~xfm #s1 "man alive" karl`},
			want: []Term{
				{Field: "actor", Value: String("steve"), Op: filter.CompOpEq},
				{Field: "publication", Value: String("xfm"), Op: filter.CompOpEq},
				{Field: "series", Value: Int(1), Op: filter.CompOpEq},
				{Field: "content", Value: String("man alive"), Op: filter.CompOpEq},
				{Field: "content", Value: String("karl"), Op: filter.CompOpFuzzyLike},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MustParse(tt.args.s); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MustParse() = %v, want %v", got, tt.want)
			}
		})
	}
}
