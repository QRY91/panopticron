package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "projects.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(writeTOML(t, `
[[project]]
name = "qry.zone"
url  = "https://www.qry.zone/"
repo = "QRY91/qryzone"

[[project]]
name = "tool"
repo = "QRY91/uroboro"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 || cfg.Projects[0].Domain != "qry.zone" || cfg.Projects[1].Domain != "" {
		t.Fatalf("%+v", cfg.Projects)
	}
	for name, body := range map[string]string{
		"unknown key": "[[project]]\nname = \"x\"\npage = \"typo\"\n",
		"no name":     "[[project]]\nurl = \"https://x\"\n",
		"duplicate":   "[[project]]\nname = \"x\"\n[[project]]\nname = \"x\"\n",
		"bad repo":    "[[project]]\nname = \"x\"\nrepo = \"just-a-name\"\n",
	} {
		if _, err := loadConfig(writeTOML(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := loadConfig("/nonexistent/projects.toml"); err == nil || !strings.Contains(err.Error(), "projects.toml") {
		t.Errorf("missing file: %v", err)
	}
}

func TestRegistrableDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://qry.zone":           "qry.zone",
		"https://www.chasmlogic.com": "chasmlogic.com",
		"https://a.b.c.example.net/": "example.net",
		"http://localhost:8080":      "localhost",
		"":                           "",
		"::nope":                     "",
	} {
		if got := registrableDomain(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
