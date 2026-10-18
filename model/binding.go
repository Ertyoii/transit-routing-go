package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ResultBinding detects accidental mixing of complete results from different
// queries or network snapshots. It is provenance, not an authenticated proof:
// callers able to forge all fields can also forge completeness flags.
type ResultBinding struct {
	Version       string `json:"version"`
	DatasetSHA256 string `json:"dataset_sha256"`
	QuerySHA256   string `json:"query_sha256"`
}

const ResultBindingVersion = "static-routing-binding-v1"

func bindingDigest(domain string, v any) string {
	// These model structs contain only JSON-representable strings, integers,
	// booleans, pointers and slices. Their complete JSON schema is the preimage.
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	h := sha256.New()
	h.Write([]byte(domain + "\n"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// DatasetFingerprint is independent of input row order and nil/empty tables.
// Every normalized field participates, including dates, windows and provenance.
// Compute once when owning a validated immutable dataset, never per itinerary.
func DatasetFingerprint(d Dataset) string {
	d = d.Clone()
	d.Stops = append([]Stop{}, d.Stops...)
	d.Trips = append([]Trip{}, d.Trips...)
	d.Connections = append([]Connection{}, d.Connections...)
	d.Footpaths = append([]Footpath{}, d.Footpaths...)
	d.Transfers = append([]TransferRule{}, d.Transfers...)
	sort.Slice(d.Stops, func(i, j int) bool { return d.Stops[i].ID < d.Stops[j].ID })
	sort.Slice(d.Trips, func(i, j int) bool { return d.Trips[i].ID < d.Trips[j].ID })
	sort.Slice(d.Connections, func(i, j int) bool { return d.Connections[i].ID < d.Connections[j].ID })
	sort.Slice(d.Footpaths, func(i, j int) bool { return d.Footpaths[i].ID < d.Footpaths[j].ID })
	sort.Slice(d.Transfers, func(i, j int) bool { return d.Transfers[i].ID < d.Transfers[j].ID })
	return bindingDigest(ResultBindingVersion+"/dataset", d)
}
func BindQuery(datasetSHA256 string, q Query) ResultBinding {
	return ResultBinding{ResultBindingVersion, datasetSHA256, bindingDigest(ResultBindingVersion+"/query", q)}
}
