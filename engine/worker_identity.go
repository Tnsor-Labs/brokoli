package engine

import "os"

// The identity a worker presents when it reaches the control plane for
// data-plane bytes (ADR-033 section 6).
//
// A capability says which single object may be touched. It does not say
// who is touching it, and the blob endpoint deliberately requires both:
// the tenant comes from the caller's own credential and the grant from
// the capability, so verification compares two independently-derived
// descriptions. Presenting only the capability means the request is
// rejected before the capability is ever examined -- which is exactly
// what happened, as a 401 on every reference-based input fetch.
//
// The credential is an OPAQUE token, never a JWT. A JWT is
// self-describing, cannot be revoked before it expires, and requires the
// holder to possess a signing secret -- every property to avoid in a
// credential handed to a machine, and especially to one outside our own
// network. An opaque token carries no claims, is revoked centrally, and
// lets the server resolve the tenant itself.

// WorkerDataPlaneAuth returns the Authorization header value this
// process presents for data-plane requests, or "" when it has none.
//
// A settable hook rather than a config read, following the same pattern
// as api.OrgResolverFunc and extensions.NodeTypeGateFunc: the
// enterprise worker holds a pool token that core knows nothing about,
// and core must not learn its shape.
//
// Deliberately NOT carried on the work order. A queue message is
// readable by anything that can read the queue, and is retained,
// redelivered and logged; a long-lived credential does not belong in
// one. The worker already knows its own identity.
var WorkerDataPlaneAuth func() string

// workerAuthHeader resolves this process's data-plane credential.
//
// Falls back to BROKOLI_WORKER_TOKEN so a core-only deployment has a way
// to configure one without the enterprise hook. Empty is legitimate and
// means "send no identity" -- an in-process worker never reaches the
// endpoint at all, and a deployment that has not configured one gets the
// endpoint's own 401 rather than a silent half-authenticated request.
func workerAuthHeader() string {
	if WorkerDataPlaneAuth != nil {
		if h := WorkerDataPlaneAuth(); h != "" {
			return h
		}
	}
	if tok := os.Getenv("BROKOLI_WORKER_TOKEN"); tok != "" {
		return "Bearer " + tok
	}
	return ""
}
