package main

import "testing"

func kinds(ls []Lens) []string {
	var ks []string
	for _, l := range ls {
		ks = append(ks, l.Kind)
	}
	return ks
}

func TestClusterPromotesProblems(t *testing.T) {
	c := cluster([]Lens{
		{Kind: KindCI, Status: StatusGood},
		{Kind: KindHTTP, Status: StatusGood},
		{Kind: KindTLS, Status: StatusWarn},
		{Kind: KindDomain, Status: StatusError},
		{Kind: KindDNS, Status: StatusGood},
		{Kind: KindPages, Status: StatusNeutral},
	})
	// error first, then warn, then the first good by kind order
	if got, want := kinds(c.Big[:]), []string{KindDomain, KindTLS, KindHTTP}; !equal(got, want) {
		t.Errorf("big = %v, want %v", got, want)
	}
	// remaining goods by kind order, neutral last, padded with a placeholder
	if got, want := kinds(c.Small[:]), []string{KindDNS, KindCI, KindPages, ""}; !equal(got, want) {
		t.Errorf("small = %v, want %v", got, want)
	}
	if c.Small[3].Status != StatusNeutral {
		t.Errorf("placeholder should be neutral, got %q", c.Small[3].Status)
	}
}

func TestClusterEmptyAndOverflow(t *testing.T) {
	c := cluster(nil)
	for _, l := range append(c.Big[:], c.Small[:]...) {
		if l.Kind != "" || l.Status != StatusNeutral {
			t.Fatalf("empty cluster should be all placeholders, got %+v", l)
		}
	}
	var many []Lens
	for i := 0; i < 9; i++ {
		many = append(many, Lens{Kind: KindHTTP, Status: StatusGood, Value: string(rune('a' + i))})
	}
	c = cluster(many)
	if c.Big[0].Value != "a" || c.Small[3].Value != "g" {
		t.Errorf("overflow: the first seven should show, got big0=%q small3=%q", c.Big[0].Value, c.Small[3].Value)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
