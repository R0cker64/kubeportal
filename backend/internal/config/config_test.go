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
		"KUBEPORTAL_PUBLIC_URL":   "http://localhost:8080",
		"KUBEPORTAL_ENVIRONMENTS": "dev, staging,prod,dev",
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
	for _, key := range []string{"KUBEPORTAL_PUBLIC_URL", "KUBEPORTAL_ENVIRONMENTS"} {
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
