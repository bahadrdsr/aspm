package execution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type bundleManifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Release    string            `json:"release"`
	Files      map[string]string `json:"files"`
	Images     map[string]string `json:"images"`
}

type verifiedBundle struct {
	directory string
	digest    string
	manifest  bundleManifest
	files     map[string][]byte
}

var (
	pinnedImage  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*@sha256:[a-f0-9]{64}$`)
	fileDigest   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	machineID    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	namespaceUID = regexp.MustCompile(`^[A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{12}$`)
)

func (i *installer) verifyBundle(ctx context.Context, directory string, config configuration) (verifiedBundle, error) {
	directory, err := relativePath(directory)
	if err != nil {
		return verifiedBundle{}, ErrBundle
	}
	raw, err := i.read(ctx, filepath.Join(directory, "manifest.json"), maxInputBytes)
	if err != nil {
		return verifiedBundle{}, ErrBundle
	}
	signature, err := i.read(ctx, filepath.Join(directory, "manifest.sig"), ed25519.SignatureSize)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(i.options.TrustedKey, raw, signature) {
		return verifiedBundle{}, ErrBundle
	}
	var manifest bundleManifest
	if decode(raw, &manifest) != nil || manifest.APIVersion != "aspm/v1alpha1" || manifest.Kind != "InstallerBundle" ||
		manifest.Release != config.Release.Version || len(manifest.Files) < 3 || len(manifest.Files) > 512 || len(manifest.Images) != 3 {
		return verifiedBundle{}, ErrBundle
	}
	for _, name := range []string{"application", "postgres", "storage"} {
		if !pinnedImage.MatchString(manifest.Images[name]) {
			return verifiedBundle{}, ErrBundle
		}
	}
	if !strings.HasSuffix(manifest.Images["storage"], "@"+config.ObjectStore.Image.Digest) {
		return verifiedBundle{}, ErrBundle
	}
	bundle := verifiedBundle{directory: directory, digest: digest(raw), manifest: manifest, files: make(map[string][]byte, len(manifest.Files))}
	names := make([]string, 0, len(manifest.Files))
	for name := range manifest.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	var total int
	for _, name := range names {
		relative, err := relativePath(name)
		if err != nil || filepath.ToSlash(relative) != name || !fileDigest.MatchString(manifest.Files[name]) {
			return verifiedBundle{}, ErrBundle
		}
		data, err := i.read(ctx, filepath.Join(directory, relative), 8<<20)
		if err != nil || digest(data) != manifest.Files[name] {
			return verifiedBundle{}, ErrBundle
		}
		total += len(data)
		if total > 64<<20 {
			return verifiedBundle{}, ErrBundle
		}
		bundle.files[name] = data
	}
	required := []string{"deploy/helm/aspm/Chart.yaml", "deploy/helm/aspm/values.yaml"}
	if config.Target.Kind == "linux" {
		required = append(required, "deploy/quadlet/aspm-core.container", "deploy/quadlet/aspm-ingestion@.container",
			"deploy/quadlet/aspm-reports.container", "deploy/quadlet/aspm-postgres.container", "deploy/quadlet/aspm-storage.container",
			"deploy/quadlet/aspm.network", "deploy/quadlet/aspm-postgres.volume", "deploy/quadlet/aspm-storage.volume")
	}
	for _, name := range required {
		if len(bundle.files[name]) == 0 {
			return verifiedBundle{}, ErrBundle
		}
	}
	return bundle, nil
}

func (i *installer) requireApprovedBundle(ctx context.Context, p prepared) error {
	verified, err := i.verifyBundle(ctx, p.input.BundleDir, p.config)
	if err != nil {
		return err
	}
	if verified.digest != p.bundle.digest {
		return ErrApproval
	}
	return nil
}

// The archive is built from verified memory, never from a directory walk of
// mutable bundle input. Every member is a regular file beneath one chart root.
func approvedChartArchive(ctx context.Context, bundle verifiedBundle) ([]byte, error) {
	const prefix = "deploy/helm/aspm/"
	var names []string
	var total int
	folded := make(map[string]bool)
	for name, data := range bundle.files {
		relative, inside := strings.CutPrefix(name, prefix)
		if !inside {
			continue
		}
		path, err := relativePath(relative)
		canonical := filepath.ToSlash(path)
		if err != nil || canonical != relative || folded[strings.ToLower(relative)] ||
			digest(data) != bundle.manifest.Files[name] {
			return nil, ErrBundle
		}
		folded[strings.ToLower(relative)] = true
		names = append(names, name)
		total += len(data)
		if len(names) > 256 || total > 8<<20 {
			return nil, ErrBundle
		}
	}
	if len(names) < 3 || len(bundle.files[prefix+"Chart.yaml"]) == 0 {
		return nil, ErrBundle
	}
	sort.Strings(names)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	archive := tar.NewWriter(gzipWriter)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := bundle.files[name]
		member := "aspm/" + strings.TrimPrefix(name, prefix)
		if err := archive.WriteHeader(&tar.Header{
			Name: member, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
		}); err != nil {
			return nil, ErrBundle
		}
		if _, err := archive.Write(data); err != nil {
			return nil, ErrBundle
		}
	}
	if archive.Close() != nil || gzipWriter.Close() != nil || compressed.Len() > 8<<20 {
		return nil, ErrBundle
	}
	return compressed.Bytes(), nil
}

func (i *installer) chartSnapshot(ctx context.Context, p prepared) (string, error) {
	data, err := approvedChartArchive(ctx, p.bundle)
	if err != nil {
		return "", err
	}
	path := filepath.Join("var", "lib", "aspm", "installer", "charts",
		strings.TrimPrefix(p.bundle.digest, "sha256:")+".tgz")
	current, err := i.read(ctx, path, 8<<20)
	if err == nil {
		if !bytes.Equal(current, data) {
			return "", ErrBundle
		}
	} else {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", ErrBundle
		}
	}
	// Reuse is also subject to native private-file permissions; matching bytes
	// alone must not admit a publicly writable snapshot.
	if err := i.writeSized(ctx, path, data, 8<<20); err != nil {
		return "", err
	}
	confirmed, err := i.read(ctx, path, 8<<20)
	if err != nil || !bytes.Equal(confirmed, data) {
		return "", ErrBundle
	}
	return filepath.Join(i.root, path), nil
}
