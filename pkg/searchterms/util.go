package searchterms

import (
	"github.com/warmans/tvgif/pkg/util"
	"slices"
)

func ExtractOffset(terms []Term) ([]Term, *int64) {
	if len(terms) != 1 {
		return terms, nil
	}
	offsetIdx := slices.IndexFunc(terms, func(val Term) bool {
		return val.Field[0] == "offset"
	})
	if offsetIdx == -1 {
		return terms, nil
	}
	var offset *int64
	if offsetIdx >= 0 {
		if offsetVal := terms[offsetIdx].Value.Value().(int64); offsetVal >= 0 {
			offset = util.ToPtr(offsetVal)
		}
		terms = append(terms[:offsetIdx], terms[offsetIdx+1:]...)
	}
	return terms, offset
}
