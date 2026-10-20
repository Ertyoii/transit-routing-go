package model

import "fmt"

// QueryValidator owns the static endpoint/window portion of Query.Validate.
// Every call still validates the supplied query's complete bounds and endpoints;
// it never caches a previous query or supplies a journey-feasibility certificate.
// Construct it after validating the dataset, as with other immutable indexes.
type QueryValidator struct {
	stops  map[string]struct{}
	window *TimeWindow
}

func NewQueryValidator(d Dataset) *QueryValidator {
	v := &QueryValidator{stops: make(map[string]struct{}, len(d.Stops))}
	for _, s := range d.Stops {
		v.stops[s.ID] = struct{}{}
	}
	if d.Window != nil {
		w := *d.Window
		v.window = &w
	}
	return v
}

func (v *QueryValidator) Validate(q Query) error {
	if err := q.validateBounds(v.window); err != nil {
		return err
	}
	_, origin := v.stops[q.Origin]
	_, destination := v.stops[q.Destination]
	if !origin || !destination {
		return fmt.Errorf("query %q has unknown endpoint", q.ID)
	}
	return nil
}
