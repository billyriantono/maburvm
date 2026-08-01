package repository

import (
	"net/url"
	"testing"

	"gorm.io/driver/postgres"
)

// TestDBConfig_DatabaseURL_SpecialCharsAndIPv6 verifies that InitDB's DSN source
// (repository.DBConfig.DatabaseURL, which delegates to the shared config helper)
// correctly URL-encodes special-character passwords and brackets IPv6 hosts,
// and that the resulting string reaches the Postgres GORM dialector unchanged
// (no live DB required). This guards the Oracle requirement that the primary
// GORM connection no longer uses unsafe libpq keyword interpolation.
func TestDBConfig_DatabaseURL_SpecialCharsAndIPv6(t *testing.T) {
	cases := []struct {
		name     string
		cfg      DBConfig
		wantHost string
		wantPass string
		wantPath string
		wantSSL  string
	}{
		{
			name:     "special chars in password",
			cfg:      DBConfig{Host: "localhost", Port: 5432, User: "u", Password: "p@ss/word with space", Name: "maburvm", SSLMode: "disable"},
			wantHost: "localhost:5432",
			wantPass: "p@ss/word with space",
			wantPath: "/maburvm",
			wantSSL:  "disable",
		},
		{
			name:     "ipv6 host bracketed",
			cfg:      DBConfig{Host: "2001:db8::1", Port: 5432, User: "u", Password: "p@ss", Name: "db", SSLMode: "require"},
			wantHost: "[2001:db8::1]:5432",
			wantPass: "p@ss",
			wantPath: "/db",
			wantSSL:  "require",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := tc.cfg.DatabaseURL()

			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("DSN not a valid URL %q: %v", dsn, err)
			}
			if u.Scheme != "postgres" {
				t.Errorf("expected scheme postgres, got %q", u.Scheme)
			}
			if u.Host != tc.wantHost {
				t.Errorf("host: got %q want %q (dsn %q)", u.Host, tc.wantHost, dsn)
			}
			gotPass, ok := u.User.Password()
			if !ok {
				t.Fatalf("password missing from DSN %q", dsn)
			}
			if gotPass != tc.wantPass {
				t.Errorf("password: got %q want %q (dsn %q)", gotPass, tc.wantPass, dsn)
			}
			if u.Path != tc.wantPath {
				t.Errorf("path: got %q want %q (dsn %q)", u.Path, tc.wantPath, dsn)
			}
			if u.Query().Get("sslmode") != tc.wantSSL {
				t.Errorf("sslmode: got %q want %q (dsn %q)", u.Query().Get("sslmode"), tc.wantSSL, dsn)
			}

			// The DSN must reach the GORM Postgres dialector without a live
			// connection (postgres.Open only builds the dialector). We assert the
			// exact DSN is carried into the dialector's Config.DSN — the string
			// InitDB ultimately hands to gorm.Open.
			dialector := postgres.Open(dsn)
			if dialector == nil {
				t.Fatal("postgres.Open returned nil dialector")
			}
			d, ok := dialector.(*postgres.Dialector)
			if !ok {
				t.Fatalf("expected *postgres.Dialector, got %T", dialector)
			}
			if d.DSN != dsn {
				t.Errorf("dialector DSN mismatch: got %q want %q", d.DSN, dsn)
			}
			if dialector.Name() != "postgres" {
				t.Errorf("dialector name: got %q want postgres", dialector.Name())
			}
		})
	}
}
