package discord

import (
	"encoding/json"
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"strconv"
	"strings"
	"time"
)

type stickerOpts struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`
}

type customIdOpts struct {
	ExtendOrTrim time.Duration `json:"x"`
	Shift        time.Duration `json:"s"`
	Sticker      *stickerOpts  `json:"t"`
}

func (c *customIdOpts) UnmarshalJSON(bytes []byte) error {
	raw := &struct {
		ExtendOrTrim string       `json:"x"`
		Shift        string       `json:"s"`
		Sticker      *stickerOpts `json:"t"`
	}{}

	if err := json.Unmarshal(bytes, raw); err != nil {
		return err
	}

	c.Sticker = raw.Sticker

	var err error
	c.ExtendOrTrim, err = time.ParseDuration(raw.ExtendOrTrim)
	if err != nil {
		return err
	}
	c.Shift, err = time.ParseDuration(raw.Shift)
	if err != nil {
		return err
	}
	return nil
}

func (c *customIdOpts) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ExtendOrTrim string       `json:"x"`
		Shift        string       `json:"s"`
		Sticker      *stickerOpts `json:"t"`
	}{
		ExtendOrTrim: c.ExtendOrTrim.String(),
		Shift:        c.Shift.String(),
		Sticker:      c.Sticker,
	})
}

type customIdPayload struct {
	Publication   string
	Series        int32
	Episode       int32
	StartPosition int64
	EndPosition   int64
	Opts          customIdOpts
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
	optsString := ""
	// care: the custom JSON marshallers use a pointer type
	if optsBytes, err := json.Marshal(&c.Opts); err == nil {
		optsString = fmt.Sprintf("-%s", string(optsBytes))
	} else {
		fmt.Println("failed to marshal opts: ", err.Error())
	}

	return fmt.Sprintf(
		"%s-%s-%s%s",
		c.Publication,
		util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)),
		c.PositionRange(),
		optsString,
	)
}

func (c *customIdPayload) WithShift(shift time.Duration) *customIdPayload {
	cp := *c
	cp.Opts.Shift = shift
	return &cp
}

func (c *customIdPayload) WithExtend(extendOrTrim time.Duration) *customIdPayload {
	cp := *c
	cp.Opts.ExtendOrTrim = extendOrTrim
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

func (c *customIdPayload) WithToggleStickerMode() *customIdPayload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: 0, Y: 0}
	} else {
		cp.Opts.Sticker = nil
	}
	return &cp
}

func (c *customIdPayload) WithStickerXIncrement(increment int32) *customIdPayload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: positiveOrZero(increment), Y: 0}
	} else {
		cp.Opts.Sticker = &stickerOpts{X: positiveOrZero(cp.Opts.Sticker.X + increment), Y: cp.Opts.Sticker.Y}
	}
	return &cp
}
func (c *customIdPayload) WithStickerYIncrement(increment int32) *customIdPayload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: 0, Y: positiveOrZero(increment)}
	} else {
		cp.Opts.Sticker = &stickerOpts{X: cp.Opts.Sticker.X, Y: positiveOrZero(cp.Opts.Sticker.Y + increment)}
	}
	return &cp
}

// e.g. peepshow-S08E06-1[_4]-{s:1,e:4...}
func parseCustomIDPayload(payloadStr string) (*customIdPayload, error) {

	parts := strings.SplitN(payloadStr, "-", 4)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unrecognized payload format: %s", payloadStr)
	}
	payload := &customIdPayload{
		Publication: parts[0],
		Opts:        customIdOpts{},
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

	if len(parts) > 3 && parts[3] != "" {
		if err := json.Unmarshal([]byte(parts[3]), &payload.Opts); err != nil {
			return nil, err
		}
	}

	return payload, nil
}
