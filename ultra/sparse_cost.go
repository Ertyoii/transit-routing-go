package ultra

// SparseCostSummary is an owned scalar-only view. Native proof construction,
// storage, validity gates and Search are unchanged. Full owned certificates
// remain available through the original Preparation method.
type SparseCostSummary struct {
	Complete             bool              `json:"complete"`
	Error                string            `json:"error,omitempty"`
	Profile              string            `json:"profile"`
	Config               SparseConfig      `json:"config"`
	Control              CappedPreparation `json:"transfer_complete_preparation"`
	RetainedPairs        int               `json:"retained_nonempty_pairs"`
	PrunedPairs          int               `json:"pruned_nonempty_pairs"`
	ProofChecks          int               `json:"proof_checks"`
	CertificateCount     int               `json:"certificate_count"`
	ProofRecordCount     int               `json:"proof_record_count"`
	CertificatesExported bool              `json:"certificates_exported"`
	ProofRecordsExported bool              `json:"proof_records_exported"`
	DiagnosticsProfile   string            `json:"diagnostics_profile"`
}

// CostSummary visits certificate HEADERS only to sum proof-slice lengths. It
// never clones, traverses or returns a certificate/proof slice, changes a guard,
// mutates index state, or substitutes counts for native safety validation.
func (x *SparseIndex) CostSummary() SparseCostSummary {
	if x == nil {
		return SparseCostSummary{Error: "ULTRA sparse: nil index", DiagnosticsProfile: "sparse_scalar_cost_summary_v1"}
	}
	p := &x.prep
	s := SparseCostSummary{Complete: p.Complete, Error: p.Error, Profile: p.Profile, Config: p.Config, Control: p.Control,
		RetainedPairs: p.RetainedPairs, PrunedPairs: p.PrunedPairs, ProofChecks: p.ProofChecks,
		CertificateCount: len(p.Certificates), DiagnosticsProfile: "sparse_scalar_cost_summary_v1"}
	for i := range p.Certificates {
		s.ProofRecordCount += len(p.Certificates[i].Proofs)
	}
	return s
}
