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

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// cloudflare reads Pages deployments. Needs a token with Pages:Read and the
// account id; without both the pages lens simply does not exist.
type cloudflare struct {
	token, account string
	client         *http.Client
}

func (c cloudflare) enabled() bool { return c.token != "" && c.account != "" }

type cfDeployment struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Environment string    `json:"environment"` // production | preview
	CreatedOn   time.Time `json:"created_on"`
	LatestStage struct {
		Name   string `json:"name"`   // queued | initialize | clone_repo | build | deploy
		Status string `json:"status"` // idle | active | success | failure | canceled | skipped
	} `json:"latest_stage"`
	Trigger struct {
		Metadata struct {
			Branch     string `json:"branch"`
			CommitHash string `json:"commit_hash"`
		} `json:"metadata"`
	} `json:"deployment_trigger"`
}

// pagesLens reports the latest production deployment of a Pages project.
func (c cloudflare) pagesLens(ctx context.Context, project string, now time.Time) Lens {
	l := Lens{Kind: KindPages, Status: StatusNeutral, Value: "?",
		Link: fmt.Sprintf("https://dash.cloudflare.com/%s/pages/view/%s", c.account, url.PathEscape(project))}
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s/deployments?env=production&per_page=5", url.PathEscape(c.account), url.PathEscape(project))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cloudflareAPI+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		l.Detail = "cloudflare unreachable: " + shortErr(err)
		return l
	}
	defer resp.Body.Close()
	var body struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result []cfDeployment `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		l.Detail = fmt.Sprintf("cloudflare HTTP %d: bad json", resp.StatusCode)
		return l
	}
	if !body.Success {
		l.Detail = fmt.Sprintf("cloudflare HTTP %d", resp.StatusCode)
		if len(body.Errors) > 0 {
			l.Detail += ": " + body.Errors[0].Message
		}
		if resp.StatusCode == http.StatusNotFound {
			l.Status, l.Value = StatusError, "404"
		}
		return l
	}
	return pagesLensFrom(body.Result, now, l)
}

// pagesLensFrom is the pure half: deployments → lens.
func pagesLensFrom(ds []cfDeployment, now time.Time, l Lens) Lens {
	for _, d := range ds {
		if d.Environment != "production" {
			continue
		}
		sha := d.Trigger.Metadata.CommitHash
		if len(sha) > 7 {
			sha = sha[:7]
		}
		l.Detail = fmt.Sprintf("%s %s · %s@%s · %s", d.LatestStage.Name, d.LatestStage.Status, d.Trigger.Metadata.Branch, sha, ago(now, d.CreatedOn))
		if d.URL != "" {
			l.Link = d.URL
		}
		switch d.LatestStage.Status {
		case "success":
			l.Status, l.Value = StatusGood, "live"
		case "failure":
			l.Status, l.Value = StatusError, "fail"
		case "canceled", "cancelled":
			l.Status, l.Value = StatusWarn, "canc"
		case "active", "idle", "queued":
			l.Status, l.Value = StatusWarn, "bld"
		default:
			l.Status, l.Value = StatusNeutral, d.LatestStage.Status
		}
		return l
	}
	l.Value, l.Detail = "none", "no production deployments"
	return l
}
