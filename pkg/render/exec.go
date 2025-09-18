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
	"strings"
	"time"
)

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

		_, err := r.mediaCache.Get(createFileName(customID, extension), buff, opts.disableCaching || opts.overlayGifs > 0, func(writer io.Writer) error {
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
			if opts.overlayGifs > 0 {
				filterPrefix = ""

				for i, gif := range r.overlayCache.Random(opts.overlayGifs) {

					randomX := rand.Float64()
					randomY := rand.Float64()

					filterPrefix += fmt.Sprintf(
						"[%s][%d]overlay=x=(W*%0.2f):y=(H*%0.2f):shortest=1:[o%d];",
						util.IfElse(i == 0, "0", fmt.Sprintf("o%d", i-1)),
						i+1,
						randomX,
						randomY,
						i,
					)

					args = append(args, []string{
						//"-stream_loop", "-1",
						"-ignore_loop", "0",
						"-i", path.Join(r.mediaPath, "overlay", gif),
					})
				}

				filtersStartAt = fmt.Sprintf("o%d", opts.overlayGifs-1)

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
