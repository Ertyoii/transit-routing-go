package comparison

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/csa"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

// CheckBinding verifies provenance before a caller consumes any completeness
// flag. It cannot authenticate a deliberately forged result or prove a search.
func CheckBinding(checker *validate.Checker, q model.Query, r csa.Result) (model.ResultBinding, error) {
	if checker == nil {
		return model.ResultBinding{}, fmt.Errorf("missing result checker")
	}
	expected, e := checker.Binding(q)
	if e != nil {
		return model.ResultBinding{}, e
	}
	if r.QueryID != q.ID || r.Binding != expected {
		return model.ResultBinding{}, fmt.Errorf("result query/network binding mismatch")
	}
	return expected, nil
}
