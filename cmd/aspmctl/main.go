package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/install"
)

const version = "0.1.0-dev.1"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "aspmctl:", err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("choose init, plan, deploy-plan, apply, status, uninstall, doctor, or version")
	}
	switch args[0] {
	case "deploy-plan", "apply", "status", "uninstall":
		return deployment(args, output)
	case "version":
		fmt.Fprintln(output, "aspmctl", version)
		return nil
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(output)
		path := flags.String("output", "install.json", "Versioned configuration destination")
		overwrite := flags.Bool("overwrite", false, "Replace the selected existing configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if _, err := os.Stat(*path); err == nil && !*overwrite {
			return errors.New("configuration already exists; use plan to inspect it or explicitly choose --overwrite")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		scanner := bufio.NewScanner(input)
		question := func(label, fallback string) (string, error) {
			fmt.Fprintf(output, "%s [%s]: ", label, fallback)
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return "", err
				}
				return "", errors.New("input ended before configuration was confirmed; nothing was written")
			}
			if value := strings.TrimSpace(scanner.Text()); value != "" {
				return value, nil
			}
			return fallback, nil
		}
		fmt.Fprintln(output, "1/3 Destination")
		targetKind, err := question("Target (linux or kubernetes)", "linux")
		if err != nil {
			return err
		}
		target := map[string]any{"kind": targetKind}
		switch targetKind {
		case "linux":
			target["host"], err = question("Existing host", "localhost")
		case "kubernetes":
			target["context"], err = question("Existing Kubernetes context", "")
			if err == nil {
				target["namespace"], err = question("Application namespace", "aspm")
			}
		default:
			return errors.New("target must be linux or kubernetes")
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(output, "\n2/3 Access")
		address, err := question("Instance HTTPS address", "https://localhost")
		if err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{
			"apiVersion": "aspm/v1alpha1", "kind": "Installation",
			"release": map[string]any{"version": version, "artifactSource": "installer-bundle"},
			"target":  target, "access": map[string]any{"baseURL": address},
		})
		if err != nil {
			return err
		}
		plan, err := install.Resolve(context.Background(), raw)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, "\n3/3 Review")
		fmt.Fprintf(output, "Target: %s\nAddress: %s\nManaged PostgreSQL and private shared storage.\nAI and active verification disabled.\n", targetKind, address)
		fmt.Fprintln(output, "This saves a configuration preview. It does not grant deployment permission or contain generated credentials.")
		confirmed, err := question("Save configuration (yes/no)", "no")
		if err != nil {
			return err
		}
		if confirmed != "yes" {
			return errors.New("configuration was not saved")
		}
		var formatted strings.Builder
		var object any
		if err := json.Unmarshal(plan.Configuration, &object); err != nil {
			return err
		}
		encoder := json.NewEncoder(&formatted)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(object); err != nil {
			return err
		}
		if err := writePrivate(*path, []byte(formatted.String()), *overwrite); err != nil {
			return err
		}
		fmt.Fprintf(output, "Saved %s\nConfiguration ID: %s\n", *path, plan.ID)
		return nil
	case "plan":
		flags := flag.NewFlagSet("plan", flag.ContinueOnError)
		flags.SetOutput(output)
		path := flags.String("config", "install.json", "JSON installation configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		file, err := os.Open(*path)
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return err
		}
		plan, err := install.Resolve(context.Background(), raw)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	case "doctor":
		flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
		flags.SetOutput(output)
		address := flags.String("url", "", "Existing instance URL; HTTP allowed only on literal loopback")
		caFile := flags.String("ca", "", "Explicit trusted CA PEM file for a private HTTPS endpoint")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return doctor(*address, *caFile, output)
	default:
		return fmt.Errorf("command %q is not implemented; no deployment or data change was performed", args[0])
	}
}

func writePrivate(path string, data []byte, overwrite bool) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".aspm-config-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !overwrite {
		// Linking a completed same-directory file is an atomic no-replace
		// operation; another Stat followed by Rename would retain the race.
		return os.Link(name, path)
	}
	return os.Rename(name, path)
}

func doctor(address, caFile string, output io.Writer) error {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("doctor needs an uncredentialed instance URL")
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return errors.New("doctor requires HTTPS except on a literal loopback development address")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			return errors.New("system trust store could not be loaded")
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("CA file contains no usable certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("diagnostic redirects are disabled") }}
	for _, path := range []string{"/healthz", "/readyz"} {
		target := endpoint.JoinPath(path)
		response, err := client.Get(target.String())
		if err != nil {
			return errors.New("instance diagnostic failed; verify address, TLS trust and connectivity")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		closeErr := response.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if len(body) > 64<<10 {
			return errors.New("diagnostic response exceeds the 64 KiB limit")
		}
		fmt.Fprintf(output, "%s: HTTP %d\n", path, response.StatusCode)
		if response.StatusCode != http.StatusOK || !json.Valid(body) {
			return errors.New("instance is not fully ready; process health is distinct from dependency and pipeline health")
		}
	}
	fmt.Fprintln(output, "Process and dependency endpoints respond. Inspect pipeline freshness and authorization separately.")
	return nil
}
