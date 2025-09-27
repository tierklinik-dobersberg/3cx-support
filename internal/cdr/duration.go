package cdr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}

	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	parse := func(p string, factor time.Duration) (time.Duration, error) {
		i, err := strconv.ParseInt(
			strings.TrimPrefix(p, "0"),
			10,
			0,
		)
		if err != nil {
			return 0, err
		}

		return time.Duration(i) * factor, nil
	}

	hours, err := parse(parts[0], time.Hour)
	if err != nil {
		return 0, err
	}
	minutes, err := parse(parts[1], time.Minute)
	if err != nil {
		return 0, err
	}
	seconds, err := parse(parts[2], time.Second)
	if err != nil {
		return 0, err
	}

	return hours + minutes + seconds, nil
}
