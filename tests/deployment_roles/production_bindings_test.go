package deployment_roles

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/install/quadlet"
)

func init() {
	Production.RenderQuadletRole = func(ctx context.Context, source []byte, config QuadletRoleConfig) ([]byte, error) {
		return quadlet.RenderRole(ctx, source, quadlet.Config{
			Role:             config.Role,
			EnvironmentFile:  config.EnvironmentFile,
			RawPrefix:        config.RawPrefix,
			ReadinessKey:     config.ReadinessKey,
			NormalizedPrefix: config.NormalizedPrefix,
		})
	}
}
