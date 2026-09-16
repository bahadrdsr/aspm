package contractdata

import "embed"

// Files is the versioned, reviewed specification used by both the planner and
// distribution. Embedded schemas cannot trigger a runtime registry download.
//
//go:embed v1alpha1/*.json
var Files embed.FS
