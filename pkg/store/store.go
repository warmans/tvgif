package store

import (
	"github.com/jmoiron/sqlx"
	"github.com/warmans/tvgif/pkg/model"
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
		    (id, publication, series, episode, pos, start_timestamp, end_timestamp, content) 
		VALUES 
		    ($1, $2, $3, $4, $5, $6, $7, $8)
		`,
			v.ID(m.ID()),
			m.Publication,
			m.Series,
			m.Episode,
			v.Pos,
			v.StartTimestamp.Milliseconds(),
			v.EndTimestamp.Milliseconds(),
			v.Content,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SRTStore) GetDialogContext(publication string, series, episode, pos int32) ([]model.Dialog, []model.Dialog, error) {
	rows, err := s.conn.Queryx(
		`SELECT pos, start_timestamp, end_timestamp, content  FROM "dialog" WHERE publication=$1 AND series=$2 AND episode=$3 AND pos >= $4 AND pos <= $5`,
		publication,
		series,
		episode,
		pos-1,
		pos+1,
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
		if row.Pos < int64(pos) {
			before = append(before, row)
		}
		if row.Pos > int64(pos) {
			after = append(after, row)
		}
	}
	return before, after, nil
}
