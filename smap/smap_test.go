package smap

import (
	"sync"
	"testing"
)

// Note: these tests were generated.

func TestSetAndGet(t *testing.T) {
	m := New[string, int]()

	// Set and get a value
	m.Set("foo", 42)
	val, ok := m.Get("foo")
	if !ok {
		t.Error("Expected key 'foo' to exist")
	}
	if val != 42 {
		t.Errorf("Expected value 42, got %d", val)
	}

	// Get non-existent key
	_, ok = m.Get("bar")
	if ok {
		t.Error("Expected key 'bar' to not exist")
	}

	// Update existing key
	m.Set("foo", 100)
	val, ok = m.Get("foo")
	if !ok || val != 100 {
		t.Errorf("Expected updated value 100, got %d", val)
	}
}

func TestDelete(t *testing.T) {
	m := New[string, int]()
	m.Set("foo", 1)
	m.Set("bar", 2)

	// Delete existing key
	m.Delete("foo")
	_, ok := m.Get("foo")
	if ok {
		t.Error("Expected key 'foo' to be deleted")
	}

	// Verify other key still exists
	val, ok := m.Get("bar")
	if !ok || val != 2 {
		t.Error("Expected key 'bar' to still exist")
	}

	// Delete non-existent key (should not panic)
	m.Delete("nonexistent")
}

func TestHas(t *testing.T) {
	m := New[string, int]()

	// Check non-existent key
	if m.Has("foo") {
		t.Error("Expected Has to return false for non-existent key")
	}

	// Set and check
	m.Set("foo", 42)
	if !m.Has("foo") {
		t.Error("Expected Has to return true for existing key")
	}

	// Delete and check
	m.Delete("foo")
	if m.Has("foo") {
		t.Error("Expected Has to return false after delete")
	}
}

func TestLen(t *testing.T) {
	m := New[string, int]()

	// Empty map
	if m.Len() != 0 {
		t.Errorf("Expected length 0, got %d", m.Len())
	}

	// Add items
	m.Set("a", 1)
	if m.Len() != 1 {
		t.Errorf("Expected length 1, got %d", m.Len())
	}

	m.Set("b", 2)
	m.Set("c", 3)
	if m.Len() != 3 {
		t.Errorf("Expected length 3, got %d", m.Len())
	}

	// Update existing (shouldn't change length)
	m.Set("a", 10)
	if m.Len() != 3 {
		t.Errorf("Expected length 3 after update, got %d", m.Len())
	}

	// Delete item
	m.Delete("b")
	if m.Len() != 2 {
		t.Errorf("Expected length 2 after delete, got %d", m.Len())
	}
}

func TestClear(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	// Verify items exist
	if m.Len() != 3 {
		t.Error("Expected 3 items before clear")
	}

	// Clear
	m.Clear()

	// Verify map is empty
	if m.Len() != 0 {
		t.Errorf("Expected length 0 after clear, got %d", m.Len())
	}

	if m.Has("a") || m.Has("b") || m.Has("c") {
		t.Error("Expected all keys to be removed after clear")
	}

	// Clear empty map (should not panic)
	m.Clear()
	if m.Len() != 0 {
		t.Error("Expected length to remain 0")
	}
}

func TestAll(t *testing.T) {
	m := New[string, int]()
	m.Set("foo", 1)
	m.Set("bar", 2)
	m.Set("baz", 3)

	// Collect all key-value pairs
	collected := make(map[string]int)
	for k, v := range m.All() {
		collected[k] = v
	}

	// Verify all entries were collected
	if len(collected) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(collected))
	}
	if collected["foo"] != 1 {
		t.Errorf("Expected foo=1, got %d", collected["foo"])
	}
	if collected["bar"] != 2 {
		t.Errorf("Expected bar=2, got %d", collected["bar"])
	}
	if collected["baz"] != 3 {
		t.Errorf("Expected baz=3, got %d", collected["baz"])
	}
}

func TestAllEmpty(t *testing.T) {
	m := New[string, int]()

	count := 0
	for range m.All() {
		count++
	}

	if count != 0 {
		t.Errorf("Expected 0 iterations for empty map, got %d", count)
	}
}

func TestAllEarlyTermination(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)
	m.Set("d", 4)

	count := 0
	for range m.All() {
		count++
		if count == 2 {
			break
		}
	}

	if count != 2 {
		t.Errorf("Expected early termination at 2 iterations, got %d", count)
	}
}

func TestValues(t *testing.T) {
	m := New[string, int]()
	m.Set("foo", 1)
	m.Set("bar", 2)
	m.Set("baz", 3)

	// Collect all values
	values := make(map[int]bool)
	for v := range m.Values() {
		values[v] = true
	}

	// Verify all values were collected
	if len(values) != 3 {
		t.Errorf("Expected 3 values, got %d", len(values))
	}
	if !values[1] || !values[2] || !values[3] {
		t.Errorf("Expected values 1, 2, 3, got %v", values)
	}
}

func TestValuesEmpty(t *testing.T) {
	m := New[string, int]()

	count := 0
	for range m.Values() {
		count++
	}

	if count != 0 {
		t.Errorf("Expected 0 iterations for empty map, got %d", count)
	}
}

func TestValuesEarlyTermination(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	count := 0
	for range m.Values() {
		count++
		if count == 1 {
			break
		}
	}

	if count != 1 {
		t.Errorf("Expected early termination at 1 iteration, got %d", count)
	}
}

func TestKeys(t *testing.T) {
	m := New[string, int]()
	m.Set("foo", 1)
	m.Set("bar", 2)
	m.Set("baz", 3)

	// Collect all keys
	keys := make(map[string]bool)
	for k := range m.Keys() {
		keys[k] = true
	}

	// Verify all keys were collected
	if len(keys) != 3 {
		t.Errorf("Expected 3 keys, got %d", len(keys))
	}
	if !keys["foo"] || !keys["bar"] || !keys["baz"] {
		t.Errorf("Expected keys foo, bar, baz, got %v", keys)
	}
}

func TestKeysEmpty(t *testing.T) {
	m := New[string, int]()

	count := 0
	for range m.Keys() {
		count++
	}

	if count != 0 {
		t.Errorf("Expected 0 iterations for empty map, got %d", count)
	}
}

func TestKeysEarlyTermination(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)
	m.Set("d", 4)
	m.Set("e", 5)

	count := 0
	for range m.Keys() {
		count++
		if count == 3 {
			break
		}
	}

	if count != 3 {
		t.Errorf("Expected early termination at 3 iterations, got %d", count)
	}
}

func TestKeysSorted(t *testing.T) {
	m := New[string, int]()
	m.Set("c", 3)
	m.Set("a", 1)
	m.Set("b", 2)

	expected := []string{"a", "b", "c"}
	idx := 0
	for k := range m.KeysSorted(func(a, b string) bool { return a < b }) {
		if idx >= len(expected) {
			t.Fatalf("received more keys than expected; extra key %q", k)
		}
		if k != expected[idx] {
			t.Fatalf("expected key %q at position %d, got %q", expected[idx], idx, k)
		}
		idx++
	}
	if idx != len(expected) {
		t.Fatalf("expected %d keys, got %d", len(expected), idx)
	}

	t.Run("earlyTermination", func(t *testing.T) {
		seqMap := New[string, int]()
		seqMap.Set("y", 2)
		seqMap.Set("x", 1)
		seq := seqMap.KeysSorted(func(a, b string) bool { return a < b })
		calls := 0
		seq(func(string) bool {
			calls++
			return false
		})
		if calls != 1 {
			t.Fatalf("expected to stop after 1 iteration, got %d", calls)
		}
	})

	t.Run("structKeyCustomSort", func(t *testing.T) {
		type compoundKey struct {
			label    string
			priority int
		}

		s := New[compoundKey, int]()
		s.Set(compoundKey{label: "gamma", priority: 2}, 20)
		s.Set(compoundKey{label: "alpha", priority: 3}, 30)
		s.Set(compoundKey{label: "omega", priority: 1}, 10)

		less := func(a, b compoundKey) bool {
			return a.priority < b.priority
		}

		order := make([]compoundKey, 0, 3)
		for k := range s.KeysSorted(less) {
			order = append(order, k)
		}

		expectedOrder := []compoundKey{
			{label: "omega", priority: 1},
			{label: "gamma", priority: 2},
			{label: "alpha", priority: 3},
		}

		if len(order) != len(expectedOrder) {
			t.Fatalf("expected %d keys, got %d", len(expectedOrder), len(order))
		}

		for i, got := range order {
			want := expectedOrder[i]
			if got != want {
				t.Fatalf("at position %d expected %v, got %v", i, want, got)
			}
		}
	})
}

func TestAllSorted(t *testing.T) {
	m := New[int, string]()
	m.Set(3, "three")
	m.Set(1, "one")
	m.Set(2, "two")

	visited := make([]KV[int, string], 0, 3)
	for k, v := range m.AllSorted(func(a, b int) bool { return a < b }) {
		visited = append(visited, KV[int, string]{Key: k, Value: v})
		if k == 1 {
			m.Delete(2)
		}
	}

	expected := []KV[int, string]{
		{Key: 1, Value: "one"},
		{Key: 3, Value: "three"},
	}

	if len(visited) != len(expected) {
		t.Fatalf("expected %d key/value pairs, got %d", len(expected), len(visited))
	}

	for i, kv := range expected {
		if visited[i] != kv {
			t.Fatalf("expected pair %v at position %d, got %v", kv, i, visited[i])
		}
	}

	t.Run("earlyTermination", func(t *testing.T) {
		seqMap := New[int, string]()
		seqMap.Set(2, "two")
		seqMap.Set(1, "one")
		seq := seqMap.AllSorted(func(a, b int) bool { return a < b })
		calls := 0
		seq(func(int, string) bool {
			calls++
			return false
		})
		if calls != 1 {
			t.Fatalf("expected to stop after 1 iteration, got %d", calls)
		}
	})
}

func TestIteratorWithDifferentTypes(t *testing.T) {
	// Test with different types
	m := New[int, string]()
	m.Set(1, "one")
	m.Set(2, "two")
	m.Set(3, "three")

	// Test All()
	count := 0
	for k, v := range m.All() {
		if k == 1 && v != "one" {
			t.Errorf("Expected key 1 to have value 'one', got '%s'", v)
		}
		count++
	}
	if count != 3 {
		t.Errorf("Expected 3 items, got %d", count)
	}

	// Test Keys()
	keyCount := 0
	for k := range m.Keys() {
		if k < 1 || k > 3 {
			t.Errorf("Unexpected key: %d", k)
		}
		keyCount++
	}
	if keyCount != 3 {
		t.Errorf("Expected 3 keys, got %d", keyCount)
	}

	// Test Values()
	valueCount := 0
	for v := range m.Values() {
		if v != "one" && v != "two" && v != "three" {
			t.Errorf("Unexpected value: %s", v)
		}
		valueCount++
	}
	if valueCount != 3 {
		t.Errorf("Expected 3 values, got %d", valueCount)
	}
}

func TestIteratorAfterModification(t *testing.T) {
	m := New[string, int]()
	m.Set("foo", 1)
	m.Set("bar", 2)

	// Iterate and verify
	count := 0
	for range m.All() {
		count++
	}
	if count != 2 {
		t.Errorf("Expected 2 items before modification, got %d", count)
	}

	// Modify map
	m.Set("baz", 3)
	m.Delete("foo")

	// Iterate again and verify changes
	collected := make(map[string]int)
	for k, v := range m.All() {
		collected[k] = v
	}

	if len(collected) != 2 {
		t.Errorf("Expected 2 items after modification, got %d", len(collected))
	}
	if _, exists := collected["foo"]; exists {
		t.Error("Expected 'foo' to be deleted")
	}
	if collected["bar"] != 2 {
		t.Error("Expected 'bar' to still exist with value 2")
	}
	if collected["baz"] != 3 {
		t.Error("Expected 'baz' to exist with value 3")
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	m := New[int, int]()
	const numGoroutines = 100
	const numOperations = 1000

	// Start multiple goroutines performing various operations
	done := make(chan bool, numGoroutines)

	// Writers
	for i := range numGoroutines / 2 {
		go func(id int) {
			for j := range numOperations {
				key := (id * numOperations) + j
				m.Set(key, key*2)
			}
			done <- true
		}(i)
	}

	// Readers
	for i := range numGoroutines / 4 {
		go func(id int) {
			for j := range numOperations {
				key := (id * numOperations) + j
				m.Get(key)
				m.Has(key)
			}
			done <- true
		}(i)
	}

	// Mixed operations (read, write, delete)
	for i := range numGoroutines / 4 {
		go func(id int) {
			for j := range numOperations {
				key := (id * numOperations) + j
				m.Set(key, j)
				m.Get(key)
				if j%2 == 0 {
					m.Delete(key)
				}
				m.Has(key)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for range numGoroutines {
		<-done
	}

	// Verify map is still functional
	m.Set(999999, 123)
	val, ok := m.Get(999999)
	if !ok || val != 123 {
		t.Error("Map should still be functional after concurrent operations")
	}
}

func TestConcurrentIterators(t *testing.T) {
	m := New[string, int]()
	const numGoroutines = 30
	const numItems = 100

	// Pre-populate the map
	for i := range numItems {
		m.Set(string(rune('A'+i%26))+string(rune('0'+i/26)), i)
	}

	done := make(chan bool, numGoroutines+1)

	// Multiple goroutines iterating with All()
	for range numGoroutines / 3 {
		go func() {
			defer func() { done <- true }()
			count := 0
			for range m.All() {
				count++
			}
			// Just verify iteration completes without panic
		}()
	}

	// Multiple goroutines iterating with Keys()
	for range numGoroutines / 3 {
		go func() {
			defer func() { done <- true }()
			count := 0
			for range m.Keys() {
				count++
			}
		}()
	}

	// Multiple goroutines iterating with Values()
	for range numGoroutines / 3 {
		go func() {
			defer func() { done <- true }()
			count := 0
			for range m.Values() {
				count++
			}
		}()
	}

	// Concurrent writes while iterating
	go func() {
		defer func() { done <- true }()
		for i := range 100 {
			m.Set("concurrent", i)
		}
	}()
	// Wait for all goroutines
	for range numGoroutines + 1 {
		<-done
	}
	// Verify map is still functional after concurrent iterations
	m.Set("test", 999)
	val, ok := m.Get("test")
	if !ok || val != 999 {
		t.Error("Map should still be functional after concurrent iterations")
	}
}

func TestConcurrentClearAndLen(t *testing.T) {
	m := New[int, string]()
	const numGoroutines = 20
	done := make(chan bool, numGoroutines)

	// Goroutines adding items
	for i := range numGoroutines / 2 {
		go func(id int) {
			for j := range 100 {
				m.Set(id*100+j, "value")
			}
			done <- true
		}(i)
	}

	// Goroutines checking length
	for range numGoroutines / 4 {
		go func() {
			for range 100 {
				_ = m.Len()
			}
			done <- true
		}()
	}

	// Goroutines clearing (after a bit)
	for range numGoroutines / 4 {
		go func() {
			for range 10 {
				m.Clear()
			}
			done <- true
		}()
	}

	// Wait for all
	for range numGoroutines {
		<-done
	}

	// Map should still be functional
	m.Clear()
	m.Set(1, "test")
	if m.Len() != 1 {
		t.Errorf("Expected length 1, got %d", m.Len())
	}
}

func TestVersion(t *testing.T) {
	m := New[string, int]()
	initial := m.Version()
	m.Set("a", 1)
	if m.Version() == initial {
		t.Error("Version should increment after Set")
	}
	m.Set("b", 2)
	if m.Version() <= initial+1 {
		t.Error("Version should increment again after another Set")
	}
	m.Delete("a")
	if m.Version() <= initial+2 {
		t.Error("Version should increment after Delete")
	}
}

func TestMultiSet(t *testing.T) {
	m := New[string, int]()
	kvs := []KV[string, int]{
		{"x", 10},
		{"y", 20},
		{"z", 30},
	}
	m.MultiSet(kvs)
	if m.Len() != 3 {
		t.Errorf("Expected 3 entries after MultiSet, got %d", m.Len())
	}
	for _, kv := range kvs {
		v, ok := m.Get(kv.Key)
		if !ok || v != kv.Value {
			t.Errorf("Expected %s=%d, got %d", kv.Key, kv.Value, v)
		}
	}
}

func TestDeleteMultiple(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)
	m.Delete("a", "b")
	if m.Has("a") || m.Has("b") {
		t.Error("Keys 'a' and 'b' should be deleted")
	}
	if !m.Has("c") {
		t.Error("Key 'c' should still exist")
	}
}

// This deadlocks (by design/as documented) so isn't actually a test.
func DeleteDuringIterationDeadlock(t *testing.T) {
	m := New[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for k := range m.Keys() {
			m.Delete(k) // This should deadlock due to lock ordering
		}
		wg.Done()
	}()
	wg.Wait()
	t.Errorf("Unexpected no hang")
}

func TestAllNaturalSort(t *testing.T) { //nolint:gocognit // it's a test!
	t.Run("int", func(t *testing.T) {
		m := New[int, string]()
		m.Set(3, "three")
		m.Set(1, "one")
		m.Set(2, "two")

		expected := []int{1, 2, 3}
		i := 0
		for k, v := range NaturalSort(m) {
			if k != expected[i] {
				t.Errorf("Expected key %d, got %d", expected[i], k)
			}
			if i == 0 && v != "one" {
				t.Errorf("Expected value 'one', got '%s'", v)
			}
			i++
		}
		if i != 3 {
			t.Errorf("Expected 3 iterations, got %d", i)
		}
	})

	t.Run("string", func(t *testing.T) {
		m := New[string, int]()
		m.Set("charlie", 3)
		m.Set("alice", 1)
		m.Set("bob", 2)

		expected := []string{"alice", "bob", "charlie"}
		i := 0
		for k, v := range NaturalSort(m) {
			if k != expected[i] {
				t.Errorf("Expected key %s, got %s", expected[i], k)
			}
			if i == 0 && v != 1 {
				t.Errorf("Expected value 1, got %d", v)
			}
			i++
		}
		if i != 3 {
			t.Errorf("Expected 3 iterations, got %d", i)
		}
	})

	t.Run("keyDeletedDuringIteration", func(t *testing.T) {
		m := New[int, string]()
		m.Set(3, "three")
		m.Set(1, "one")
		m.Set(2, "two")
		m.Set(4, "four")

		visited := make([]KV[int, string], 0, 4)
		for k, v := range NaturalSort(m) {
			visited = append(visited, KV[int, string]{Key: k, Value: v})
			// Delete key 2 after visiting key 1
			if k == 1 {
				m.Delete(2)
			}
		}

		// Expected: 1, 3, 4 (key 2 should be skipped because it was deleted)
		expected := []KV[int, string]{
			{Key: 1, Value: "one"},
			{Key: 3, Value: "three"},
			{Key: 4, Value: "four"},
		}

		if len(visited) != len(expected) {
			t.Fatalf("expected %d key/value pairs, got %d", len(expected), len(visited))
		}

		for i, kv := range expected {
			if visited[i] != kv {
				t.Fatalf("expected pair %v at position %d, got %v", kv, i, visited[i])
			}
		}
	})

	t.Run("earlyTermination", func(t *testing.T) {
		m := New[int, string]()
		m.Set(3, "three")
		m.Set(1, "one")
		m.Set(2, "two")
		m.Set(4, "four")

		calls := 0
		for range NaturalSort(m) {
			calls++
			if calls == 2 {
				break
			}
		}

		if calls != 2 {
			t.Fatalf("expected to stop after 2 iterations, got %d", calls)
		}
	})

	t.Run("earlyTerminationViaYield", func(t *testing.T) {
		m := New[int, string]()
		m.Set(5, "five")
		m.Set(1, "one")
		m.Set(3, "three")

		seq := NaturalSort(m)
		calls := 0
		seq(func(int, string) bool {
			calls++
			return false // Stop immediately
		})

		if calls != 1 {
			t.Fatalf("expected to stop after 1 iteration when yield returns false, got %d", calls)
		}
	})
}
