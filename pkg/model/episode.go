package model

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"time"
)

type EpisodeMeta struct {
	SourceSRTName    string    `json:"source_srt_name"`
	SourceSRTModTime time.Time `json:"source_srt_mod_time"`
	ImportedIndex    bool      `json:"imported_index"`
	ImportedDB       bool      `json:"imported_db"`
}

type Dialog struct {
	Pos            int64         `json:"pos" db:"pos"`
	StartTimestamp time.Duration `json:"start_timestamp" db:"start_timestamp"`
	EndTimestamp   time.Duration `json:"end_timestamp" db:"end_timestamp"`
	Content        string        `json:"content" db:"content"`
	VideoFileName  string        `json:"video_file_name" db:"video_file_name"`
}

func (e *Dialog) ID(episodeID string) string {
	return fmt.Sprintf("%s-%d", episodeID, e.Pos)
}

type Episode struct {
	SRTFile     string    `json:"srt_file"`
	SRTModTime  time.Time `json:"srt_mod_time"`
	VideoFile   string    `json:"video_file"`
	Publication string    `json:"publication"`
	Series      int32     `json:"season"`
	Episode     int32     `json:"episode"`
	Dialog      []Dialog  `json:"dialog"`
}

func (e *Episode) ID() string {
	return fmt.Sprintf("%s-%s", e.Publication, util.FormatSeriesAndEpisode(int(e.Series), int(e.Episode)))
}

type Publication struct {
	Name   string   `json:"name"`
	Series []string `json:"series"`
}
