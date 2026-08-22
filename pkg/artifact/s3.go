package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
)

// S3Scheme is the URI scheme S3Store stamps into the references it
// produces. Open refuses any other scheme, same reasoning as
// LocalDiskStore: a reference produced by a different backend must not be
// silently resolved against the wrong one.
const S3Scheme = "s3"

// S3StoreConfig configures an S3Store. Bucket is required; everything else
// is optional and exists to support non-AWS S3-compatible providers
// (MinIO, Cloudflare R2, ...), not just AWS itself.
type S3StoreConfig struct {
	// Bucket is the bucket every blob is written to and read from. Required.
	Bucket string

	// Region is the AWS region, or the region value an S3-compatible
	// provider expects (some accept any non-empty string). Falls back to
	// the AWS SDK's normal credential-chain resolution when empty.
	Region string

	// Endpoint overrides the default AWS endpoint, for S3-compatible
	// providers. Empty means "talk to real AWS S3."
	Endpoint string

	// AccessKeyID and SecretAccessKey provide static credentials. Both
	// empty falls back to the AWS SDK's default credential chain (env
	// vars, shared config, IAM role, ...) — the right default for AWS
	// itself; most S3-compatible providers require the static pair.
	AccessKeyID     string
	SecretAccessKey string

	// UsePathStyle addresses objects as "endpoint/bucket/key" instead of
	// "bucket.endpoint/key". Most S3-compatible providers other than AWS
	// itself require this.
	UsePathStyle bool

	// CredentialsProvider supplies credentials that cannot be expressed
	// as a static key pair — most importantly ones that expire and are
	// re-minted, such as STS sessions wrapped in aws.CredentialsCache.
	// Callers that hold a long-lived key pair should keep using
	// AccessKeyID/SecretAccessKey; this exists for callers that mint
	// short-lived, scoped credentials per tenant and need the SDK to
	// refresh them mid-flight.
	//
	// Takes precedence over AccessKeyID/SecretAccessKey when both are
	// set, since a caller that went to the trouble of building a
	// provider means it. Nil keeps the previous behavior exactly.
	CredentialsProvider aws.CredentialsProvider
}

// S3Store keeps blobs in an S3-compatible bucket.
//
// This is a table-stakes connector: no tenant awareness, no multi-bucket
// orchestration, no credential scoping beyond what S3StoreConfig is handed
// at construction — a single bucket, a single set of credentials, exactly
// the same scope LocalDiskStore and SQLArtifactStore's blob store already
// have. A managed, multi-tenant storage product built on top of this is
// explicitly out of scope here.
//
// Writes are content-addressed the same way LocalDiskStore's are: the
// digest is not known until every byte has been read, so a write streams
// to a temporary key while hashing, then is published under its final,
// content-addressed key. S3 has no atomic rename, so "publish" here is a
// server-side CopyObject (no data re-transfers) followed by deleting the
// temporary key — the closest available equivalent, and idempotent:
// identical content copied over identical content is a no-op in substance
// even though it costs two extra API calls.
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store constructs an S3Store from cfg, resolving AWS credentials and
// building the client eagerly so a misconfiguration (missing region with
// no way to infer one, for instance) fails at construction rather than on
// the first write.
func NewS3Store(ctx context.Context, cfg S3StoreConfig) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("artifact: s3 bucket is required")
	}

	var loadOpts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	switch {
	case cfg.CredentialsProvider != nil:
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(cfg.CredentialsProvider))
	case cfg.AccessKeyID != "":
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("artifact: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

// namespacePrefix maps a namespace to a key prefix without letting the
// caller's string reach an S3 key directly — same reasoning and technique
// as LocalDiskStore.namespaceDir: namespaces are run IDs, but node IDs and
// similar identifiers elsewhere in this codebase come from pipeline JSON
// end users fully control, and this store's blobPath/stagingKey both
// derive from namespace, so a hostile namespace value must not be able to
// influence the resulting key.
func (s *S3Store) namespacePrefix(namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	return hex.EncodeToString(sum[:])
}

func (s *S3Store) blobKey(namespace, digest string) string {
	return s.namespacePrefix(namespace) + "/" + digest + ".blob"
}

func (s *S3Store) stagingKey(namespace string) string {
	return s.namespacePrefix(namespace) + "/.incoming/" + uuid.NewString()
}

// Put implements Store.
func (s *S3Store) Put(ctx context.Context, namespace string, r io.Reader, opts PutOptions) (*ArtifactRef, error) {
	if namespace == "" {
		return nil, fmt.Errorf("artifact: namespace is required")
	}
	if r == nil {
		return nil, fmt.Errorf("artifact: reader is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	staging := s.stagingKey(namespace)
	hasher := sha256.New()
	size, err := s.uploadCounting(ctx, staging, io.TeeReader(r, hasher))
	if err != nil {
		return nil, fmt.Errorf("artifact: upload blob: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	final := s.blobKey(namespace, digest)

	// Publish: server-side copy to the content-addressed key, then remove
	// the staging object. No data re-transfers through this process for
	// the copy — S3 performs it internally. Identical content already
	// present at `final` (a prior write of the same bytes) is overwritten
	// with identical bytes, the same no-op-in-substance behavior
	// LocalDiskStore.Put's os.Rename has when the destination already
	// exists.
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		CopySource: aws.String(s.bucket + "/" + staging),
		Key:        aws.String(final),
	})
	if err != nil {
		_ = s.deleteKey(context.Background(), staging)
		return nil, fmt.Errorf("artifact: publish blob: %w", err)
	}
	if err := s.deleteKey(ctx, staging); err != nil {
		// The blob is already published under `final` and readable; a
		// failed cleanup of the staging copy is unfortunate scratch-space
		// growth, not data loss or a corrupted reference. Do not fail the
		// write for it.
		return nil, fmt.Errorf("artifact: put succeeded but staging cleanup failed (blob is safe at %s): %w", final, err)
	}

	mediaType := opts.MediaType
	if mediaType == "" {
		mediaType = MediaTypeOctetStream
	}
	return &ArtifactRef{
		URI:       fmt.Sprintf("%s://%s/%s", S3Scheme, s.namespacePrefix(namespace), digest),
		MediaType: mediaType,
		SizeBytes: size,
		Checksum:  "sha256:" + digest,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// uploadCounting uploads r to key via a genuine streaming multipart
// upload — manager.Uploader reads r in bounded part-size chunks (5 MiB by
// default) and uploads each as it's read, never buffering the whole value
// in memory. This is the property ADR-012 exists to protect: an artifact
// large enough to be worth spilling must never be held in memory to store
// it, on write or on this path.
//
// countingReader tracks bytes read so the final size is known without a
// second pass — the uploader's own return value does not expose it
// directly for a streamed, unsized io.Reader input.
func (s *S3Store) uploadCounting(ctx context.Context, key string, r io.Reader) (int64, error) {
	cr := &countingReader{r: r}
	uploader := manager.NewUploader(s.client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   cr,
	})
	if err != nil {
		return 0, err
	}
	return cr.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (s *S3Store) deleteKey(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

// parseS3URI splits an s3:// reference into its namespace-prefix and
// digest, refusing anything it does not recognize — same reasoning as
// LocalDiskStore's parseURI, including validating both halves are hex
// before they reach an S3 key, so a hand-edited or hostile reference
// cannot smuggle a path-traversal-shaped key into a GetObject call.
func parseS3URI(uri string) (nsPrefix, digest string, err error) {
	rest, ok := strings.CutPrefix(uri, S3Scheme+"://")
	if !ok {
		return "", "", fmt.Errorf("artifact: %q is not a %s:// reference", uri, S3Scheme)
	}
	nsPrefix, digest, ok = strings.Cut(rest, "/")
	if !ok || nsPrefix == "" || digest == "" {
		return "", "", fmt.Errorf("artifact: malformed reference %q", uri)
	}
	if !isHex(nsPrefix) || !isHex(digest) {
		return "", "", fmt.Errorf("artifact: reference %q has non-hex components", uri)
	}
	return nsPrefix, digest, nil
}

// Open implements Store.
//
// The returned reader streams the object body directly rather than
// buffering it — buffering an entire object into memory before returning
// the first byte to the caller would reintroduce, on read, the exact
// memory-scaling problem ADR-012 exists to solve on write. Checksum
// verification is therefore incremental rather than eager: bytes are
// hashed as the caller reads them, and a mismatch surfaces as
// ErrChecksumMismatch from the Read call that would otherwise report EOF.
// This is a real, disclosed tradeoff — a caller that reads only part of
// the stream and stops will not learn whether the unread remainder was
// corrupted — chosen because eager verification is a full download before
// any byte is usable, and this store's whole reason to exist is not
// forcing large data through memory unnecessarily.
func (s *S3Store) Open(ctx context.Context, ref *ArtifactRef) (io.ReadCloser, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nsPrefix, digest, err := parseS3URI(ref.URI)
	if err != nil {
		return nil, err
	}
	key := nsPrefix + "/" + digest + ".blob"

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		var apiErr smithy.APIError
		if errors.As(err, &nsk) || (errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchKey") {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref.URI)
		}
		return nil, fmt.Errorf("artifact: open blob: %w", err)
	}

	return &checksumVerifyingReadCloser{
		body:   out.Body,
		hasher: sha256.New(),
		want:   ref.Checksum,
	}, nil
}

// checksumVerifyingReadCloser tees every byte read through a hasher and,
// once the underlying body reports io.EOF, compares the accumulated
// digest against the expected checksum before letting EOF propagate —
// substituting ErrChecksumMismatch instead if they differ. A caller using
// the normal io.Copy/io.ReadAll idiom therefore sees the mismatch as the
// terminal error of the read, exactly where LocalDiskStore.Open's eager
// check would have surfaced it, without requiring the whole object in
// memory first.
type checksumVerifyingReadCloser struct {
	body   io.ReadCloser
	hasher hash.Hash
	want   string
	done   bool
}

func (c *checksumVerifyingReadCloser) Read(p []byte) (int, error) {
	n, err := c.body.Read(p)
	if n > 0 {
		c.hasher.Write(p[:n])
	}
	if err == io.EOF && !c.done {
		c.done = true
		got := "sha256:" + hex.EncodeToString(c.hasher.Sum(nil))
		if got != c.want {
			return n, fmt.Errorf("%w: stored %s, reference says %s", ErrChecksumMismatch, got, c.want)
		}
	}
	return n, err
}

func (c *checksumVerifyingReadCloser) Close() error {
	return c.body.Close()
}

// DeleteNamespace implements Store. Every blob for a namespace lives under
// one key prefix, so this lists and batch-deletes everything under it —
// the S3 equivalent of LocalDiskStore's single os.RemoveAll, in as few
// round trips as the batch-delete API allows (up to 1000 keys per call).
func (s *S3Store) DeleteNamespace(ctx context.Context, namespace string) error {
	if namespace == "" {
		return fmt.Errorf("artifact: namespace is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix := s.namespacePrefix(namespace) + "/"

	var continuation *string
	for {
		listOut, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuation,
		})
		if err != nil {
			return fmt.Errorf("artifact: list namespace: %w", err)
		}
		if len(listOut.Contents) == 0 {
			if listOut.IsTruncated == nil || !*listOut.IsTruncated {
				return nil
			}
			continuation = listOut.NextContinuationToken
			continue
		}

		objs := make([]s3types.ObjectIdentifier, 0, len(listOut.Contents))
		for _, obj := range listOut.Contents {
			objs = append(objs, s3types.ObjectIdentifier{Key: obj.Key})
		}
		if _, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &s3types.Delete{Objects: objs},
		}); err != nil {
			return fmt.Errorf("artifact: delete namespace objects: %w", err)
		}

		if listOut.IsTruncated == nil || !*listOut.IsTruncated {
			return nil
		}
		continuation = listOut.NextContinuationToken
	}
}
