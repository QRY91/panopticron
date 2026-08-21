package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	rdapBase         = "https://rdap.org/domain/"
	domainWarnWithin = 30 * 24 * time.Hour
)

// domainLens asks RDAP when the registration expires. rdap.org bootstraps to
// the right registry (Verisign for .com/.net, Identity Digital for .zone, …).
// A 404 from the registry means the name is not registered at all — its own
// kind of red.
func domainLens(ctx context.Context, client *http.Client, domain string, now time.Time) Lens {
	l := Lens{Kind: KindDomain, Status: StatusNeutral, Value: "?", Link: rdapBase + domain}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rdapBase+domain, nil)
	req.Header.Set("Accept", "application/rdap+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		l.Detail = "rdap unreachable: " + shortErr(err)
		return l
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		l.Status, l.Value, l.Detail = StatusError, "unreg", "not registered (RDAP 404)"
		return l
	case resp.StatusCode != http.StatusOK:
		l.Detail = fmt.Sprintf("rdap HTTP %d", resp.StatusCode)
		return l
	}
	var body struct {
		Events []struct {
			Action string    `json:"eventAction"`
			Date   time.Time `json:"eventDate"`
		} `json:"events"`
		Status []string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		l.Detail = "rdap: bad json: " + err.Error()
		return l
	}
	return domainLensFrom(body.Events, body.Status, now, l)
}

type rdapEvent = struct {
	Action string    `json:"eventAction"`
	Date   time.Time `json:"eventDate"`
}

// domainLensFrom is the pure half: RDAP events + status → lens.
func domainLensFrom(events []rdapEvent, status []string, now time.Time, l Lens) Lens {
	var exp time.Time
	for _, e := range events {
		if e.Action == "expiration" {
			exp = e.Date
		}
	}
	if exp.IsZero() {
		l.Detail = "rdap: no expiration event"
		return l
	}
	left := exp.Sub(now)
	l.Value = dayValue(left)
	l.Detail = fmt.Sprintf("registration expires %s (%dd)", exp.Format("2006-01-02"), int(left.Hours()/24))
	if len(status) > 0 {
		l.Detail += " · " + strings.Join(status, ", ")
	}
	switch {
	case left <= 0, hasAny(status, "pending delete", "redemption period"):
		l.Status = StatusError
	case left < domainWarnWithin:
		l.Status = StatusWarn
	default:
		l.Status = StatusGood
	}
	return l
}

func hasAny(ss []string, wants ...string) bool {
	for _, s := range ss {
		for _, w := range wants {
			if s == w {
				return true
			}
		}
	}
	return false
}
