// Package adaptertest provides a reusable conformance suite for compiled-in
// report adapters.
package adaptertest

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
)

type Expected struct {
	SourceFindingID string
	Title           string
	SourceSeverity  string
	Severity        string
	Location        reportadapter.Location
	Impact          string
	Remediation     string
	Unmapped        map[string]any
}

type Suite struct {
	Adapter            reportadapter.Adapter
	Mapping            reportadapter.Mapping
	Valid              []byte
	UnsupportedVersion []byte
	Malformed          []byte
	OverLimit          []byte
	Expected           Expected
}

func Run(t *testing.T, suite Suite) {
	t.Helper()
	if suite.Adapter == nil || len(suite.Valid) == 0 ||
		len(suite.UnsupportedVersion) == 0 || len(suite.OverLimit) == 0 ||
		len(suite.Valid) > reportadapter.MaxReportBytes ||
		len(suite.UnsupportedVersion) > reportadapter.MaxReportBytes ||
		len(suite.OverLimit) > reportadapter.MaxReportBytes {
		t.Fatal("adapter conformance requires an adapter and all bounded fixtures")
	}
	malformed := suite.Malformed
	if len(malformed) == 0 {
		malformed = []byte("{")
	}

	t.Run("descriptor", func(t *testing.T) {
		if err := reportadapter.ValidateDescriptor(suite.Adapter.Descriptor()); err != nil {
			t.Fatalf("invalid adapter descriptor: %v", err)
		}
	})

	t.Run("semantic-fields-and-stable-identity", func(t *testing.T) {
		first, err := reportadapter.Parse(suite.Adapter, suite.Valid, suite.Mapping)
		if err != nil || len(first) != 1 {
			t.Fatalf("valid fixture was not admitted: findings=%d err=%v", len(first), err)
		}
		second, err := reportadapter.Parse(suite.Adapter, suite.Valid, suite.Mapping)
		if err != nil || len(second) != 1 || first[0].Identity != second[0].Identity {
			t.Fatal("identical source input did not retain a stable identity")
		}
		finding := first[0]
		if finding.SourceFindingID != suite.Expected.SourceFindingID ||
			finding.Title != suite.Expected.Title ||
			finding.SourceSeverity != suite.Expected.SourceSeverity ||
			finding.Severity != suite.Expected.Severity ||
			finding.Location != suite.Expected.Location ||
			finding.Impact != suite.Expected.Impact ||
			finding.Remediation != suite.Expected.Remediation {
			t.Fatalf("adapter lost required semantic fields: %#v", finding)
		}
		for key, expected := range suite.Expected.Unmapped {
			if !reflect.DeepEqual(finding.Unmapped[key], expected) {
				t.Fatalf("adapter lost unmapped field %q: got %#v want %#v", key, finding.Unmapped[key], expected)
			}
		}
	})

	for name, data := range map[string][]byte{
		"empty":               nil,
		"malformed":           malformed,
		"unsupported-version": suite.UnsupportedVersion,
		"over-limit":          suite.OverLimit,
		"invalid-utf8":        {0xff},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := reportadapter.Parse(suite.Adapter, data, suite.Mapping); !errors.Is(err, reportadapter.ErrInvalid) {
				t.Fatalf("invalid fixture returned %v", err)
			}
		})
	}

	t.Run("oversized", func(t *testing.T) {
		data := bytes.Repeat([]byte("x"), reportadapter.MaxReportBytes+1)
		if _, err := reportadapter.Parse(suite.Adapter, data, suite.Mapping); !errors.Is(err, reportadapter.ErrInvalid) {
			t.Fatalf("oversized input returned %v", err)
		}
	})
}
