//go:build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestM02EvidenceSurvivesIndependentClientsAndProcesses(t *testing.T) {
	requireEvidence(t)
	f := newFixture(t)
	writer := f.openEvidence(t)
	payload := []byte("synthetic immutable evidence\r\n\x00\xff\n")
	ref, err := writer.Put(f.ctx, f.workspace, identifier(t), bytes.NewReader(payload), int64(len(payload)))
	requireOK(t, "publish evidence through the production S3 client", err)
	if ref.WorkspaceID != f.workspace || ref.Bucket != f.evidence.Bucket ||
		!strings.HasPrefix(ref.Key, f.evidence.Prefix+f.workspace+"/") ||
		ref.SHA256 != digest(payload) || ref.SizeBytes != int64(len(payload)) {
		t.Fatal("published evidence lacks scoped remote identity or exact integrity metadata")
	}
	raw, err := f.s3.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)})
	requireOK(t, "independently verify that production evidence actually exists in S3", err)
	direct, readErr := io.ReadAll(raw.Body)
	closeErr := raw.Body.Close()
	requireOK(t, "read independently stored S3 bytes", errors.Join(readErr, closeErr))
	if !bytes.Equal(direct, payload) {
		t.Fatal("production evidence publication did not preserve exact remote bytes")
	}
	requireOK(t, "close the original evidence client", writer.Close())
	reader := f.openEvidence(t)
	data, err := readEvidence(f.ctx, reader, f.workspace, ref)
	requireOK(t, "read evidence after reopening the client", err)
	if !bytes.Equal(data, payload) {
		t.Fatal("reopened client changed the evidence bytes")
	}
	child, report := startChild(t, f, childRequest{Mode: "read-evidence", WorkspaceID: f.workspace, Reference: ref})
	child.finish(t)
	if report.Result == nil || report.Result.SHA256 != digest(payload) || report.Result.SizeBytes != int64(len(payload)) {
		t.Fatal("a fresh process could not independently retrieve the shared evidence")
	}
}

type failingInput struct{}

func (failingInput) Read([]byte) (int, error) {
	return 0, errors.New("controlled synthetic input failure")
}

func TestM02EvidenceRejectsCorruptionMissingObjectsAndForeignScope(t *testing.T) {
	requireEvidence(t)
	f := newFixture(t)
	payload := []byte("synthetic externally seeded evidence\r\n")
	ref := f.rawPut(t, f.workspace, "externally-seeded", payload)
	store := f.openEvidence(t)
	data, err := readEvidence(f.ctx, store, f.workspace, ref)
	requireOK(t, "read an S3 object never seen by a local writer", err)
	if !bytes.Equal(data, payload) {
		t.Fatal("evidence reader depends on a local-writer cache or changes source bytes")
	}
	_, err = readEvidence(f.ctx, store, identifier(t), ref)
	requireError(t, "cross-workspace evidence read", err, ErrScope)
	restrictedConfig := f.evidence
	restrictedConfig.Prefix += "other-reader/"
	restricted, err := Production.OpenEvidence(f.ctx, restrictedConfig)
	requireOK(t, "open a more narrowly scoped evidence client", err)
	if restricted == nil {
		t.Fatal("production OpenEvidence returned a nil restricted client")
	}
	t.Cleanup(func() { requireOK(t, "close restricted evidence client", restricted.Close()) })
	_, err = readEvidence(f.ctx, restricted, f.workspace, ref)
	requireError(t, "out-of-prefix evidence read", err, ErrScope)
	f.rawPut(t, f.workspace, "externally-seeded", []byte("controlled corruption"))
	_, err = readEvidence(f.ctx, store, f.workspace, ref)
	requireError(t, "corrupted evidence must not be returned as verified", err, ErrIntegrity)
	_, err = f.s3.DeleteObject(f.ctx, &s3.DeleteObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)})
	requireOK(t, "remove only the owned fault-injection object", err)
	_, err = readEvidence(f.ctx, store, f.workspace, ref)
	requireError(t, "missing evidence must not use a stale local cache", err, ErrNotFound)
	badInput := io.MultiReader(bytes.NewReader([]byte("synthetic partial input")), failingInput{})
	published, err := store.Put(f.ctx, f.workspace, "failed-publication", badInput, 200)
	if err == nil || published.Key != "" || published.SHA256 != "" {
		t.Fatal("failed upload returned a success-shaped usable evidence reference")
	}
}

func TestM02IntegrityJobConnectsDurableQueueAndSharedEvidence(t *testing.T) {
	requireJobs(t)
	requireWorker(t)
	f := newFixture(t)
	jobs := f.openJobs(t, "-integrity-observer")
	request := f.envelope(t)
	success := enqueue(t, f, jobs, request)
	worker := WorkerConfig{
		Jobs: f.jobs, Evidence: f.evidence,
		Claim: Claim{WorkspaceID: f.workspace, WorkerID: "synthetic-integrity-worker", LeaseFor: f.jobs.MaxLease},
	}
	worker.Jobs.ApplicationName += "-integrity-worker"
	report, err := Production.RunIntegrityOnce(f.ctx, worker)
	requireOK(t, "run the production deterministic integrity worker", err)
	if !report.Processed || report.JobID != success.JobID || report.State != "succeeded" {
		t.Fatal("the internal integrity job did not produce its real durable success state")
	}
	finished := snapshot(t, f, jobs, success.JobID)
	if finished.State != "succeeded" || finished.Receipt == nil ||
		finished.Receipt.Result.SHA256 != request.Evidence.SHA256 ||
		finished.Receipt.Result.SizeBytes != request.Evidence.SizeBytes {
		t.Fatal("integrity success is not backed by a durable exact-byte receipt")
	}
	bad := f.envelope(t)
	bad.MaxAttempts = 1
	failed := enqueue(t, f, jobs, bad)
	corrupted := []byte("controlled changed object")
	_, err = f.s3.PutObject(f.ctx, &s3.PutObjectInput{
		Bucket: aws.String(bad.Evidence.Bucket), Key: aws.String(bad.Evidence.Key),
		Body: bytes.NewReader(corrupted), ContentLength: aws.Int64(int64(len(corrupted))),
	})
	requireOK(t, "corrupt only the owned queued evidence object", err)
	report, err = Production.RunIntegrityOnce(f.ctx, worker)
	requireOK(t, "record the deterministic integrity failure", err)
	diagnostic := snapshot(t, f, jobs, failed.JobID)
	if !report.Processed || report.JobID != failed.JobID || report.State != "failed" ||
		report.FailureCode != "evidence-integrity" || diagnostic.State != "failed" ||
		diagnostic.LastFailure == nil || diagnostic.LastFailure.Code != "evidence-integrity" || diagnostic.Receipt != nil {
		t.Fatal("corrupt evidence was hidden, retried without a bound, or reported as successful")
	}
	idle, err := Production.RunIntegrityOnce(f.ctx, worker)
	requireOK(t, "observe an empty integrity queue", err)
	if idle.Processed || idle.JobID != "" {
		t.Fatal("completed or permanently failed work was processed again")
	}
}
