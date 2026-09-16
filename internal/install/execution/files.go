package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxInputBytes = 1 << 20

func relativePath(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, ":\x00\r\n") {
		return "", ErrUnsupported
	}
	value = strings.ReplaceAll(value, "\\", "/")
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrUnsupported
		}
	}
	path := filepath.FromSlash(value)
	if !filepath.IsLocal(path) {
		return "", ErrUnsupported
	}
	return path, nil
}

type rootFiles struct{ root *os.Root }

func (f rootFiles) Stat(path string) (fs.FileInfo, error) {
	path, err := relativePath(path)
	if err != nil {
		return nil, err
	}
	return f.root.Stat(path)
}

func (f rootFiles) ReadFile(path string) ([]byte, error) {
	path, err := relativePath(path)
	if err != nil {
		return nil, err
	}
	return f.root.ReadFile(path)
}

func (f rootFiles) WriteFile(path string, data []byte, mode fs.FileMode) error {
	path, err := relativePath(path)
	if err != nil {
		return err
	}
	if err := f.root.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := f.root.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return errors.Join(err, file.Close())
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	return errors.Join(file.Sync(), file.Close())
}

func (f rootFiles) Rename(from, to string) error {
	from, err := relativePath(from)
	if err != nil {
		return err
	}
	to, err = relativePath(to)
	if err != nil {
		return err
	}
	return f.root.Rename(from, to)
}

func (f rootFiles) Remove(path string) error {
	path, err := relativePath(path)
	if err != nil {
		return err
	}
	return f.root.Remove(path)
}

func decode(data []byte, destination any) error {
	if len(data) == 0 || len(data) > maxInputBytes {
		return ErrUnsupported
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return ErrUnsupported
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrUnsupported
	}
	return nil
}

func (i *installer) read(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := relativePath(path)
	if err != nil {
		return nil, err
	}
	info, err := i.files.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, ErrUnsupported
	}
	data, err := i.files.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() || int64(len(data)) > limit {
		return nil, ErrUnsupported
	}
	return data, ctx.Err()
}

func (i *installer) write(ctx context.Context, path string, data []byte) error {
	return i.writeSized(ctx, path, data, maxInputBytes)
}

func (i *installer) writeSized(ctx context.Context, path string, data []byte, limit int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if limit < 1 || int64(len(data)) > limit {
		return errors.New("protected installer file exceeds its bound")
	}
	path, err := relativePath(path)
	if err != nil {
		return err
	}
	info, statErr := i.files.Stat(path)
	if statErr == nil {
		if !info.Mode().IsRegular() || info.Size() > limit ||
			(i.nativeFiles && i.options.LocalHost.OS != "windows" && info.Mode().Perm()&0077 != 0) {
			return errors.New("protected installer file is unsafe")
		}
		current, err := i.files.ReadFile(path)
		if err != nil || int64(len(current)) != info.Size() {
			return errors.New("protected installer file could not be inspected")
		}
		if bytes.Equal(current, data) {
			return nil
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return errors.New("protected installer file could not be inspected")
	}
	next := path + ".installer-next"
	if err := i.files.WriteFile(next, data, 0600); err != nil {
		return errors.New("protected installer file could not be written")
	}
	if err := i.files.Rename(next, path); err != nil {
		return errors.New("protected installer file could not be published")
	}
	return nil
}
