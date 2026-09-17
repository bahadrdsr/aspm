//go:build integration && collection_runtime_rights

package collection_runtime

import "testing"

func TestCollectionRuntimeOwnedStorageRightsCalibration(t *testing.T) {
	f := newFixture(t)
	f.rights()
	t.Log("fixture calibration only: actual existing local store keys proved publisher write, core read and core write denial; no service/runtime business behavior executed")
}
