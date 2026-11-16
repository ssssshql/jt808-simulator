package main

import "testing"

func TestNewerThan(t *testing.T) {
	cases := []struct {
		l, c string
		want bool
	}{
		{"v1.1.2", "v1.1.1", true}, {"v1.2.0", "v1.1.9", true}, {"v2.0.0", "v1.9.9", true},
		{"v1.1.1", "v1.1.1", false}, {"v1.1.0", "v1.1.1", false},
		{"v1.1.2", "dev", false}, {"v1.1.2", "v1.1.1-3-gabc-dirty", true}, {"garbage", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := newerThan(c.l, c.c); got != c.want {
			t.Errorf("newerThan(%q,%q)=%v want %v", c.l, c.c, got, c.want)
		}
	}
}
