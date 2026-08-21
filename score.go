package main

import "time"

// The priority model, ported from the 2025 plpgsql function: one integer sorts
// the whole wall. Every project starts at 10000 (calm); each problem subtracts
// a malus; the floor is 1 (crisis). Lower = more urgent = higher on the wall.
// A human may override the result — visibly — and the sort key is
// COALESCE(override, score), exactly as before.
const (
	baseScore   = 10000
	staleAfter  = 3 * 24 * time.Hour
	malusNoData = 1000 // never checked
	malusStale  = 300  // some lens not refreshed within staleAfter
)

// malus is the whole scoring table. Error: the thing is broken. Warn: it is
// about to be (expiring, building, running).
func malus(kind string, s Status) int {
	switch s {
	case StatusError:
		switch kind {
		case KindHTTP, KindDNS:
			return 8000 // down for visitors
		case KindTLS:
			return 7000 // browsers refuse the connection
		case KindDomain:
			return 5000 // unregistered or expired — about to vanish
		case KindPages:
			return 2000 // production deploy failed; the old build still serves
		case KindCI:
			return 500
		}
	case StatusWarn:
		switch kind {
		case KindPages:
			return 1000 // deploy in progress
		case KindCI:
			return 200 // run in progress or cancelled
		default:
			return 300 // slow, expiring, …
		}
	}
	return 0
}

// score folds a project's current lenses into its calculated priority.
func score(lenses []Lens, now time.Time) int {
	s := baseScore
	var oldest time.Time
	for i, l := range lenses {
		s -= malus(l.Kind, l.Status)
		if i == 0 || l.CheckedAt.Before(oldest) {
			oldest = l.CheckedAt
		}
	}
	switch {
	case len(lenses) == 0:
		s -= malusNoData
	case now.Sub(oldest) > staleAfter:
		s -= malusStale
	}
	return max(1, s)
}

// sortKey is what the wall sorts by: the human wins when they have spoken.
func sortKey(override *int, score int) int {
	if override != nil {
		return *override
	}
	return score
}
