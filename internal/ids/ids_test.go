package ids

import (
	"strings"
	"testing"
)

func TestNanoID(t *testing.T) {
	const alphabet = "useandom-26T198340PX75pxJACKVERYMINDBUSHWOLF_GQZbfghjklqvwyzrict"
	for _, size := range []int{4, 21, 128} {
		gen := NanoID(alphabet, size)
		seen := map[string]bool{}
		for i := 0; i < 200; i++ {
			id := gen()
			if len(id) != size {
				t.Fatalf("len %d", len(id))
			}
			for _, c := range id {
				if !strings.ContainsRune(alphabet, c) {
					t.Fatalf("char %q", c)
				}
			}
			seen[id] = true
		}
		if size >= 21 && len(seen) != 200 {
			t.Fatal("collisions")
		}
	}
	// small, odd alphabets work and are all used
	gen := NanoID("abc", 3000)
	id := gen()
	for _, c := range "abc" {
		if n := strings.Count(id, string(c)); n < 800 {
			t.Fatalf("not uniform: %c=%d", c, n)
		}
	}
}

func TestWordID(t *testing.T) {
	gen := WordID(3)
	id := gen()
	n := 0
	for _, c := range id {
		if c >= 'A' && c <= 'Z' {
			n++
		}
	}
	if n < 3 || len(Words) != 2638 {
		t.Fatalf("%q (%d words)", id, len(Words))
	}
}
