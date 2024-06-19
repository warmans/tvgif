package searchterms

import (
	"github.com/warmans/tvgif/pkg/filter"
	"time"
)

func TermsToFilter(terms []Term) filter.Filter {
	var fil filter.Filter
	for _, t := range terms {
		var val filter.Value
		switch t.Value.Type() {
		case StringType:
			val = filter.String(t.Value.Value().(string))
		case IntType:
			val = filter.Int(t.Value.Value().(int64))
		case DurationType:
			val = filter.Duration(t.Value.Value().(time.Duration))
		}
		if fil == nil {
			fil = &filter.CompFilter{
				Field: t.Field,
				Op:    t.Op,
				Value: val,
			}
		} else {
			fil = filter.And(fil, &filter.CompFilter{
				Field: t.Field,
				Op:    t.Op,
				Value: val,
			})
		}
	}
	return fil
}
