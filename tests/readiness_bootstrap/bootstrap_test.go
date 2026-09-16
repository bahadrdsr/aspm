//go:build integration

package readiness_bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/service"
)

func nonsecretProbe(t *testing.T, f *fixture) []byte {
	t.Helper()
	data, err := f.read()
	must(t, "independently read actual prepared probe", err)
	if len(data) == 0 || len(data) > 4096 {
		t.Fatal("prepared probe must be a bounded nonempty nonsecret object")
	}
	for _, key := range f.roles {
		for _, secret := range []string{key.access, key.secret, base64.StdEncoding.EncodeToString([]byte(key.secret))} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("prepared probe contains credential material; bytes withheld")
			}
		}
		database, err := url.Parse(f.database)
		must(t, "parse private fixture URL without displaying it", err)
		password, _ := database.User.Password()
		if bytes.Contains(data, []byte(f.database)) || bytes.Contains(data, []byte(password)) {
			t.Fatal("prepared probe contains database credential material; bytes withheld")
		}
	}
	return data
}

func successfulPuts(p *tap) int {
	count := 0
	for _, c := range p.snapshot() {
		if c.method == "PUT" && c.status >= 200 && c.status < 300 {
			count++
		}
	}
	return count
}

func TestReadinessBootstrap_CoreCreatesAbsentProbeAndOwnKeyIngestionStarts(t *testing.T) {
	f := newFixture(t)
	coreTap := f.observe(t, "core")
	config, err := f.configure(t, "core", "true", coreTap)
	must(t, "load real opt-in core environment", err)
	// Run must consume the configuration snapshot, not re-read ambient flags.
	t.Setenv("ASPM_S3_PREPARE_READINESS", "false")
	core := launch(t, f.ctx, "core", config)
	core.ready(t, "core")
	if successfulPuts(coreTap) != 1 {
		t.Fatal("fresh readiness requires one actual conditional PUT signed by the core key")
	}
	original := nonsecretProbe(t, f)
	ingestionTap := f.observe(t, "ingestion")
	ingestionConfig, err := f.configure(t, "ingestion", "false", ingestionTap)
	must(t, "load independent read-only ingestion configuration", err)
	ingestion := launch(t, f.ctx, "ingestion", ingestionConfig)
	ingestion.ready(t, "ingestion")
	if successfulPuts(ingestionTap) != 0 || len(ingestionTap.snapshot()) == 0 {
		t.Fatal("ingestion must reach readiness using its OWN read identity without preparing objects")
	}
	if !bytes.Equal(nonsecretProbe(t, f), original) {
		t.Fatal("independent startup modified the prepared probe")
	}
	ingestion.stop(t)
	core.stop(t)
}

func TestReadinessBootstrap_ExistingSentinelRemainsByteExact(t *testing.T) {
	f := newFixture(t)
	sentinel := []byte("independent nonsecret sentinel\r\npreserve these exact bytes\n")
	f.seed(t, sentinel)
	p := f.observe(t, "core")
	config, err := f.configure(t, "core", "true", p)
	must(t, "load real core preparation option", err)
	core := launch(t, f.ctx, "core", config)
	core.ready(t, "core")
	if !bytes.Equal(nonsecretProbe(t, f), sentinel) || successfulPuts(p) != 0 {
		t.Fatal("opt-in startup overwrote an existing sentinel")
	}
	ingestionTap := f.observe(t, "ingestion")
	config, err = f.configure(t, "ingestion", "false", ingestionTap)
	must(t, "load independent ingestion sentinel consumer", err)
	ingestion := launch(t, f.ctx, "ingestion", config)
	ingestion.ready(t, "ingestion")
	if successfulPuts(ingestionTap) != 0 || !bytes.Equal(nonsecretProbe(t, f), sentinel) {
		t.Fatal("read-only ingestion changed the operator-owned sentinel")
	}
	ingestion.stop(t)
	core.stop(t)
}

func TestReadinessBootstrap_ConcurrentCoreStartsConvergeAtomically(t *testing.T) {
	f := newFixture(t)
	p := f.observe(t, "core")
	p.mu.Lock()
	p.holdPUT = true
	p.mu.Unlock()
	firstConfig, err := f.configure(t, "core", "true", p)
	must(t, "load concurrent preparation configuration", err)
	secondConfig := firstConfig
	secondConfig.Listen = freeAddress(t)
	secondConfig.Jobs.ApplicationName += "_second"
	first, second := launch(t, f.ctx, "core", firstConfig), launch(t, f.ctx, "core", secondConfig)
	for i := 0; i < 2; i++ {
		select {
		case <-p.arrivals:
		case <-first.done:
			t.Fatal("first actual core exited before conditional preparation reached the shared probe")
		case <-second.done:
			t.Fatal("second actual core exited before conditional preparation reached the shared probe")
		case <-time.After(8 * time.Second):
			t.Fatal("two actual conditional preparation requests did not reach the controlled barrier")
		}
	}
	p.open()
	first.ready(t, "core")
	second.ready(t, "core")
	conflicts := 0
	for _, c := range p.snapshot() {
		if c.method == "PUT" && c.status == 412 {
			conflicts++
		}
	}
	if successfulPuts(p) != 1 || conflicts < 1 {
		t.Fatal("concurrent preparation must have one conditional winner and a real precondition conflict")
	}
	before := nonsecretProbe(t, f)
	first.stop(t)
	second.stop(t)
	again := launch(t, f.ctx, "core", firstConfig)
	again.ready(t, "core")
	if successfulPuts(p) != 1 || !bytes.Equal(nonsecretProbe(t, f), before) {
		t.Fatal("repeated/concurrent startup rewrote the winning probe")
	}
	again.stop(t)
}

func TestReadinessBootstrap_InvalidExplicitInputsFailBeforeDatabaseOrHTTP(t *testing.T) {
	f := newFixture(t)
	p := f.observe(t, "core")
	base, err := f.configure(t, "core", "true", p)
	must(t, "load valid caller configuration before negative mutations", err)
	for _, item := range []struct {
		name   string
		change func(*service.Config)
	}{
		{"missing-access-key", func(c *service.Config) { c.Evidence.AccessKey = "" }},
		{"missing-secret-key", func(c *service.Config) { c.Evidence.SecretKey = "" }},
		{"missing-readiness-key", func(c *service.Config) { c.ReadinessKey = "" }},
		{"out-of-scope-key", func(c *service.Config) { c.ReadinessKey = "raw/team-a/not-this-owned-prefix/readiness.txt" }},
		{"invalid-raw-prefix", func(c *service.Config) { c.Evidence.Prefix = strings.TrimSuffix(c.Evidence.Prefix, "/") }},
	} {
		t.Run(item.name, func(t *testing.T) {
			config := base
			database, endpoint, dbCalls, httpCalls := ioTripwires(t, f.database)
			config.Jobs.DatabaseURL, config.Evidence.Endpoint = database, endpoint
			item.change(&config)
			ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
			defer cancel()
			err := service.Run(ctx, "core", config)
			if err == nil {
				t.Error("invalid preparation inputs returned startup success")
			}
			if dbCalls.Load() != 0 || httpCalls.Load() != 0 {
				t.Errorf("invalid preparation inputs reached native I/O: DB=%d HTTP=%d", dbCalls.Load(), httpCalls.Load())
			}
		})
	}
}

func TestReadinessBootstrap_StorageDenialAndConditionalTransportFailureStayVisible(t *testing.T) {
	for _, mode := range []string{"real-write-denial", "conditional-write-transport-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			identity := "core"
			if mode == "real-write-denial" {
				identity = "ingestion"
			}
			p := f.observe(t, identity)
			if mode == "conditional-write-transport-failure" {
				p.mu.Lock()
				p.failPUT = true
				p.mu.Unlock()
			}
			config, err := f.configure(t, "core", "true", p)
			must(t, "load explicit preparation configuration", err)
			if mode == "real-write-denial" {
				config.Evidence.AccessKey, config.Evidence.SecretKey = f.roles["ingestion"].access, f.roles["ingestion"].secret
			}
			core := launch(t, f.ctx, "core", config)
			core.failed(t)
			attempted := false
			for _, c := range p.snapshot() {
				wanted := 0
				if mode == "real-write-denial" {
					wanted = 403
				}
				attempted = attempted || (c.method == "PUT" && c.condition == "*" && c.status == wanted)
			}
			if !attempted {
				t.Error("opt-in preparation never exercised the failing conditional write; absent behavior is not failure-handling proof")
			}
			if successfulPuts(p) != 0 {
				t.Error("failed preparation reported a successful publication")
			}
			if _, err := f.read(); status(err) != 404 {
				t.Fatal("failed preparation created or ambiguously changed the probe")
			}
		})
	}
}

func TestReadinessBootstrap_DefaultOffAndNonCoreRolesCannotPrepare(t *testing.T) {
	f := newFixture(t)
	coreTap := f.observe(t, "core")
	config, err := f.configure(t, "core", "", coreTap)
	must(t, "default flag-off environment", err)
	field := reflect.ValueOf(config).FieldByName("PrepareReadiness")
	if !field.IsValid() || field.Kind() != reflect.Bool {
		t.Error("chosen production API is missing Config.PrepareReadiness bool")
	} else if field.Bool() {
		t.Error("readiness preparation must default to false")
	}
	core := launch(t, f.ctx, "core", config)
	core.failed(t)
	for _, c := range coreTap.snapshot() {
		if c.method == "PUT" {
			t.Fatal("flag-off startup attempted to prepare its required preseeded probe")
		}
	}
	if _, err := f.configure(t, "core", "not-a-boolean", coreTap); err == nil {
		t.Error("invalid explicit preparation flag was silently accepted")
	}
	f.seed(t, []byte("nonsecret preseed for non-core role checks\n"))
	for _, role := range []string{"ingestion", "reports"} {
		t.Run(role, func(t *testing.T) {
			p := f.observe(t, "ingestion")
			config, err := f.configure(t, role, "true", p)
			if err != nil {
				if len(p.snapshot()) != 0 {
					t.Fatal("rejected non-core preparation performed storage I/O")
				}
				return
			}
			var forbiddenHTTP atomic.Int32
			if role == "reports" {
				original := http.DefaultTransport
				guard := original.(*http.Transport).Clone()
				guard.Proxy = nil
				guard.DialContext = func(context.Context, string, string) (net.Conn, error) {
					forbiddenHTTP.Add(1)
					return nil, errors.New("DB-only reports attempted HTTP I/O")
				}
				http.DefaultTransport = guard
				t.Cleanup(func() {
					http.DefaultTransport = original
					guard.CloseIdleConnections()
					if forbiddenHTTP.Load() != 0 {
						t.Error("DB-only reports attempted storage/HTTP I/O")
					}
				})
			}
			flag := reflect.ValueOf(&config).Elem().FieldByName("PrepareReadiness")
			if flag.IsValid() && flag.Kind() == reflect.Bool {
				flag.SetBool(true)
				database, endpoint, dbCalls, httpCalls := ioTripwires(t, f.database)
				config.Jobs.DatabaseURL, config.Evidence.Endpoint = database, endpoint
				ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
				defer cancel()
				if err := service.Run(ctx, role, config); err == nil || dbCalls.Load() != 0 || httpCalls.Load() != 0 {
					t.Fatal("direct non-core PrepareReadiness=true must reject before native I/O")
				}
				return
			}
			r := launch(t, f.ctx, role, config)
			r.ready(t, role)
			r.stop(t)
			if role == "reports" && len(p.snapshot()) != 0 {
				t.Fatal("DB-only reports performed storage I/O")
			}
			for _, c := range p.snapshot() {
				if c.method == "PUT" {
					t.Fatal("non-core role prepared a readiness object")
				}
			}
		})
	}
}
