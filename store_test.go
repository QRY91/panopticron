package main

import (
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	s, err := openStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.syncProjects([]ProjectConfig{{Name: "a", URL: "https://a.example"}, {Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	ps, err := s.projects()
	if err != nil || len(ps) != 2 || ps[0].Name != "a" || ps[0].SortKey != 10000 {
		t.Fatalf("projects: %v %+v", err, ps)
	}
	a := ps[0]
	now := time.Now().Truncate(time.Second)

	// first observation: new lens → changed; same status again → unchanged
	changed, note, err := s.observe(a.ID, Lens{Kind: KindHTTP, Status: StatusGood, Value: "200", CheckedAt: now})
	if err != nil || !changed || note != "http: good (200)" {
		t.Fatalf("first observe: %v %v %q", changed, err, note)
	}
	if changed, _, _ = s.observe(a.ID, Lens{Kind: KindHTTP, Status: StatusGood, Value: "200", CheckedAt: now.Add(time.Minute)}); changed {
		t.Fatal("same status should not count as a change")
	}
	if err := s.rescore(a.ID, now, "", false); err != nil {
		t.Fatal(err)
	}
	p, _ := s.project("a")
	if p.Score != 10000 || p.SortKey != 10000 || len(p.Lenses) != 1 || !p.Lenses[0].ChangedAt.Equal(now) {
		t.Fatalf("after calm rescore: %+v", p)
	}

	// the site goes down: lens event, priority event, score 2000
	if changed, note, _ = s.observe(a.ID, Lens{Kind: KindHTTP, Status: StatusError, Value: "503", CheckedAt: now.Add(2 * time.Minute)}); !changed || note != "http: good → error (503)" {
		t.Fatalf("change: %v %q", changed, note)
	}
	if err := s.rescore(a.ID, now.Add(2*time.Minute), note, false); err != nil {
		t.Fatal(err)
	}
	p, _ = s.project("a")
	if p.Score != 2000 || p.SortKey != 2000 {
		t.Fatalf("after outage: score %d key %d", p.Score, p.SortKey)
	}

	// a human overrides, then clears
	ov := 7000
	if err := s.setOverride(a.ID, &ov, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, _ = s.project("a")
	if p.Score != 2000 || p.SortKey != 7000 || p.Override == nil || *p.Override != 7000 {
		t.Fatalf("after override: %+v", p)
	}
	if err := s.setOverride(a.ID, nil, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.project("a"); p.SortKey != 2000 || p.Override != nil {
		t.Fatalf("after clear: %+v", p)
	}

	// the wall sorts the urgent project first
	if ps, _ = s.projects(); ps[0].Name != "a" || ps[1].Name != "b" {
		t.Fatalf("wall order: %s, %s", ps[0].Name, ps[1].Name)
	}

	// history: http new, priority first, http change, priority change, override set, override clear
	evs, _ := s.events(a.ID, 100)
	if len(evs) != 6 || evs[0].Note != "override cleared" || evs[5].Kind != KindHTTP {
		t.Fatalf("events: %d %+v", len(evs), evs)
	}
	line, _ := s.lifeline(a.ID, now.Add(90*time.Second))
	if len(line) != 4 || line[0].SortKey != 10000 || line[1].SortKey != 2000 || line[2].Override == nil {
		t.Fatalf("lifeline should carry the point before `since` plus three after: %+v", line)
	}

	// runs
	id, _ := s.startRun("probe", now)
	if err := s.finishRun(id, "ok", "2 projects", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.runs(10); len(rs) != 1 || rs[0].Status != "ok" || rs[0].EndedAt.Sub(rs[0].StartedAt) != time.Second {
		t.Fatalf("runs: %+v", rs)
	}
	for i := 0; i < 5; i++ {
		_, _ = s.startRun("ci", now)
	}
	_ = s.pruneRuns(3)
	if rs, _ := s.runs(10); len(rs) != 3 {
		t.Fatalf("prune: %d", len(rs))
	}

	// removing a project from the config hides it but keeps its history
	if err := s.syncProjects([]ProjectConfig{{Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.project("a"); p != nil {
		t.Fatal("a should be inactive")
	}
	if evs, _ = s.events(a.ID, 100); len(evs) != 6 {
		t.Fatal("history should survive deactivation")
	}
}
