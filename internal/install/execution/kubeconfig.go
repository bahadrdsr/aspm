package execution

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type callerIdentity struct {
	path, server, digest string
	privateValues        []string
}

type kubeConfiguration struct {
	APIVersion     string          `json:"apiVersion"`
	Kind           string          `json:"kind"`
	CurrentContext string          `json:"current-context"`
	Preferences    json.RawMessage `json:"preferences"`
	Extensions     json.RawMessage `json:"extensions"`
	Clusters       []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server                   string `json:"server"`
			CertificateAuthority     string `json:"certificate-authority"`
			CertificateAuthorityData string `json:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `json:"insecure-skip-tls-verify"`
			TLSServerName            string `json:"tls-server-name"`
			ProxyURL                 string `json:"proxy-url"`
		} `json:"cluster"`
	} `json:"clusters"`
	Contexts []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster, User, Namespace string
		} `json:"context"`
	} `json:"contexts"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Token                 string          `json:"token"`
			TokenFile             string          `json:"tokenFile"`
			ClientCertificate     string          `json:"client-certificate"`
			ClientKey             string          `json:"client-key"`
			ClientCertificateData string          `json:"client-certificate-data"`
			ClientKeyData         string          `json:"client-key-data"`
			Exec                  json.RawMessage `json:"exec"`
			AuthProvider          json.RawMessage `json:"auth-provider"`
		} `json:"user"`
	} `json:"users"`
}

func (i *installer) kubeCaller(ctx context.Context, selected string) (callerIdentity, error) {
	path, err := relativePath(i.options.Kubeconfig)
	if err != nil {
		return callerIdentity{}, ErrCredential
	}
	data, err := i.read(ctx, path, maxInputBytes)
	if err != nil {
		return callerIdentity{}, ErrCredential
	}
	var config kubeConfiguration
	if decode(data, &config) != nil || config.APIVersion != "v1" || config.Kind != "Config" ||
		len(config.Contexts) > 128 || len(config.Clusters) > 128 || len(config.Users) > 128 {
		return callerIdentity{}, ErrCredential
	}
	clusterName, userName := "", ""
	seen := make(map[string]bool)
	for _, item := range config.Contexts {
		if item.Name == "" || seen[item.Name] {
			return callerIdentity{}, ErrCredential
		}
		seen[item.Name] = true
		if item.Name == selected {
			clusterName, userName = item.Context.Cluster, item.Context.User
		}
	}
	if clusterName == "" || userName == "" {
		return callerIdentity{}, ErrCredential
	}
	caller := callerIdentity{path: path}
	material := append([]byte(nil), data...)
	clear(seen)
	for _, item := range config.Clusters {
		if item.Name == "" || seen[item.Name] {
			return callerIdentity{}, ErrCredential
		}
		seen[item.Name] = true
		if item.Name != clusterName {
			continue
		}
		cluster := item.Cluster
		server, err := url.Parse(cluster.Server)
		if err != nil || server.Scheme != "https" || server.Hostname() == "" || server.User != nil ||
			server.RawQuery != "" || server.Fragment != "" || cluster.InsecureSkipTLSVerify || cluster.ProxyURL != "" {
			return callerIdentity{}, ErrCredential
		}
		caller.server = cluster.Server
		if cluster.CertificateAuthority != "" || cluster.CertificateAuthorityData != "" {
			ca, err := i.kubeBytes(ctx, path, cluster.CertificateAuthority, cluster.CertificateAuthorityData)
			if err != nil || !usableCertificate(ca) {
				return callerIdentity{}, ErrCredential
			}
			material = append(material, ca...)
		}
	}
	if caller.server == "" {
		return callerIdentity{}, ErrCredential
	}
	clear(seen)
	authenticated := false
	for _, item := range config.Users {
		if item.Name == "" || seen[item.Name] {
			return callerIdentity{}, ErrCredential
		}
		seen[item.Name] = true
		if item.Name != userName {
			continue
		}
		user := item.User
		if nonnull(user.Exec) || nonnull(user.AuthProvider) {
			return callerIdentity{}, ErrCredential
		}
		token := user.Token
		if user.TokenFile != "" {
			if token != "" {
				return callerIdentity{}, ErrCredential
			}
			raw, err := i.kubeBytes(ctx, path, user.TokenFile, "")
			if err != nil {
				return callerIdentity{}, ErrCredential
			}
			token = strings.TrimSpace(string(raw))
			material = append(material, raw...)
		}
		hasCertificate := user.ClientCertificate != "" || user.ClientCertificateData != "" || user.ClientKey != "" || user.ClientKeyData != ""
		if token != "" {
			if hasCertificate || strings.ContainsAny(token, " \r\n\t\x00") {
				return callerIdentity{}, ErrCredential
			}
			caller.privateValues = append(caller.privateValues, token)
			authenticated = true
		} else if hasCertificate {
			certificate, e1 := i.kubeBytes(ctx, path, user.ClientCertificate, user.ClientCertificateData)
			key, e2 := i.kubeBytes(ctx, path, user.ClientKey, user.ClientKeyData)
			if e1 != nil || e2 != nil || !usableCertificate(certificate) {
				return callerIdentity{}, ErrCredential
			}
			if _, err := tls.X509KeyPair(certificate, key); err != nil {
				return callerIdentity{}, ErrCredential
			}
			material = append(append(material, certificate...), key...)
			caller.privateValues = append(caller.privateValues, string(key))
			authenticated = true
		}
	}
	if !authenticated {
		return callerIdentity{}, ErrCredential
	}
	caller.digest = digest(material)
	return caller, nil
}

func nonnull(raw json.RawMessage) bool {
	return len(raw) != 0 && strings.TrimSpace(string(raw)) != "null"
}

func (i *installer) kubeBytes(ctx context.Context, configPath, file, inline string) ([]byte, error) {
	if (file == "") == (inline == "") {
		return nil, ErrCredential
	}
	if inline != "" {
		data, err := base64.StdEncoding.DecodeString(inline)
		if err != nil || len(data) == 0 || len(data) > maxInputBytes {
			return nil, ErrCredential
		}
		return data, nil
	}
	relative, err := relativePath(file)
	if err != nil {
		return nil, ErrCredential
	}
	return i.read(ctx, filepath.Join(filepath.Dir(configPath), relative), maxInputBytes)
}

func usableCertificate(data []byte) bool {
	found := false
	for len(data) != 0 {
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return false
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || time.Now().Before(certificate.NotBefore) || !time.Now().Before(certificate.NotAfter) {
			return false
		}
		found, data = true, []byte(strings.TrimSpace(string(rest)))
	}
	return found
}
