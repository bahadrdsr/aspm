package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

type v2ChartCapture struct {
	files map[string][]byte
	err   error
}

func v2ExpectedChart(f *fixture) map[string]string {
	result := map[string]string{}
	for name, hash := range f.manifest.Files {
		if relative, ok := strings.CutPrefix(name, "deploy/helm/aspm/"); ok {
			result[relative] = hash
		}
	}
	return result
}

// Observe the real command input without changing its result or installer policy.
func v2CaptureChart(f *fixture, command Command) v2ChartCapture {
	var selected string
	for _, arg := range command.Args {
		if !filepath.IsAbs(arg) {
			continue
		}
		relative, err := filepath.Rel(f.options.Root, arg)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		info, err := f.files.root.Stat(relative)
		if err != nil {
			continue
		}
		candidate := false
		if info.IsDir() {
			_, err = f.files.root.Stat(filepath.Join(relative, "Chart.yaml"))
			candidate = err == nil
		} else {
			candidate = strings.HasSuffix(arg, ".tgz") || strings.HasSuffix(arg, ".tar.gz") || strings.HasSuffix(arg, ".tar")
		}
		if candidate {
			if selected != "" {
				return v2ChartCapture{err: errors.New("ambiguous Helm chart input")}
			}
			selected = relative
		}
	}
	if selected == "" {
		return v2ChartCapture{err: errors.New("Helm did not receive an owned chart directory/archive")}
	}
	files := map[string][]byte{}
	info, err := f.files.root.Stat(selected)
	if err != nil {
		return v2ChartCapture{err: err}
	}
	if info.IsDir() {
		total := 0
		err = fs.WalkDir(f.files.root.FS(), filepath.ToSlash(selected), func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			if !entry.Type().IsRegular() || len(files) >= 256 {
				return errors.New("chart input has nonregular or excessive files")
			}
			relative, err := filepath.Rel(selected, filepath.FromSlash(name))
			if err != nil {
				return err
			}
			data, err := f.files.root.ReadFile(filepath.FromSlash(name))
			if err != nil {
				return err
			}
			total += len(data)
			if total > 8<<20 {
				return errors.New("chart input exceeds fixture bound")
			}
			files[filepath.ToSlash(relative)] = data
			return nil
		})
		return v2ChartCapture{files: files, err: err}
	}
	input, err := f.files.root.Open(selected)
	if err != nil {
		return v2ChartCapture{err: err}
	}
	defer input.Close()
	raw, err := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return v2ChartCapture{err: errors.New("archive input exceeds fixture bound or cannot be read")}
	}
	var reader io.Reader = bytes.NewReader(raw)
	if bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		compressed, err := gzip.NewReader(reader)
		if err != nil {
			return v2ChartCapture{err: err}
		}
		defer compressed.Close()
		reader = compressed
	}
	archive := tar.NewReader(reader)
	total := 0
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return v2ChartCapture{err: err}
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return v2ChartCapture{err: errors.New("archive member is outside its chart root")}
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || len(files) >= 256 || header.Size < 0 || header.Size > 8<<20 {
			return v2ChartCapture{err: errors.New("archive contains unsupported members")}
		}
		if _, duplicate := files[name]; duplicate {
			return v2ChartCapture{err: errors.New("duplicate chart archive member")}
		}
		data, err := io.ReadAll(io.LimitReader(archive, (8<<20)+1))
		total += len(data)
		if err != nil || total > 8<<20 {
			return v2ChartCapture{err: errors.New("expanded chart exceeds fixture bound")}
		}
		files[name] = data
	}
	root := ""
	for name := range files {
		if path.Base(name) == "Chart.yaml" && (root == "" || len(path.Dir(name)) < len(root)) {
			root = path.Dir(name)
		}
	}
	if root == "" {
		return v2ChartCapture{err: errors.New("archive contains no Chart.yaml")}
	}
	result := map[string][]byte{}
	for name, data := range files {
		relative := name
		if root != "." {
			var inside bool
			relative, inside = strings.CutPrefix(name, root+"/")
			if !inside {
				return v2ChartCapture{err: errors.New("archive includes files outside the approved chart")}
			}
		}
		result[relative] = data
	}
	return v2ChartCapture{files: result}
}

func v2RequireApprovedChart(t *testing.T, got v2ChartCapture, expected map[string]string) {
	t.Helper()
	ok(t, "inspect exact Helm input", got.err)
	if len(got.files) != len(expected) || len(expected) < 3 {
		t.Fatalf("Helm input is not exactly the approved owned chart: got %d files, expected %d", len(got.files), len(expected))
	}
	for name, want := range expected {
		data, present := got.files[name]
		if !present || digest(data) != want {
			t.Fatalf("Helm input changed/omitted approved chart member %s", name)
		}
	}
}
