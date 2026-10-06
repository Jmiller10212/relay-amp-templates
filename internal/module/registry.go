package module

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

type Module interface {
	Name() string
	Dependencies() []string
	Init(context.Context) error
	RegisterHTTP(*http.ServeMux)
	RegisterWebSockets(*http.ServeMux)
	Start(context.Context) error
	Stop(context.Context) error
}

type Registry struct {
	available map[string]Module
	ordered   []Module
}

func New(modules ...Module) (*Registry, error) {
	r := &Registry{available: make(map[string]Module)}
	for _, m := range modules {
		if m == nil || m.Name() == "" {
			return nil, fmt.Errorf("module name cannot be empty")
		}
		if _, exists := r.available[m.Name()]; exists {
			return nil, fmt.Errorf("duplicate module %q", m.Name())
		}
		r.available[m.Name()] = m
	}
	return r, nil
}

func (r *Registry) Enable(names []string) error {
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		if _, ok := r.available[name]; !ok {
			return fmt.Errorf("unknown module %q", name)
		}
		enabled[name] = true
	}
	for name := range enabled {
		for _, dep := range r.available[name].Dependencies() {
			if !enabled[dep] {
				return fmt.Errorf("module %q requires enabled module %q", name, dep)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("module dependency cycle at %q", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		deps := append([]string(nil), r.available[name].Dependencies()...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		r.ordered = append(r.ordered, r.available[name])
		return nil
	}
	keys := make([]string, 0, len(enabled))
	for name := range enabled {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) Init(ctx context.Context) error {
	for _, m := range r.ordered {
		if err := m.Init(ctx); err != nil {
			return fmt.Errorf("init module %s: %w", m.Name(), err)
		}
	}
	return nil
}
func (r *Registry) Register(mux *http.ServeMux) {
	for _, m := range r.ordered {
		m.RegisterHTTP(mux)
		m.RegisterWebSockets(mux)
	}
}
func (r *Registry) Start(ctx context.Context) error {
	for _, m := range r.ordered {
		if err := m.Start(ctx); err != nil {
			return fmt.Errorf("start module %s: %w", m.Name(), err)
		}
	}
	return nil
}
func (r *Registry) Stop(ctx context.Context) error {
	var first error
	for i := len(r.ordered) - 1; i >= 0; i-- {
		if err := r.ordered[i].Stop(ctx); err != nil && first == nil {
			first = fmt.Errorf("stop module %s: %w", r.ordered[i].Name(), err)
		}
	}
	return first
}
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.ordered))
	for _, m := range r.ordered {
		out = append(out, m.Name())
	}
	return out
}
