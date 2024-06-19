package importer

import (
	"encoding/json"
	"fmt"
	"github.com/blugelabs/bluge"
	"github.com/blugelabs/bluge/analysis"
	"github.com/blugelabs/bluge/analysis/token"
	"github.com/blugelabs/bluge/analysis/tokenizer"
	"github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/search/mapping"
	searchModel "github.com/warmans/tvgif/pkg/search/model"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

func PopulateIndex(logger *slog.Logger, metadataPath string, indexPath string) error {

	logger.Info("Removing old index...")
	if indexPath == "/" {
		panic("refusing to rm /*")
	}
	if err := exec.Command("rm", "-rf", indexPath).Run(); err != nil {
		return fmt.Errorf("failed to remove index: %w", err)
	}

	config := bluge.DefaultConfig(indexPath)

	index, err := bluge.OpenWriter(config)
	if err != nil {
		return err
	}
	logger.Info("Populating index...", slog.String("path", metadataPath))
	return populateIndex(metadataPath, index, logger)
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

func documentsFromPath(filePath string) ([]searchModel.DialogDocument, error) {

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

func populateIndex(inputDir string, writer *bluge.Writer, logger *slog.Logger) error {

	dirEntries, err := os.ReadDir(inputDir)
	if err != nil {
		return err
	}
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".json") {
			continue
		}

		logger.Info("Processing file...", slog.String("name", dirEntry.Name()))
		docs, err := documentsFromPath(path.Join(inputDir, dirEntry.Name()))
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
			batch.Insert(doc)
		}
		if err := writer.Batch(batch); err != nil {
			return err
		}
	}
	return nil
}
