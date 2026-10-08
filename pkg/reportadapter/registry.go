package reportadapter

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type registryEntry struct {
	adapter    Adapter
	descriptor Descriptor
}

// Registry is an immutable, deterministic set of compiled-in adapters.
type Registry struct {
	entries []registryEntry
	byID    map[string]registryEntry
}

func NewRegistry(adapters ...Adapter) (*Registry, error) {
	entries := make([]registryEntry, 0, len(adapters))
	byID := make(map[string]registryEntry, len(adapters))
	for _, adapter := range adapters {
		if isNilAdapter(adapter) {
			return nil, ErrInvalid
		}
		descriptor := adapter.Descriptor().Clone()
		if err := ValidateDescriptor(descriptor); err != nil {
			return nil, fmt.Errorf("%w: adapter descriptor", ErrInvalid)
		}
		if _, exists := byID[descriptor.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate adapter %q", ErrInvalid, descriptor.ID)
		}
		entry := registryEntry{adapter: adapter, descriptor: descriptor}
		entries = append(entries, entry)
		byID[descriptor.ID] = entry
	}
	slices.SortFunc(entries, func(a, b registryEntry) int {
		return strings.Compare(a.descriptor.ID, b.descriptor.ID)
	})
	return &Registry{entries: entries, byID: byID}, nil
}

func MustNewRegistry(adapters ...Adapter) *Registry {
	registry, err := NewRegistry(adapters...)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Supports(id string) bool {
	if r == nil {
		return false
	}
	_, found := r.byID[id]
	return found
}

func (r *Registry) Descriptors() []Descriptor {
	if r == nil {
		return []Descriptor{}
	}
	result := make([]Descriptor, len(r.entries))
	for i, entry := range r.entries {
		result[i] = entry.descriptor.Clone()
	}
	return result
}

func (r *Registry) ValidateMapping(id string, mapping Mapping) error {
	if r == nil {
		return ErrUnsupported
	}
	entry, found := r.byID[id]
	if !found {
		return ErrUnsupported
	}
	if err := entry.adapter.ValidateMapping(mapping); err != nil {
		if errors.Is(err, ErrUnsupported) {
			return ErrUnsupported
		}
		return ErrInvalid
	}
	return nil
}

func (r *Registry) Parse(id string, data []byte, mapping Mapping) ([]Finding, error) {
	if r == nil {
		return nil, ErrUnsupported
	}
	entry, found := r.byID[id]
	if !found {
		return nil, ErrUnsupported
	}
	return Parse(entry.adapter, data, mapping)
}
