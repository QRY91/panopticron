package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// One SQLite file holds everything: the projects (identity + current score),
// the latest lens per (project, kind), an append-only events log that doubles
// as the lifeline, and one row per poller run.
const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id        INTEGER PRIMARY KEY,
	name      TEXT NOT NULL UNIQUE,
	url       TEXT NOT NULL DEFAULT '',
	domain    TEXT NOT NULL DEFAULT '',
	repo      TEXT NOT NULL DEFAULT '',
	pages     TEXT NOT NULL DEFAULT '',
	active    INTEGER NOT NULL DEFAULT 1,      -- 0 once removed from projects.toml; history kept
	score     INTEGER NOT NULL DEFAULT 10000,  -- calculated, see score.go
	override  INTEGER,                         -- the human's number; NULL = none
	sort_key  INTEGER NOT NULL DEFAULT 10000,  -- COALESCE(override, score)
	scored_at TEXT
);
CREATE TABLE IF NOT EXISTS lenses (            -- latest observation per (project, kind)
	project_id INTEGER NOT NULL REFERENCES projects(id),
	kind       TEXT NOT NULL,
	status     TEXT NOT NULL,
	value      TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	link       TEXT NOT NULL DEFAULT '',
	checked_at TEXT NOT NULL,
	changed_at TEXT NOT NULL,
	PRIMARY KEY (project_id, kind)
);
CREATE TABLE IF NOT EXISTS events (            -- append-only: the history
	id         INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES projects(id),
	at         TEXT NOT NULL,
	kind       TEXT NOT NULL,                  -- a lens kind, or 'priority'
	status     TEXT,                           -- lens events: the new status
	score      INTEGER,                        -- priority events: the new numbers
	override   INTEGER,
	sort_key   INTEGER,
	note       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_project_at ON events(project_id, at);
CREATE TABLE IF NOT EXISTS runs (              -- one row per poller run, see poll.go
	id         INTEGER PRIMARY KEY,
	job        TEXT NOT NULL,
	started_at TEXT NOT NULL,
	ended_at   TEXT,
	status     TEXT,                           -- ok | partial | fail
	summary    TEXT NOT NULL DEFAULT ''
);`

type Project struct {
	ID                             int64
	Name, URL, Domain, Repo, Pages string
	Score                          int
	Override                       *int
	SortKey                        int
	ScoredAt                       time.Time
	Lenses                         []Lens
}

type Event struct {
	At       time.Time
	Kind     string
	Status   Status
	Score    int
	Override *int
	SortKey  int
	Note     string
}

type Run struct {
	ID                 int64
	Job                string
	StartedAt, EndedAt time.Time // EndedAt zero while running
	Status, Summary    string
}

type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one connection: every transaction is serialised, no SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// syncProjects makes the projects table mirror projects.toml. Projects that
// vanished from the file are deactivated, never deleted — their history stays.
func (s *Store) syncProjects(cfg []ProjectConfig) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE projects SET active = 0`); err != nil {
		return err
	}
	for _, p := range cfg {
		_, err := tx.Exec(`INSERT INTO projects (name, url, domain, repo, pages, active) VALUES (?, ?, ?, ?, ?, 1)
			ON CONFLICT(name) DO UPDATE SET url = excluded.url, domain = excluded.domain,
			repo = excluded.repo, pages = excluded.pages, active = 1`,
			p.Name, p.URL, p.Domain, p.Repo, p.Pages)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const projectCols = `id, name, url, domain, repo, pages, score, override, sort_key, scored_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var override sql.NullInt64
	var scoredAt sql.NullString
	err := row.Scan(&p.ID, &p.Name, &p.URL, &p.Domain, &p.Repo, &p.Pages, &p.Score, &override, &p.SortKey, &scoredAt)
	if override.Valid {
		v := int(override.Int64)
		p.Override = &v
	}
	p.ScoredAt = parseTS(scoredAt.String)
	return p, err
}

// projects returns the active projects in wall order — most urgent first —
// with their current lenses attached.
func (s *Store) projects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects WHERE active = 1 ORDER BY sort_key, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range ps {
		if ps[i].Lenses, err = s.lenses(ps[i].ID); err != nil {
			return nil, err
		}
	}
	return ps, nil
}

// project returns one active project by name, or nil when there is none.
func (s *Store) project(name string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE name = ? AND active = 1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Lenses, err = s.lenses(p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) lenses(projectID int64) ([]Lens, error) {
	rows, err := s.db.Query(`SELECT kind, status, value, detail, link, checked_at, changed_at FROM lenses WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ls []Lens
	for rows.Next() {
		var l Lens
		var checked, changed string
		if err := rows.Scan(&l.Kind, &l.Status, &l.Value, &l.Detail, &l.Link, &checked, &changed); err != nil {
			return nil, err
		}
		l.CheckedAt, l.ChangedAt = parseTS(checked), parseTS(changed)
		ls = append(ls, l)
	}
	return byKind(ls), rows.Err()
}

// observe records one fresh observation: the lens row is upserted, and an
// event is appended only when the status changed (or the lens is new). It
// returns whether it changed and a one-line note saying how.
func (s *Store) observe(projectID int64, l Lens) (changed bool, note string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	var prev Status
	err = tx.QueryRow(`SELECT status FROM lenses WHERE project_id = ? AND kind = ?`, projectID, l.Kind).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, "", err
	}
	now := ts(l.CheckedAt)
	_, err = tx.Exec(`INSERT INTO lenses (project_id, kind, status, value, detail, link, checked_at, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, kind) DO UPDATE SET status = excluded.status, value = excluded.value,
		detail = excluded.detail, link = excluded.link, checked_at = excluded.checked_at,
		changed_at = CASE WHEN lenses.status = excluded.status THEN lenses.changed_at ELSE excluded.changed_at END`,
		projectID, l.Kind, l.Status, l.Value, l.Detail, l.Link, now, now)
	if err != nil {
		return false, "", err
	}
	if prev == l.Status {
		return false, "", tx.Commit()
	}
	if prev == "" {
		note = fmt.Sprintf("%s: %s (%s)", l.Kind, l.Status, l.Value)
	} else {
		note = fmt.Sprintf("%s: %s → %s (%s)", l.Kind, prev, l.Status, l.Value)
	}
	_, err = tx.Exec(`INSERT INTO events (project_id, at, kind, status, note) VALUES (?, ?, ?, ?, ?)`,
		projectID, now, l.Kind, l.Status, note)
	if err != nil {
		return false, "", err
	}
	return true, note, tx.Commit()
}

// rescore recomputes a project's priority from its current lenses and appends
// a priority event when the sort key moved (always on the first scoring, and
// always when force is set — override changes are worth a line even when the
// number happens to stay). note says why.
func (s *Store) rescore(projectID int64, now time.Time, note string, force bool) error {
	lenses, err := s.lenses(projectID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var override sql.NullInt64
	var oldKey int
	var scoredAt sql.NullString
	if err := tx.QueryRow(`SELECT override, sort_key, scored_at FROM projects WHERE id = ?`, projectID).Scan(&override, &oldKey, &scoredAt); err != nil {
		return err
	}
	var ov *int
	if override.Valid {
		v := int(override.Int64)
		ov = &v
	}
	sc := score(lenses, now)
	key := sortKey(ov, sc)
	if _, err := tx.Exec(`UPDATE projects SET score = ?, sort_key = ?, scored_at = ? WHERE id = ?`, sc, key, ts(now), projectID); err != nil {
		return err
	}
	if key != oldKey || !scoredAt.Valid || force {
		if note == "" {
			note = "rescored"
		}
		_, err := tx.Exec(`INSERT INTO events (project_id, at, kind, score, override, sort_key, note) VALUES (?, ?, 'priority', ?, ?, ?, ?)`,
			projectID, ts(now), sc, override, key, note)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// setOverride is the one thing a human changes. nil clears it.
func (s *Store) setOverride(projectID int64, v *int, now time.Time) error {
	var val sql.NullInt64
	note := "override cleared"
	if v != nil {
		val = sql.NullInt64{Int64: int64(*v), Valid: true}
		note = fmt.Sprintf("override set to %d", *v)
	}
	if _, err := s.db.Exec(`UPDATE projects SET override = ? WHERE id = ?`, val, projectID); err != nil {
		return err
	}
	return s.rescore(projectID, now, note, true)
}

// lifeline returns the priority events in [since, now] plus the one before
// `since`, so a step chart knows where the line starts.
func (s *Store) lifeline(projectID int64, since time.Time) ([]Event, error) {
	rows, err := s.db.Query(`
		SELECT * FROM (SELECT at, score, override, sort_key, note FROM events
			WHERE project_id = ? AND kind = 'priority' AND at < ? ORDER BY at DESC LIMIT 1)
		UNION ALL
		SELECT * FROM (SELECT at, score, override, sort_key, note FROM events
			WHERE project_id = ? AND kind = 'priority' AND at >= ? ORDER BY at)`,
		projectID, ts(since), projectID, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []Event
	for rows.Next() {
		var e Event
		var at string
		var override sql.NullInt64
		if err := rows.Scan(&at, &e.Score, &override, &e.SortKey, &e.Note); err != nil {
			return nil, err
		}
		e.At, e.Kind = parseTS(at), "priority"
		if override.Valid {
			v := int(override.Int64)
			e.Override = &v
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// events returns a project's most recent events of every kind, newest first.
func (s *Store) events(projectID int64, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT at, kind, status, score, override, sort_key, note FROM events
		WHERE project_id = ? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []Event
	for rows.Next() {
		var e Event
		var at string
		var status sql.NullString
		var score, override, key sql.NullInt64
		if err := rows.Scan(&at, &e.Kind, &status, &score, &override, &key, &e.Note); err != nil {
			return nil, err
		}
		e.At, e.Status, e.Score, e.SortKey = parseTS(at), Status(status.String), int(score.Int64), int(key.Int64)
		if override.Valid {
			v := int(override.Int64)
			e.Override = &v
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

func (s *Store) startRun(job string, now time.Time) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs (job, started_at) VALUES (?, ?)`, job, ts(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) finishRun(id int64, status, summary string, now time.Time) error {
	_, err := s.db.Exec(`UPDATE runs SET ended_at = ?, status = ?, summary = ? WHERE id = ?`, ts(now), status, summary, id)
	return err
}

func (s *Store) runs(limit int) ([]Run, error) {
	rows, err := s.db.Query(`SELECT id, job, started_at, ended_at, status, summary FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rs []Run
	for rows.Next() {
		var r Run
		var started string
		var ended, status sql.NullString
		if err := rows.Scan(&r.ID, &r.Job, &started, &ended, &status, &r.Summary); err != nil {
			return nil, err
		}
		r.StartedAt, r.EndedAt, r.Status = parseTS(started), parseTS(ended.String), status.String
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// pruneRuns keeps the runs table from growing forever.
func (s *Store) pruneRuns(keep int) error {
	_, err := s.db.Exec(`DELETE FROM runs WHERE id NOT IN (SELECT id FROM runs ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

func (s *Store) count(table string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
	return n
}

// Timestamps are stored as RFC 3339 UTC text: sortable, greppable, no driver magic.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
