package service

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

func TestRenderEmailEscapesAndFills(t *testing.T) {
	def, _ := EmailTemplateDefFor(EmailWelcome)
	brand := Branding{Name: "Acme <b>Cloud</b>", Accent: "#ffd500", SupportEmail: "help@acme.test"}
	got := RenderEmail(def.Default, brand, EmailData{
		Name: "Dewi\r\nBcc: evil@example.com", Email: "dewi@example.com", Link: "https://panel.acme.test/login?a=1&b=2",
	})

	if strings.ContainsAny(got.Subject, "\r\n") {
		t.Fatalf("subject carries a line break: %q", got.Subject)
	}
	if strings.Contains(got.HTML, "<b>Cloud</b>") {
		t.Fatal("brand name was not escaped in the HTML")
	}
	for _, want := range []string{"Acme &lt;b&gt;Cloud&lt;/b&gt;", "Username: dewi@example.com", "a=1&amp;b=2", "color:#0a0a0a", "help@acme.test"} {
		if !strings.Contains(got.HTML, want) {
			t.Errorf("HTML is missing %q", want)
		}
	}
	if !strings.Contains(got.Text, "Sign in: https://panel.acme.test/login?a=1&b=2") {
		t.Errorf("plain text is missing the link:\n%s", got.Text)
	}
	if strings.Contains(got.Text+got.HTML, "{{") {
		t.Error("a placeholder was left unfilled")
	}
}

func TestEmailTemplateNormalize(t *testing.T) {
	ok := EmailTemplate{Subject: " Hi ", Body: "Body\r\nline", Button: "Go"}
	if err := ok.Normalize(); err != nil || ok.Subject != "Hi" || ok.Body != "Body\nline" {
		t.Fatalf("%v %+v", err, ok)
	}
	for name, bad := range map[string]EmailTemplate{
		"no subject":       {Body: "x"},
		"no body":          {Subject: "x"},
		"two-line subject": {Subject: "a\nb", Body: "x"},
		"long button":      {Subject: "a", Body: "x", Button: strings.Repeat("b", 41)},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A minimal SMTP server that accepts one message and hands back what it got.
func fakeSMTP(t *testing.T) (host string, port int, data <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { c.Write([]byte(s + "\r\n")) }
		say("220 test")
		var body strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case inData && line == ".\r\n":
				inData = false
				out <- body.String()
				say("250 ok")
			case inData:
				body.WriteString(line)
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				say("250 test")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				say("354 go")
			case strings.HasPrefix(line, "QUIT"):
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port, out
}

func TestSendMailIsMultipartWithBothParts(t *testing.T) {
	host, port, data := fakeSMTP(t)
	cfg := SMTPSettings{Host: host, Port: port, From: "noreply@acme.test", FromName: "Acme\r\nX-Evil: 1", Brand: Branding{Name: "Acmé"}}
	if err := SendPasswordResetEmail(cfg, "dewi@example.com", "Dewi", "https://panel.acme.test/reset?token=t"); err != nil {
		t.Fatal(err)
	}
	msg := <-data
	head, _, _ := strings.Cut(msg, "\r\n\r\n")
	if strings.Contains(head, "\r\nX-Evil:") {
		t.Fatal("a line break in the sender name became a header")
	}
	for _, want := range []string{"multipart/alternative", "text/plain; charset=UTF-8", "text/html; charset=UTF-8", "Subject: =?utf-8?q?Reset_your_Acm=C3=A9_password?="} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q", want)
		}
	}
	// The HTML part decodes back to markup carrying the reset link.
	parts := strings.Split(msg, "Content-Transfer-Encoding: base64\r\n\r\n")
	htmlB64, _, _ := strings.Cut(parts[2], "\r\n--")
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(htmlB64, "\r\n", ""))
	if err != nil || !strings.Contains(string(raw), "https://panel.acme.test/reset?token=t") {
		t.Fatalf("HTML part did not decode to the message: %v", err)
	}
}
