// Package httpapi exposes the provider contract without imposing a third-party API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ertyoii/transit-routing-go/strictjson"
	"github.com/ertyoii/transit-routing-go/walking"
	"io"
	"net/http"
	"time"
)

type Limits struct {
	MaxBodyBytes   int64
	MaxMatrixCells int
	Timeout        time.Duration
}

func DefaultLimits() Limits { return Limits{1 << 20, 10000, 5 * time.Second} }
func New(p walking.Provider, limits Limits) (http.Handler, error) {
	if p == nil || limits.MaxBodyBytes <= 0 || limits.MaxMatrixCells <= 0 || limits.Timeout <= 0 {
		return nil, walking.ErrInvalid
	}
	return &handler{p, limits}, nil
}

type handler struct {
	provider walking.Provider
	limits   Limits
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if e != nil {
		return fmt.Errorf("%w: body: %v", walking.ErrInvalid, e)
	}
	if e = strictjson.Decode(b, out); e != nil {
		return fmt.Errorf("%w: body: %v", walking.ErrInvalid, e)
	}
	return nil
}
func respond(w http.ResponseWriter, value any, e error) {
	status := http.StatusOK
	if e != nil {
		switch {
		case errors.Is(e, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(e, context.Canceled):
			status = 499
		case errors.Is(e, walking.ErrStale):
			status = http.StatusConflict
		case errors.Is(e, walking.ErrUnsupported):
			status = http.StatusUnprocessableEntity
		case errors.Is(e, walking.ErrBudget):
			status = http.StatusTooManyRequests
		case errors.Is(e, walking.ErrInvalid):
			status = http.StatusBadRequest
		default:
			status = http.StatusBadGateway
		}
		value = map[string]string{"error": e.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/snapshot" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		respond(w, h.provider.Descriptor(), nil)
		return
	}
	if r.URL.Path != "/v1/route" && r.URL.Path != "/v1/duration" && r.URL.Path != "/v1/matrix" && r.URL.Path != "/v1/batch" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.limits.Timeout)
	defer cancel()
	var out any
	var e error
	switch r.URL.Path {
	case "/v1/matrix":
		var req walking.MatrixRequest
		e = decode(w, r, h.limits.MaxBodyBytes, &req)
		if e == nil {
			if len(req.Targets) == 0 || len(req.Sources) == 0 {
				e = walking.ErrInvalid
			} else if len(req.Sources) > h.limits.MaxMatrixCells/len(req.Targets) {
				e = walking.ErrBudget
			} else if p, ok := h.provider.(walking.MatrixProvider); ok {
				out, e = p.Matrix(ctx, req)
			} else {
				e = walking.ErrUnsupported
			}
		}
	case "/v1/batch":
		var req []walking.Request
		e = decode(w, r, h.limits.MaxBodyBytes, &req)
		if e == nil {
			if len(req) == 0 {
				e = walking.ErrInvalid
			} else if len(req) > h.limits.MaxMatrixCells {
				e = walking.ErrBudget
			} else if p, ok := h.provider.(walking.BatchProvider); ok {
				out, e = p.Batch(ctx, req)
			} else {
				e = walking.ErrUnsupported
			}
		}
	default:
		var req walking.Request
		e = decode(w, r, h.limits.MaxBodyBytes, &req)
		if e == nil {
			if r.URL.Path == "/v1/route" {
				out, e = h.provider.Route(ctx, req)
			} else if p, ok := h.provider.(walking.DurationProvider); ok {
				out, e = p.Duration(ctx, req)
			} else {
				e = walking.ErrUnsupported
			}
		}
	}
	if e == nil {
		e = ctx.Err()
	}
	respond(w, out, e)
}
