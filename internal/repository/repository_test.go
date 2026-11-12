package repository

import (
	"errors"
	"reflect"
	"testing"
)

// Ported from noray test/spec/repository.test.mjs.

type item struct {
	ID    int
	Value string
}

func newRepo() *Repository[int, *item] {
	return New[int, *item](func(i *item) int { return i.ID }, func(c, u *item) *item { *c = *u; return c })
}

func TestAdd(t *testing.T) {
	r := newRepo()
	it := &item{0, "foo"}
	got, err := r.Add(it)
	if err != nil || got != it || len(r.List()) != 1 {
		t.Fatal("should add item")
	}
	if _, err := r.Add(&item{0, "bar"}); !errors.Is(err, ErrIDInUse) {
		t.Fatal("should reject items with same id")
	}
}

func TestUpdate(t *testing.T) {
	r := newRepo()
	r.Add(&item{0, "foo"})
	if err := r.Update(&item{0, "bar"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Find(0); v.Value != "bar" {
		t.Fatal("should update item")
	}
	if err := newRepo().Update(&item{0, "bar"}); !errors.Is(err, ErrUnknownItem) {
		t.Fatal("should reject unknown item")
	}
}

func TestFindHasList(t *testing.T) {
	r := newRepo()
	a, b := &item{0, "foo"}, &item{1, "bar"}
	if len(r.List()) != 0 {
		t.Fatal("should return empty")
	}
	r.Add(a)
	r.Add(b)
	if v, ok := r.Find(0); !ok || v != a {
		t.Fatal("should return known item")
	}
	if _, ok := r.Find(7); ok {
		t.Fatal("should return undefined on unknown")
	}
	if !r.Has(0) || r.Has(7) || !r.HasItem(b) || r.HasItem(&item{ID: 9}) {
		t.Fatal("has / hasItem")
	}
	if !reflect.DeepEqual(r.List(), []*item{a, b}) {
		t.Fatal("should return items in order")
	}
}

func TestRemove(t *testing.T) {
	r := newRepo()
	a := &item{0, "foo"}
	r.Add(a)
	if !r.Remove(0) || len(r.List()) != 0 {
		t.Fatal("should remove known")
	}
	if r.Remove(0) {
		t.Fatal("should ignore unknown")
	}
	r.Add(a)
	if !r.RemoveItem(a) || r.RemoveItem(a) {
		t.Fatal("removeItem")
	}
}
