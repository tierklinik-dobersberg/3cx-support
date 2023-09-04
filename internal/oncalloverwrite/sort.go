package oncalloverwrite

import "github.com/tierklinik-dobersberg/3cx-support/internal/structs"

// ByTime can be used to sort a slice of overwrites by From time.
type ByTime []*structs.Overwrite

func (sl ByTime) Less(i, j int) bool { return sl[i].From.Before(sl[j].From) }
func (sl ByTime) Swap(i, j int)      { sl[i], sl[j] = sl[j], sl[i] }
func (sl ByTime) Len() int           { return len(sl) }
