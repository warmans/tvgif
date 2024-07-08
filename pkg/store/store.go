package store

import (
	"github.com/jmoiron/sqlx"
	"github.com/warmans/tvgif/pkg/model"
	"strings"
)

type DB interface {
	sqlx.Queryer
	sqlx.Execer
}

func NewSRTStore(conn DB) *SRTStore {
	return &SRTStore{conn: conn}
}

type SRTStore struct {
	conn DB
}

func (s *SRTStore) ImportEpisode(m model.Episode) error {
	for _, v := range m.Dialog {
		_, err := s.conn.Exec(`
		REPLACE INTO dialog
		    (id, publication, series, episode, pos, start_timestamp, end_timestamp, content, video_file_name) 
		VALUES 
		    ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`,
			v.ID(m.ID()),
			m.Publication,
			m.Series,
			m.Episode,
			v.Pos,
			v.StartTimestamp,
			v.EndTimestamp,
			v.Content,
			m.VideoFile,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SRTStore) GetDialogRange(publication string, series int32, episode int32, startPos int64, endPos int64) ([]model.Dialog, error) {
	rows, err := s.conn.Queryx(
		`SELECT pos, start_timestamp, end_timestamp, content, video_file_name  FROM "dialog" WHERE publication=$1 AND series=$2 AND episode=$3 AND pos >= $4 AND pos <= $5`,
		publication,
		series,
		episode,
		startPos,
		endPos,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dialog := []model.Dialog{}
	for rows.Next() {
		row := model.Dialog{}
		if err := rows.StructScan(&row); err != nil {
			return nil, err
		}
		dialog = append(dialog, row)
	}
	return dialog, nil
}

func (s *SRTStore) GetDialogContext(publication string, series int32, episode int32, startPos int64, endPos int64) ([]model.Dialog, []model.Dialog, error) {
	rows, err := s.conn.Queryx(
		`SELECT pos, start_timestamp, end_timestamp, content, video_file_name  FROM "dialog" WHERE publication=$1 AND series=$2 AND episode=$3 AND pos >= $4 AND pos <= $5`,
		publication,
		series,
		episode,
		startPos-1,
		endPos+1,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	before := []model.Dialog{}
	after := []model.Dialog{}
	for rows.Next() {
		row := model.Dialog{}
		if err := rows.StructScan(&row); err != nil {
			return nil, nil, err
		}
		if row.Pos < startPos {
			before = append(before, row)
		}
		if row.Pos > endPos {
			after = append(after, row)
		}
	}
	return before, after, nil
}

func (s *SRTStore) ListPublications() ([]model.Publication, error) {
	rows, err := s.conn.Queryx(`SELECT publication, GROUP_CONCAT(DISTINCT series) FROM dialog GROUP BY publication;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	publications := []model.Publication{}
	for rows.Next() {
		row := model.Publication{}
		var series string
		if err := rows.Scan(&row.Name, &series); err != nil {
			return nil, err
		}
		row.Series = strings.Split(series, ",")
		publications = append(publications, row)
	}
	return publications, nil
}
