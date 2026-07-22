package nacos

import "testing"

// TestSplitAddress is a pure unit test for address parsing.
func TestSplitAddress(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port uint64
	}{
		{"10.0.0.1:8848", "10.0.0.1", 8848},
		{"nacos-host", "nacos-host", defaultPort},
	}
	for _, c := range cases {
		h, p, err := splitAddress(c.in)
		if err != nil || h != c.host || p != c.port {
			t.Fatalf("splitAddress(%q) = (%q,%d,%v), want (%q,%d)", c.in, h, p, err, c.host, c.port)
		}
	}
}
