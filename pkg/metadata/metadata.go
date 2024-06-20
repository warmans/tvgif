package metadata

import (
	"encoding/json"
	"github.com/warmans/tvgif/pkg/model"
	"os"
	"path"
	"strings"
)

func Process(inputDir string, fn func(ep model.Episode) error) error {
	dirEntries, err := os.ReadDir(inputDir)
	if err != nil {
		return err
	}
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".json") {
			continue
		}
		f, err := os.Open(path.Join(inputDir, dirEntry.Name()))
		if err != nil {
			return err
		}
		if err := func() error {
			defer f.Close()
			episode := &model.Episode{}
			if err := json.NewDecoder(f).Decode(episode); err != nil {
				return err
			}
			if err := fn(*episode); err != nil {
				return err
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}
