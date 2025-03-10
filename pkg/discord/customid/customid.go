package customid

import (
	"encoding/json"
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"strconv"
	"strings"
	"time"
)

type OutputFileType string

const (
	OutputGif  = OutputFileType("gif")
	OutputWebm = OutputFileType("webm")
	OutputWebp = OutputFileType("webp")
)

type Mode string

const (
	NormalMode  Mode = ""
	StickerMode Mode = "sticker"
	CaptionMode Mode = "caption"
	VideoMode   Mode = "video"
	WebpMode    Mode = "webp"
)

type stickerOpts struct {
	X           int32 `json:"x,omitempty"`
	Y           int32 `json:"y,omitempty"`
	WidthOffset int32 `json:"w,omitempty"`
}

type Opts struct {
	ExtendOrTrim time.Duration `json:"x,omitempty"`
	Shift        time.Duration `json:"s,omitempty"`
	Sticker      *stickerOpts  `json:"t,omitempty"`
	Mode         Mode          `json:"m,omitempty"`
}

type RawOpts struct {
	ExtendOrTrim string       `json:"x,omitempty"`
	Shift        string       `json:"s,omitempty"`
	Sticker      *stickerOpts `json:"t,omitempty"`
	Mode         Mode         `json:"m,omitempty"`
}

func (c *Opts) UnmarshalJSON(bytes []byte) error {
	raw := &RawOpts{}
	if err := json.Unmarshal(bytes, raw); err != nil {
		return err
	}

	c.Sticker = raw.Sticker
	c.Mode = raw.Mode

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

func (c *Opts) MarshalJSON() ([]byte, error) {
	return json.Marshal(RawOpts{
		ExtendOrTrim: c.ExtendOrTrim.String(),
		Shift:        c.Shift.String(),
		Sticker:      c.Sticker,
		Mode:         c.Mode,
	})
}

type Payload struct {
	Publication   string `json:"p,omitempty"`
	Series        int32  `json:"s,omitempty"`
	Episode       int32  `json:"e,omitempty"`
	StartPosition int64  `json:"sp,omitempty"`
	EndPosition   int64  `json:"ep,omitempty"`
	Opts          Opts   `json:"o,omitempty"`
}

func (c *Payload) DialogID() string {
	return c.DialogIDWithRange(c.PositionRange())
}

func (c *Payload) DialogIDWithRange(customRange string) string {
	return fmt.Sprintf("%s-%s", c.EpisodeID(), customRange)
}

func (c *Payload) EpisodeID() string {
	return fmt.Sprintf("%s-%s", c.Publication, util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)))
}

func (c *Payload) PositionRange() string {
	position := fmt.Sprintf("%d", c.StartPosition)
	if c.EndPosition > c.StartPosition {
		position = fmt.Sprintf("%s_%d", position, c.EndPosition)
	}
	return position
}

func (c *Payload) String() string {
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

func (c *Payload) WithShift(shift time.Duration) *Payload {
	cp := *c
	cp.Opts.Shift = shift
	return &cp
}

func (c *Payload) WithExtend(extendOrTrim time.Duration) *Payload {
	cp := *c
	cp.Opts.ExtendOrTrim = extendOrTrim
	return &cp
}

func (c *Payload) WithStartPosition(start int64) *Payload {
	cp := *c
	cp.StartPosition = start
	return &cp
}
func (c *Payload) WithEndPosition(end int64) *Payload {
	cp := *c
	cp.EndPosition = end
	return &cp
}

func (c *Payload) WithMode(mode Mode) *Payload {
	cp := *c
	cp.Opts.Mode = mode
	if mode == StickerMode {
		cp.Opts.Sticker = &stickerOpts{X: 0, Y: 0}
	} else {
		cp.Opts.Sticker = nil
	}
	return &cp
}

func (c *Payload) WithStickerXIncrement(increment int32) *Payload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: positiveOrZero(increment), Y: 0}
	} else {
		cp.Opts.Sticker = &stickerOpts{X: positiveOrZero(cp.Opts.Sticker.X + increment), Y: cp.Opts.Sticker.Y, WidthOffset: cp.Opts.Sticker.WidthOffset}
	}
	return &cp
}

func (c *Payload) WithStickerYIncrement(increment int32) *Payload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: 0, Y: positiveOrZero(increment)}
	} else {
		cp.Opts.Sticker = &stickerOpts{X: cp.Opts.Sticker.X, Y: positiveOrZero(cp.Opts.Sticker.Y + increment), WidthOffset: cp.Opts.Sticker.WidthOffset}
	}
	return &cp
}

func (c *Payload) WithStickerWidthIncrement(increment int32) *Payload {
	cp := *c
	if cp.Opts.Sticker == nil {
		cp.Opts.Sticker = &stickerOpts{X: 0, Y: 0, WidthOffset: increment}
	} else {
		cp.Opts.Sticker = &stickerOpts{X: cp.Opts.Sticker.X, Y: cp.Opts.Sticker.Y, WidthOffset: cp.Opts.Sticker.WidthOffset + increment}
	}
	return &cp
}

// e.g. peepshow-S08E06-1[_4]-{s:1,e:4...}
func ParsePayload(payloadStr string) (*Payload, error) {

	parts := strings.SplitN(payloadStr, "-", 4)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unrecognized payload format: %s", payloadStr)
	}
	payload := &Payload{
		Publication: parts[0],
		Opts:        Opts{},
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

func positiveOrZero(val int32) int32 {
	if val < 0 {
		return 0
	}
	return val
}
