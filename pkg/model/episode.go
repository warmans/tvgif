package model

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/util"
	"time"
)

type EpisodeMeta struct {
	SourceSRTName string `json:"source_srt_name"`
	ImportedIndex bool   `json:"imported_index"`
	ImportedDB    bool   `json:"imported_db"`
}

type Manifest struct {
	SrtIndex map[string]string       `json:"srt_index"`
	Episodes map[string]*EpisodeMeta `json:"episodes"`
}

func (m *Manifest) Add(metaFileName string, ep *EpisodeMeta) {
	if m.Episodes == nil {
		m.Episodes = make(map[string]*EpisodeMeta)
	}
	m.Episodes[metaFileName] = ep
	if m.SrtIndex == nil {
		m.SrtIndex = make(map[string]string)
	}
	m.SrtIndex[ep.SourceSRTName] = metaFileName
}

type Dialog struct {
	Pos            int64         `json:"pos" db:"pos"`
	StartTimestamp time.Duration `json:"start_timestamp" db:"start_timestamp"`
	EndTimestamp   time.Duration `json:"end_timestamp" db:"end_timestamp"`
	Content        string        `json:"content" db:"content"`
}

func (e *Dialog) ID(episodeID string) string {
	return fmt.Sprintf("%s-%d", episodeID, e.Pos)
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
