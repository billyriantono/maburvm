package middleware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

// ErrorEnvelope gives every JSON error answer one shape, whichever handler
// wrote it:
//
//	{"success": false, "error": {"code": "...", "message": "...", "details": ...}, "message": "...", ...}
//
// The top-level message, and any other top-level field the handler wrote
// (code, request_id, ...), stays where clients already read it. Only a
// string "error" is replaced by the object. Success answers pass through
// untouched and unbuffered; JSON-RPC answers (the MCP endpoint) keep their
// own error shape. It also accepts per_page wherever page_size is read.
//
// Register it first, so it also shapes errors from the middleware after it
// and from handlers that return an error instead of writing one.
func ErrorEnvelope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			aliasPerPage(c.Request())
			res := c.Response()
			w := &envelopeWriter{ResponseWriter: res.Writer}
			res.Writer = w
			defer func() { res.Writer = w.ResponseWriter }()
			if err := next(c); err != nil {
				c.Error(err)
			}
			w.finish()
			return nil
		}
	}
}

// aliasPerPage makes per_page a name for page_size, starting at page 1.
func aliasPerPage(r *http.Request) {
	q := r.URL.Query()
	per := q.Get("per_page")
	if per == "" || q.Get("page_size") != "" {
		return
	}
	q.Set("page_size", per)
	if q.Get("page") == "" {
		q.Set("page", "1")
	}
	r.URL.RawQuery = q.Encode()
}

// envelopeWriter holds back an error answer until the handler is done, so its
// body can be reshaped; anything else goes straight through.
type envelopeWriter struct {
	http.ResponseWriter
	status int
	held   *bytes.Buffer
}

func (w *envelopeWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusBadRequest {
		w.status, w.held = code, &bytes.Buffer{}
		return
	}
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *envelopeWriter) Write(b []byte) (int, error) {
	if w.held != nil {
		return w.held.Write(b)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// finish sends a held error answer, reshaped when it is a JSON object.
func (w *envelopeWriter) finish() {
	if w.held == nil {
		return
	}
	body := w.held.Bytes()
	w.held = nil
	h := w.Header()
	if strings.HasPrefix(h.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		body = NormalizeErrorBody(w.status, body)
		h.Del(echo.HeaderContentLength)
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(body)
}

func (w *envelopeWriter) Flush() {
	w.finish()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *envelopeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("the response cannot be hijacked")
}

func (w *envelopeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// machineCode is an "error" value that is already a code, not a sentence.
var machineCode = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// ErrorCodeForStatus is the code an error answer carries when its handler
// named none.
func ErrorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusPaymentRequired:
		return "payment_required"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusLocked:
		return "locked"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusNotImplemented:
		return "not_implemented"
	case http.StatusBadGateway:
		return "bad_gateway"
	case http.StatusServiceUnavailable:
		return "unavailable"
	case http.StatusGatewayTimeout:
		return "timeout"
	}
	if status >= 500 {
		return "internal_error"
	}
	return "error"
}

// NormalizeErrorBody reshapes one JSON error body into the envelope described
// on ErrorEnvelope. A body that is not a JSON object, or is a JSON-RPC answer,
// is returned as it is.
func NormalizeErrorBody(status int, body []byte) []byte {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	if _, rpc := m["jsonrpc"]; rpc {
		return body
	}
	code, _ := m["code"].(string)
	msg, _ := m["message"].(string)
	details, hasDetails := m["details"]
	switch e := m["error"].(type) {
	case string:
		if msg == "" {
			msg = e
		}
		if code == "" && machineCode.MatchString(e) {
			code = e
		}
	case map[string]any:
		if s, _ := e["message"].(string); msg == "" {
			msg = s
		}
		if s, _ := e["code"].(string); code == "" {
			code = s
		}
		if d, ok := e["details"]; ok && !hasDetails {
			details, hasDetails = d, true
		}
	}
	if code == "" {
		code = ErrorCodeForStatus(status)
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	errObj := map[string]any{"code": code, "message": msg}
	if hasDetails && details != nil {
		errObj["details"] = details
	}
	m["success"], m["error"], m["message"] = false, errObj, msg
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return append(out, '\n')
}
