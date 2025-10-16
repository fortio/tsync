// Package smap provides concurrent safe map.
package smap

import (
	"cmp"
	"iter"
	"slices"
	"sort"
	"sync"
)

type KV[K comparable, V any] struct {
	Key   K
	Value V
}

// Map is a concurrent safe map.
type Map[K comparable, V any] struct {
	mu      sync.RWMutex
	version uint64
	m       map[K]V
}

// New creates a new sync Map.
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		m: make(map[K]V),
	}
}

// Version returns the current version of the map. If any changes were made, the version is incremented by 1 each time.
func (s *Map[K, V]) Version() (current uint64) {
	s.mu.RLock()
	current = s.version
	s.mu.RUnlock()
	return current
}

func (s *Map[K, V]) Set(key K, value V) (newVersion uint64) {
	s.mu.Lock()
	s.m[key] = value
	s.version++
	newVersion = s.version
	s.mu.Unlock()
	return newVersion
}

func (s *Map[K, V]) MultiSet(kvs []KV[K, V]) (newVersion uint64) {
	s.mu.Lock()
	for _, kv := range kvs {
		s.m[kv.Key] = kv.Value
	}
	s.version++
	newVersion = s.version
	s.mu.Unlock()
	return newVersion
}

func (s *Map[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	value, ok := s.m[key]
	s.mu.RUnlock()
	return value, ok
}

func (s *Map[K, V]) Delete(key ...K) (newVersion uint64) {
	s.mu.Lock()
	for _, k := range key {
		delete(s.m, k)
	}
	s.version++
	newVersion = s.version
	s.mu.Unlock()
	return newVersion
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
	s.version++
	s.mu.Unlock()
}

// All returns an iterator over key-value pairs from the map.
// This allows ranging over the sync Map like a regular map using Go 1.24+ iterators.
// The iteration takes a read lock for the duration of going over the entries.
// If you wish to modify the map during iteration, you should postpone to after the loop.
// eg. accumulate entries in a slice and call s.Delete(toDeleteSlice) or [MultiSet] for instance.
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

// KeysSorted returns an iterator over keys sorted using the provided comparison function.
// The map snapshot occurs under a read lock, then sorting happens without holding the lock.
func (s *Map[K, V]) KeysSorted(less func(a, b K) bool) iter.Seq[K] {
	return func(yield func(K) bool) {
		s.mu.RLock()
		keys := make([]K, 0, len(s.m))
		for k := range s.m {
			keys = append(keys, k)
		}
		s.mu.RUnlock()

		sort.Slice(keys, func(i, j int) bool {
			return less(keys[i], keys[j])
		})

		for _, k := range keys {
			if !yield(k) {
				return
			}
		}
	}
}

// AllSorted returns an iterator over key-value pairs where keys are visited in the order defined by less.
// The keys snapshot occurs under a read lock, then sorting and value lookups happen without holding it.
// Because of that, by the time a key is revisited later, it may have been deleted; such entries are skipped.
func (s *Map[K, V]) AllSorted(less func(a, b K) bool) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		s.mu.RLock()
		keys := make([]K, 0, len(s.m))
		for k := range s.m {
			keys = append(keys, k)
		}
		s.mu.RUnlock()

		sort.Slice(keys, func(i, j int) bool {
			return less(keys[i], keys[j])
		})

		for _, k := range keys {
			s.mu.RLock()
			v, ok := s.m[k]
			s.mu.RUnlock()
			if !ok {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
	}
}

// NaturalSort returns an iterator that visits key-value pairs in the natural order of Q (using <).
// This requires Q (K from the Map[Q, V]) to be an ordered type.
func NaturalSort[Q cmp.Ordered, V any](s *Map[Q, V]) iter.Seq2[Q, V] {
	return func(yield func(Q, V) bool) {
		s.mu.RLock()
		keys := make([]Q, 0, len(s.m))
		for k := range s.m {
			keys = append(keys, k)
		}
		s.mu.RUnlock()
		slices.Sort(keys)
		for _, k := range keys {
			s.mu.RLock()
			v, ok := s.m[k]
			s.mu.RUnlock()
			if !ok {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
	}
}
