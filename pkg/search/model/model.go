package model

import (
	"fmt"
	"github.com/warmans/tvgif/pkg/search/mapping"
)

type DialogDocument struct {
	ID             string `json:"id"`
	EpisodeID      string `json:"episode_id"`
	Publication    string `json:"publication"`
	Series         int64  `json:"series"`
	Episode        int64  `json:"episode"`
	StartTimestamp string `json:"start_timestamp"`
	EndTimestamp   string `json:"end_timestamp"`
	VideoFileName  string `json:"video_file_name"`
	Content        string `json:"content"`
}

func (d DialogDocument) ShortEpisodeID() string {
	return fmt.Sprintf("S%02dE%02d", d.Series, d.Episode)
}

func (d DialogDocument) FieldMapping() map[string]mapping.FieldType {
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

func (d DialogDocument) GetNamedField(name string) any {
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
