package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordHashUsesIndependentSaltAndBoundedParameters(t *testing.T) {
	a := &Application{hashSlots: make(chan struct{}, 2)}
	password := randomToken()
	first, err := a.hashPassword(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.hashPassword(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "$argon2id$v=19$"+passwordParameters+"$") {
		t.Fatal("password hash must use a fresh salt and the configured Argon2id parameters")
	}
	if !a.checkPassword(context.Background(), password, first) ||
		a.checkPassword(context.Background(), randomToken(), first) ||
		a.checkPassword(context.Background(), password, strings.Replace(first, "m=65536", "m=1", 1)) {
		t.Fatal("password verification did not enforce the stored hash contract")
	}
}

func TestWritesRequireAnExplicitMatchingHTTPSOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, target, origin, configured string
		valid                            bool
	}{
		{"same-origin", "https://aspm.test/api/v1/assets", "https://aspm.test", "", true},
		{"default-port", "https://aspm.test:443/api/v1/assets", "https://aspm.test", "", true},
		{"missing", "https://aspm.test/api/v1/assets", "", "", false},
		{"foreign", "https://aspm.test/api/v1/assets", "https://other.invalid", "", false},
		{"plaintext-origin", "https://aspm.test/api/v1/assets", "http://aspm.test", "", false},
		{"different-port", "https://aspm.test/api/v1/assets", "https://aspm.test:8443", "", false},
		{"credentials", "https://aspm.test/api/v1/assets", "https://user@aspm.test", "", false},
		{"origin-path", "https://aspm.test/api/v1/assets", "https://aspm.test/path", "", false},
		{"plaintext-host", "http://aspm.test/api/v1/assets", "https://aspm.test", "", false},
		{"explicit-proxy-origin", "http://localhost/api/v1/assets", "https://aspm.test", "https://aspm.test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Application{config: Config{PublicOrigin: tc.configured}}
			request := httptest.NewRequest("POST", tc.target, nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if a.validOrigin(request) != tc.valid {
				t.Fatal("unexpected origin decision")
			}
			request.Header.Set("X-Forwarded-Host", "other.invalid")
			if a.validOrigin(request) != tc.valid {
				t.Fatal("untrusted forwarded headers changed the origin decision")
			}
		})
	}
}
