// Package evidence stores and verifies exact remote evidence bytes using
// explicitly configured S3 endpoints, static credentials, and workspace prefixes.
//
// Open returns the full Store and retains its HeadBucket startup check, Put,
// Open, Ping, and Close behavior. OpenReader returns a Reader with only Open and
// Close. It shares the same private client construction, scope checks, remote
// GetObject operation, and streaming length/SHA-256 verification, but performs no
// startup HTTP request and exposes neither writes nor a bucket-wide probe.
//
// A successful OpenReader validates configuration, not remote readiness or
// authorization. Consumers must read an authorized object to establish those
// properties; they must not grant broader credentials merely to enable a
// HeadBucket probe. Missing explicit keys or secrets are rejected before I/O,
// never replaced from environment profiles or an AWS ambient credential chain.
// Storage AccessDenied/Forbidden errors retain errors.Is(ErrScope), with an
// explicit S3-denial message. Failed reads never fall back to local cached data.
//
// The restricted method set is not a deployment security boundary by itself.
// Actual role-scoped credentials and independent storage allow/deny evidence
// remain necessary; service-role credential wiring is outside this package.
package evidence
