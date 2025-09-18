package render

import (
	"bytes"
	"fmt"
	"github.com/bwmarrin/discordgo"
	ffmpeg_go "github.com/u2takey/ffmpeg-go"
	"github.com/warmans/tvgif/pkg/discord/media"
	"github.com/warmans/tvgif/pkg/mediacache"
	model2 "github.com/warmans/tvgif/pkg/model"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

type OutputFileType string

const (
	OutputWebp OutputFileType = "webp"
	OutputWebm OutputFileType = "webm"
	OutputGif  OutputFileType = "gif"
)

type SpecialMode string

const (
	NormalMode  SpecialMode = ""
	StickerMode SpecialMode = "string"
	CaptionMode SpecialMode = "caption"
)

type StickerModeOpts struct {
	X           int32 `json:"x,omitempty"`
	Y           int32 `json:"y,omitempty"`
	WidthOffset int32 `json:"w,omitempty"`
}

func resolveRenderOpts(opt ...Option) *renderOpts {
	opts := &renderOpts{
		outputFileType: OutputWebp,
	}

	for _, v := range opt {
		v(opts)
	}

	return opts
}

type renderOpts struct {
	startTimestamp  time.Duration
	endTimestamp    time.Duration
	outputFileType  OutputFileType
	disableCaching  bool
	customText      []string
	caption         string
	disableSubs     bool
	specialMode     SpecialMode
	stickerModeOpts *StickerModeOpts
	overlayGifs     int
}

func WithOutputFileType(tp OutputFileType) Option {
	return func(opts *renderOpts) {
		opts.outputFileType = tp
	}
}

func WithStartTimestamp(ts time.Duration) Option {
	return func(opts *renderOpts) {
		opts.startTimestamp = ts
	}
}

func WithEndTimestamp(ts time.Duration) Option {
	return func(opts *renderOpts) {
		opts.endTimestamp = ts
	}
}

func WithCaching(disabled bool) Option {
	return func(opts *renderOpts) {
		opts.disableCaching = disabled
	}
}

func WithCustomText(text []string) Option {
	return func(opts *renderOpts) {
		opts.customText = text
	}
}

func WithCaption(text string) Option {
	return func(opts *renderOpts) {
		if text == "" {
			return
		}
		opts.caption = text
	}
}

func WithDisableSubs(disable bool) Option {
	return func(opts *renderOpts) {
		opts.disableSubs = disable
	}
}

func WithStickerMode(enable bool, stickerOpts *StickerModeOpts) Option {
	return func(opts *renderOpts) {
		if enable {
			opts.specialMode = StickerMode
			opts.stickerModeOpts = stickerOpts
		} else {
			opts.specialMode = NormalMode
			opts.stickerModeOpts = nil
		}
	}
}

func WithCaptionMode(enable bool) Option {
	return func(opts *renderOpts) {
		if enable {
			opts.specialMode = CaptionMode
		} else {
			opts.specialMode = NormalMode
		}
	}
}

func WithGifOverlays(num int) Option {
	return func(opts *renderOpts) {
		opts.overlayGifs = num
	}
}

type Option func(opts *renderOpts)

type drawTextOpts struct {
	font       string
	boxOpacity float32
	fontSize   int
}

type drawTextOpt func(opts *drawTextOpts)

func withSimpsonsFont(enable bool) drawTextOpt {
	return func(opts *drawTextOpts) {
		if enable {
			opts.font = "assets/akbar.ttf"
			opts.boxOpacity = 0
			opts.fontSize = 22
		}
	}
}

func NewRenderer(cache *mediacache.Cache, mediaPath string) *FfmpegRenderer {
	return &FfmpegRenderer{mediaCache: cache, mediaPath: mediaPath}
}

type FfmpegRenderer struct {
	mediaCache *mediacache.Cache
	mediaPath  string
}

func (r *FfmpegRenderer) RenderFile(
	videoFileName string,
	customID *media.ID,
	dialog []model2.Dialog,
	opt ...Option,
) (*discordgo.File, error) {

	opts := resolveRenderOpts(opt...)

	var mimeType string
	var extension string
	buff := &bytes.Buffer{}

	var err error
	switch opts.outputFileType {
	case OutputWebm:
		mimeType = "video/webm"
		extension = "webm"
		_, err = r.mediaCache.Get(createFileName(customID, extension), buff, opts.disableCaching, func(writer io.Writer) error {
			err := ffmpeg_go.
				Input(path.Join(r.mediaPath, videoFileName),
					ffmpeg_go.KwArgs{
						"ss": fmt.Sprintf("%0.2f", opts.startTimestamp.Seconds()),
					}).
				Output("pipe:",
					ffmpeg_go.KwArgs{
						"t":            fmt.Sprintf("%0.2f", opts.endTimestamp.Seconds()-opts.startTimestamp.Seconds()),
						"map_metadata": "-1",
						"format":       "webm",
						"filter_complex": joinFilters(
							"0:v",
							onlyIf(
								!opts.disableSubs,
								createDrawtextFilter(
									dialog,
									opts,
									withSimpsonsFont(customID.Publication == "simpsons"),
								),
							),
						),
					},
				).WithOutput(writer, os.Stderr).Run()
			if err != nil {
				return fmt.Errorf("ffmpeg command failed: %w", err)
			}
			return nil
		})
	case OutputGif, OutputWebp:
		mimeType = "image/gif"
		extension = "gif"
		format := "gif"
		if opts.outputFileType == OutputWebp {
			mimeType = "image/webp"
			extension = "webp"
			format = "webp"
		}
		_, err = r.mediaCache.Get(createFileName(customID, extension), buff, opts.disableCaching, func(writer io.Writer) error {
			err := ffmpeg_go.
				Input(path.Join(r.mediaPath, videoFileName),
					ffmpeg_go.KwArgs{
						"ss": fmt.Sprintf("%0.2f", opts.startTimestamp.Seconds()),
						"to": fmt.Sprintf("%0.2f", opts.endTimestamp.Seconds()),
					}).
				Output("pipe:",
					ffmpeg_go.KwArgs{
						"format": format,
						"filter_complex": joinFilters(
							"0:v",
							onlyIf(
								!opts.disableSubs,
								createDrawtextFilter(
									dialog,
									opts,
									withSimpsonsFont(customID.Publication == "simpsons"),
								),
							),
							createStickerCropFilter(opts),
							createStickerResizeFilter(opts),
							createCaptionScaleFilter(opts),
							createDrawtextCaptionFilter(opts.caption),
						),
						// for some reason this is necessary for discord to display webp images.
						// it doesn't actually stop it from looping or affect gifs...
						"loop":    "0",
						"quality": "90",
					},
				).WithOutput(writer, os.Stderr).Run()
			if err != nil {
				return fmt.Errorf("ffmpeg failed: %w", err)
			}

			return nil
		})
	}
	if err != nil {
		return nil, fmt.Errorf("cache fetch failed: %w", err)
	}
	return &discordgo.File{
		Name:        createFileName(customID, extension),
		ContentType: mimeType,
		Reader:      buff,
	}, nil
}

func createDrawtextFilter(dialog []model2.Dialog, renderOpts *renderOpts, opts ...drawTextOpt) string {
	options := &drawTextOpts{boxOpacity: 0.5, fontSize: 18}
	for _, v := range opts {
		v(options)
	}
	if renderOpts.specialMode == StickerMode {
		return ""
	}
	drawTextCommands := []string{}
	timestampOffsets := dialog[0].StartTimestamp
	for k, line := range dialog {
		dialogText := line.Content
		if len(renderOpts.customText) > k {
			dialogText = renderOpts.customText[k]
		}
		startSecond := line.StartTimestamp - timestampOffsets
		endSecond := line.EndTimestamp - timestampOffsets
		font := ""
		if options.font != "" {
			font = fmt.Sprintf("fontfile='%s':", options.font)
		}

		lineHeight := 28
		marginBottom := 10
		lines := strings.Split(formatGifText(56, strings.Split(dialogText, "\n")), "\n")
		verticalOffset := ((len(lines) - 1) * lineHeight) + marginBottom
		for _, line := range lines {
			drawTextCommands = append(drawTextCommands, fmt.Sprintf(
				`drawtext=%stext='%s':line_spacing=10:expansion=none:fontcolor=white:fontsize=%d:box=1:boxcolor=black@%0.1f:boxborderw=5:x=(w-text_w)/2:y=(h-(text_h+%d)):enable='between(t,%0.2f,%0.2f):shadowx=2:shadowy=2'`,
				font,
				line,
				options.fontSize,
				options.boxOpacity,
				verticalOffset,
				startSecond.Seconds(),
				endSecond.Seconds(),
			))
			verticalOffset -= lineHeight
		}
	}
	return strings.Join(drawTextCommands, ", ")
}

func createDrawtextCaptionFilter(caption string) string {
	if caption == "" {
		return ""
	}
	lineHeight := 28
	lines := strings.Split(formatGifText(56, strings.Split(caption, "\n")), "\n")
	verticalOffset := 0

	drawTextCommands := []string{}
	for _, line := range lines {
		drawTextCommands = append(drawTextCommands, fmt.Sprintf(
			`drawtext=text='%s':expansion=none:fontcolor=white:fontsize=18:x=(w-text_w)/2:y=20+%d`,
			line,
			verticalOffset,
		))
		verticalOffset += lineHeight
	}
	return strings.Join(drawTextCommands, ", ")
}

func createStickerCropFilter(opts *renderOpts) string {
	if opts.specialMode != StickerMode || opts.stickerModeOpts == nil {
		return ""
	}
	if opts.stickerModeOpts.X > 0 || opts.stickerModeOpts.Y > 0 || opts.stickerModeOpts.WidthOffset != 0 {
		diameter := int32(336)
		if opts.stickerModeOpts.WidthOffset != 0 {
			diameter = 336 + opts.stickerModeOpts.WidthOffset
		}
		return fmt.Sprintf("crop=w=%d:h=%d:x=%d:y=%d", diameter, diameter, opts.stickerModeOpts.X, opts.stickerModeOpts.Y)
	}
	return "crop=w=336:h=336"
}

func createStickerResizeFilter(opts *renderOpts) string {
	if opts.specialMode != StickerMode {
		return ""
	}
	return "scale=160:160"
}

func createCaptionScaleFilter(opts *renderOpts) string {
	if opts.specialMode != CaptionMode {
		return ""
	}
	return "scale=421:238:force_original_aspect_ratio=decrease,pad=596:336:(ow-iw)/2:(oh-ih)/2+30,setsar=1"
}

func joinFilters(startAt string, filters ...string) string {
	joined := ""
	filters = dropEmptyFilters(filters)
	for k, v := range filters {
		connector := ""
		if k < len(filters)-1 {
			connector = fmt.Sprintf("[f%d];[f%d]", k, k)
		}
		joined += fmt.Sprintf("%s%s", v, connector)
	}
	return fmt.Sprintf("[%s]%s", startAt, joined)
}

func dropEmptyFilters(filters []string) []string {
	clean := []string{}
	for _, v := range filters {
		if v != "" {
			clean = append(clean, v)
		}
	}
	return clean
}

func createFileName(customID *media.ID, suffix string) string {
	return fmt.Sprintf("%s.%s", customID.DialogID(), suffix)
}

// formatGifText
// max length should be 56ish
func formatGifText(maxLineLength int, lines []string) string {
	text := []string{}
	for _, line := range lines {
		currentLine := []string{}
		for _, word := range strings.Split(line, " ") {
			if lineLength(currentLine)+(len(word)+1) > maxLineLength {
				text = append(text, strings.Join(currentLine, " "))
				currentLine = []string{word}
				continue
			}
			currentLine = append(currentLine, word)
		}
		if len(currentLine) > 0 {
			text = append(text, strings.Join(currentLine, " "))
		}
	}

	finalText := strings.Join(text, "\n")
	finalText = strings.Replace(finalText, "'", "’", -1)
	finalText = strings.Replace(finalText, ":", `\:`, -1)
	return strings.TrimSpace(finalText)
}

func lineLength(line []string) int {
	if len(line) == 0 {
		return 0
	}
	total := 0
	for _, v := range line {
		total += len(v)
	}
	// total + number of spaces that would be in the line
	return total + (len(line) - 1)
}

func onlyIf(cond bool, value string) string {
	if cond {
		return value
	}
	return ""
}
