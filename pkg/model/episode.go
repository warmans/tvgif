package model

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"time"
)

type Dialog struct {
	Pos            int64         `json:"pos"`
	StartTimestamp time.Duration `json:"start_timestamp"`
	EndTimestamp   time.Duration `json:"end_timestamp"`
	Content        string        `json:"content"`
}

type Episode struct {
	SRTFile     string   `json:"srt_file"`
	VideoFile   string   `json:"video_file"`
	Publication string   `json:"publication"`
	Series      int32    `json:"season"`
	Episode     int32    `json:"episode"`
	Dialog      []Dialog `json:"dialog"`
}

func (e *Episode) ID() string {
	return fmt.Sprintf("%s-%s", e.Publication, util.FormatSeriesAndEpisode(int(e.Series), int(e.Episode)))
}
