package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const readinessMarker = "ASPM scoped readiness probe v1\n"
const maxReadinessBytes = 4096

// PrepareReadiness uses the caller's explicit S3 identity to create only a
// missing, selected nonsecret probe. It never probes a bucket, lists objects,
// discovers credentials, or overwrites existing data. Service.Run restricts
// this operation to an explicitly opted-in core role.
func PrepareReadiness(ctx context.Context, config Config, key string) error {
	if !validPrefix(config.Prefix) || len(key) > 1024 || len(key) <= len(config.Prefix) ||
		!strings.HasPrefix(key, config.Prefix) {
		return ErrInvalid
	}
	for _, part := range strings.Split(key, "/") {
		if !safeSegment.MatchString(part) || part == "." || part == ".." {
			return ErrInvalid
		}
	}
	store, err := newStore(config)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	// An object-scoped role may receive 403 for a missing key, so absence is
	// established by conditional creation rather than a preceding GET.
	marker := []byte(readinessMarker)
	_, err = store.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(config.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(marker), ContentLength: aws.Int64(int64(len(marker))),
		ContentType: aws.String("text/plain; charset=utf-8"), IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		var api smithy.APIError
		var response interface{ HTTPStatusCode() int }
		conflict := errors.As(err, &api) && api.ErrorCode() == "PreconditionFailed" &&
			errors.As(err, &response) && response.HTTPStatusCode() == 412
		if !conflict {
			return fmt.Errorf("prepare scoped readiness object: %w", serviceError(err))
		}
	}
	// A conditional conflict is not a readiness receipt. Both the winner and
	// loser must successfully consume the real object with their supplied key.
	return store.readReadiness(ctx, key)
}

func (s *Store) readReadiness(ctx context.Context, key string) error {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.config.Bucket), Key: aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("read scoped readiness object: %w", serviceError(err))
	}
	if object == nil || object.Body == nil {
		return errors.New("scoped readiness object has no readable body")
	}
	if object.ContentLength != nil && (aws.ToInt64(object.ContentLength) < 1 || aws.ToInt64(object.ContentLength) > maxReadinessBytes) {
		return errors.Join(ErrIntegrity, object.Body.Close())
	}
	count, readErr := io.Copy(io.Discard, io.LimitReader(object.Body, maxReadinessBytes+1))
	if err := errors.Join(readErr, object.Body.Close()); err != nil {
		return fmt.Errorf("consume scoped readiness object: %w", err)
	}
	if count < 1 || count > maxReadinessBytes || (object.ContentLength != nil && count != aws.ToInt64(object.ContentLength)) {
		return ErrIntegrity
	}
	return ctx.Err()
}
