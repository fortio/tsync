// Package smap provides concurrent safe map.
package smap

import (
	"iter"
	"sync"
)

// Map is a concurrent safe map.
type Map[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

// New creates a new sync Map.
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		m: make(map[K]V),
	}
}

func (s *Map[K, V]) Set(key K, value V) {
	s.mu.Lock()
	s.m[key] = value
	s.mu.Unlock()
}

func (s *Map[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	value, ok := s.m[key]
	s.mu.RUnlock()
	return value, ok
}

func (s *Map[K, V]) Delete(key K) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

func (s *Map[K, V]) Has(key K) bool {
	s.mu.RLock()
	_, ok := s.m[key]
	s.mu.RUnlock()
	return ok
}

func (s *Map[K, V]) Len() int {
	s.mu.RLock()
	l := len(s.m)
	s.mu.RUnlock()
	return l
}

func (s *Map[K, V]) Clear() {
	s.mu.Lock()
	clear(s.m)
	s.mu.Unlock()
}

// All returns an iterator over key-value pairs from the map.
// This allows ranging over the sync Map like a regular map using Go 1.24+ iterators.
// The iteration takes a read lock for the duration of copying the entries.
func (s *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for k, v := range s.m {
			if !yield(k, v) {
				return
			}
		}
	}
}

// Values returns an iterator over values from the map.
// This allows ranging over just the values using Go 1.24+ iterators.
// The iteration takes a read lock for the duration of copying the entries.
func (s *Map[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, v := range s.m {
			if !yield(v) {
				return
			}
		}
	}
}

// Keys returns an iterator over keys from the map.
// This allows ranging over just the keys using Go 1.24+ iterators.
// The iteration takes a read lock for the duration of copying the entries.
func (s *Map[K, V]) Keys() iter.Seq[K] {
	return func(yield func(K) bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for k := range s.m {
			if !yield(k) {
				return
			}
		}
	}
}
