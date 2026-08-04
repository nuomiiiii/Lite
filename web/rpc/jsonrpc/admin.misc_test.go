package jsonrpc

import "testing"

func TestNormalizeAdminDefaultPageSize(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
		ok    bool
	}{
		{name: "minimum", value: float64(5), want: 5, ok: true},
		{name: "custom", value: float64(40), want: 40, ok: true},
		{name: "maximum", value: float64(100), want: 100, ok: true},
		{name: "below minimum", value: float64(4), ok: false},
		{name: "above maximum", value: float64(101), ok: false},
		{name: "fraction", value: 10.5, ok: false},
		{name: "wrong type", value: "30", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeAdminDefaultPageSize(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("normalizeAdminDefaultPageSize(%v) = (%d, %v), want (%d, %v)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}
