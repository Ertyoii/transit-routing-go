package tripbased

// transferLinkStorage packs immutable per-exit rows into chunks. Growing one
// unfinished row copies only that row; previously published rows are never
// moved or overwritten. Transfer emission and its guards remain in newIndex.
// A finished row has no spare capacity, so a later append cannot overwrite
// a neighbouring row. The builder is construction-local, not a shared cache.
type transferLinkStorage struct {
	data                  []link
	start, emitted, limit int
}

const transferLinkChunk = 4096

func (s *transferLinkStorage) begin() { s.start = len(s.data) }
func (s *transferLinkStorage) append(v link) {
	if len(s.data) == cap(s.data) {
		n := len(s.data) - s.start
		// No row can exceed the remaining declared transfer count. Multiplication
		// is bounded by the remaining count to avoid int overflow on huge limits.
		remaining := s.limit - s.emitted + n
		capacity := min(transferLinkChunk, remaining)
		if n >= capacity {
			capacity = remaining
			if n <= remaining/2 {
				capacity = 2 * n
			}
		}
		next := make([]link, n, capacity)
		copy(next, s.data[s.start:])
		s.data = next
		s.start = 0
	}
	s.data = append(s.data, v)
	s.emitted++
}
func (s *transferLinkStorage) finish() []link {
	if s.start == len(s.data) {
		return nil
	}
	return s.data[s.start:len(s.data):len(s.data)]
}
