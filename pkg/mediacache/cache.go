package mediacache

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"syscall"
)

type Cache struct {
	logger   *slog.Logger
	cacheDir string
}

func NewCache(cacheDir string, log *slog.Logger) (*Cache, error) {
	return &Cache{cacheDir: cacheDir, logger: log.With(slog.String("component", "media_cache"))}, nil
}

func (c *Cache) Get(key string, writeTo io.Writer, noCache bool, fetchFn func(writer io.Writer) error) (bool, error) {
	if noCache {
		return false, fetchFn(writeTo)
	}
	filePath := path.Join(c.cacheDir, key)
	f, err := os.Open(filePath)
	if err == nil {
		defer f.Close()
		if _, err = io.Copy(writeTo, f); err == nil {
			return true, nil
		}
		c.logger.Error("failed to write to writer", slog.String("err", err.Error()))
		return false, fetchFn(writeTo)
	}
	if !errors.Is(err, os.ErrNotExist) {
		c.logger.Error("failed to open cached file", slog.String("file_path", filePath), slog.String("err", err.Error()))
		return false, fetchFn(writeTo)
	}

	// cached file doesn't exist
	cacheFileCreated, err := func() (bool, error) {
		newFile, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0666)
		if err != nil {
			c.logger.Error("failed to create cached file", slog.String("file_path", filePath), slog.String("err", err.Error()))
			return false, fetchFn(writeTo)
		}
		defer func() {
			if err := newFile.Close(); err != nil {
				panic(fmt.Sprintf("failed to close file after write: %s", err.Error()))
			}
		}()
		if err = syscall.Flock(int(newFile.Fd()), syscall.LOCK_EX); err != nil {
			c.logger.Error("failed to lock file for writing", slog.String("file_path", filePath), slog.String("err", err.Error()))
			return true, fetchFn(writeTo)
		}
		defer func() {
			if err := syscall.Flock(int(newFile.Fd()), syscall.LOCK_UN); err != nil {
				panic(fmt.Sprintf("failed to unlock file after write: %s", err.Error()))
			}
		}()
		return true, fetchFn(io.MultiWriter(writeTo, newFile))
	}()
	if err != nil {
		if cacheFileCreated {
			if err := os.Remove(filePath); err != nil {
				c.logger.Error("failed to remove cached file after write error.", slog.String("file_path", filePath), slog.String("err", err.Error()))
			}
		}
	}
	return false, err
}
