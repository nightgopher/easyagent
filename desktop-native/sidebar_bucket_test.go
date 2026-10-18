package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSessionBucketGroupsByLocalCalendarAge(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"now", now, 0},
		{"start of today", today, 0},
		{"yesterday evening", today.Add(-time.Hour), 1},
		{"six days ago", today.AddDate(0, 0, -6), 1},
		{"seven days ago", today.AddDate(0, 0, -7), 2},
		{"last month", today.AddDate(0, -1, 0), 2},
	}
	for _, tc := range cases {
		if got := sessionBucket(tc.at.Format(time.RFC3339)); got != tc.want {
			t.Errorf("%s: bucket = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := sessionBucket(fmt.Sprint(today.Unix())); got != 2 {
		t.Errorf("non-RFC3339 stamp should sort into the oldest bucket, got %d", got)
	}
}
