package srt

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/warmans/tvgif/pkg/model"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var htmlTag = regexp.MustCompile(`<[^<>]+>`)

type srtEntity string

const (
	srtEntryPos    = srtEntity("pos")
	srtEntryTs     = srtEntity("ts")
	srtEntryDialog = srtEntity("dialog")
)

func Read(source io.Reader) ([]model.Dialog, error) {

	dialog := []model.Dialog{}
	currentDialog := model.Dialog{}
	wantNext := srtEntryPos

	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		// strip random BOMs
		line := strings.Replace(strings.TrimSpace(scanner.Text()), "\ufeff", "", -1)
		if line == "" {
			if currentDialog != (model.Dialog{}) {
				dialog = append(dialog, currentDialog)
				currentDialog = model.Dialog{}
			}
			wantNext = srtEntryPos
			continue
		}

		var err error
		switch wantNext {
		case srtEntryPos:
			if currentDialog.Pos, err = scanPos(line); err != nil {
				return nil, fmt.Errorf("failed to scan position: %w", err)
			}
			wantNext = srtEntryTs
		case srtEntryTs:
			if currentDialog.StartTimestamp, currentDialog.EndTimestamp, err = scanTimestamps(line); err != nil {
				return nil, fmt.Errorf("failed to scan timestamps: %w", err)
			}
			wantNext = srtEntryDialog
		case srtEntryDialog:
			line = htmlTag.ReplaceAllString(line, "")
			// just keep adding content until a blank line is encountered
			if currentDialog.Content == "" {
				currentDialog.Content = line
			} else {
				currentDialog.Content += "\n" + line
			}
		}
	}
	if currentDialog != (model.Dialog{}) {
		dialog = append(dialog, currentDialog)
	}

	return dialog, nil
}

func scanPos(line string) (int64, error) {
	intVal, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0, fmt.Errorf("failed to parse position '%s': %w", line, err)
	}
	return int64(intVal), nil
}

func scanTimestamps(line string) (time.Duration, time.Duration, error) {
	times := strings.Split(line, "-->")
	if len(times) < 2 {
		return time.Duration(0), time.Duration(0), fmt.Errorf("invalid timestamp line: '%s'", line)
	}

	startTime, err := parseTime(times[0])
	if err != nil {
		return time.Duration(0), time.Duration(0), fmt.Errorf("invalid start timestamp '%s': %w", times[0], err)
	}
	endTime, err := parseTime(times[1])
	if err != nil {
		return time.Duration(0), time.Duration(0), fmt.Errorf("invalid end timestamp '%s': %w", times[1], err)
	}

	return startTime, endTime, nil
}

func parseTime(input string) (time.Duration, error) {
	regex := regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2}),(\d{3})`)
	matches := regex.FindStringSubmatch(input)

	if len(matches) < 4 {
		return time.Duration(0), fmt.Errorf("invalid time format:%s", input)
	}

	hour, err := strconv.Atoi(matches[1])
	if err != nil {
		return time.Duration(0), err
	}
	minute, err := strconv.Atoi(matches[2])
	if err != nil {
		return time.Duration(0), err
	}
	second, err := strconv.Atoi(matches[3])
	if err != nil {
		return time.Duration(0), err
	}
	millisecond, err := strconv.Atoi(matches[4])
	if err != nil {
		return time.Duration(0), err
	}

	return time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second + time.Duration(millisecond)*time.Millisecond, nil
}
