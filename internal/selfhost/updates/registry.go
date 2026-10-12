package updates

import (
	"errors"
	"fmt"
	"sort"

	"usesesame.app/backend/internal/selfhost"
)

const (
	ServerProductID = "sesame-server"
	ChannelStable   = "stable"
	ChannelBeta     = "beta"
)

type ProductSpec struct {
	ID       string
	Channels []string
}

func (p ProductSpec) HasChannel(channel string) bool {
	for _, known := range p.Channels {
		if known == channel {
			return true
		}
	}
	return false
}

type Registry struct {
	products map[string]ProductSpec
}

func NewRegistry() *Registry {
	return &Registry{products: map[string]ProductSpec{}}
}

func DefaultRegistry() *Registry {
	registry := NewRegistry()
	if err := registry.Register(ProductSpec{ID: ServerProductID, Channels: []string{ChannelStable, ChannelBeta}}); err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Register(spec ProductSpec) error {
	if !validProductID(spec.ID) {
		return fmt.Errorf("updates: product id %q is not valid", spec.ID)
	}
	if len(spec.Channels) == 0 {
		return errors.New("updates: a product needs at least one channel")
	}
	for _, channel := range spec.Channels {
		if !validChannel(channel) {
			return fmt.Errorf("updates: channel %q is not valid", channel)
		}
	}
	if _, exists := r.products[spec.ID]; exists {
		return fmt.Errorf("updates: product %q is already registered", spec.ID)
	}
	r.products[spec.ID] = ProductSpec{ID: spec.ID, Channels: append([]string(nil), spec.Channels...)}
	return nil
}

func (r *Registry) Lookup(id string) (ProductSpec, bool) {
	spec, ok := r.products[id]
	return spec, ok
}

func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.products))
	for id := range r.products {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validProductID(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

func validChannel(value string) bool {
	return selfhost.ValidUpdateChannel(value)
}
