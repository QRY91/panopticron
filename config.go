package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is projects.toml: the list of things panopticron watches. Membership
// lives here, versioned; state and history live in the SQLite file.
type Config struct {
	Projects []ProjectConfig `toml:"project"`
}

type ProjectConfig struct {
	Name   string `toml:"name"`   // unique, shown on the wall; usually the domain
	URL    string `toml:"url"`    // probed for http/dns/tls; empty = no probe
	Domain string `toml:"domain"` // registrable domain for RDAP; default derived from url
	Repo   string `toml:"repo"`   // GitHub "owner/name"; empty = no ci lens
	Pages  string `toml:"pages"`  // Cloudflare Pages project name; empty = no pages lens
}

func loadConfig(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("%s: unknown key %q", path, keys[0].String())
	}
	seen := map[string]bool{}
	for i := range c.Projects {
		p := &c.Projects[i]
		switch {
		case p.Name == "":
			return nil, fmt.Errorf("%s: project #%d has no name", path, i+1)
		case seen[p.Name]:
			return nil, fmt.Errorf("%s: duplicate project %q", path, p.Name)
		case p.Repo != "" && strings.Count(p.Repo, "/") != 1:
			return nil, fmt.Errorf("%s: %s: repo must be owner/name, got %q", path, p.Name, p.Repo)
		}
		seen[p.Name] = true
		if p.Domain == "" {
			p.Domain = registrableDomain(p.URL)
		}
	}
	return &c, nil
}

// registrableDomain guesses the registered name behind a URL: the last two
// labels of the host. Right for .com/.net/.zone; set `domain` in projects.toml
// for anything fancier (co.uk, …).
func registrableDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) > 2 {
		labels = labels[len(labels)-2:]
	}
	return strings.Join(labels, ".")
}
