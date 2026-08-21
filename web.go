package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

//go:embed templates static
var embedded embed.FS

// app wires the store, the outbound clients and the templates together; the
// pollers (poll.go) and the handlers (below) hang off it.
type app struct {
	store       *Store
	probeClient *http.Client
	apiClient   *http.Client
	github      github
	cf          cloudflare
	adminPass   string
	dev         bool
	started     time.Time
	tmpl        *template.Template
}

func newApp(store *Store, dev bool) *app {
	api := &http.Client{Timeout: 20 * time.Second}
	a := &app{
		store:       store,
		probeClient: newProbeClient(),
		apiClient:   api,
		github:      github{token: os.Getenv("GITHUB_TOKEN"), client: api},
		cf:          cloudflare{token: os.Getenv("CLOUDFLARE_API_TOKEN"), account: os.Getenv("CLOUDFLARE_ACCOUNT_ID"), client: api},
		adminPass:   os.Getenv("PANOPTICRON_ADMIN_PASSWORD"),
		dev:         dev,
		started:     time.Now(),
	}
	a.tmpl = template.Must(parseTemplates(embedded))
	return a
}

var funcs = template.FuncMap{
	"ago":     ago,
	"cluster": cluster,
	"date":    func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
	"dur":     func(d time.Duration) string { return d.Round(time.Millisecond).String() },
	"every":   every,
}

// every renders a poller interval the way people say it: "5m", "24h".
func every(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}

func parseTemplates(fsys fs.FS) (*template.Template, error) {
	return template.New("").Funcs(funcs).ParseFS(fsys, "templates/*.html")
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.wall)
	mux.HandleFunc("GET /p/{name}", a.projectPage)
	mux.HandleFunc("POST /p/{name}/override", a.override)
	mux.HandleFunc("GET /status", a.statusPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /static/", http.FileServerFS(a.assets()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'self' https://panopticron.com https://*.panopticron.com")
		h.Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (a *app) assets() fs.FS {
	if a.dev {
		return os.DirFS(".")
	}
	return embedded
}

func (a *app) render(w http.ResponseWriter, name string, data any) {
	t := a.tmpl
	if a.dev {
		var err error
		if t, err = parseTemplates(os.DirFS(".")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (a *app) fail(w http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// page is what every template gets: title, refresh, footer bits.
type page struct {
	Title   string
	Refresh int // seconds; 0 = none
	Version string
	Now     time.Time
	Embed   bool // ?embed=1: no header/footer, for framing into the landing page
}

func (a *app) page(title string, refresh int) page {
	return page{Title: title, Refresh: refresh, Version: version, Now: time.Now()}
}

var version = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "dev"
}()

// --- the wall -------------------------------------------------------------

type wallView struct {
	page
	Projects      []wallProject
	Errors, Warns int
}

type wallProject struct {
	Project
	Cluster Cluster
	Checked time.Time // newest observation
}

func (a *app) wall(w http.ResponseWriter, r *http.Request) {
	ps, err := a.store.projects()
	if err != nil {
		a.fail(w, err)
		return
	}
	v := wallView{page: a.page("wall", 60)}
	v.Embed = r.URL.Query().Get("embed") != ""
	for _, p := range ps {
		wp := wallProject{Project: p, Cluster: cluster(p.Lenses)}
		for _, l := range p.Lenses {
			if l.CheckedAt.After(wp.Checked) {
				wp.Checked = l.CheckedAt
			}
			switch l.Status {
			case StatusError:
				v.Errors++
			case StatusWarn:
				v.Warns++
			}
		}
		v.Projects = append(v.Projects, wp)
	}
	a.render(w, "wall.html", v)
}

// --- one project ----------------------------------------------------------

var ranges = []struct {
	Name string
	Back time.Duration
}{{"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}, {"90d", 90 * 24 * time.Hour}, {"all", 0}}

type projectView struct {
	page
	Project
	Lifeline    template.HTML
	Range       string
	Ranges      []string
	Events      []Event
	CanOverride bool
}

func (a *app) projectPage(w http.ResponseWriter, r *http.Request) {
	p, err := a.store.project(r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	rng, since := ranges[1], now.Add(-ranges[1].Back)
	for _, r2 := range ranges {
		if r2.Name == r.URL.Query().Get("range") {
			rng = r2
			since = time.Time{}
			if r2.Back > 0 {
				since = now.Add(-r2.Back)
			}
		}
	}
	line, err := a.store.lifeline(p.ID, since)
	if err != nil {
		a.fail(w, err)
		return
	}
	events, err := a.store.events(p.ID, 50)
	if err != nil {
		a.fail(w, err)
		return
	}
	v := projectView{page: a.page(p.Name, 60), Project: *p, Lifeline: lifelineSVG(line, since, now),
		Range: rng.Name, Events: events, CanOverride: a.adminPass != ""}
	for _, r2 := range ranges {
		v.Ranges = append(v.Ranges, r2.Name)
	}
	a.render(w, "project.html", v)
}

// override is the one write: a human's sort key, behind one password. Basic
// auth needs no login page; the Sec-Fetch-Site check stops a cross-site form
// from riding on cached credentials.
func (a *app) override(w http.ResponseWriter, r *http.Request) {
	if a.adminPass == "" {
		http.Error(w, "overrides disabled: set PANOPTICRON_ADMIN_PASSWORD", http.StatusForbidden)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	if _, pw, ok := r.BasicAuth(); !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(a.adminPass)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="panopticron"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, err := a.store.project(r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	var v *int
	if r.FormValue("clear") == "" {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("override")))
		if err != nil || n < 0 {
			http.Error(w, "override must be a non-negative integer", http.StatusBadRequest)
			return
		}
		v = &n
	}
	if err := a.store.setOverride(p.ID, v, time.Now()); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, "/p/"+url.PathEscape(p.Name), http.StatusSeeOther)
}

// lifelineSVG draws priority over time as a step line: calm (10000) at the
// top, crisis (1) at the bottom. Overrides are drawn hollow, so a human's hand
// is visible on the chart as well as on the wall.
func lifelineSVG(evs []Event, since, now time.Time) template.HTML {
	const w, h = 640, 160
	const padL, padR, padT, padB = 44, 12, 10, 22
	plotW, plotH := float64(w-padL-padR), float64(h-padT-padB)
	if since.IsZero() && len(evs) > 0 {
		since = evs[0].At
	}
	if !since.Before(now) {
		since = now.Add(-time.Hour)
	}
	x := func(t time.Time) float64 {
		if t.Before(since) {
			t = since
		}
		return padL + float64(t.Sub(since))/float64(now.Sub(since))*plotW
	}
	y := func(v int) float64 {
		v = min(max(v, 0), baseScore)
		return padT + (1-float64(v)/baseScore)*plotH
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="lifeline" viewBox="0 0 %d %d" role="img" aria-label="priority over time">`, w, h)
	for _, g := range []int{baseScore, baseScore / 2, 1} {
		fmt.Fprintf(&b, `<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/><text class="tick y" x="%d" y="%.1f">%d</text>`,
			padL, y(g), w-padR, y(g), padL-6, y(g)+3, g)
	}
	fmt.Fprintf(&b, `<text class="tick" x="%d" y="%d">%s</text><text class="tick end" x="%d" y="%d">now</text>`,
		padL, h-6, since.UTC().Format("2006-01-02"), w-padR, h-6)
	if len(evs) == 0 {
		b.WriteString(`<text class="empty" x="50%" y="50%">no history yet</text></svg>`)
		return template.HTML(b.String())
	}
	var d strings.Builder
	fmt.Fprintf(&d, "M%.1f %.1f", x(evs[0].At), y(evs[0].SortKey))
	for _, e := range evs[1:] {
		fmt.Fprintf(&d, " H%.1f V%.1f", x(e.At), y(e.SortKey))
	}
	fmt.Fprintf(&d, " H%.1f", x(now))
	fmt.Fprintf(&b, `<path class="line" d="%s"/>`, d.String())
	for _, e := range evs {
		class := "ev"
		if e.Override != nil {
			class += " ov"
		}
		fmt.Fprintf(&b, `<circle class="%s" cx="%.1f" cy="%.1f" r="3.5"><title>%s · %d · %s</title></circle>`,
			class, x(e.At), y(e.SortKey), e.At.UTC().Format("2006-01-02 15:04"), e.SortKey, template.HTMLEscapeString(e.Note))
	}
	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// --- status ---------------------------------------------------------------

type statusView struct {
	page
	Jobs                          []jobView
	Runs                          []Run
	Projects, Events              int
	Started                       time.Time
	GitHubToken, Pages, Overrides bool
}

type jobView struct {
	Name  string
	Every time.Duration
	Last  *Run
}

func (a *app) statusPage(w http.ResponseWriter, r *http.Request) {
	runs, err := a.store.runs(50)
	if err != nil {
		a.fail(w, err)
		return
	}
	v := statusView{page: a.page("status", 60), Runs: runs,
		Projects: a.store.count("projects"), Events: a.store.count("events"), Started: a.started,
		GitHubToken: a.github.token != "", Pages: a.cf.enabled(), Overrides: a.adminPass != ""}
	for _, j := range a.jobs() {
		jv := jobView{Name: j.name, Every: j.every}
		for i := range runs {
			if runs[i].Job == j.name {
				jv.Last = &runs[i]
				break
			}
		}
		v.Jobs = append(v.Jobs, jv)
	}
	a.render(w, "status.html", v)
}
