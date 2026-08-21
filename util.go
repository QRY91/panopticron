package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

const userAgent = "panopticron (+https://panopticron.com)"

// ago renders a past time for humans: "just now", "12m ago", "3d ago".
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// dayValue is the tile text for "time left": "61d", "3y", or "exp".
func dayValue(left time.Duration) string {
	days := int(left.Hours() / 24)
	switch {
	case left <= 0:
		return "exp"
	case days >= 730:
		return fmt.Sprintf("%dy", days/365)
	default:
		return fmt.Sprintf("%dd", days)
	}
}

// shortErr strips the transport wrappers Go puts around network errors so a
// tile detail reads "server misbehaving", not "Get \"https://…\": dial tcp: …".
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return de.Err
	}
	s := err.Error()
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}
