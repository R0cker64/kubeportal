package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"KUBEPORTAL_PUBLIC_URL":         "http://localhost:8080",
		"KUBEPORTAL_ENVIRONMENTS":       "dev, staging,prod,dev",
		"KUBEPORTAL_OIDC_ISSUER_URL":    "http://localhost:8180/realms/kubeportal",
		"KUBEPORTAL_OIDC_CLIENT_ID":     "kubeportal",
		"KUBEPORTAL_OIDC_CLIENT_SECRET": "test-client-secret",
		"KUBEPORTAL_SESSION_KEY":        strings.Repeat("ab", 32),
		"KUBEPORTAL_DATABASE_URL":       "postgres://test@localhost/test",
		"KUBEPORTAL_LOKI_URL":           "http://loki.test",
		"KUBEPORTAL_PROMETHEUS_URL":     "http://prometheus.test",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if got := strings.Join(cfg.Environments, ","); got != "dev,staging,prod" {
		t.Errorf("Environments = %q", got)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second || cfg.MaxBodyBytes <= 0 {
		t.Errorf("unexpected HTTP limits: %+v", cfg)
	}
	if cfg.OIDCDiscoveryURL.String() != cfg.OIDCIssuerURL.String() {
		t.Errorf("OIDCDiscoveryURL should default to the issuer, got %v", cfg.OIDCDiscoveryURL)
	}
	if cfg.AdminGroup != "kubeportal-admin" || len(cfg.SessionKey) != 32 {
		t.Errorf("unexpected auth defaults: admin=%q key=%d bytes", cfg.AdminGroup, len(cfg.SessionKey))
	}
	if cfg.LogMaxRange != 7*24*time.Hour || cfg.LogMaxLines != 1000 || cfg.LiveTailsPerUser != 3 {
		t.Errorf("unexpected log limits: %+v", cfg)
	}
}

func TestErrorsNeverIncludeSecretValues(t *testing.T) {
	env := validEnv()
	env["KUBEPORTAL_SESSION_KEY"] = "deadbeef-not-hex-secret"
	_, err := LoadFrom(lookupFrom(env))
	if err == nil || strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("expected an error without the secret value, got %v", err)
	}
}

func TestKubeconfigRequiresExplicitContext(t *testing.T) {
	env := validEnv()
	env["KUBEPORTAL_KUBECONFIG"] = "/tmp/kubeconfig"
	if _, err := LoadFrom(lookupFrom(env)); err == nil {
		t.Fatal("expected an error when KUBECONFIG is set without KUBE_CONTEXT")
	}
	env["KUBEPORTAL_KUBE_CONTEXT"] = "kind-kubeportal"
	if _, err := LoadFrom(lookupFrom(env)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"KUBEPORTAL_PUBLIC_URL":               "ftp://example.com",
		"KUBEPORTAL_ENVIRONMENTS":             "dev,Prod",
		"KUBEPORTAL_HTTP_READ_TIMEOUT":        "-1s",
		"KUBEPORTAL_LOG_LEVEL":                "verbose",
		"KUBEPORTAL_HTTP_IDLE_TIMEOUT":        "soon",
		"KUBEPORTAL_SHUTDOWN_TIMEOUT":         "0s",
		"KUBEPORTAL_HTTP_WRITE_TIMEOUT":       "abc",
		"KUBEPORTAL_HTTP_READ_HEADER_TIMEOUT": "1",
		"KUBEPORTAL_SESSION_KEY":              "abcd",
		"KUBEPORTAL_OIDC_ISSUER_URL":          "localhost:8180",
		"KUBEPORTAL_OIDC_DISCOVERY_URL":       "keycloak",
		"KUBEPORTAL_LOG_MAX_LINES":            "0",
		"KUBEPORTAL_LOKI_URL":                 "file:///etc/passwd",
	}
	for key, val := range cases {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			env[key] = val
			if _, err := LoadFrom(lookupFrom(env)); err == nil {
				t.Fatalf("expected error for %s=%q", key, val)
			}
		})
	}
}

func TestLoadRequiresMandatoryKeys(t *testing.T) {
	for _, key := range []string{
		"KUBEPORTAL_PUBLIC_URL", "KUBEPORTAL_ENVIRONMENTS", "KUBEPORTAL_OIDC_ISSUER_URL",
		"KUBEPORTAL_OIDC_CLIENT_ID", "KUBEPORTAL_OIDC_CLIENT_SECRET", "KUBEPORTAL_SESSION_KEY",
		"KUBEPORTAL_DATABASE_URL", "KUBEPORTAL_LOKI_URL", "KUBEPORTAL_PROMETHEUS_URL",
	} {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			delete(env, key)
			_, err := LoadFrom(lookupFrom(env))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected error mentioning %s, got %v", key, err)
			}
		})
	}
}
