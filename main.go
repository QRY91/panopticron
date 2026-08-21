// panopticron — one wall for the owner's web real estate.
//
// Every few minutes it probes each project in projects.toml (does the name
// resolve, does the page answer, is the certificate healthy); once a day it asks
// RDAP when the domain expires; it watches GitHub Actions and, with a token,
// Cloudflare Pages. Each observation is a lens; a project is a cluster of
// lenses; one integer — the priority score, overridable by a human — sorts the
// wall. State and history live in one SQLite file. No JavaScript, no hosted
// database, one binary.
//
// usage:
//
//	panopticron                 serve :8080 from projects.toml + panopticron.db
//	panopticron -once           run every poller once and exit (smoke test, cron)
//	panopticron -dev            also reload templates and css from disk per request
//
// env:
//
//	GITHUB_TOKEN                 optional: 5000 req/h instead of 60, private repos
//	CLOUDFLARE_API_TOKEN         optional, with CLOUDFLARE_ACCOUNT_ID: enables the pages lens
//	PANOPTICRON_ADMIN_PASSWORD   optional: enables manual overrides (HTTP Basic auth)
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "projects.toml", "what to watch")
	dbPath := flag.String("db", "panopticron.db", "SQLite file for state and history")
	listen := flag.String("listen", ":8080", "address to serve the wall on")
	once := flag.Bool("once", false, "run every poller once and exit")
	dev := flag.Bool("dev", false, "reload templates and css from disk on every request")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.syncProjects(cfg.Projects); err != nil {
		log.Fatal(err)
	}
	if err := store.pruneRuns(500); err != nil {
		log.Fatal(err)
	}

	a := newApp(store, *dev)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		for _, j := range a.jobs() {
			a.runJob(ctx, j)
		}
		return
	}
	for _, j := range a.jobs() {
		go a.loop(ctx, j)
	}

	srv := &http.Server{Addr: *listen, Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("panopticron: %d projects, %d pollers, serving on %s", len(cfg.Projects), len(a.jobs()), *listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
