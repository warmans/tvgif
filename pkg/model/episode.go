package model

import (
	"fmt"
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
	Series      int64    `json:"season"`
	Episode     int64    `json:"episode"`
	Dialog      []Dialog `json:"dialog"`
}

func (e *Episode) ID() string {
	return fmt.Sprintf("%s-S%02dE%02d", e.Publication, e.Series, e.Episode)
}
