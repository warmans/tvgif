package discord

import (
	"encoding/json"
	"fmt"
	"github.com/warmans/tvgif/pkg/discord/media"
	"github.com/warmans/tvgif/pkg/util"
	"strings"
	"time"
)

type StateUpdateType string

const StateUpdateSetSearchResPosFromCID = StateUpdateType("set_search_res_pos_from_cid") // todo: is this needed?
const StateUpdateSubsEnabled = StateUpdateType("enable_subs")
const StateUpdateSetCaption = StateUpdateType("set_caption")
const StateUpdateSetSubs = StateUpdateType("set_subs")
const StateUpdateUpdateMediaID = StateUpdateType("update_custom_id")
const StateUpdateResetMediaID = StateUpdateType("reset_custom_id")
const StateUpdateSetExtendOrTrim = StateUpdateType("set_extend_trim")
const StateUpdateSetShift = StateUpdateType("set_shift")
const StateUpdateMode = StateUpdateType("set_mode")
const StateUpdateOutputFormat = StateUpdateType("set_output_format")

type Mode string

const (
	NormalMode  Mode = ""
	StickerMode Mode = "sticker"
	CaptionMode Mode = "caption"
	VideoMode   Mode = "video"
)

type OutputFileType string

const (
	OutputDefault = OutputFileType("")
	OutputGif     = OutputFileType("gif")
	OutputWebm    = OutputFileType("webm")
	OutputWebp    = OutputFileType("webp")
)

type Settings struct {
	ExtendOrTrim time.Duration  `json:"x,omitempty"`
	Shift        time.Duration  `json:"s,omitempty"`
	Mode         Mode           `json:"m,omitempty"`
	Sticker      *stickerOpts   `json:"t,omitempty"`
	Caption      string         `json:"c,omitempty"`
	OverrideSubs []string       `json:"u,omitempty"`
	SubsEnabled  bool           `json:"d,omitempty"`
	OutputFormat OutputFileType `json:"o,omitempty"`
}

// rawSettings is just Settings with simple types used for encoding/decoding
type rawSettings struct {
	ExtendOrTrim string         `json:"x,omitempty"`
	Shift        string         `json:"s,omitempty"`
	Mode         Mode           `json:"m,omitempty"`
	Sticker      *stickerOpts   `json:"t,omitempty"`
	Caption      string         `json:"c,omitempty"`
	OverrideSubs []string       `json:"u,omitempty"`
	SubsEnabled  bool           `json:"d,omitempty"`
	OutputFormat OutputFileType `json:"o,omitempty"`
}

func (c *Settings) UnmarshalJSON(bytes []byte) error {
	raw := &rawSettings{}
	if err := json.Unmarshal(bytes, raw); err != nil {
		return err
	}

	var err error
	c.ExtendOrTrim, err = time.ParseDuration(raw.ExtendOrTrim)
	if err != nil {
		return err
	}
	c.Shift, err = time.ParseDuration(raw.Shift)
	if err != nil {
		return err
	}

	// todo: this is annoying, these all have to be copied manually
	// also see MarshalJSON for similar copying
	c.Mode = raw.Mode
	c.Sticker = raw.Sticker
	c.Caption = raw.Caption
	c.OverrideSubs = raw.OverrideSubs
	c.SubsEnabled = raw.SubsEnabled
	c.OutputFormat = raw.OutputFormat

	return nil
}

func (c *Settings) MarshalJSON() ([]byte, error) {
	return json.Marshal(rawSettings{
		ExtendOrTrim: c.ExtendOrTrim.String(),
		Shift:        c.Shift.String(),
		Mode:         c.Mode,
		Sticker:      c.Sticker,
		Caption:      c.Caption,
		OverrideSubs: c.OverrideSubs,
		SubsEnabled:  c.SubsEnabled,
		OutputFormat: c.OutputFormat,
	})
}

type PreviewState struct {
	ID               *media.ID `json:"i,omitempty"`
	Settings         Settings  `json:"x,omitempty"`
	OriginalTerms    string    `json:"t,omitempty" `
	OriginalPosition *string   `json:"p,omitempty"`
}

func (c *PreviewState) String() string {
	optsString := ""
	// care: the custom JSON marshallers use a pointer type
	if optsBytes, err := json.Marshal(&c.Settings); err == nil {
		optsString = fmt.Sprintf("-%s", string(optsBytes))
	} else {
		fmt.Println("failed to marshal opts: ", err.Error())
	}

	return fmt.Sprintf("%s%s", c.ID.String(), optsString)
}

func (c *PreviewState) WithShift(shift time.Duration) *PreviewState {
	cp := *c
	cp.Settings.Shift = shift
	return &cp
}

func (c *PreviewState) WithExtend(extendOrTrim time.Duration) *PreviewState {
	cp := *c
	cp.Settings.ExtendOrTrim = extendOrTrim
	return &cp
}

func (c *PreviewState) WithMode(mode Mode) *PreviewState {
	cp := *c
	cp.Settings.Mode = mode
	if mode == StickerMode {
		cp.Settings.Sticker = &stickerOpts{X: 0, Y: 0}
	} else {
		cp.Settings.Sticker = nil
	}
	return &cp
}

func (c *PreviewState) WithStickerXIncrement(increment int32) *PreviewState {
	cp := *c
	if cp.Settings.Sticker == nil {
		cp.Settings.Sticker = &stickerOpts{X: positiveOrZero(increment), Y: 0}
	} else {
		cp.Settings.Sticker = &stickerOpts{
			X:           positiveOrZero(cp.Settings.Sticker.X + increment),
			Y:           cp.Settings.Sticker.Y,
			WidthOffset: cp.Settings.Sticker.WidthOffset,
		}
	}
	return &cp
}

func (c *PreviewState) WithStickerYIncrement(increment int32) *PreviewState {
	cp := *c
	if cp.Settings.Sticker == nil {
		cp.Settings.Sticker = &stickerOpts{X: 0, Y: positiveOrZero(increment)}
	} else {
		cp.Settings.Sticker = &stickerOpts{
			X:           cp.Settings.Sticker.X,
			Y:           positiveOrZero(cp.Settings.Sticker.Y + increment),
			WidthOffset: cp.Settings.Sticker.WidthOffset,
		}
	}
	return &cp
}

func (c *PreviewState) WithStickerWidthIncrement(increment int32) *PreviewState {
	cp := *c
	if cp.Settings.Sticker == nil {
		cp.Settings.Sticker = &stickerOpts{X: 0, Y: 0, WidthOffset: increment}
	} else {
		cp.Settings.Sticker = &stickerOpts{
			X:           cp.Settings.Sticker.X,
			Y:           cp.Settings.Sticker.Y,
			WidthOffset: cp.Settings.Sticker.WidthOffset + increment,
		}
	}
	return &cp
}

func (c *PreviewState) ApplyUpdate(upd StateUpdate) error {
	var ok bool
	switch upd.Type {
	case StateUpdateSubsEnabled:
		if c.Settings.SubsEnabled, ok = upd.Value.(bool); !ok {
			return fmt.Errorf("%s was not expected type (wanted bool got %T)", upd.Type, upd.Value)
		}
	case StateUpdateSetCaption:
		if c.Settings.Caption, ok = upd.Value.(string); !ok {
			return fmt.Errorf("%s was not expected type (wanted string got %T)", upd.Type, upd.Value)
		}
	case StateUpdateSetSubs:
		if subs, ok := upd.Value.([]string); !ok {
			return fmt.Errorf("%s was not expected type (wanted string got %T)", upd.Type, upd.Value)
		} else {
			c.Settings.OverrideSubs = util.TrimStrings(subs)
		}
	case StateUpdateResetMediaID:
		rawId, ok := upd.Value.(string)
		if !ok {
			return fmt.Errorf("%s was not expected type (wanted string got %T)", upd.Type, upd.Value)
		}
		customID, err := media.ParseID(rawId)
		if err != nil {
			return fmt.Errorf("failed to parse customID (%s): %w", customID, err)
		}
		// reset state completely when customID is reset
		newState := PreviewState{
			ID: customID,
			// must keep this value to allow navigating between results
			OriginalTerms:    c.OriginalTerms,
			OriginalPosition: util.ToPtr(customID.PositionRange()),
		}
		*c = newState
	case StateUpdateUpdateMediaID:
		rawId, ok := upd.Value.(string)
		if !ok {
			return fmt.Errorf("%s was not expected type (wanted string got %T)", upd.Type, upd.Value)
		}
		customID, err := media.ParseID(rawId)
		if err != nil {
			return fmt.Errorf("failed to parse customID (%s): %w", customID, err)
		}
		if !c.ID.SameSubRange(customID) {
			// if the customID has changed too much, we have to reset the custom subs
			c.Settings.OverrideSubs = nil
		}

		// just update the ID without resetting
		c.ID = customID
	case StateUpdateSetExtendOrTrim:
		//json decode will make this a float even if it's a whole number
		floatVal, ok := upd.Value.(float64)
		if !ok {
			return fmt.Errorf("%s was not expected type (wanted float64 got %T)", upd.Type, upd.Value)
		}
		c.Settings.ExtendOrTrim = time.Duration(floatVal)
	case StateUpdateSetShift:
		//json decode will make this a float even if it's a whole number
		floatVal, ok := upd.Value.(float64)
		if !ok {
			return fmt.Errorf("%s was not expected type (wanted float64 got %T)", upd.Type, upd.Value)
		}
		c.Settings.Shift = time.Duration(floatVal)
	case StateUpdateMode:
		if strVal, ok := upd.Value.(string); !ok {
			return fmt.Errorf("%s was not expected type (wanted Mode got %T)", upd.Type, upd.Value)
		} else {
			c.Settings.Mode = Mode(strVal)
		}
	case StateUpdateOutputFormat:
		if strVal, ok := upd.Value.(string); !ok {
			return fmt.Errorf("%s was not expected type (wanted Mode got %T)", upd.Type, upd.Value)
		} else {
			c.Settings.OutputFormat = OutputFileType(strVal)
		}
	}

	return nil
}

type stickerOpts struct {
	X           int32 `json:"x,omitempty"`
	Y           int32 `json:"y,omitempty"`
	WidthOffset int32 `json:"w,omitempty"`
}

func positiveOrZero(val int32) int32 {
	if val < 0 {
		return 0
	}
	return val
}

type StateUpdate struct {
	Type  StateUpdateType `json:"t"`
	Value any             `json:"v"`
}

func (s StateUpdate) CustomID() string {
	enc, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("failed to encode state update: %s", err.Error()))
	}
	return fmt.Sprintf("%s:%s", ActionUpdateState, string(enc))
}

func StateSetSubsEnabled(value bool) StateUpdate {
	return newStateUpdate(StateUpdateSubsEnabled, value)
}

func StateSetCaption(value string) StateUpdate {
	return newStateUpdate(StateUpdateSetCaption, value)
}

func StateSetSubs(subs []string) StateUpdate {
	return newStateUpdate(StateUpdateSetSubs, subs)
}

func StateSetMediaID(id *media.ID) StateUpdate {
	return newStateUpdate(StateUpdateUpdateMediaID, id.String())
}

func StateResetMediaID(newID string) StateUpdate {
	return newStateUpdate(StateUpdateResetMediaID, newID)
}

func StateSetExtendOrTrim(duration time.Duration) StateUpdate {
	// encode this as a float to match result of json decode
	return newStateUpdate(StateUpdateSetExtendOrTrim, float64(duration))
}

func StateSetShift(duration time.Duration) StateUpdate {
	return newStateUpdate(StateUpdateSetShift, float64(duration))
}

func StateSetMode(mode Mode) StateUpdate {
	return newStateUpdate(StateUpdateMode, mode)
}

func StateSetOutputFormat(format OutputFileType) StateUpdate {
	return newStateUpdate(StateUpdateOutputFormat, format)
}

func newStateUpdate(kind StateUpdateType, value any) StateUpdate {
	return StateUpdate{
		Type:  kind,
		Value: value,
	}
}

func decodeUpdateStateAction(encoded string) (StateUpdate, error) {
	upd := &StateUpdate{}
	err := json.Unmarshal([]byte(strings.TrimPrefix(encoded, fmt.Sprintf("%s:", ActionUpdateState))), upd)
	if err != nil {
		return StateUpdate{}, err
	}
	return *upd, err
}
