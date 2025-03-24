package media

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"strconv"
	"strings"
)

// ID is the unique identifier for a specific publication segment.
type ID struct {
	Publication   string `json:"p,omitempty"`
	Series        int32  `json:"s,omitempty"`
	Episode       int32  `json:"e,omitempty"`
	StartPosition int64  `json:"sp,omitempty"`
	EndPosition   int64  `json:"ep,omitempty"`
}

func (i *ID) String() string {
	return fmt.Sprintf(
		"%s-%s-%s",
		i.Publication,
		util.FormatSeriesAndEpisode(int(i.Series), int(i.Episode)),
		i.FormatPositionRange(),
	)
}

func (i *ID) DialogID() string {
	return i.DialogIDWithRange(i.FormatPositionRange())
}

func (i *ID) DialogIDWithRange(customRange string) string {
	return fmt.Sprintf("%s-%s", i.EpisodeID(), customRange)
}

func (i *ID) EpisodeID() string {
	return fmt.Sprintf("%s-%s", i.Publication, util.FormatSeriesAndEpisode(int(i.Series), int(i.Episode)))
}

func (i *ID) PositionRange() int64 {
	return i.EndPosition - i.StartPosition
}

func (i *ID) FormatPositionRange() string {
	position := fmt.Sprintf("%d", i.StartPosition)
	if i.EndPosition > i.StartPosition {
		position = fmt.Sprintf("%s_%d", position, i.EndPosition)
	}
	return position
}

func (i *ID) SameSubRange(newPayload *ID) bool {
	if newPayload.EpisodeID() != i.EpisodeID() {
		return false
	}
	if newPayload.StartPosition != i.StartPosition {
		return false
	}
	return newPayload.EndPosition >= i.EndPosition
}

func (i *ID) WithStartPosition(start int64) *ID {
	cp := *i
	cp.StartPosition = start
	return &cp
}
func (i *ID) WithEndPosition(end int64) *ID {
	cp := *i
	cp.EndPosition = end
	return &cp
}

// ParseID e.g. peepshow-S08E06-1[_4]
func ParseID(payloadStr string) (*ID, error) {

	parts := strings.SplitN(payloadStr, "-", 4)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unrecognized payload format: %s", payloadStr)
	}
	payload := &ID{
		Publication: parts[0],
	}
	var err error
	payload.Series, payload.Episode, err = util.ExtractSeriesAndEpisode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("unrecognozied episode format: %w", err)
	}

	if parts[2] != "" {
		var startAndEnd []string
		if strings.Contains(parts[2], "_") {
			startAndEnd = strings.SplitN(parts[2], "_", 2)
		} else {
			startAndEnd = []string{parts[2], parts[2]}
		}
		startPosition, err := strconv.Atoi(startAndEnd[0])
		if err != nil {
			return nil, fmt.Errorf("unrecognized start position: %s (%s)", startAndEnd[0], payloadStr)
		}
		payload.StartPosition = int64(startPosition)

		endPosition, err := strconv.Atoi(startAndEnd[1])
		if err != nil {
			return nil, fmt.Errorf("unrecognized end position: %s (%s)", startAndEnd[1], payloadStr)
		}
		payload.EndPosition = max(int64(endPosition), payload.StartPosition)
	}

	return payload, nil
}
