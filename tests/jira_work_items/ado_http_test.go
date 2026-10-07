//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

const adoProfile = "ado-services-build-artifacts"
const adoSources = "/api/v1/sources"
const adoReportCanary = "ADO-EXACT-REPORT-ONLY"

type adoTarget struct {
	Organization string `json:"organization"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
}

type adoSelection struct {
	BuildID      string `json:"buildId"`
	ArtifactName string `json:"artifactName"`
	ArtifactPath string `json:"artifactPath"`
}

func adoDefaultTarget() adoTarget {
	return adoTarget{"owned-org", "11111111-2222-4333-8444-aaaaaaaaaaaa", "55555555-6666-4777-8888-bbbbbbbbbbbb"}
}

func adoDefaultSelection() adoSelection {
	return adoSelection{"81", "aspm-report", "reports/report.sarif"}
}

type adoSource struct {
	app.SourceConnection
	AzureDevOps *adoTarget `json:"azureDevOps"`
}

type adoCollection struct {
	app.SourceCollection
	AzureDevOps *adoTarget   `json:"azureDevOps"`
	Selection   *adoSelection `json:"selection"`
}

type adoReply struct {
	APIVersion string
	Source     adoSource
	Collection adoCollection
	Items      []json.RawMessage
	Total      int
	NextCursor *string
	Error      *app.Failure
}

type adoCredential struct{ AccessKey, SecretKey string }
type adoCapabilities struct {
	CollectionReader, CollectionPublisher, RawIntake, RawReader adoCredential
}

type adoHarness struct {
	*harness
	capabilities adoCapabilities
	upstream     app.StorageConfig
	collection   app.StorageConfig
	reader       *adoStorageTap
	publisher    *adoStorageTap
	raw          *adoStorageTap
	native       *adoNative
	publishedStarted, publishedClosed atomic.Bool
}

var adoObserved struct {
	fixtures, api, sql, native, objects, wire atomic.Int64
}

func newADO(t *testing.T, open bool) *adoHarness {
	t.Helper()
	var h *adoHarness
	t.Cleanup(func() {
		if h == nil || h.native == nil {
			t.Log("ADO initialization did not complete; no completed-fixture aggregate claimed")
			return
		}
		api, sql := h.api.Load(), h.queries.calls.Load()+h.publishedQueries.Load()
		native := h.native.calls.Load()
		objects, wire := h.reader.calls.Load()+h.publisher.calls.Load()+h.raw.calls.Load(), h.writes.Load()+h.publishedWrites.Load()
		t.Logf("ADO fixture totals: API=%d/220 SQL=%d/1400 native=%d/64 S3-object=%d/128 S3-wire-writes=%d/64",
			api, sql, native, objects, wire)
		if h.publishedStarted.Load() && !h.publishedClosed.Load() {
			t.Log("Published child final SQL count unavailable; values above are partial, excluded from complete-fixture aggregate")
			return
		}
		adoObserved.api.Add(int64(api))
		adoObserved.sql.Add(int64(sql))
		adoObserved.native.Add(int64(native))
		adoObserved.objects.Add(int64(objects))
		adoObserved.wire.Add(int64(wire))
		count := adoObserved.fixtures.Add(1)
		t.Logf("ADO cumulative observed fixtures=%d (including failures): API=%d SQL=%d native=%d S3-object=%d S3-wire-writes=%d; not a latency/capacity claim",
			count, adoObserved.api.Load(), adoObserved.sql.Load(), adoObserved.native.Load(), adoObserved.objects.Load(), adoObserved.wire.Load())
		check(t, native <= 64 && objects <= 128, "ADO native/object budget exceeded")
	})
	h = &adoHarness{harness: &harness{fixture: newFixture(t)}}
	path := os.Getenv("ASPM_ADO_CAPABILITIES")
	check(t, path != "", "BLOCKED: explicit existing ADO fixture storage capabilities required")
	data, err := os.ReadFile(path)
	must(t, "read explicit private existing role capabilities", err)
	check(t, len(data) <= 16<<10, "private role capability input exceeded its bound")
	h.capabilities = decoded[adoCapabilities](t, data)
	for _, role := range []adoCredential{h.capabilities.CollectionReader, h.capabilities.CollectionPublisher,
		h.capabilities.RawIntake, h.capabilities.RawReader} {
		check(t, role.AccessKey != "" && role.SecretKey != "", "BLOCKED: an explicit object-scoped identity is missing")
		h.remember(role.AccessKey)
		h.remember(role.SecretKey)
	}
	check(t, h.capabilities.CollectionReader.AccessKey != h.capabilities.CollectionPublisher.AccessKey &&
		h.capabilities.CollectionPublisher.AccessKey != h.capabilities.RawIntake.AccessKey &&
		h.capabilities.RawReader.AccessKey != h.capabilities.RawIntake.AccessKey,
		"BLOCKED: collection and intake roles must be distinct existing identities")
	h.upstream = h.cfg.Storage
	t.Cleanup(func() { h.cfg.Storage = h.upstream })
	h.collection = h.upstream
	h.collection.Prefix += "collection/"
	h.cfg.Storage.Prefix += "raw/"
	h.reader = h.storageTap(h.collection.Prefix, "GET", "HEAD")
	h.publisher = h.storageTap(h.collection.Prefix, "PUT", "HEAD")
	h.raw = h.storageTap(h.cfg.Storage.Prefix, "GET", "HEAD", "PUT")
	h.installStorageDialGuard()
	h.cfg.Storage.Endpoint = h.raw.server.URL
	h.cfg.Storage.AccessKey, h.cfg.Storage.SecretKey = h.capabilities.RawIntake.AccessKey, h.capabilities.RawIntake.SecretKey
	read := h.collection
	read.Endpoint, read.AccessKey, read.SecretKey = h.reader.server.URL,
		h.capabilities.CollectionReader.AccessKey, h.capabilities.CollectionReader.SecretKey
	h.cfg.CollectionStorage = &read
	h.native = h.newNative()
	if open {
		h.open()
		h.enroll()
	}
	return h
}

func (h *adoHarness) adoJSON(who actor, method, path string, input any, status int) adoReply {
	h.t.Helper()
	data, _ := h.request(h.ctx, who, method, path, input, status)
	result := decoded[adoReply](h.t, data)
	check(h.t, result.APIVersion == app.APIVersion, "source API version changed")
	if status >= 400 {
		check(h.t, result.Error != nil && result.Error.Code != "" && !result.Error.Retryable, "missing safe source denial")
	}
	root := decoded[map[string]json.RawMessage](h.t, data)
	if result.Source.Profile == adoProfile {
		adoFields(h.t, root["source"], "id", "workspaceId", "profile", "name", "enabled", "credentialConfigured",
			"revision", "createdAt", "updatedAt", "azureDevOps")
	}
	if result.Collection.Profile == adoProfile {
		adoFields(h.t, root["collection"], "id", "workspaceId", "sourceId", "profile", "connectionRevision",
			"requestedBy", "state", "complete", "assetId", "repositoryId", "recordCount", "gaps",
			"createdAt", "collectedAt", "completedAt", "failure", "azureDevOps", "selection")
	}
	return result
}

func adoFields(t *testing.T, raw []byte, fields ...string) {
	t.Helper()
	value := decoded[map[string]json.RawMessage](t, raw)
	check(t, len(value) == len(fields), "profile metadata returned extra or missing fields")
	for _, field := range fields {
		_, present := value[field]
		check(t, present, "profile metadata omitted a declared field")
	}
}

func adoSourceInput(target adoTarget, token string) object {
	return object{"profile": adoProfile, "name": "Explicit ADO source", "enabled": true, "token": token, "azureDevOps": target}
}

func (h *adoHarness) adoSource(who actor, target adoTarget, token string) adoSource {
	h.t.Helper()
	h.remember(token)
	h.remember("Basic " + base64.StdEncoding.EncodeToString([]byte(":"+token)))
	value := h.adoJSON(who, "POST", adoSources, adoSourceInput(target, token), 201).Source
	canonical := adoTarget{strings.ToLower(target.Organization), strings.ToLower(target.ProjectID), strings.ToLower(target.RepositoryID)}
	check(h.t, value.ID != "" && value.WorkspaceID == who.Workspace && value.Profile == adoProfile &&
		value.Enabled && value.CredentialConfigured && value.Revision == 1, "source lost explicit target/credential state")
	same(h.t, "ADO target was not canonicalized", value.AzureDevOps, &canonical)
	return value
}

func adoCollectionPath(id string) string { return adoSources + "/collections/" + id }
func adoRecordsPath(id string) string    { return adoCollectionPath(id) + "/records" }
func adoEvidencePath(collection, record string) string {
	return adoRecordsPath(collection) + "/" + record + "/evidence"
}
func adoBridgePath(collection, record string) string {
	return adoRecordsPath(collection) + "/" + record + "/imports"
}
func adoQueueInput(selection adoSelection, key string) object {
	return object{"idempotencyKey": key, "buildId": selection.BuildID,
		"artifactName": selection.ArtifactName, "artifactPath": selection.ArtifactPath}
}
func (h *adoHarness) adoQueue(who actor, source string, selection adoSelection, key string, status int) adoCollection {
	return h.adoJSON(who, "POST", adoSources+"/"+source+"/collections", adoQueueInput(selection, key), status).Collection
}
func (h *adoHarness) adoJob(who actor, id string) adoCollection {
	return h.adoJSON(who, "GET", adoCollectionPath(id), nil, 200).Collection
}
func (h *adoHarness) adoRecords(who actor, id string) []app.SourceCollectionRecord {
	h.t.Helper()
	page := h.adoJSON(who, "GET", adoRecordsPath(id)+"?limit=10", nil, 200)
	check(h.t, page.NextCursor == nil && page.Total == len(page.Items), "bounded record page lost rows")
	result := make([]app.SourceCollectionRecord, 0, len(page.Items))
	for _, raw := range page.Items {
		adoFields(h.t, raw, "id", "collectionId", "ordinal", "kind", "externalId", "parentId", "nativeRunId",
			"state", "severity", "location", "rawURL", "sourceScanAt", "sourceUpdatedAt", "evidence")
		result = append(result, decoded[app.SourceCollectionRecord](h.t, raw))
	}
	return result
}
func (h *adoHarness) adoReport(who actor, id string) app.SourceCollectionRecord {
	h.t.Helper()
	for _, record := range h.adoRecords(who, id) {
		if record.Kind == "report" {
			return record
		}
	}
	h.t.Fatal("completed native collection omitted the selected report")
	return app.SourceCollectionRecord{}
}

func (h *adoHarness) adoDatabase(label string) app.DatabaseConfig {
	return app.DatabaseConfig{DatabaseURL: h.cfg.DatabaseURL, Schema: h.cfg.Schema,
		ApplicationName: "ado-" + label + "-" + nonce(h.t), MaxConnections: 1,
		QueryTracer: &h.queries, LogOutput: &h.log}
}
func (h *adoHarness) adoWorkerConfig() app.CollectionWorkerConfig {
	storage := h.collection
	storage.Endpoint, storage.AccessKey, storage.SecretKey = h.publisher.server.URL,
		h.capabilities.CollectionPublisher.AccessKey, h.capabilities.CollectionPublisher.SecretKey
	return app.CollectionWorkerConfig{Database: h.adoDatabase("collect"), Storage: storage,
		EncryptionKey: h.cfg.IntegrationEncryptionKey, WorkerID: "ado-" + nonce(h.t),
		LeaseDuration: 4 * time.Second, GitHubEndpoint: h.native.server.URL,
		AzureDevOpsEndpoint: h.native.server.URL, Client: h.native.client,
		Limits: connectors.Limits{Requests: 4, Pages: 1, PageSize: 1, Bytes: 32 << 10}}
}
func (h *adoHarness) adoWorker(config app.CollectionWorkerConfig) *app.CollectionWorker {
	h.t.Helper()
	worker, err := app.OpenCollectionWorker(h.ctx, config)
	must(h.t, "open independent actual collection worker", err)
	h.t.Cleanup(func() { must(h.t, "close independent collection worker", worker.Close()) })
	return worker
}
func adoStep(t *testing.T, ctx context.Context, worker *app.CollectionWorker, want bool) {
	t.Helper()
	processed, err := worker.ProcessNext(ctx)
	must(t, "process one actual collection intent", err)
	check(t, processed == want, "wrong eligible collection result")
}
func (h *adoHarness) adoIngest() {
	h.t.Helper()
	storage := h.cfg.Storage
	storage.AccessKey, storage.SecretKey = h.capabilities.RawReader.AccessKey, h.capabilities.RawReader.SecretKey
	worker, err := app.OpenImportWorker(h.ctx, app.ImportWorkerConfig{
		Database: h.adoDatabase("ingest"), Storage: storage, MaxUploadBytes: h.cfg.MaxUploadBytes,
		NormalizedPrefix: h.upstream.Prefix + "normalized/",
	})
	must(h.t, "open separate existing ingestion worker", err)
	defer worker.Close()
	must(h.t, "ingest actual bridge report from rawEvidence", worker.ProcessImports(h.ctx))
}
func (h *adoHarness) adoRef(record string) evidence.Ref {
	h.t.Helper()
	var raw []byte
	must(h.t, "inspect actual immutable record evidence ref", h.db.QueryRow(h.ctx,
		"SELECT evidence FROM "+h.table("source_collection_records")+" WHERE id=$1", record).Scan(&raw))
	return decoded[evidence.Ref](h.t, raw)
}
func (h *adoHarness) adoEncrypted(source adoSource, token string) {
	h.t.Helper()
	var raw []byte
	must(h.t, "inspect encrypted source bytes", h.db.QueryRow(h.ctx,
		"SELECT credential_ciphertext FROM "+h.table("source_connections")+" WHERE workspace_id=$1 AND id=$2",
		source.WorkspaceID, source.ID).Scan(&raw))
	block, err := aes.NewCipher(h.cfg.IntegrationEncryptionKey)
	must(h.t, "independent source AES assertion", err)
	aead, err := cipher.NewGCM(block)
	must(h.t, "independent source GCM assertion", err)
	end := 1 + aead.NonceSize()
	check(h.t, len(raw) >= end+aead.Overhead() && raw[0] == 1 && !bytes.Contains(raw, []byte(token)), "source credential envelope changed")
	aad := func(profile, workspace, id string) []byte {
		return []byte("aspm/source-credential/v1\x00" + profile + "\x00" + workspace + "\x00" + id)
	}
	plain, err := aead.Open(nil, raw[1:end], raw[end:], aad(source.Profile, source.WorkspaceID, source.ID))
	must(h.t, "authenticate profile/workspace/source credential AAD", err)
	check(h.t, string(plain) == token, "explicit source credential bytes changed")
	clear(plain)
	for _, binding := range [][]byte{aad(source.Profile, "wrong-workspace", source.ID),
		aad(source.Profile, source.WorkspaceID, "wrong-id"), aad("other-profile", source.WorkspaceID, source.ID),
		aad(map[string]string{adoProfile: "github-cloud-app", "github-cloud-app": adoProfile}[source.Profile], source.WorkspaceID, source.ID)} {
		_, err = aead.Open(nil, raw[1:end], raw[end:], binding)
		check(h.t, err != nil, "source ciphertext escaped its selected AAD domain")
	}
	h.remember(base64.StdEncoding.EncodeToString(raw))
	h.remember(hex.EncodeToString(raw))
}

func adoBridgeInput() object {
	return object{"apiVersion": app.APIVersion, "format": "sarif",
		"scope": object{"id": "selected-security-scope", "revision": "source-revision-1", "branch": "refs/heads/main"},
		"sourceStatus": "succeeded", "scanKind": "delta", "completeness": "unknown"}
}
func adoNativeIdentity(target adoTarget, selection adoSelection) (string, string) {
	source, _ := json.Marshal([]string{"ado-source-v1", target.Organization, target.ProjectID, target.RepositoryID})
	scan, _ := json.Marshal([]string{"sarif-2.1.0", target.Organization, target.ProjectID, target.RepositoryID,
		selection.BuildID, selection.ArtifactName, selection.ArtifactPath})
	return adoProfile + ":" + digest(source), "ado-report-v1:" + digest(scan)
}
