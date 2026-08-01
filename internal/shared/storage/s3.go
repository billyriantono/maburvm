package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Provider represents a storage provider type
type Provider string

const (
	ProviderS3    Provider = "s3"
	ProviderMinIO Provider = "minio"
	ProviderLocal Provider = "local"
)

// Client provides S3-compatible storage operations
type Client struct {
	provider     Provider
	s3Client     *s3.Client
	bucket       string
	endpoint     string
	region       string
	forceHTTP    bool
	usePathStyle bool
}

// Config holds storage client configuration.
//
// Endpoint normalization and path-style addressing are resolved inside
// NewClient so callers (agent/queue env constructors) pass raw endpoint and
// explicit flag values. The historical production behavior used path-style
// addressing for MinIO; constructors in this repo default UsePathStyle=true
// when the flag is omitted so existing MinIO callers keep working.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	Provider  Provider
	// UseSSL is retained for backward compatibility but no longer drives
	// endpoint scheme resolution; ForceHTTP is the explicit control.
	UseSSL bool
	// ForceHTTP forces the http:// scheme for a scheme-less endpoint
	// (e.g. an in-cluster MinIO reachable only over plain HTTP). When the
	// endpoint already carries an explicit scheme, ForceHTTP is ignored.
	ForceHTTP bool
	// UsePathStyle enables path-style addressing (required for MinIO and most
	// S3-compatible stores). Honored as-is by NewClient.
	UsePathStyle bool
}

// normalizeEndpoint resolves a raw endpoint string into a fully-qualified URL.
//
// Resolution rules (no network access):
//   - If the endpoint is empty it returns an empty string (the AWS SDK uses its
//     default resolution for the provider's public service). This is the only
//     "accept as default" case.
//   - A non-empty endpoint carrying leading/trailing whitespace (including a
//     whitespace-only string) is REJECTED. It is never silently trimmed into a
//     different endpoint nor reinterpreted as the empty SDK default — that would
//     let a misconfigured " STORAGE_ENDPOINT=  " fall through to a lower-priority
//     S3_* var or to provider default resolution. Fail closed.
//   - If the endpoint already carries http:// or https:// it is returned as-is
//     (the explicit scheme wins; ForceHTTP is ignored in that case).
//   - If the endpoint is a clean (no surrounding whitespace) scheme-less value:
//   - ForceHTTP=true  -> http://<endpoint>
//   - otherwise       -> https://<endpoint>
//   - A scheme other than http/https (e.g. ftp://) is rejected as unsupported
//     rather than producing an ambiguous endpoint.
func normalizeEndpoint(endpoint string, forceHTTP bool) (string, error) {
	if endpoint == "" {
		return "", nil
	}
	// Fail-closed: reject any leading/trailing whitespace (whitespace-only
	// included). We intentionally do NOT TrimSpace-then-check-empty, because that
	// would turn "   " into the valid SDK-default empty endpoint. The check uses
	// the raw value so whitespace is surfaced as an error, not as a different
	// endpoint or as empty.
	if strings.TrimSpace(endpoint) != endpoint {
		return "", fmt.Errorf("storage endpoint must not contain leading/trailing whitespace: %q", endpoint)
	}
	lower := strings.ToLower(endpoint)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return endpoint, nil
	case strings.HasPrefix(lower, "http://"):
		return endpoint, nil
	case strings.Contains(lower, "://"):
		// Any other scheme (ftp://, file://, ...) is unsupported.
		return "", fmt.Errorf("unsupported storage endpoint scheme: %q", endpoint)
	default:
		scheme := "https"
		if forceHTTP {
			scheme = "http"
		}
		return scheme + "://" + endpoint, nil
	}
}

// NewClient creates a new storage client
func NewClient(cfg *Config) (*Client, error) {
	if cfg.Provider == "" {
		cfg.Provider = ProviderS3
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	endpoint, err := normalizeEndpoint(cfg.Endpoint, cfg.ForceHTTP)
	if err != nil {
		return nil, fmt.Errorf("invalid storage endpoint: %w", err)
	}

	usePathStyle := cfg.UsePathStyle

	// Create custom resolver for MinIO/S3-compatible endpoints
	var endpointResolver aws.EndpointResolverWithOptionsFunc
	if endpoint != "" {
		endpointResolver = func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               endpoint,
				HostnameImmutable: true,
				Source:            aws.EndpointSourceCustom,
			}, nil
		}
	}

	// Load AWS configuration
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		config.WithEndpointResolverWithOptions(endpointResolver),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client with path-style addressing for MinIO
	client := &Client{
		provider: cfg.Provider,
		s3Client: s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.UsePathStyle = usePathStyle
		}),
		bucket:       cfg.Bucket,
		endpoint:     endpoint,
		region:       cfg.Region,
		forceHTTP:    cfg.ForceHTTP,
		usePathStyle: usePathStyle,
	}

	return client, nil
}

// Upload uploads data to S3/MinIO
func (c *Client) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	}

	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	_, err := c.s3Client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	return nil
}

// Download downloads data from S3/MinIO
func (c *Client) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}

	output, err := c.s3Client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to download object: %w", err)
	}

	return output.Body, nil
}

// Delete removes an object from S3/MinIO
func (c *Client) Delete(ctx context.Context, key string) error {
	input := &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}

	_, err := c.s3Client.DeleteObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// DeleteMultiple removes multiple objects from S3/MinIO
func (c *Client) DeleteMultiple(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	objects := make([]types.ObjectIdentifier, len(keys))
	for i, key := range keys {
		objects[i] = types.ObjectIdentifier{Key: aws.String(key)}
	}

	input := &s3.DeleteObjectsInput{
		Bucket: aws.String(c.bucket),
		Delete: &types.Delete{
			Objects: objects,
		},
	}

	_, err := c.s3Client.DeleteObjects(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete objects: %w", err)
	}

	return nil
}

// Exists checks if an object exists in S3/MinIO
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}

	_, err := c.s3Client.HeadObject(ctx, input)
	if err != nil {
		var notFound *types.NotFound
		if ok := fmt.Sprintf("%T", err) == "*types.NotFound"; ok || notFound != nil {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// GetSize returns the size of an object
func (c *Client) GetSize(ctx context.Context, key string) (int64, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}

	output, err := c.s3Client.HeadObject(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to get object size: %w", err)
	}

	return aws.ToInt64(output.ContentLength), nil
}

// List lists objects with a given prefix
func (c *Client) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	}

	var objects []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(c.s3Client, input)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			objects = append(objects, ObjectInfo{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
			})
		}
	}

	return objects, nil
}

// GeneratePresignedURL generates a presigned URL for temporary access
func (c *Client) GeneratePresignedURL(ctx context.Context, key string, expiration time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(c.s3Client)

	input := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}

	req, err := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(expiration))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return req.URL, nil
}

// ObjectInfo holds information about a stored object
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// GetProvider returns the storage provider type
func (c *Client) GetProvider() Provider {
	return c.provider
}

// Endpoint returns the resolved (scheme-qualified) endpoint URL.
func (c *Client) Endpoint() string {
	return c.endpoint
}

// ForceHTTP reports whether the client was configured to use plain HTTP.
func (c *Client) ForceHTTP() bool {
	return c.forceHTTP
}

// UsePathStyle reports whether the client uses path-style addressing.
func (c *Client) UsePathStyle() bool {
	return c.usePathStyle
}

// GetBucket returns the bucket name
func (c *Client) GetBucket() string {
	return c.bucket
}

// Close closes the storage client
func (c *Client) Close() error {
	// S3 client doesn't require explicit closing
	return nil
}
