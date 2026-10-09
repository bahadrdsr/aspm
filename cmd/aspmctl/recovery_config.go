package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

var recoveryInstanceID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}$`)
var recoverySchemaName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var recoverySegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var recoveryHexPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var recoveryDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type recoveryRuntime struct {
	config    recoveryConfiguration
	pool      *pgxpool.Pool
	s3        *s3.Client
	transport *http.Transport
	tools     recoveryToolVersions
}

func loadRecoveryConfiguration(path string) (recoveryConfiguration, error) {
	var config recoveryConfiguration
	if path == "" {
		return config, errors.New("an explicit recovery configuration is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return config, errors.New("recovery configuration is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return config, errors.New("recovery configuration exceeds its limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return config, errors.New("recovery configuration is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config, errors.New("recovery configuration contains trailing JSON")
	}
	if err = validateRecoveryConfiguration(config); err != nil {
		return recoveryConfiguration{}, err
	}
	return config, nil
}

func validateRecoveryConfiguration(config recoveryConfiguration) error {
	if config.APIVersion != recoveryAPIVersion || config.Kind != "RecoveryConfiguration" ||
		!recoveryInstanceID.MatchString(config.InstanceID) ||
		(config.TargetKind != "linux" && config.TargetKind != "kubernetes") ||
		!recoverySchemaName.MatchString(config.Database.Schema) {
		return errors.New("unsupported recovery configuration identity")
	}
	database, err := url.Parse(config.Database.URL)
	if err != nil || (database.Scheme != "postgres" && database.Scheme != "postgresql") ||
		database.Host == "" || database.User == nil || database.User.Username() == "" ||
		database.Path == "" || database.Path == "/" || database.RawQuery == "" {
		return errors.New("recovery database selection is invalid")
	}
	password, present := database.User.Password()
	if !present || password == "" {
		return errors.New("recovery database credentials are incomplete")
	}
	name, err := url.PathUnescape(strings.TrimPrefix(database.EscapedPath(), "/"))
	if err != nil || name == "" || strings.Contains(name, "/") || name != config.Database.Name {
		return errors.New("recovery database name does not match the selected URL")
	}
	endpoint, err := url.Parse(config.Storage.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		config.Storage.Bucket == "" || strings.ContainsAny(config.Storage.Bucket, `/\:`) ||
		config.Storage.Region == "" || strings.TrimSpace(config.Storage.AccessKey) == "" ||
		strings.TrimSpace(config.Storage.SecretKey) == "" {
		return errors.New("recovery storage selection is invalid")
	}
	prefixes := []string{config.Storage.RawPrefix, config.Storage.NormalizedPrefix, config.Storage.ArchivePrefix}
	for index, prefix := range prefixes {
		if !validRecoveryPrefix(prefix) {
			return errors.New("recovery storage prefix is invalid")
		}
		for _, prior := range prefixes[:index] {
			if strings.HasPrefix(prefix, prior) || strings.HasPrefix(prior, prefix) {
				return errors.New("recovery storage prefixes overlap")
			}
		}
	}
	if config.Tools.RequiredVersion != "18.6" {
		return errors.New("recovery requires PostgreSQL client version 18.6")
	}
	seen := map[string]bool{}
	for _, path := range []string{config.Tools.PGDump, config.Tools.PGRestore, config.Tools.PSQL} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[strings.ToLower(path)] {
			return errors.New("recovery tool paths must be explicit, absolute and distinct")
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.New("a required recovery tool is unavailable")
		}
		seen[strings.ToLower(path)] = true
	}
	if config.Limits.MaxDatabaseBytes < 1 || config.Limits.MaxDatabaseBytes > 8<<30 ||
		config.Limits.MaxObjects < 1 || config.Limits.MaxObjects > 1_000_000 ||
		config.Limits.MaxObjectBytes < 1 || config.Limits.MaxObjectBytes > 8<<30 ||
		config.Limits.MaxTotalObjectBytes < config.Limits.MaxObjectBytes ||
		config.Limits.MaxTotalObjectBytes > 64<<30 {
		return errors.New("recovery limits are invalid")
	}
	return nil
}

func validRecoveryPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 1024 || strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") ||
		strings.Contains(prefix, `\`) || strings.ContainsRune(prefix, 0) {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if part == "." || part == ".." || !recoverySegment.MatchString(part) {
			return false
		}
	}
	return true
}

func openRecoveryRuntime(ctx context.Context, config recoveryConfiguration) (*recoveryRuntime, error) {
	tools, err := verifyRecoveryTools(ctx, config)
	if err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(config.Database.URL)
	if err != nil {
		return nil, errors.New("recovery database configuration is invalid")
	}
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 0
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "aspm-recovery"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("recovery database is unavailable")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("recovery database is unavailable")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := s3.New(s3.Options{
		Region: config.Storage.Region, BaseEndpoint: aws.String(config.Storage.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.Storage.AccessKey, config.Storage.SecretKey, ""),
		HTTPClient: &http.Client{
			Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("recovery storage redirects are disabled")
			},
		},
		RetryMaxAttempts: 2,
	})
	if _, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(config.Storage.Bucket)}); err != nil {
		pool.Close()
		transport.CloseIdleConnections()
		return nil, errors.New("recovery storage bucket is unavailable")
	}
	return &recoveryRuntime{config: config, pool: pool, s3: client, transport: transport, tools: tools}, nil
}

func (r *recoveryRuntime) close() {
	r.pool.Close()
	r.transport.CloseIdleConnections()
}

func verifyRecoveryTools(ctx context.Context, config recoveryConfiguration) (recoveryToolVersions, error) {
	var result recoveryToolVersions
	for _, item := range []struct {
		path  string
		value *string
	}{
		{config.Tools.PGDump, &result.PGDump},
		{config.Tools.PGRestore, &result.PGRestore},
		{config.Tools.PSQL, &result.PSQL},
	} {
		command := exec.CommandContext(ctx, item.path, "--version")
		command.Env = recoveryToolEnvironment(config.Database.URL)
		output, err := command.Output()
		if err != nil {
			return result, errors.New("recovery PostgreSQL tool version check failed")
		}
		version := postgresVersion(string(output))
		if version != config.Tools.RequiredVersion {
			return result, errors.New("recovery PostgreSQL tool version is unsupported")
		}
		*item.value = version
	}
	poolConfig, err := pgxpool.ParseConfig(config.Database.URL)
	if err != nil {
		return result, errors.New("recovery database configuration is invalid")
	}
	poolConfig.MaxConns = 1
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "aspm-recovery-preflight"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return result, errors.New("recovery database is unavailable")
	}
	defer pool.Close()
	var server string
	if err = pool.QueryRow(ctx, "SHOW server_version").Scan(&server); err != nil {
		return result, errors.New("recovery database version is unavailable")
	}
	result.Server = postgresVersion(server)
	if result.Server != config.Tools.RequiredVersion {
		return result, errors.New("recovery PostgreSQL server version is unsupported")
	}
	return result, nil
}

func postgresVersion(value string) string {
	match := regexp.MustCompile(`(?:^|[^0-9])([0-9]+\.[0-9]+)(?:[^0-9]|$)`).FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func recoveryToolEnvironment(databaseURL string) []string {
	var result []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "PG") || strings.HasPrefix(upper, "ASPM_") ||
			strings.HasPrefix(upper, "AWS_") || upper == "HTTP_PROXY" ||
			upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		result = append(result, entry)
	}
	parsed, err := url.Parse(databaseURL)
	if err == nil && parsed.User != nil {
		if password, present := parsed.User.Password(); present {
			result = append(result, "PGPASSWORD="+password)
		}
	}
	return result
}

func recoveryDatabaseArgument(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.User == nil {
		return databaseURL
	}
	parsed.User = url.User(parsed.User.Username())
	return parsed.String()
}

func recoveryHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func recoveryHexID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("generate recovery identifier failed")
	}
	return hex.EncodeToString(data[:]), nil
}

func recoveryPlanID(operation string, config recoveryConfiguration, extra any) (string, error) {
	binding := struct {
		Operation string                `json:"operation"`
		Config    recoveryConfiguration `json:"config"`
		Extra     any                   `json:"extra"`
	}{operation, config, extra}
	data, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	return "plan-" + strings.TrimPrefix(recoveryHash(data), "sha256:"), nil
}

func writeCanonicalJSON(path string, value any, mode os.FileMode) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err = atomicWrite(path, data, mode); err != nil {
		return nil, err
	}
	return data, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".aspm-recovery-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func canonicalObjectLines(objects []recoveryObject) []byte {
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	var output bytes.Buffer
	for _, object := range objects {
		data, _ := json.Marshal(object)
		output.Write(data)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func parseRecoveryObjectLines(data []byte) ([]recoveryObject, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var result []recoveryObject
	last := ""
	for scanner.Scan() {
		var object recoveryObject
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&object) != nil {
			return nil, errors.New("recovery object index is invalid")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("recovery object index is invalid")
		}
		canonical, err := json.Marshal(object)
		if err != nil || !bytes.Equal(canonical, scanner.Bytes()) ||
			object.Key <= last || !recoveryDigestPattern.MatchString(object.SHA256) {
			return nil, errors.New("recovery object index is invalid")
		}
		last = object.Key
		result = append(result, object)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parseInteger(value string) (int64, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return 0, errors.New("invalid nonnegative integer")
	}
	return number, nil
}
