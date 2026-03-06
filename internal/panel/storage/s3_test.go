package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test BytesReader implementation
func TestBytesReader_Read(t *testing.T) {
	data := []byte("hello world")
	reader := NewBytesReader(data)

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf))

	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, " worl", string(buf))

	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, "d", string(buf[:n]))

	_, err = reader.Read(buf)
	assert.Equal(t, io.EOF, err)
}

func TestBytesReader_ReadAt(t *testing.T) {
	data := []byte("hello world")
	reader := NewBytesReader(data)

	// Read from offset
	buf := make([]byte, 5)
	n, err := reader.ReadAt(buf, 6)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "world", string(buf))

	// Read at beginning
	n, err = reader.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf))

	// Read with partial data
	buf = make([]byte, 10)
	n, err = reader.ReadAt(buf, 5)
	require.Equal(t, io.EOF, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, " world", string(buf[:n]))

	// Read beyond end
	_, err = reader.ReadAt(buf, 20)
	assert.Equal(t, io.EOF, err)

	// Negative offset
	_, err = reader.ReadAt(buf, -1)
	assert.Error(t, err)
}

func TestBytesReader_Seek(t *testing.T) {
	data := []byte("hello world")
	reader := NewBytesReader(data)

	// Seek from start
	offset, err := reader.Seek(6, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(6), offset)

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "world", string(buf[:n]))

	// Seek from current
	reader = NewBytesReader(data)
	_, err = reader.Read(make([]byte, 3))
	require.NoError(t, err)

	offset, err = reader.Seek(2, io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, int64(5), offset)

	buf = make([]byte, 5)
	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, " worl", string(buf[:n]))

	// Seek from end
	offset, err = reader.Seek(-5, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(6), offset)

	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "world", string(buf[:n]))

	// Negative result
	_, err = reader.Seek(-100, io.SeekStart)
	assert.Error(t, err)
}

// Test S3Config validation
func TestS3Config_Validation(t *testing.T) {
	// Valid config
	cfg := &S3Config{
		Endpoint:               "http://localhost:9000",
		Region:                 "us-east-1",
		AccessKey:              "test-access-key",
		SecretKey:              "test-secret-key",
		Bucket:                 "test-bucket",
		UsePathStyle:           true,
		ForceHTTP:              true,
		PresignedURLExpiration: time.Hour,
	}
	assert.NotNil(t, cfg)
	assert.Equal(t, "http://localhost:9000", cfg.Endpoint)
	assert.Equal(t, "us-east-1", cfg.Region)
	assert.True(t, cfg.UsePathStyle)
	assert.True(t, cfg.ForceHTTP)
}

// Test Object struct
func TestObject(t *testing.T) {
	now := time.Now()
	obj := Object{
		Key:          "backup/test.tar.gz",
		Size:         1024 * 1024,
		LastModified: now,
		ETag:         `"abc123"`,
		StorageClass: "STANDARD",
	}

	assert.Equal(t, "backup/test.tar.gz", obj.Key)
	assert.Equal(t, int64(1024*1024), obj.Size)
	assert.Equal(t, now, obj.LastModified)
	assert.Equal(t, `"abc123"`, obj.ETag)
	assert.Equal(t, "STANDARD", obj.StorageClass)
}

// Test progressReader
func TestProgressReader(t *testing.T) {
	data := []byte("hello world this is a test string")
	reader := strings.NewReader(string(data))

	var lastProgress UploadProgress
	callback := func(p UploadProgress) {
		lastProgress = p
	}

	pr := &progressReader{
		reader:     reader,
		totalBytes: int64(len(data)),
		callback:   callback,
	}

	// Read some data
	buf := make([]byte, 10)
	n, err := pr.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, int64(10), lastProgress.BytesUploaded)
	assert.Equal(t, int64(len(data)), lastProgress.TotalBytes)
	assert.True(t, lastProgress.Percentage > 0)

	// Read remaining
	_, _ = io.ReadAll(pr)
	assert.Equal(t, int64(len(data)), lastProgress.BytesUploaded)
	assert.Equal(t, float64(100), lastProgress.Percentage)
}

// Test NewS3Client validation
func TestNewS3Client_Validation(t *testing.T) {
	// Nil config
	client, err := NewS3Client(nil)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "config is required")

	// Missing access key
	cfg := &S3Config{
		SecretKey: "secret",
	}
	client, err = NewS3Client(cfg)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "access key and secret key are required")

	// Missing secret key
	cfg = &S3Config{
		AccessKey: "access",
	}
	client, err = NewS3Client(cfg)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "access key and secret key are required")
}

// Integration tests would require a real S3/MinIO instance
// These are marked to be skipped in normal test runs

func TestS3Client_Upload_SmallFile(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	data := []byte("test data content")
	reader := bytes.NewReader(data)

	var progressCalled bool
	callback := func(p UploadProgress) {
		progressCalled = true
		t.Logf("Progress: %d/%d (%.2f%%)", p.BytesUploaded, p.TotalBytes, p.Percentage)
	}

	err = client.Upload(ctx, "", "test/small-file.txt", reader, int64(len(data)), callback)
	require.NoError(t, err)
	assert.True(t, progressCalled)
}

func TestS3Client_Upload_LargeFile_Multipart(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	// Create data larger than multipart threshold
	data := make([]byte, MultipartUploadThreshold+1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	reader := bytes.NewReader(data)

	var lastProgress UploadProgress
	callback := func(p UploadProgress) {
		lastProgress = p
		t.Logf("Progress: %d/%d (%.2f%%)", p.BytesUploaded, p.TotalBytes, p.Percentage)
	}

	err = client.Upload(ctx, "", "test/large-file.bin", reader, int64(len(data)), callback)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), lastProgress.BytesUploaded)
	assert.Equal(t, float64(100), lastProgress.Percentage)
}

func TestS3Client_Download(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	reader, err := client.Download(ctx, "", "test/small-file.txt")
	require.NoError(t, err)
	defer reader.Close()

	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "test data content", string(content))
}

func TestS3Client_Delete(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	err = client.Delete(ctx, "", "test/delete-me.txt")
	require.NoError(t, err)
}

func TestS3Client_List(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	objects, err := client.List(ctx, "", "test/")
	require.NoError(t, err)
	assert.NotNil(t, objects)

	for _, obj := range objects {
		t.Logf("Object: %s, Size: %d, Modified: %s", obj.Key, obj.Size, obj.LastModified)
	}
}

func TestS3Client_GeneratePresignedURL(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	url, err := client.GeneratePresignedURL(ctx, "", "test/small-file.txt", time.Hour)
	require.NoError(t, err)
	assert.NotEmpty(t, url)
	t.Logf("Presigned URL: %s", url)

	// Verify URL is accessible
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestS3Client_GeneratePresignedUploadURL(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	url, err := client.GeneratePresignedUploadURL(ctx, "", "test/presigned-upload.txt", time.Hour, "text/plain")
	require.NoError(t, err)
	assert.NotEmpty(t, url)
	t.Logf("Presigned Upload URL: %s", url)

	// Upload using presigned URL
	data := []byte("uploaded via presigned URL")
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestS3Client_BucketOperations(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket-new",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Check if bucket exists
	exists, err := client.BucketExists(ctx, "")
	require.NoError(t, err)
	t.Logf("Bucket exists: %v", exists)

	// Create bucket if not exists
	err = client.CreateBucket(ctx, "")
	require.NoError(t, err)

	// Verify bucket exists
	exists, err = client.BucketExists(ctx, "")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestS3Client_HeadObject(t *testing.T) {
	// Skip in CI/normal runs - requires S3/MinIO
	t.Skip("Integration test: requires S3/MinIO server")

	cfg := &S3Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		Bucket:       "test-bucket",
		UsePathStyle: true,
		ForceHTTP:    true,
	}

	client, err := NewS3Client(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	obj, err := client.HeadObject(ctx, "", "test/small-file.txt")
	require.NoError(t, err)
	assert.NotNil(t, obj)
	t.Logf("Object: %s, Size: %d, ETag: %s", obj.Key, obj.Size, obj.ETag)
}

// Benchmarks

func BenchmarkBytesReader_Read(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	buf := make([]byte, 4096)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reader := NewBytesReader(data)
		for {
			_, err := reader.Read(buf)
			if err == io.EOF {
				break
			}
		}
	}
}

func BenchmarkProgressReader(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	buf := make([]byte, 4096)
	callback := func(p UploadProgress) {}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reader := bytes.NewReader(data)
		pr := &progressReader{
			reader:     reader,
			totalBytes: int64(len(data)),
			callback:   callback,
		}
		for {
			_, err := pr.Read(buf)
			if err == io.EOF {
				break
			}
		}
	}
}
