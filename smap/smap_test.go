package smap

import (
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
