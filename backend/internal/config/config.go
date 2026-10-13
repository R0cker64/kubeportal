// Package config loads and validates the server configuration from the
// environment. Loading fails fast so the process never starts half-configured.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const envPrefix = "KUBEPORTAL_"

// Config holds the validated runtime configuration.
type Config struct {
	ListenAddr   string
	PublicURL    *url.URL
	StaticDir    string
	LogLevel     slog.Level
	Environments []string
	MaxBodyBytes int64

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// k8sNameRe matches an RFC 1123 label (max length checked separately).
var k8sNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Load reads the configuration using os.LookupEnv.
func Load() (*Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads the configuration using the given lookup function, which
// makes it testable without touching the process environment.
func LoadFrom(lookup func(string) (string, bool)) (*Config, error) {
	l := loader{lookup: lookup}

	cfg := &Config{
		ListenAddr:        l.str("LISTEN_ADDR", ":8080"),
		StaticDir:         l.str("STATIC_DIR", ""),
		MaxBodyBytes:      1 << 20,
		ReadHeaderTimeout: l.duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       l.duration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      l.duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout:   l.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
	}

	cfg.PublicURL = l.url("PUBLIC_URL")
	cfg.LogLevel = l.logLevel("LOG_LEVEL", slog.LevelInfo)
	cfg.Environments = l.environments("ENVIRONMENTS")

	if err := errors.Join(l.errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

type loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (l *loader) raw(key string) (string, bool) {
	v, ok := l.lookup(envPrefix + key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s%s: %s", envPrefix, key, fmt.Sprintf(format, args...)))
}

func (l *loader) str(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail(key, "must be a positive duration")
		return def
	}
	return d
}

func (l *loader) url(key string) *url.URL {
	v, ok := l.raw(key)
	if !ok {
		l.fail(key, "is required")
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail(key, "must be an absolute http(s) URL")
		return nil
	}
	return u
}

func (l *loader) logLevel(key string, def slog.Level) slog.Level {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		l.fail(key, "must be one of debug, info, warn, error")
		return def
	}
	return lvl
}

func (l *loader) environments(key string) []string {
	v, ok := l.raw(key)
	if !ok {
		l.fail(key, "is required")
		return nil
	}
	seen := map[string]bool{}
	var envs []string
	for _, e := range strings.Split(v, ",") {
		e = strings.TrimSpace(e)
		if len(e) > 63 || !k8sNameRe.MatchString(e) {
			l.fail(key, "%q is not a valid namespace name", e)
			continue
		}
		if !seen[e] {
			seen[e] = true
			envs = append(envs, e)
		}
	}
	return envs
}
