// Package storagepolicy builds a deterministic SeaweedFS 4.47 identity policy
// from explicit bucket, prefix, and credential inputs. Build performs no I/O,
// credential discovery, fallback, generation, or live-policy installation.
//
// Required handling notices:
//
//   - Build's successful JSON contains all four caller-supplied credentials.
//     Treat it as private operator configuration; never log it or commit it.
//     Ordinary Config/Credential JSON and formatted diagnostics omit credentials.
//   - Keep the sole Admin identity operator-only, outside service workloads.
//     Core can read/write raw and approved objects; ingestion can read raw and
//     write normalized objects; AI can only read its exact approved prefix.
//   - No service identity receives List, bucket-wide Read, or Admin. Readiness
//     checks must not demand HeadBucket privileges or broaden these grants.
//   - Prefixes are literal, nonoverlapping ASCII paths ending in "/". Wildcards,
//     encoded paths, empty/dot segments, backslashes, and absolute paths are
//     rejected rather than normalized. Only the builder appends a terminal "*".
//   - Generated grants do not prove runtime isolation. SeaweedFS environment
//     credentials, dynamic identities, attached policies, and bucket policies
//     must not introduce broader authority. Independent allowed/403-denied S3
//     operations with distinct role credentials are a separate required gate.
//
// Credential values remain unchanged, must be valid bounded UTF-8 without
// control characters or surrounding whitespace, and must all be distinct.
// Output role/action order is fixed; there are no timestamps or random values.
package storagepolicy
