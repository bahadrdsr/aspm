package acceptance

import (
	"context"
	"encoding/json"

	"github.com/bahadrdsr/aspm/internal/install"
)

func init() {
	Production.Plan = func(ctx context.Context, raw json.RawMessage) (InstallationPlan, error) {
		plan, err := install.Resolve(ctx, raw)
		return InstallationPlan{ID: plan.ID, Configuration: plan.Configuration}, err
	}
}
