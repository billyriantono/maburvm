package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Destination is an S3-compatible store as an operator describes it: where the
// panel and its nodes keep backups and images.
type Destination struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// PlainHTTP reaches an endpoint given without a scheme over HTTP.
	PlainHTTP bool
}

// ErrDestinationIncomplete is returned for a destination missing its bucket or
// credentials.
var ErrDestinationIncomplete = errors.New("the backup destination needs a bucket, an access key and a secret key")

// Client builds a client for the destination.
func (d Destination) Client() (*Client, error) {
	if d.Bucket == "" || d.AccessKey == "" || d.SecretKey == "" {
		return nil, ErrDestinationIncomplete
	}
	return NewClient(&Config{
		Endpoint:  d.Endpoint,
		Region:    d.Region,
		Bucket:    d.Bucket,
		AccessKey: d.AccessKey,
		SecretKey: d.SecretKey,
		ForceHTTP: d.PlainHTTP,
		// Path-style works on every S3-compatible store; the other style needs
		// a DNS name per bucket, which most of them do not have.
		UsePathStyle: true,
	})
}

// Steps of a Probe, in the order they run.
const (
	ProbeWrite  = "write"
	ProbeRead   = "read"
	ProbeDelete = "delete"
)

// ProbeError is a failed Probe and the step it failed at.
type ProbeError struct {
	Step string
	Err  error
}

func (e *ProbeError) Error() string {
	return fmt.Sprintf("could not %s a test object: %v", e.Step, e.Err)
}
func (e *ProbeError) Unwrap() error { return e.Err }

// Probe proves the store can be used with these credentials: it writes a small
// object at key, reads it back and deletes it.
func (c *Client) Probe(ctx context.Context, key string) error {
	body := []byte("backup destination test " + time.Now().UTC().Format(time.RFC3339Nano))
	if err := c.Upload(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		return &ProbeError{Step: ProbeWrite, Err: err}
	}
	rc, err := c.Download(ctx, key)
	if err == nil {
		var got []byte
		got, err = io.ReadAll(io.LimitReader(rc, int64(len(body))+1))
		rc.Close()
		if err == nil && !bytes.Equal(got, body) {
			err = errors.New("the object read back is not the one written")
		}
	}
	// Deleted even when the read failed, so a failed test leaves nothing behind.
	delErr := c.Delete(ctx, key)
	if err != nil {
		return &ProbeError{Step: ProbeRead, Err: err}
	}
	if delErr != nil {
		return &ProbeError{Step: ProbeDelete, Err: delErr}
	}
	return nil
}

// Rejected reports that the store answered and refused the request (wrong
// credentials, no such bucket, access denied), as opposed to not being reached
// at all. A refusal does not change on its own, so it is not worth retrying.
func Rejected(err error) bool {
	var api interface{ ErrorCode() string }
	return errors.As(err, &api)
}

// JoinKey puts an object key under a path prefix. Slashes around the prefix are
// dropped, so "vps/", "/vps" and "vps" name the same place.
func JoinKey(prefix, key string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}
