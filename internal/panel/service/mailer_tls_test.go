package service

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainSMTP is a mail server that offers no TLS. It reports whether a message
// reached it.
func plainSMTP(t *testing.T) (port int, delivered *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	delivered = &atomic.Bool{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { conn.Write([]byte(s + "\r\n")) }
				say("220 test ESMTP")
				inData := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimSpace(line)
					switch {
					case inData:
						if line == "." {
							inData = false
							delivered.Store(true)
							say("250 queued")
						}
					case strings.HasPrefix(line, "EHLO"):
						say("250-test\r\n250 8BITMIME")
					case line == "DATA":
						inData = true
						say("354 go on")
					case line == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, delivered
}

func TestSMTPRequireTLS(t *testing.T) {
	msg := RenderedEmail{Subject: "s", Text: "t", HTML: "<p>t</p>"}
	cfg := func(port int, requireTLS bool) SMTPSettings {
		return SMTPSettings{Host: "127.0.0.1", Port: port, From: "panel@example.com", RequireTLS: requireTLS}
	}

	port, delivered := plainSMTP(t)
	err := SendMail(cfg(port, true), "to@example.com", msg)
	require.Error(t, err, "TLS required and the server has none: nothing may be sent")
	assert.Contains(t, err.Error(), "does not offer TLS on port "+strconv.Itoa(port))
	assert.False(t, delivered.Load(), "the message must not have gone out in the clear")

	require.NoError(t, SendMail(cfg(port, false), "to@example.com", msg), "TLS not required: sent as before")
	assert.True(t, delivered.Load())
}

func TestLoadSMTPSettingsReadsTLSSwitch(t *testing.T) {
	db := setupStorageTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	for data, want := range map[string]bool{
		`{"smtpHost":"mail.example.com","smtpPort":587,"enableTls":true}`:  true,
		`{"smtpHost":"mail.example.com","smtpPort":587,"enableTls":false}`: false,
		// Saved before the switch did anything, or by something other than the form.
		`{"smtpHost":"mail.example.com","smtpPort":587}`: false,
	} {
		require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('email', ?)`, data).Error)
		cfg, ok, err := LoadSMTPSettings(db)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, want, cfg.RequireTLS, data)
	}
}
