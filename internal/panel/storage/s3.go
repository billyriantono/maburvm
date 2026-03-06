// Package storage provides S3-compatible storage backends for backup operations.
// Supports AWS S3, MinIO, Wasabi, and other S3-compatible services.
package storage

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MultipartUploadThreshold is the size threshold for using multipart upload (100MB).
const MultipartUploadThreshold = 100 * 1024 * 1024

// DefaultPartSize is the default size for each part in multipart upload (5MB).
const DefaultPartSize = 5 * 1024 * 1024

// S3Config holds configuration for S3-compatible storage.
type S3Config struct {
	// Endpoint is the S3 service endpoint (e.g., "s3.amazonaws.com" or "localhost:9000" for MinIO).
	Endpoint string `env:"S3_ENDPOINT" envDefault:"s3.amazonaws.com"`

	// Region is the AWS region (e.g., "us-east-1").
	Region string `env:"S3_REGION" envDefault:"us-east-1"`

	// AccessKey is the access key ID.
	AccessKey string `env:"S3_ACCESS_KEY,required"`

	// SecretKey is the secret access key.
	SecretKey string `env:"S3_SECRET_KEY,required"`

	// Bucket is the default bucket name.
	Bucket string `env:"S3_BUCKET,required"`

	// UsePathStyle indicates whether to use path-style addressing (required for MinIO).
	// true:  http://endpoint/bucket/key (path-style)
	// false: http://bucket.endpoint/key (virtual-hosted style)
	UsePathStyle bool `env:"S3_USE_PATH_STYLE" envDefault:"false"`

	// ForceHTTP forces HTTP instead of HTTPS (useful for local MinIO).
	ForceHTTP bool `env:"S3_FORCE_HTTP" envDefault:"false"`

	// PresignedURLExpiration is the expiration time for presigned URLs.
	PresignedURLExpiration time.Duration `env:"S3_PRESIGNED_URL_EXPIRATION" envDefault:"1h"`
}

// Object represents an S3 object in a bucket.
type Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag"`
	StorageClass string    `json:"storage_class"`
}

// UploadProgress represents the progress of an upload operation.
type UploadProgress struct {
	BytesUploaded int64
	TotalBytes    int64
	Percentage    float64
}

// ProgressCallback is called during upload to report progress.
type ProgressCallback func(progress UploadProgress)

// progressReader wraps an io.Reader to track read progress.
type progressReader struct {
	reader     io.Reader
	totalBytes int64
	uploaded   int64
	callback   ProgressCallback
	mu         sync.Mutex
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)

	pr.mu.Lock()
	pr.uploaded += int64(n)
	if pr.callback != nil && pr.totalBytes > 0 {
		pr.callback(UploadProgress{
			BytesUploaded: pr.uploaded,
			TotalBytes:    pr.totalBytes,
			Percentage:    float64(pr.uploaded) / float64(pr.totalBytes) * 100,
		})
	}
	pr.mu.Unlock()

	return n, err
}

// S3Client provides S3-compatible storage operations.
type S3Client struct {
	client   *s3.Client
	config   *S3Config
	s3Config aws.Config
}

// NewS3Client creates a new S3 client with the provided configuration.
func NewS3Client(cfg *S3Config) (*S3Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("s3 config is required")
	}

	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("access key and secret key are required")
	}

	staticProvider := credentials.NewStaticCredentialsProvider(
		cfg.AccessKey,
		cfg.SecretKey,
		"",
	)

	awsCfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(staticProvider),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Configure endpoint options
	endpointResolver := aws.EndpointResolverWithOptionsFunc(
		func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			if cfg.Endpoint == "" || cfg.Endpoint == "s3.amazonaws.com" {
				return aws.Endpoint{}, &aws.EndpointNotFoundError{}
			}

			scheme := "https"
			if cfg.ForceHTTP {
				scheme = "http"
			}

			return aws.Endpoint{
				PartitionID:       "aws",
				URL:               fmt.Sprintf("%s://%s", scheme, cfg.Endpoint),
				SigningRegion:     cfg.Region,
				HostnameImmutable: true,
			}, nil
		},
	)

	awsCfg, err = config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(staticProvider),
		config.WithEndpointResolverWithOptions(endpointResolver),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config with endpoint: %w", err)
	}

	// Create S3 client with path-style addressing option
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &S3Client{
		client:   client,
		config:   cfg,
		s3Config: awsCfg,
	}, nil
}

// Upload uploads data to S3 with optional progress tracking.
// For files larger than MultipartUploadThreshold, it uses multipart upload.
func (s *S3Client) Upload(
	ctx context.Context,
	bucket string,
	key string,
	reader io.Reader,
	contentLength int64,
	callback ProgressCallback,
) error {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return fmt.Errorf("object key is required")
	}
	if reader == nil {
		return fmt.Errorf("reader is required")
	}

	// Use multipart upload for large files
	if contentLength > MultipartUploadThreshold {
		return s.multipartUpload(ctx, bucket, key, reader, contentLength, callback)
	}

	// Wrap reader with progress tracking
	var body io.Reader = reader
	if callback != nil && contentLength > 0 {
		body = &progressReader{
			reader:     reader,
			totalBytes: contentLength,
			callback:   callback,
		}
	}

	// Single-part upload
	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	}

	if contentLength > 0 {
		input.ContentLength = aws.Int64(contentLength)
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	return nil
}

// multipartUpload performs a multipart upload for large files.
func (s *S3Client) multipartUpload(
	ctx context.Context,
	bucket string,
	key string,
	reader io.Reader,
	contentLength int64,
	callback ProgressCallback,
) error {
	// Create multipart upload
	createInput := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	createOutput, err := s.client.CreateMultipartUpload(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create multipart upload: %w", err)
	}

	uploadID := aws.ToString(createOutput.UploadId)
	var completedParts []types.CompletedPart
	var partNumber int32 = 1
	var uploadedBytes int64

	// Calculate part size
	partSize := int64(DefaultPartSize)
	if contentLength > 0 {
		// Calculate optimal part size for the file
		minParts := contentLength / partSize
		if minParts > 10000 { // AWS S3 max parts is 10000
			partSize = (contentLength / 10000) + 1
		}
	}

	// Buffer for reading parts
	buffer := make([]byte, partSize)

	defer func() {
		// Abort multipart upload if not completed successfully
		if len(completedParts) == 0 || err != nil {
			_, _ = s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(bucket),
				Key:      aws.String(key),
				UploadId: aws.String(uploadID),
			})
		}
	}()

	for {
		// Read a part
		n, readErr := io.ReadFull(reader, buffer)
		if n == 0 && readErr == io.EOF {
			break
		}
		if n == 0 {
			break
		}

		// Upload part
		partInput := &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(partNumber),
			Body:       io.NopCloser(NewBytesReader(buffer[:n])),
		}

		partOutput, uploadErr := s.client.UploadPart(ctx, partInput)
		if uploadErr != nil {
			err = uploadErr
			return fmt.Errorf("failed to upload part %d: %w", partNumber, uploadErr)
		}

		completedParts = append(completedParts, types.CompletedPart{
			ETag:       partOutput.ETag,
			PartNumber: aws.Int32(partNumber),
		})

		uploadedBytes += int64(n)
		if callback != nil && contentLength > 0 {
			callback(UploadProgress{
				BytesUploaded: uploadedBytes,
				TotalBytes:    contentLength,
				Percentage:    float64(uploadedBytes) / float64(contentLength) * 100,
			})
		}

		partNumber++

		if readErr == io.EOF {
			break
		}
	}

	// Complete multipart upload
	completeInput := &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	}

	_, err = s.client.CompleteMultipartUpload(ctx, completeInput)
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return nil
}

// BytesReader wraps a byte slice to implement io.Reader, io.Seeker, and io.ReaderAt.
type BytesReader struct {
	data   []byte
	offset int
}

// NewBytesReader creates a new BytesReader from a byte slice.
func NewBytesReader(data []byte) *BytesReader {
	return &BytesReader{data: data}
}

// Read implements io.Reader.
func (r *BytesReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

// ReadAt implements io.ReaderAt.
func (r *BytesReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Seek implements io.Seeker.
func (r *BytesReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = int64(r.offset) + offset
	case io.SeekEnd:
		newOffset = int64(len(r.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if newOffset < 0 {
		return 0, fmt.Errorf("negative offset")
	}

	r.offset = int(newOffset)
	if r.offset > len(r.data) {
		r.offset = len(r.data)
	}

	return int64(r.offset), nil
}

// Download retrieves an object from S3.
func (s *S3Client) Download(ctx context.Context, bucket string, key string) (io.ReadCloser, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return nil, fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return nil, fmt.Errorf("object key is required")
	}

	input := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	output, err := s.client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to download object: %w", err)
	}

	return output.Body, nil
}

// Delete removes an object from S3.
func (s *S3Client) Delete(ctx context.Context, bucket string, key string) error {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return fmt.Errorf("object key is required")
	}

	input := &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	_, err := s.client.DeleteObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// List lists objects in a bucket with an optional prefix.
func (s *S3Client) List(ctx context.Context, bucket string, prefix string) ([]Object, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return nil, fmt.Errorf("bucket name is required")
	}

	var objects []Object
	var continuationToken *string

	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
			MaxKeys:           aws.Int32(1000),
		}

		output, err := s.client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range output.Contents {
			objects = append(objects, Object{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
				StorageClass: string(obj.StorageClass),
			})
		}

		if !aws.ToBool(output.IsTruncated) {
			break
		}
		continuationToken = output.NextContinuationToken
	}

	return objects, nil
}

// GeneratePresignedURL creates a presigned URL for temporary access to an object.
// This is useful for restore operations where you want to give temporary access.
func (s *S3Client) GeneratePresignedURL(
	ctx context.Context,
	bucket string,
	key string,
	expiration time.Duration,
) (string, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return "", fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return "", fmt.Errorf("object key is required")
	}

	if expiration == 0 {
		expiration = s.config.PresignedURLExpiration
	}

	presignClient := s3.NewPresignClient(s.client)

	input := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	presignedReq, err := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(expiration))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedReq.URL, nil
}

// GeneratePresignedUploadURL creates a presigned URL for uploading an object.
func (s *S3Client) GeneratePresignedUploadURL(
	ctx context.Context,
	bucket string,
	key string,
	expiration time.Duration,
	contentType string,
) (string, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return "", fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return "", fmt.Errorf("object key is required")
	}

	if expiration == 0 {
		expiration = s.config.PresignedURLExpiration
	}

	presignClient := s3.NewPresignClient(s.client)

	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	presignedReq, err := presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(expiration))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned upload URL: %w", err)
	}

	return presignedReq.URL, nil
}

// HeadObject retrieves metadata for an object without downloading it.
func (s *S3Client) HeadObject(ctx context.Context, bucket string, key string) (*Object, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return nil, fmt.Errorf("bucket name is required")
	}
	if key == "" {
		return nil, fmt.Errorf("object key is required")
	}

	input := &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	output, err := s.client.HeadObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to head object: %w", err)
	}

	return &Object{
		Key:          key,
		Size:         aws.ToInt64(output.ContentLength),
		LastModified: aws.ToTime(output.LastModified),
		ETag:         aws.ToString(output.ETag),
		StorageClass: string(output.StorageClass),
	}, nil
}

// BucketExists checks if a bucket exists and is accessible.
func (s *S3Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return false, fmt.Errorf("bucket name is required")
	}

	input := &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	}

	_, err := s.client.HeadBucket(ctx, input)
	if err != nil {
		// Check if it's a "not found" error
		var notFound *types.NotFound
		if ok := fmt.Sprintf("%T", err) == "*smithy.OperationError"; ok {
			return false, nil
		}
		if notFound != nil {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bucket: %w", err)
	}

	return true, nil
}

// CreateBucket creates a new bucket if it doesn't exist.
func (s *S3Client) CreateBucket(ctx context.Context, bucket string) error {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return fmt.Errorf("bucket name is required")
	}

	// Check if bucket exists
	exists, err := s.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	input := &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}

	// For regions other than us-east-1, we need to specify LocationConstraint
	if s.config.Region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(s.config.Region),
		}
	}

	_, err = s.client.CreateBucket(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	return nil
}

// DeleteBucket deletes an empty bucket.
func (s *S3Client) DeleteBucket(ctx context.Context, bucket string) error {
	if bucket == "" {
		bucket = s.config.Bucket
	}
	if bucket == "" {
		return fmt.Errorf("bucket name is required")
	}

	input := &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	}

	_, err := s.client.DeleteBucket(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	return nil
}

// GetClient returns the underlying S3 client for advanced operations.
func (s *S3Client) GetClient() *s3.Client {
	return s.client
}

// GetConfig returns the S3 configuration.
func (s *S3Client) GetConfig() *S3Config {
	return s.config
}
