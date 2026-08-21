package main

import (
	"testing"
	"time"
)

func TestScore(t *testing.T) {
	now := time.Now()
	fresh := func(kind string, s Status) Lens { return Lens{Kind: kind, Status: s, CheckedAt: now} }
	cases := []struct {
		name   string
		lenses []Lens
		want   int
	}{
		{"calm", []Lens{fresh(KindHTTP, StatusGood), fresh(KindCI, StatusGood)}, 10000},
		{"site down", []Lens{fresh(KindHTTP, StatusError)}, 2000},
		{"down and unresolvable floors at 1", []Lens{fresh(KindHTTP, StatusError), fresh(KindDNS, StatusError)}, 1},
		{"certificate expiring", []Lens{fresh(KindTLS, StatusWarn)}, 9700},
		{"domain unregistered", []Lens{fresh(KindDomain, StatusError)}, 5000},
		{"deploy building", []Lens{fresh(KindPages, StatusWarn)}, 9000},
		{"ci red", []Lens{fresh(KindCI, StatusError)}, 9500},
		{"ci running", []Lens{fresh(KindCI, StatusWarn)}, 9800},
		{"neutral costs nothing", []Lens{fresh(KindCI, StatusNeutral), fresh(KindHTTP, StatusGood)}, 10000},
		{"never checked", nil, 9000},
		{"one stale lens", []Lens{fresh(KindHTTP, StatusGood), {Kind: KindCI, Status: StatusGood, CheckedAt: now.Add(-4 * 24 * time.Hour)}}, 9700},
	}
	for _, c := range cases {
		if got := score(c.lenses, now); got != c.want {
			t.Errorf("%s: score = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSortKey(t *testing.T) {
	if got := sortKey(nil, 4200); got != 4200 {
		t.Errorf("no override: %d", got)
	}
	ov := 15
	if got := sortKey(&ov, 4200); got != 15 {
		t.Errorf("override: %d", got)
	}
}
