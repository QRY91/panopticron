package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func lensByKind(ls []Lens, kind string) Lens {
	for _, l := range ls {
		if l.Kind == kind {
			return l
		}
	}
	return Lens{}
}

func TestProbe(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the untrusted-client case below is noisy by design
	srv.StartTLS()
	defer srv.Close()
	now := time.Now()

	ls := probe(context.Background(), srv.Client(), srv.URL, now)
	if got := lensByKind(ls, KindDNS); got.Status != StatusGood {
		t.Errorf("dns: %+v", got)
	}
	if got := lensByKind(ls, KindHTTP); got.Status != StatusGood || got.Value != "200" {
		t.Errorf("http: %+v", got)
	}
	if got := lensByKind(ls, KindTLS); got.Status != StatusGood {
		t.Errorf("tls: %+v", got)
	}

	code = http.StatusServiceUnavailable
	if got := lensByKind(probe(context.Background(), srv.Client(), srv.URL, now), KindHTTP); got.Status != StatusError || got.Value != "503" {
		t.Errorf("503: %+v", got)
	}

	// the default client does not trust the test certificate: http down, tls bad
	ls = probe(context.Background(), newProbeClient(), srv.URL, now)
	if got := lensByKind(ls, KindHTTP); got.Status != StatusError || got.Value != "down" {
		t.Errorf("untrusted http: %+v", got)
	}
	if got := lensByKind(ls, KindTLS); got.Status != StatusError || got.Value != "bad" {
		t.Errorf("untrusted tls: %+v", got)
	}

	srv.Close()
	if got := lensByKind(probe(context.Background(), srv.Client(), srv.URL, now), KindHTTP); got.Status != StatusError {
		t.Errorf("closed server: %+v", got)
	}
	if got := probe(context.Background(), srv.Client(), "::nope", now); got[0].Status != StatusError {
		t.Errorf("bad url: %+v", got)
	}
}

func TestDomainLensFrom(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	days := func(n int) time.Time { return now.Add(time.Duration(n) * 24 * time.Hour) }
	cases := []struct {
		name   string
		exp    time.Time
		status []string
		want   Status
		value  string
	}{
		{"healthy", days(267), []string{"client transfer prohibited"}, StatusGood, "267d"},
		{"years away", days(800), nil, StatusGood, "2y"},
		{"expiring", days(12), nil, StatusWarn, "12d"},
		{"expired", days(-1), nil, StatusError, "exp"},
		{"pending delete", days(20), []string{"pending delete"}, StatusError, "20d"},
	}
	for _, c := range cases {
		l := domainLensFrom([]rdapEvent{{Action: "registration", Date: days(-700)}, {Action: "expiration", Date: c.exp}}, c.status, now, Lens{Kind: KindDomain})
		if l.Status != c.want || l.Value != c.value {
			t.Errorf("%s: got %s %q (%s), want %s %q", c.name, l.Status, l.Value, l.Detail, c.want, c.value)
		}
	}
	if l := domainLensFrom(nil, nil, now, Lens{Kind: KindDomain, Status: StatusNeutral}); l.Status != StatusNeutral {
		t.Errorf("no events: %+v", l)
	}
}

func TestCILensFrom(t *testing.T) {
	now := time.Now()
	base := Lens{Kind: KindCI, Status: StatusNeutral, Value: "?"}
	runs := []ghRun{
		{Name: "npm_and_yarn in /. for js-yaml", Status: "completed", Conclusion: "failure", Event: "dynamic", HTMLURL: "u1"},
		{Name: "ci", Status: "completed", Conclusion: "success", Event: "push", HTMLURL: "u2", HeadBranch: "main", UpdatedAt: now},
	}
	if l := ciLensFrom(runs, now, base); l.Status != StatusGood || l.Value != "pass" || l.Link != "u2" {
		t.Errorf("dependabot run should be skipped: %+v", l)
	}
	for _, c := range []struct {
		status, conclusion string
		want               Status
		value              string
	}{
		{"in_progress", "", StatusWarn, "run"},
		{"completed", "failure", StatusError, "fail"},
		{"completed", "timed_out", StatusError, "fail"},
		{"completed", "cancelled", StatusWarn, "canc"},
		{"completed", "skipped", StatusNeutral, "skip"},
	} {
		l := ciLensFrom([]ghRun{{Name: "ci", Status: c.status, Conclusion: c.conclusion, Event: "push"}}, now, base)
		if l.Status != c.want || l.Value != c.value {
			t.Errorf("%s/%s: got %s %q", c.status, c.conclusion, l.Status, l.Value)
		}
	}
	if l := ciLensFrom(nil, now, base); l.Status != StatusNeutral || l.Value != "none" {
		t.Errorf("no runs: %+v", l)
	}
}

func TestPagesLensFrom(t *testing.T) {
	now := time.Now()
	base := Lens{Kind: KindPages, Status: StatusNeutral, Value: "?"}
	mk := func(env, stage, status string) cfDeployment {
		var d cfDeployment
		d.Environment, d.LatestStage.Name, d.LatestStage.Status = env, stage, status
		d.Trigger.Metadata.Branch, d.Trigger.Metadata.CommitHash = "main", "0123456789abcdef"
		return d
	}
	if l := pagesLensFrom([]cfDeployment{mk("preview", "deploy", "failure"), mk("production", "deploy", "success")}, now, base); l.Status != StatusGood || l.Value != "live" {
		t.Errorf("preview should be skipped: %+v", l)
	}
	if l := pagesLensFrom([]cfDeployment{mk("production", "build", "failure")}, now, base); l.Status != StatusError || l.Value != "fail" {
		t.Errorf("failed build: %+v", l)
	}
	if l := pagesLensFrom([]cfDeployment{mk("production", "build", "active")}, now, base); l.Status != StatusWarn || l.Value != "bld" {
		t.Errorf("building: %+v", l)
	}
	if l := pagesLensFrom(nil, now, base); l.Status != StatusNeutral || l.Value != "none" {
		t.Errorf("none: %+v", l)
	}
}
