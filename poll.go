package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// A job is one poller. It visits every active project, produces lenses, hands
// them to the store, and leaves a row in `runs` saying how it went — the 2025
// WorkerLogger pattern (start → do → finish), surfaced on /status.
type job struct {
	name  string
	every time.Duration
	// lensesFor observes one project. nil, nil means "nothing to say here".
	lensesFor func(ctx context.Context, p Project, now time.Time) ([]Lens, error)
}

func (a *app) jobs() []job {
	js := []job{
		{name: "probe", every: 5 * time.Minute, lensesFor: func(ctx context.Context, p Project, now time.Time) ([]Lens, error) {
			if p.URL == "" {
				return nil, nil
			}
			return probe(ctx, a.probeClient, p.URL, now), nil
		}},
		{name: "domain", every: 24 * time.Hour, lensesFor: func(ctx context.Context, p Project, now time.Time) ([]Lens, error) {
			if p.Domain == "" {
				return nil, nil
			}
			return []Lens{domainLens(ctx, a.apiClient, p.Domain, now)}, nil
		}},
		{name: "ci", every: 15 * time.Minute, lensesFor: func(ctx context.Context, p Project, now time.Time) ([]Lens, error) {
			if p.Repo == "" {
				return nil, nil
			}
			return []Lens{a.github.ciLens(ctx, p.Repo, now)}, nil
		}},
	}
	if a.cf.enabled() {
		js = append(js, job{name: "pages", every: 10 * time.Minute, lensesFor: func(ctx context.Context, p Project, now time.Time) ([]Lens, error) {
			if p.Pages == "" {
				return nil, nil
			}
			return []Lens{a.cf.pagesLens(ctx, p.Pages, now)}, nil
		}})
	}
	return js
}

// loop runs a job now and then on its interval until ctx ends.
func (a *app) loop(ctx context.Context, j job) {
	a.runJob(ctx, j)
	t := time.NewTicker(j.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.runJob(ctx, j)
		}
	}
}

// runJob is one run of one job: a few projects at a time, each observed,
// stored and rescored; one line in runs and one in the log.
func (a *app) runJob(ctx context.Context, j job) {
	runID, err := a.store.startRun(j.name, time.Now())
	if err != nil {
		log.Printf("%s: start: %v", j.name, err)
		return
	}
	projects, err := a.store.projects()
	if err != nil {
		log.Printf("%s: projects: %v", j.name, err)
		_ = a.store.finishRun(runID, "fail", err.Error(), time.Now())
		return
	}

	var (
		mu              sync.Mutex
		lenses, changes int
		problems        []string
		wg              sync.WaitGroup
		sem             = make(chan struct{}, 4)
	)
	for _, p := range projects {
		wg.Add(1)
		sem <- struct{}{}
		go func(p Project) {
			defer wg.Done()
			defer func() { <-sem }()
			pctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			now := time.Now()
			ls, err := j.lensesFor(pctx, p, now)
			if ctx.Err() != nil {
				return // shutting down: a cancelled probe is not an outage
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				problems = append(problems, p.Name+": "+err.Error())
				return
			}
			if len(ls) == 0 {
				return
			}
			var notes []string
			for _, l := range ls {
				l.CheckedAt = now
				changed, note, err := a.store.observe(p.ID, l)
				if err != nil {
					problems = append(problems, p.Name+": "+err.Error())
					continue
				}
				lenses++
				if changed {
					changes++
					notes = append(notes, note)
				}
			}
			if err := a.store.rescore(p.ID, now, strings.Join(notes, "; "), false); err != nil {
				problems = append(problems, p.Name+": rescore: "+err.Error())
			}
		}(p)
	}
	wg.Wait()

	status := "ok"
	summary := fmt.Sprintf("%d projects, %d lenses, %d changes", len(projects), lenses, changes)
	if len(problems) > 0 {
		status = "partial"
		if lenses == 0 {
			status = "fail"
		}
		summary += "; " + strings.Join(problems, "; ")
	}
	if err := a.store.finishRun(runID, status, summary, time.Now()); err != nil {
		log.Printf("%s: finish: %v", j.name, err)
	}
	log.Printf("%s: %s — %s", j.name, status, summary)
}
