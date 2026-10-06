package module

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

type fake struct {
	name   string
	deps   []string
	events *[]string
}

func (f *fake) Name() string                      { return f.name }
func (f *fake) Dependencies() []string            { return f.deps }
func (f *fake) Init(context.Context) error        { *f.events = append(*f.events, "init:"+f.name); return nil }
func (f *fake) RegisterHTTP(*http.ServeMux)       {}
func (f *fake) RegisterWebSockets(*http.ServeMux) {}
func (f *fake) Start(context.Context) error {
	*f.events = append(*f.events, "start:"+f.name)
	return nil
}
func (f *fake) Stop(context.Context) error { *f.events = append(*f.events, "stop:"+f.name); return nil }
func TestDependencyLifecycleOrder(t *testing.T) {
	var e []string
	a := &fake{name: "a", events: &e}
	b := &fake{name: "b", deps: []string{"a"}, events: &e}
	r, _ := New(b, a)
	if err := r.Enable([]string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	_ = r.Init(context.Background())
	_ = r.Start(context.Background())
	_ = r.Stop(context.Background())
	want := []string{"init:a", "init:b", "start:a", "start:b", "stop:b", "stop:a"}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("%v want %v", e, want)
	}
}
func TestMissingDependency(t *testing.T) {
	e := []string{}
	r, _ := New(&fake{name: "a", events: &e}, &fake{name: "b", deps: []string{"a"}, events: &e})
	if err := r.Enable([]string{"b"}); err == nil {
		t.Fatal("expected error")
	}
}
