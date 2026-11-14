package guard

import "maps"

// cloneOrEmpty returns a copy of m that a caller may write to: an empty map when m is nil, since
// maps.Clone returns nil for it.
func cloneOrEmpty[M ~map[K]V, K comparable, V any](m M) M {
	cloned := maps.Clone(m)
	if cloned == nil {
		cloned = M{}
	}

	return cloned
}
