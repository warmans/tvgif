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

		resolvedOverlays := opts.overlayConfig.resolveOverlays(r.overlayCache, r.logger)
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
				// resize all inputs
				for i, overlayConf := range resolvedOverlays {
					filterPrefix += fmt.Sprintf(
						"[%d]scale=w=iw*%0.2f:h=ih*%0.2f%s[i%d];",
						i+1,
						overlayConf.scale,
						overlayConf.scale,
						util.IfElse(overlayConf.hflip, ",hflip", ""),
						i+1,
					)
				}

				for i, overlayConf := range resolvedOverlays {

					// This should align the center of the gif with the center of the chosen grid square
					// 1. get the top left of a grid square
					// 2. add half the width/height of a grid squareso the image is placed in the middle
					// 3. offset the overlay position by half its size so the middle of the overlay aligns with the middle of the grid square.
					filterPrefix += fmt.Sprintf(
						"[%s][i%d]overlay=x=((((W/%d)*%0.2f)+((W/%d)/2))-w/2):y=((((H/%d)*%0.2f)+((H/%d)/2))-h/2):shortest=1:[o%d];",
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
	layoutConfig string
}

func (o overlayConfig) resolveOverlays(overlayCache *mediacache.OverlayCache, logger *slog.Logger) []overlay {
	out := []overlay{}
	for _, line := range strings.Split(o.layoutConfig, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "#")), " ", 4)
		if len(parts) < 2 {
			logger.Error("line did not have enough elements", slog.String("line", line))
			return out
		}

		xy := strings.Split(parts[0], "x")
		if len(xy) != 2 {
			logger.Error("XY did not have enough elements", slog.String("line", line), slog.String("xy", parts[0]))
			return out
		}

		x, err := strconv.ParseFloat(xy[0], 64)
		if err != nil {
			logger.Error("failed to parse X", slog.String("line", line), slog.String("x", xy[0]))
			return out
		}

		y, err := strconv.ParseFloat(xy[1], 64)
		if err != nil {
			logger.Error("failed to parse Y", slog.String("line", line), slog.String("y", xy[1]))
			return out
		}

		scale := float64(1)
		if len(parts) > 2 {
			scale, err = strconv.ParseFloat(parts[2], 64)
			if err != nil {
				logger.Error("failed to parse scale", slog.String("line", line), slog.String("err", err.Error()))
				return out
			}
		}

		ov := overlay{name: parts[1], x: x, y: y, scale: min(scale, 5), hflip: false}
		if len(parts) > 3 {
			for _, v := range strings.Split(parts[3], "") {
				switch v {
				case "f":
					ov.hflip = true
				}
			}
		}

		if overlayCache.Exists(ov.name) {
			out = append(out, ov)
		} else {
			logger.Error("image does not exist", slog.String("line", line))
		}
	}

	return out
}

type overlay struct {
	name  string
	x, y  float64
	scale float64
	hflip bool
}
