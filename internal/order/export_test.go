package order

// SortRowsWith exposes the sort with a chosen number of workers
// to the external tests, which generate their input with package generate
// (itself importing order).
func SortRowsWith(rows []Row, workers int) {
	sortWith(rows, func(r *Row) byte { return r.Address[0] }, CompareRows, workers)
}
