//go:build pg

package queue

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// dsnForDatabase is MABURVM_TEST_PG_DSN pointed at another database, whichever
// database it names and in either form pgx takes (a URL, or key=value pairs).
func dsnForDatabase(t *testing.T, base, name string) string {
	t.Helper()
	if u, err := url.Parse(base); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + name
		return u.String()
	}
	if dbname.MatchString(base) {
		return dbname.ReplaceAllString(base, "${1}dbname="+name)
	}
	return strings.TrimSpace(base) + " dbname=" + name
}

var dbname = regexp.MustCompile(`(^|\s)dbname=\S+`)

func TestDSNForDatabase(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"postgres://u@127.0.0.1:5432/postgres?sslmode=disable", "postgres://u@127.0.0.1:5432/x?sslmode=disable"},
		{"postgres://u@127.0.0.1:5432/maburvm_ci?sslmode=disable", "postgres://u@127.0.0.1:5432/x?sslmode=disable"},
		{"postgresql://u@db/other", "postgresql://u@db/x"},
		{"host=/tmp user=u dbname=other sslmode=disable", "host=/tmp user=u dbname=x sslmode=disable"},
		{"host=/tmp user=u", "host=/tmp user=u dbname=x"},
	} {
		if got := dsnForDatabase(t, c.in, "x"); got != c.want {
			t.Errorf("dsnForDatabase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
