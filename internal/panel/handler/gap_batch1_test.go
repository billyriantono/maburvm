package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/safefetch"
)

func ctxWithRole(role models.UserRole) echo.Context {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.New(), Role: role})
	return c
}

func TestCheckISOSource(t *testing.T) {
	names := map[string]string{"example.com": "93.184.216.34", "intranet.example.com": "10.0.0.5", "localhost": "127.0.0.1"}
	defer func(old safefetch.Lookup) { isoLookup = old }(isoLookup)
	isoLookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if a, ok := names[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, errors.New("no such host")
	}

	client, admin := ctxWithRole(models.RoleClient), ctxWithRole(models.RoleAdmin)
	nobody := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	for ref, wantOK := range map[string]bool{
		"https://example.com/debian.iso":          true,
		"http://93.184.216.34/x.iso":              true,
		"/var/lib/libvirt/images/a.qcow2":         false,
		"vm://1234":                               false,
		"s3://bucket/key":                         false,
		"file:///etc/shadow":                      false,
		"http://127.0.0.1/x.iso":                  false,
		"http://10.0.0.5/x.iso":                   false,
		"http://100.64.0.1/x.iso":                 false,
		"http://169.254.169.254/latest":           false,
		"http://localhost/x.iso":                  false,
		"http://[::1]/x.iso":                      false,
		"https://intranet.example.com/x.iso":      false, // a name for an internal address
		"https://does-not-resolve.example/x.iso":  false,
		"customer:https://example.com/debian.iso": false,
	} {
		for who, c := range map[string]echo.Context{"client": client, "no user": nobody} {
			operator, err := checkISOSource(c, ref)
			if (err == nil) != wantOK || operator {
				t.Errorf("%s %q: ok=%v operator=%v, want ok=%v and never the operator", who, ref, err == nil, operator, wantOK)
			}
		}
	}
	// Admins keep on-node paths and private mirrors, and theirs is marked as the operator's.
	for _, ref := range []string{"/var/lib/libvirt/templates/rescue.iso", "http://10.0.0.5/mirror/x.iso"} {
		if operator, err := checkISOSource(admin, ref); err != nil || !operator {
			t.Errorf("admin %q: operator=%v err=%v", ref, operator, err)
		}
	}
}

func TestReservedHostPort(t *testing.T) {
	for p, want := range map[int]bool{22: true, 80: true, 1023: true, 50051: true, 5901: true, 16509: true, 49152: true, 1024: false, 8080: false, 25565: false} {
		if got := reservedHostPort(p); got != want {
			t.Errorf("port %d: reserved=%v, want %v", p, got, want)
		}
	}
}
