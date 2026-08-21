package main

import (
	"sort"
	"time"
)

// Status is the colour of a lens. The wall is scanned by colour; everything
// else is detail.
type Status string

const (
	StatusGood    Status = "good"
	StatusWarn    Status = "warn"
	StatusError   Status = "error"
	StatusNeutral Status = "neutral" // unknown or not applicable — never a malus
)

// Lens kinds, in the order they tie-break on the wall: the most fundamental
// failure first (a name that does not resolve outranks a red CI run).
const (
	KindHTTP   = "http"   // the page answers
	KindDNS    = "dns"    // the name resolves
	KindTLS    = "tls"    // the certificate is valid and not about to expire
	KindDomain = "domain" // the registration exists and is not about to expire
	KindPages  = "pages"  // latest Cloudflare Pages production deploy
	KindCI     = "ci"     // latest GitHub Actions run on the default branch
)

var kindOrder = []string{KindHTTP, KindDNS, KindTLS, KindDomain, KindPages, KindCI}

// Lens is one observation of one aspect of one project — the unit the wall is
// built from. Checks produce them; the store keeps the latest per (project,
// kind) and appends an event whenever Status changes.
type Lens struct {
	Kind      string
	Status    Status
	Value     string    // tile text, ≤5 chars: "200", "61d", "fail"
	Detail    string    // one line for the project page
	Link      string    // where to look next, optional
	CheckedAt time.Time // set by the poller
	ChangedAt time.Time // when Status last changed, set by the store
}

// Cluster is the 2025 LensCluster layout: sort lenses by (status, kind),
// promote the top three to big tiles, nest the next four in a small 2×2, pad
// with neutral placeholders so every project is the same fixed-size frame.
// A wall of these is scanned by colour: problems are always the big tiles.
type Cluster struct {
	Big   [3]Lens
	Small [4]Lens
}

var placeholder = Lens{Status: StatusNeutral}

func cluster(lenses []Lens) Cluster {
	ls := append([]Lens(nil), lenses...)
	sort.SliceStable(ls, func(i, j int) bool {
		if a, b := statusRank(ls[i].Status), statusRank(ls[j].Status); a != b {
			return a < b
		}
		return kindRank(ls[i].Kind) < kindRank(ls[j].Kind)
	})
	var c Cluster
	for i := range c.Big {
		c.Big[i] = placeholder
		if i < len(ls) {
			c.Big[i] = ls[i]
		}
	}
	for i := range c.Small {
		c.Small[i] = placeholder
		if j := i + len(c.Big); j < len(ls) {
			c.Small[i] = ls[j]
		}
	}
	return c
}

func statusRank(s Status) int {
	switch s {
	case StatusError:
		return 0
	case StatusWarn:
		return 1
	case StatusGood:
		return 2
	case StatusNeutral:
		return 3
	}
	return 4
}

func kindRank(kind string) int {
	for i, k := range kindOrder {
		if k == kind {
			return i
		}
	}
	return len(kindOrder)
}

// byKind orders lenses the way the project page lists them.
func byKind(lenses []Lens) []Lens {
	ls := append([]Lens(nil), lenses...)
	sort.SliceStable(ls, func(i, j int) bool { return kindRank(ls[i].Kind) < kindRank(ls[j].Kind) })
	return ls
}
