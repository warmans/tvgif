package search

import (
	"encoding/json"
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/blugelabs/bluge/analysis"
	"github.com/blugelabs/bluge/analysis/token"
	"github.com/blugelabs/bluge/analysis/tokenizer"
	"github.com/warmans/tvgif/pkg/metadata"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/search/mapping"
	searchModel "github.com/warmans/tvgif/pkg/search/model"
	"log/slog"
	"os"
	"path"
	"time"
)

func PopulateIndex(logger *slog.Logger, metadataPath string, indexPath string) error {

	index, err := bluge.OpenWriter(bluge.DefaultConfig(indexPath))
	if err != nil {
		return err
	}
	defer index.Close()

	return metadata.WithManifest(metadataPath, func(manifest *model.Manifest) error {
		logger.Info("Populating index...", slog.String("path", metadataPath))

		for metaFileName, meta := range manifest.Episodes {
			if meta.ImportedIndex {
				continue
			}
			logger.Info("Indexing...", slog.String("name", metaFileName))
			if err := populateIndex(path.Join(metadataPath, metaFileName), index); err != nil {
				return err
			}
			manifest.Episodes[metaFileName].ImportedIndex = true
		}
		return nil
	})
}

func getMappedField(fieldName string, t mapping.FieldType, d searchModel.DialogDocument) (bluge.Field, bool) {
	switch t {
	case mapping.FieldTypeKeyword:
		return bluge.NewKeywordField(fieldName, d.GetNamedField(fieldName).(string)).StoreValue().Aggregatable().StoreValue(), true
	case mapping.FieldTypeDate:
		dateField := d.GetNamedField(fieldName).(*time.Time)
		if dateField == nil {
			return nil, false
		}
		return bluge.NewDateTimeField(fieldName, *dateField).Aggregatable().StoreValue(), true
	case mapping.FieldTypeNumber:
		switch typed := d.GetNamedField(fieldName).(type) {
		case float32:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case float64:
			return bluge.NewNumericField(fieldName, typed).StoreValue(), true
		case int32:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case int64:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case int:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case int8:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case uint8:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		case int16:
			return bluge.NewNumericField(fieldName, float64(typed)).StoreValue(), true
		default:
			panic("non-numeric type mapped as number")
		}

	case mapping.FieldTypeShingles:
		shingleAnalyzer := &analysis.Analyzer{
			Tokenizer: tokenizer.NewUnicodeTokenizer(),
			TokenFilters: []analysis.TokenFilter{
				token.NewNgramFilter(2, 16),
			},
		}
		return bluge.NewTextField(fieldName, fmt.Sprintf("%v", d.GetNamedField(fieldName))).WithAnalyzer(shingleAnalyzer).SearchTermPositions().StoreValue(), true
	}
	// just use text for everything else
	return bluge.NewTextField(fieldName, fmt.Sprintf("%v", d.GetNamedField(fieldName))).SearchTermPositions().StoreValue(), true
}

func documentsFromMetaFile(filePath string) ([]searchModel.DialogDocument, error) {

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer f.Close()

	episode := &model.Episode{}

	decoder := json.NewDecoder(f)
	if err := decoder.Decode(episode); err != nil {
		return nil, err
	}

	docs := []searchModel.DialogDocument{}
	for _, v := range episode.Dialog {
		docs = append(docs, searchModel.DialogDocument{
			ID:             fmt.Sprintf("%s-%d", episode.ID(), v.Pos),
			Pos:            int32(v.Pos),
			EpisodeID:      episode.ID(),
			Publication:    episode.Publication,
			Series:         episode.Series,
			Episode:        episode.Episode,
			StartTimestamp: v.StartTimestamp.Milliseconds(),
			EndTimestamp:   v.EndTimestamp.Milliseconds(),
			VideoFileName:  episode.VideoFile,
			Content:        v.Content,
		})
	}
	return docs, nil
}

func populateIndex(metaFilePath string, writer *bluge.Writer) error {
	docs, err := documentsFromMetaFile(metaFilePath)
	if err != nil {
		return err
	}
	batch := bluge.NewBatch()
	for _, d := range docs {
		doc := bluge.NewDocument(d.ID)
		for k, t := range d.FieldMapping() {
			if mapped, ok := getMappedField(k, t, d); ok {
				doc.AddField(mapped)
			}
		}
		batch.Delete(doc.ID())
		batch.Update(doc.ID(), doc)
	}
	if err := writer.Batch(batch); err != nil {
		return err
	}
	return nil
}
