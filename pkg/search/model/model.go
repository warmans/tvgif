package model

import (
	"fmt"
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
	StartTimestamp string `json:"start_timestamp"`
	EndTimestamp   string `json:"end_timestamp"`
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
		"start_timestamp": mapping.FieldTypeText,
		"end_timestamp":   mapping.FieldTypeText,
		"video_file_name": mapping.FieldTypeText,
		"content":         mapping.FieldTypeText,
	}
}

func (d *DialogDocument) Duration() (time.Duration, error) {
	startTimestamp, err := time.ParseDuration(d.StartTimestamp)
	if err != nil {
		return 0, fmt.Errorf("failed to parse start time: %w", err)
	}
	endTimestamp, err := time.ParseDuration(d.EndTimestamp)
	if err != nil {
		return 0, fmt.Errorf("failed to parse start time: %w", err)
	}
	return endTimestamp - startTimestamp, nil
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
		d.ID = value.(string)
	case "episode_id":

		d.EpisodeID = value.(string)
	case "publication":
		d.Publication = value.(string)
	case "series":
		d.Series = value.(int32) // these never seem to be set by VisitStoredFields
	case "episode":
		d.Episode = value.(int32) // these never seem to be set by VisitStoredFields
	case "start_timestamp":
		d.StartTimestamp = value.(string)
	case "end_timestamp":
		d.EndTimestamp = value.(string)
	case "video_file_name":
		d.VideoFileName = value.(string)
	case "content":
		d.Content = value.(string)
	}
}
