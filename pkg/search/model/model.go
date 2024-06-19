package model

import (
	"github.com/blugelabs/bluge"
	"github.com/warmans/tvgif/pkg/search/mapping"
	"github.com/warmans/tvgif/pkg/util"
	"time"
)

type DialogDocument struct {
	ID             string `json:"id"`
	EpisodeID      string `json:"episode_id"`
	Publication    string `json:"publication"`
	Series         int32  `json:"series"`
	Episode        int32  `json:"episode"`
	StartTimestamp int64  `json:"start_timestamp"`
	EndTimestamp   int64  `json:"end_timestamp"`
	VideoFileName  string `json:"video_file_name"`
	Content        string `json:"content"`
}

func (d *DialogDocument) ShortEpisodeID() string {
	return util.FormatSeriesAndEpisode(int(d.Series), int(d.Episode))
}

func (d *DialogDocument) FieldMapping() map[string]mapping.FieldType {
	return map[string]mapping.FieldType{
		"_id":             mapping.FieldTypeKeyword,
		"episode_id":      mapping.FieldTypeKeyword,
		"publication":     mapping.FieldTypeKeyword,
		"series":          mapping.FieldTypeNumber,
		"episode":         mapping.FieldTypeNumber,
		"start_timestamp": mapping.FieldTypeNumber,
		"end_timestamp":   mapping.FieldTypeNumber,
		"video_file_name": mapping.FieldTypeText,
		"content":         mapping.FieldTypeText,
	}
}

func (d *DialogDocument) Duration() time.Duration {
	return (time.Millisecond * time.Duration(d.EndTimestamp)) - (time.Millisecond * time.Duration(d.StartTimestamp))
}

func (d *DialogDocument) GetNamedField(name string) any {
	switch name {
	case "_id":
		return d.ID
	case "episode_id":
		return d.EpisodeID
	case "publication":
		return d.Publication
	case "series":
		return d.Series
	case "episode":
		return d.Episode
	case "start_timestamp":
		return d.StartTimestamp
	case "end_timestamp":
		return d.EndTimestamp
	case "video_file_name":
		return d.VideoFileName
	case "content":
		return d.Content
	}
	return ""
}

func (d *DialogDocument) SetNamedField(name string, value any) {
	switch name {
	case "_id":
		d.ID = string(value.([]byte))
	case "episode_id":

		d.EpisodeID = string(value.([]byte))
	case "publication":
		d.Publication = string(value.([]byte))
	case "series":
		d.Series = int32(bytesToFloatOrZero(value))
	case "episode":
		d.Episode = int32(bytesToFloatOrZero(value))
	case "start_timestamp":
		d.StartTimestamp = int64(bytesToFloatOrZero(value))
	case "end_timestamp":
		d.EndTimestamp = int64(bytesToFloatOrZero(value))
	case "video_file_name":
		d.VideoFileName = string(value.([]byte))
	case "content":
		d.Content = string(value.([]byte))
	}
}

func bytesToFloatOrZero(val any) float64 {
	bytes := val.([]byte)
	float, err := bluge.DecodeNumericFloat64(bytes)
	if err != nil {
		return 0
	}
	return float
}
