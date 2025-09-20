package render

import (
	"bytes"
	"context"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/warmans/tvgif/pkg/discord/media"
	"github.com/warmans/tvgif/pkg/mediacache"
	model2 "github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/util"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

const overlayGridSizeX = 7
const overlayGridSizeY = 5

type Renderer interface {
	RenderFile(
		videoFileName string,
		customID *media.ID,
		dialog []model2.Dialog,
		opt ...Option,
	) (*discordgo.File, error)
}

func NewExecRenderer(cache *mediacache.Cache, mediaPath string, logger *slog.Logger, overlayCache *mediacache.OverlayCache) *ExecRenderer {
	return &ExecRenderer{mediaCache: cache, mediaPath: mediaPath, logger: logger, overlayCache: overlayCache}
}

type ExecRenderer struct {
	mediaCache   *mediacache.Cache
	mediaPath    string
	logger       *slog.Logger
	overlayCache *mediacache.OverlayCache
}

func (r *ExecRenderer) RenderFile(
	videoFileName string,
	customID *media.ID,
	dialog []model2.Dialog,
	opt ...Option,
) (*discordgo.File, error) {

	opts := resolveRenderOpts(opt...)

	var mimeType string
	var extension string
	buff := &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	switch opts.outputFileType {
	case OutputGif, OutputWebp:

		mimeType = "image/gif"
		extension = "gif"
		format := "gif"
		if opts.outputFileType == OutputWebp {
			mimeType = "image/webp"
			extension = "webp"
			format = "webp"
		}

		resolvedOverlays := opts.overlayConfig.resolveOverlays(r.overlayCache)
		_, err := r.mediaCache.Get(createFileName(customID, extension), buff, opts.disableCaching || len(resolvedOverlays) > 0, func(writer io.Writer) error {
			//video input
			args := [][]string{
				{
					"-ss", fmt.Sprintf("%0.2f", opts.startTimestamp.Seconds()),
					"-to", fmt.Sprintf("%0.2f", opts.endTimestamp.Seconds()),
					"-i", path.Join(r.mediaPath, videoFileName),
				},
			}

			filterPrefix := ""
			filtersStartAt := "0:v"

			// e.g. ffmpeg -i sample.mp4 -an -stream_loop -1 -i gif/hearts-1.gif -ignore_loop 0 -i sparkles.gif -ignore_loop 0 -filter_complex "[0][1]overlay=x=W/2-w/2:y=H/2-h/2:shortest=1[out];[out][2]overlay=x=W/2-w/2:y=H/2-h/2:shortest=1" sample_with_gif.gif
			if len(resolvedOverlays) > 0 {
				filterPrefix = ""

				for i, overlayConf := range resolvedOverlays {

					// This should align the center of the gif with the center of the chosen grid square
					// 1. get the top left of a grid square
					// 2. add half the width/height of a grid squareso the image is placed in the middle
					// 3. offset the overlay position by half its size so the middle of the overlay aligns with the middle of the grid square.
					filterPrefix += fmt.Sprintf(
						"[%s][%d]overlay=x=((((W/%d)*%d)+((W/%d)/2))-w/2):y=((((H/%d)*%d)+((H/%d)/2))-h/2):shortest=1:[o%d];",
						util.IfElse(i == 0, "0", fmt.Sprintf("o%d", i-1)),
						i+1,
						overlayGridSizeX,
						overlayConf.x,
						overlayGridSizeX,
						overlayGridSizeY,
						overlayConf.y,
						overlayGridSizeY,
						i,
					)

					args = append(args, []string{
						//"-stream_loop", "-1",
						"-ignore_loop", "0",
						"-i", path.Join(r.mediaPath, "overlay", overlayConf.name),
					})
				}

				filtersStartAt = fmt.Sprintf("o%d", len(resolvedOverlays)-1)
			}

			// output
			args = append(args, []string{
				"-f", format,
				//"-ignore_loop", "0",
				"-loop", "0",
				"-quality", "90",
				"-filter_complex",
				fmt.Sprintf(
					"%s%s",
					filterPrefix,
					joinFilters(
						filtersStartAt,
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
						onlyIf(opts.showGrid, createGridFilter(overlayGridSizeX, overlayGridSizeY)),
						createDrawtextCaptionFilter(opts.caption),
					)),
				"pipe:",
			})

			finalArgs := flattenArgs(args)

			r.logger.Info("Compiled command", slog.String("cmd", strings.Join(finalArgs, " ")))

			cmd := exec.CommandContext(ctx, "ffmpeg", finalArgs...)
			cmd.Stdout = writer
			cmd.Stderr = os.Stderr

			return cmd.Run()
		})
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("Not supported")
	}

	return &discordgo.File{
		Name:        createFileName(customID, extension),
		ContentType: mimeType,
		Reader:      buff,
	}, nil

}

func flattenArgs(args [][]string) []string {
	out := []string{}
	for _, a := range args {
		out = append(out, a...)
	}
	return out
}

type overlayConfig struct {
	numRandomOverlays int
	layoutConfig      string
}

func (o overlayConfig) resolveOverlays(overlayCache *mediacache.OverlayCache) []overlay {
	if o.layoutConfig == "" {
		out := []overlay{}
		for _, name := range overlayCache.Random(o.numRandomOverlays) {
			out = append(out, overlay{name: name, x: rand.IntN(5), y: rand.IntN(3)})
		}
		return out
	}

	out := []overlay{}
	for _, line := range strings.Split(o.layoutConfig, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "#")), " ", 2)
		if len(parts) != 2 {
			return out
		}

		xy := strings.Split(parts[0], "x")
		if len(xy) != 2 {
			return out
		}

		x, err := strconv.ParseInt(xy[0], 10, 8)
		if err != nil {
			return out
		}

		y, err := strconv.ParseInt(xy[1], 10, 8)
		if err != nil {
			return out
		}

		if overlayCache.Exists(parts[1]) {
			out = append(out, overlay{name: parts[1], x: min(int(x), overlayGridSizeX), y: min(int(y), overlayGridSizeY)})
		}
	}

	return out
}

type overlay struct {
	name string
	x, y int
}
