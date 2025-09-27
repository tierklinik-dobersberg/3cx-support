package cdr

import (
	"testing"
	"time"
)

func Test_parseDuration(t *testing.T) {
	cases := []struct {
		I string
		E time.Duration
	}{
		{"", 0},
		{"00:00:00", 0},
		{"01:00:00", 1 * time.Hour},
		{"10:20:10", 10*time.Hour + 20*time.Minute + 10*time.Second},
	}

	for _, c := range cases {
		res, err := parseDuration(c.I)
		if err != nil {
			t.Error("did not expect an error")
			continue
		}

		if res != c.E {
			t.Errorf("unexpected result %s != %s", res, c.E)
		}
	}
}
