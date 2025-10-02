package tcrypto_test

import (
	"math"
	"strconv"
	"testing"

	"fortio.org/tsync/tcrypto"
)

func TestHumanHash(t *testing.T) {
	for _, tc := range []struct {
		input    []byte
		expected string
	}{
		// used the actual values produced and eyeballed them
		{nil, "766-2806"},
		{[]byte{}, "766-2806"},
		{[]byte("hello"), "427-5636"},
		{[]byte("heLlo"), "002-9560"},
		{[]byte("The quick brown fox jumps over the lazy dog"), "589-5887"},
		{[]byte("The quick brown fox jumqs over the lazy dog"), "203-0565"},
	} {
		hs := tcrypto.HumanHash(tc.input)
		if hs != tc.expected {
			t.Errorf("Unexpected hash for %q: %s instead of %s", tc.input, hs, tc.expected)
		}
	}
}

func expectedCollisions(m, n int) int {
	// expected pairs
	return int(math.Round(float64(m*(m-1)) / (2. * float64(n))))
}

func TestHumanHashDistribution(t *testing.T) {
	set := map[string]string{}
	const N = 100_000 // there is 5% chance of >= 1 collision with 1000 inputs but it happens we don't get any
	// for 10k we get 4 pairs though expectation is 5
	collisionCount := 0
	for i := range N {
		input := strconv.Itoa(i)
		h := tcrypto.HumanHash([]byte(input))
		if before, ok := set[h]; ok {
			t.Logf("Hash collision for input %q and %q -> %s", input, before, h)
			collisionCount++
		}
		set[h] = input
	}
	expect := expectedCollisions(N, 1000_0000) // N vs 7 digits
	plusMinus := 1 + 3*expect/100              // 3% margin
	t.Logf("Got %d collisions for %d inputs (expecting %d +/- %d)", collisionCount, N, expect, plusMinus)
	if collisionCount < expect-plusMinus || collisionCount > expect+plusMinus {
		t.Errorf("Unexpected number of collisions for %d inputs: %d (vs %d +/- %d)", N, collisionCount, expect, plusMinus)
	}
}
