package mediacache

import (
	"log/slog"
	"math/rand/v2"
	"os"
	"path"
	"strings"
)

func NewOverlayCache(overlayDir string, logger *slog.Logger) (*OverlayCache, error) {
	entries, err := os.ReadDir(overlayDir)
	if err != nil {
		return nil, err
	}
	cache := &OverlayCache{overlays: make([]string, 0)}
	for _, v := range entries {
		if v.IsDir() || !strings.HasSuffix(v.Name(), ".gif") {
			continue
		}
		cache.overlays = append(cache.overlays, path.Base(v.Name()))
		logger.Info("discovered overlay", slog.String("name", path.Base(v.Name())))
	}
	return cache, nil
}

type OverlayCache struct {
	overlays []string
}

func (o *OverlayCache) Random(num int) []string {
	random := []string{}
	for i := 0; i < num; i++ {
		random = append(random, o.overlays[rand.IntN(len(o.overlays)-1)])
	}
	return random
}

func (o *OverlayCache) All() []string {
	all := []string{}
	for _, val := range o.overlays {
		all = append(all, val)
	}
	return all
}

func (o *OverlayCache) Exists(name string) bool {
	for _, val := range o.overlays {
		if name == val {
			return true
		}
	}
	return false
}
