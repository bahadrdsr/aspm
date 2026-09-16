//go:build integration

package scopedreader

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

func init() {
	Production.Open = func(ctx context.Context, config evidence.Config) (Reader, error) {
		reader, err := evidence.OpenReader(ctx, config)
		if err != nil {
			return nil, err
		}
		return reader, nil
	}
}
