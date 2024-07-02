package discord

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"strconv"
	"strings"
	"time"
)

type customIdPayload struct {
	Publication   string
	Series        int32
	Episode       int32
	StartPosition int64
	EndPosition   int64
	ExtendOrTrim  time.Duration
	Shift         time.Duration
}

func (c *customIdPayload) DialogID() string {
	return fmt.Sprintf("%s-%s", c.EpisodeID(), c.PositionRange())
}

func (c *customIdPayload) EpisodeID() string {
	return fmt.Sprintf("%s-%s", c.Publication, util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)))
}

func (c *customIdPayload) PositionRange() string {
	position := fmt.Sprintf("%d", c.StartPosition)
	if c.EndPosition > c.StartPosition {
		position = fmt.Sprintf("%s_%d", position, c.EndPosition)
	}
	return position
}

func (c *customIdPayload) String() string {
	return fmt.Sprintf(
		"%s-%s-%s%s%s",
		c.Publication,
		util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)),
		c.PositionRange(),
		fmt.Sprintf(":%s", c.ExtendOrTrim.String()),
		fmt.Sprintf(":%s", c.Shift.String()),
	)
}

func (c *customIdPayload) WithShift(shift time.Duration) *customIdPayload {
	cp := *c
	cp.Shift = shift
	return &cp
}

func (c *customIdPayload) WithExtend(extendOrTrim time.Duration) *customIdPayload {
	cp := *c
	cp.ExtendOrTrim = extendOrTrim
	return &cp
}

func (c *customIdPayload) WithStartPosition(start int64) *customIdPayload {
	cp := *c
	cp.StartPosition = start
	return &cp
}
func (c *customIdPayload) WithEndPosition(end int64) *customIdPayload {
	cp := *c
	cp.EndPosition = end
	return &cp
}

// e.g. peepshow-S08E06-1[_4][:1s:1s]
func parseCustomIDPayload(payloadStr string) (*customIdPayload, error) {
	parts := strings.SplitN(payloadStr, "-", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("unrecognized payload format: %s", payloadStr)
	}
	payload := &customIdPayload{
		Publication: parts[0],
	}
	var err error
	payload.Series, payload.Episode, err = util.ExtractSeriesAndEpisode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("unrecognozied episode format: %w", err)
	}
	posParts := strings.Split(parts[2], ":")
	if len(posParts) > 0 {
		var startAndEnd []string
		if strings.Contains(posParts[0], "_") {
			startAndEnd = strings.SplitN(posParts[0], "_", 2)
		} else {
			startAndEnd = []string{posParts[0], posParts[0]}
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
	if len(posParts) > 1 {
		trimOrExtend, err := time.ParseDuration(posParts[1])
		if err != nil {
			return nil, fmt.Errorf("unrecognized trim/extend format: %s", posParts[1])
		}
		payload.ExtendOrTrim = trimOrExtend
	}
	if len(posParts) > 2 {
		shift, err := time.ParseDuration(posParts[2])
		if err != nil {
			return nil, fmt.Errorf("unrecognized shift format: %s", posParts[2])
		}
		payload.Shift = shift
	}
	return payload, nil
}
