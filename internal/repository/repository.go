// Package repository is a generic in-memory store keyed by an id derived from
// each item, keeping insertion order (the original src/repository.mjs).
// It is not safe for concurrent use; callers lock.
package repository

import (
	"container/list"
	"errors"
	"fmt"
)

var (
	// ErrIDInUse is returned by Add for a duplicate id.
	ErrIDInUse = errors.New("id in use")
	// ErrUnknownItem is returned by Update for an unknown id.
	ErrUnknownItem = errors.New("unknown item")
)

// Repository stores items of type T by id K.
type Repository[K comparable, T any] struct {
	idOf  func(T) K
	merge func(current, update T) T
	order *list.List
	items map[K]*list.Element
}

// New creates a repository. merge may be nil to replace items on Update.
func New[K comparable, T any](idOf func(T) K, merge func(current, update T) T) *Repository[K, T] {
	if merge == nil {
		merge = func(_, u T) T { return u }
	}
	return &Repository[K, T]{idOf: idOf, merge: merge, order: list.New(), items: map[K]*list.Element{}}
}

// Add stores item; ErrIDInUse if its id is taken.
func (r *Repository[K, T]) Add(item T) (T, error) {
	id := r.idOf(item)
	if _, ok := r.items[id]; ok {
		var zero T
		return zero, fmt.Errorf("%w: Item already stored with id: %v", ErrIDInUse, id)
	}
	r.items[id] = r.order.PushBack(item)
	return item, nil
}

// Update merges item into the stored one with the same id.
func (r *Repository[K, T]) Update(item T) error {
	id := r.idOf(item)
	el, ok := r.items[id]
	if !ok {
		return fmt.Errorf("%w: Trying to update unknown item with id: %v", ErrUnknownItem, id)
	}
	el.Value = r.merge(el.Value.(T), item)
	return nil
}

// Find returns the item with id.
func (r *Repository[K, T]) Find(id K) (T, bool) {
	if el, ok := r.items[id]; ok {
		return el.Value.(T), true
	}
	var zero T
	return zero, false
}

// Has reports whether id is stored.
func (r *Repository[K, T]) Has(id K) bool { _, ok := r.items[id]; return ok }

// List returns the items in insertion order.
func (r *Repository[K, T]) List() []T {
	out := make([]T, 0, len(r.items))
	for el := r.order.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(T))
	}
	return out
}

// Len is the number of items.
func (r *Repository[K, T]) Len() int { return len(r.items) }

// Remove deletes by id; false if unknown.
func (r *Repository[K, T]) Remove(id K) bool {
	el, ok := r.items[id]
	if !ok {
		return false
	}
	r.order.Remove(el)
	delete(r.items, id)
	return true
}

// HasItem reports whether the item's id is stored.
func (r *Repository[K, T]) HasItem(item T) bool { return r.Has(r.idOf(item)) }

// RemoveItem deletes by the item's id.
func (r *Repository[K, T]) RemoveItem(item T) bool { return r.Remove(r.idOf(item)) }
