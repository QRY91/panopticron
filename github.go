package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const githubAPI = "https://api.github.com"

// github is the smallest possible client: two GETs per repo per run.
type github struct {
	token  string // optional: 5000 req/h instead of 60, and private repos
	client *http.Client
}

func (g github) get(ctx context.Context, path string, v any) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+path, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

type ghRun struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`     // queued | in_progress | completed | …
	Conclusion string    `json:"conclusion"` // success | failure | cancelled | skipped | …
	Event      string    `json:"event"`      // push | schedule | workflow_dispatch | dynamic (Dependabot) | …
	HTMLURL    string    `json:"html_url"`
	HeadBranch string    `json:"head_branch"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ciLens reports the latest GitHub Actions run on the repo's default branch.
// Dependabot's own update runs (event "dynamic") are skipped: they are not the
// project's CI and would paint a false red.
func (g github) ciLens(ctx context.Context, repo string, now time.Time) Lens {
	l := Lens{Kind: KindCI, Status: StatusNeutral, Value: "?", Link: "https://github.com/" + repo + "/actions"}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	code, err := g.get(ctx, "/repos/"+repo, &r)
	if msg := githubProblem(code, err); msg != "" {
		l.Detail = msg
		if code == http.StatusNotFound {
			l.Status, l.Value = StatusError, "404"
		}
		return l
	}
	var runs struct {
		Runs []ghRun `json:"workflow_runs"`
	}
	code, err = g.get(ctx, fmt.Sprintf("/repos/%s/actions/runs?branch=%s&per_page=10&exclude_pull_requests=true", repo, url.QueryEscape(r.DefaultBranch)), &runs)
	if msg := githubProblem(code, err); msg != "" {
		l.Detail = msg
		return l
	}
	return ciLensFrom(runs.Runs, now, l)
}

func githubProblem(code int, err error) string {
	switch {
	case err != nil:
		return "github unreachable: " + shortErr(err)
	case code == http.StatusNotFound:
		return "repo not found (or private without a token)"
	case code == http.StatusForbidden || code == http.StatusTooManyRequests:
		return "github rate-limited; set GITHUB_TOKEN"
	case code != http.StatusOK:
		return fmt.Sprintf("github HTTP %d", code)
	}
	return ""
}

// ciLensFrom is the pure half: the latest non-Dependabot run → lens.
func ciLensFrom(runs []ghRun, now time.Time, l Lens) Lens {
	for _, run := range runs {
		if run.Event == "dynamic" {
			continue
		}
		l.Link = run.HTMLURL
		what := run.Status
		if run.Status == "completed" {
			what = run.Conclusion
		}
		l.Detail = fmt.Sprintf("%s: %s on %s, %s", run.Name, what, run.HeadBranch, ago(now, run.UpdatedAt))
		switch {
		case run.Status != "completed":
			l.Status, l.Value = StatusWarn, "run"
		case run.Conclusion == "success":
			l.Status, l.Value = StatusGood, "pass"
		case run.Conclusion == "cancelled":
			l.Status, l.Value = StatusWarn, "canc"
		case run.Conclusion == "skipped" || run.Conclusion == "neutral":
			l.Status, l.Value = StatusNeutral, "skip"
		default: // failure, timed_out, startup_failure, action_required
			l.Status, l.Value = StatusError, "fail"
		}
		return l
	}
	l.Value, l.Detail = "none", "no workflow runs on the default branch"
	return l
}
