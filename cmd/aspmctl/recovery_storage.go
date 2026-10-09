package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func (r *recoveryRuntime) selectedPrefixes() []recoveryPrefix {
	return []recoveryPrefix{
		{Purpose: "raw", Prefix: r.config.Storage.RawPrefix},
		{Purpose: "normalized", Prefix: r.config.Storage.NormalizedPrefix},
		{Purpose: "archive", Prefix: r.config.Storage.ArchivePrefix},
	}
}

func (r *recoveryRuntime) readSelectedObjects(ctx context.Context, staging string) (recoveryObjectSummary, error) {
	var summary recoveryObjectSummary
	blobs := map[string]int64{}
	for _, selected := range r.selectedPrefixes() {
		pages := s3.NewListObjectsV2Paginator(r.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(r.config.Storage.Bucket), Prefix: aws.String(selected.Prefix),
			MaxKeys: aws.Int32(1000),
		})
		for page := 0; pages.HasMorePages(); page++ {
			if page >= 1000 {
				return summary, errors.New("recovery object listing exceeded its page bound")
			}
			list, err := pages.NextPage(ctx)
			if err != nil {
				return summary, errors.New("recovery object listing failed")
			}
			for _, item := range list.Contents {
				if summary.ObjectCount >= r.config.Limits.MaxObjects {
					return summary, errors.New("recovery object count exceeds its limit")
				}
				key := aws.ToString(item.Key)
				if !validRecoveryObjectKey(key, selected.Prefix) {
					return summary, errors.New("recovery object key escaped its selected prefix")
				}
				object, err := r.s3.GetObject(ctx, &s3.GetObjectInput{
					Bucket: aws.String(r.config.Storage.Bucket), Key: aws.String(key),
				})
				if err != nil {
					return summary, errors.New("recovery object read failed")
				}
				data, readErr := io.ReadAll(io.LimitReader(object.Body, r.config.Limits.MaxObjectBytes+1))
				closeErr := object.Body.Close()
				if err = errors.Join(readErr, closeErr); err != nil {
					return summary, errors.New("recovery object read failed")
				}
				if int64(len(data)) > r.config.Limits.MaxObjectBytes {
					return summary, errors.New("recovery object exceeds its byte limit")
				}
				summary.LogicalBytes += int64(len(data))
				if summary.LogicalBytes > r.config.Limits.MaxTotalObjectBytes {
					return summary, errors.New("recovery object bytes exceed their total limit")
				}
				digest := recoveryHash(data)
				metadata, err := portableRecoveryMetadata(object.Metadata, digest)
				if err != nil {
					return summary, err
				}
				contentType := aws.ToString(object.ContentType)
				if !portableRecoveryContentType(contentType) {
					return summary, errors.New("recovery object content type is unsupported")
				}
				hexDigest := strings.TrimPrefix(digest, "sha256:")
				blobPath := "objects/sha256/" + hexDigest
				if _, exists := blobs[digest]; !exists {
					blobs[digest] = int64(len(data))
					summary.BlobBytes += int64(len(data))
					if staging != "" {
						target := filepath.Join(staging, filepath.FromSlash(blobPath))
						if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
							return summary, err
						}
						if err = os.WriteFile(target, data, 0600); err != nil {
							return summary, err
						}
					}
				}
				summary.Objects = append(summary.Objects, recoveryObject{
					Purpose: selected.Purpose, Bucket: r.config.Storage.Bucket, Key: key,
					SizeBytes: int64(len(data)), SHA256: digest, BlobPath: blobPath,
					ContentType: contentType, Metadata: metadata,
				})
				summary.ObjectCount++
			}
		}
	}
	sort.Slice(summary.Objects, func(i, j int) bool { return summary.Objects[i].Key < summary.Objects[j].Key })
	summary.BlobCount = len(blobs)
	return summary, nil
}

func validRecoveryObjectKey(key, prefix string) bool {
	if key == "" || !strings.HasPrefix(key, prefix) || strings.Contains(key, `\`) ||
		strings.ContainsRune(key, 0) {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func portableRecoveryMetadata(input map[string]string, digest string) (map[string]string, error) {
	result := map[string]string{}
	for name, value := range input {
		lower := strings.ToLower(name)
		if lower != "sha256" {
			return nil, errors.New("recovery object metadata is unsupported")
		}
		if value != strings.TrimPrefix(digest, "sha256:") {
			return nil, errors.New("recovery object metadata digest is inconsistent")
		}
		result[lower] = value
	}
	return result, nil
}

func portableRecoveryContentType(value string) bool {
	if len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	switch value {
	case "", "application/octet-stream", "application/json", "application/sarif+json", "text/csv":
		return true
	default:
		return strings.HasPrefix(value, "application/json;") || strings.HasPrefix(value, "text/csv;")
	}
}

func (r *recoveryRuntime) selectedPrefixesEmpty(ctx context.Context) (bool, error) {
	for _, selected := range r.selectedPrefixes() {
		output, err := r.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(r.config.Storage.Bucket), Prefix: aws.String(selected.Prefix),
			MaxKeys: aws.Int32(1),
		})
		if err != nil {
			return false, errors.New("recovery destination object listing failed")
		}
		if len(output.Contents) != 0 {
			return false, nil
		}
	}
	return true, nil
}

func verifyRecoveryObjectArtifacts(root string, objects []recoveryObject) error {
	expected := map[string]recoveryObject{}
	for _, object := range objects {
		if object.Bucket == "" || object.Key == "" || object.SizeBytes < 0 ||
			!recoveryDigestPattern.MatchString(object.SHA256) ||
			object.BlobPath != "objects/sha256/"+strings.TrimPrefix(object.SHA256, "sha256:") {
			return errors.New("recovery object index contains invalid bindings")
		}
		if !filepath.IsLocal(filepath.FromSlash(object.BlobPath)) {
			return errors.New("recovery object blob path is unsafe")
		}
		if prior, present := expected[object.BlobPath]; present &&
			(prior.SHA256 != object.SHA256 || prior.SizeBytes != object.SizeBytes) {
			return errors.New("recovery object index contains inconsistent blobs")
		}
		expected[object.BlobPath] = object
	}
	objectRoot := filepath.Join(root, "objects")
	info, err := os.Lstat(objectRoot)
	if errors.Is(err, os.ErrNotExist) && len(expected) == 0 {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recovery object blob directory is unavailable")
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(objectRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == objectRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("recovery object blob path is unsafe")
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		portable := filepath.ToSlash(relative)
		if entry.IsDir() {
			if portable != "objects/sha256" {
				return errors.New("recovery object directory contains undeclared artifacts")
			}
			return nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil || !entryInfo.Mode().IsRegular() {
			return errors.New("recovery object blob path is unsafe")
		}
		object, present := expected[portable]
		if !present {
			return errors.New("recovery object directory contains undeclared artifacts")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || int64(len(data)) != object.SizeBytes ||
			recoveryHash(data) != object.SHA256 {
			return errors.New("recovery object blob failed integrity validation")
		}
		seen[portable] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("recovery object blob is unavailable")
	}
	return nil
}

func validateRecoveryObjectIndex(manifest recoveryManifest, objects []recoveryObject,
	config recoveryConfiguration) error {
	blobs := map[string]int64{}
	var logicalBytes int64
	var blobBytes int64
	for _, object := range objects {
		prefix := prefixForPurpose(config, object.Purpose)
		if object.Bucket != config.Storage.Bucket || prefix == "" ||
			!validRecoveryObjectKey(object.Key, prefix) ||
			object.SizeBytes < 0 || object.SizeBytes > config.Limits.MaxObjectBytes ||
			!recoveryDigestPattern.MatchString(object.SHA256) ||
			object.BlobPath != "objects/sha256/"+strings.TrimPrefix(object.SHA256, "sha256:") ||
			!recoveryBlobHex(object.BlobPath) ||
			!portableRecoveryContentType(object.ContentType) {
			return errors.New("recovery object index contains invalid bindings")
		}
		metadata, err := portableRecoveryMetadata(object.Metadata, object.SHA256)
		if err != nil || !reflect.DeepEqual(metadata, object.Metadata) {
			return errors.New("recovery object index contains invalid metadata")
		}
		logicalBytes += object.SizeBytes
		if logicalBytes > config.Limits.MaxTotalObjectBytes {
			return errors.New("recovery object index exceeds its total byte limit")
		}
		if size, present := blobs[object.SHA256]; present {
			if size != object.SizeBytes {
				return errors.New("recovery object index contains inconsistent blobs")
			}
		} else {
			blobs[object.SHA256] = object.SizeBytes
			blobBytes += object.SizeBytes
		}
	}
	if len(objects) != manifest.Objects.ObjectCount ||
		logicalBytes != manifest.Objects.LogicalBytes ||
		len(blobs) != manifest.Objects.BlobCount ||
		blobBytes != manifest.Objects.BlobBytes {
		return errors.New("recovery object index does not match the manifest")
	}
	return nil
}

func (r *recoveryRuntime) restoreObjects(ctx context.Context, root string,
	objects []recoveryObject, state *recoveryRestoreState, statePath string) error {
	for _, object := range objects {
		if object.Bucket != r.config.Storage.Bucket ||
			!validRecoveryObjectKey(object.Key, prefixForPurpose(r.config, object.Purpose)) {
			return errors.New("recovery object does not match the selected destination")
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(object.BlobPath)))
		if err != nil {
			return errors.New("recovery object blob is unavailable")
		}
		state.PendingKey = object.Key
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
			return errors.New("record recovery object checkpoint failed")
		}
		_, err = r.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(r.config.Storage.Bucket), Key: aws.String(object.Key),
			Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
			ContentType: aws.String(object.ContentType), Metadata: object.Metadata,
			IfNoneMatch: aws.String("*"),
		})
		if err != nil {
			if recoveryS3PreconditionFailed(err) {
				state.PendingKey = ""
				state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				_, _ = writeCanonicalJSON(statePath, state, 0600)
			}
			return errors.New("recovery destination object creation failed")
		}
		output, err := r.s3.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(r.config.Storage.Bucket), Key: aws.String(object.Key),
		})
		if err != nil {
			return errors.New("recovery destination object verification failed")
		}
		actual, readErr := io.ReadAll(io.LimitReader(output.Body, object.SizeBytes+1))
		closeErr := output.Body.Close()
		if errors.Join(readErr, closeErr) != nil || int64(len(actual)) != object.SizeBytes ||
			recoveryHash(actual) != object.SHA256 {
			return errors.New("recovery destination object verification failed")
		}
		state.CreatedKeys = append(state.CreatedKeys, object.Key)
		state.PendingKey = ""
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
			return errors.New("record recovery object checkpoint failed")
		}
	}
	return nil
}

func recoveryS3Missing(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "NoSuchKey", "NotFound":
		return true
	default:
		return false
	}
}

func recoveryS3PreconditionFailed(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) &&
		(api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict")
}

func prefixForPurpose(config recoveryConfiguration, purpose string) string {
	switch purpose {
	case "raw":
		return config.Storage.RawPrefix
	case "normalized":
		return config.Storage.NormalizedPrefix
	case "archive":
		return config.Storage.ArchivePrefix
	default:
		return ""
	}
}

func recoveryObjectsEqual(left, right recoveryObjectSummary) bool {
	return left.ObjectCount == right.ObjectCount && left.LogicalBytes == right.LogicalBytes &&
		left.BlobCount == right.BlobCount && left.BlobBytes == right.BlobBytes &&
		reflect.DeepEqual(left.Objects, right.Objects)
}

func recoveryBlobHex(path string) bool {
	value := strings.TrimPrefix(path, "objects/sha256/")
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
