package kube

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/discovery"
)

// ServerVersionProvider detects the cluster's scheduler minor for the version
// banner / mismatch warning.
type ServerVersionProvider interface {
	// ServerMinor returns major and minor (e.g. "1", "32"). On a parse failure
	// for non-standard version strings (e.g. GKE "v1.30.1-gke.x"), it degrades
	// gracefully: returns empty strings and a nil error so the caller simply
	// turns off the version warning rather than failing the run.
	ServerMinor(ctx context.Context) (major, minor string, err error)
}

type versionProvider struct {
	discovery discovery.DiscoveryInterface
}

var _ ServerVersionProvider = (*versionProvider)(nil)

// NewVersionProvider returns a ServerVersionProvider over the discovery client.
func NewVersionProvider(d discovery.DiscoveryInterface) ServerVersionProvider {
	return &versionProvider{discovery: d}
}

func (v *versionProvider) ServerMinor(_ context.Context) (string, string, error) {
	info, err := v.discovery.ServerVersion()
	if err != nil {
		// Real RPC failure (not a parse issue) — propagate.
		return "", "", fmt.Errorf("discovery server version: %w", err)
	}
	major := sanitizeVersionField(info.Major)
	minor := sanitizeVersionField(info.Minor)
	if major == "" || minor == "" {
		// Fall back to parsing GitVersion like "v1.32.3" or "v1.30.1-gke.100".
		major, minor = parseGitVersion(info.GitVersion)
	}
	// If still unparseable, degrade gracefully (no warning, no error).
	return major, minor, nil
}

// sanitizeVersionField strips a trailing "+" (e.g. EKS reports minor "32+")
// and keeps only leading digits.
func sanitizeVersionField(s string) string {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

// parseGitVersion extracts major.minor from a git version string such as
// "v1.32.3" or "v1.30.1-gke.100". Returns empty strings if it cannot parse.
func parseGitVersion(gitVersion string) (string, string) {
	g := strings.TrimPrefix(strings.TrimSpace(gitVersion), "v")
	parts := strings.SplitN(g, ".", 3)
	if len(parts) < 2 {
		return "", ""
	}
	major := sanitizeVersionField(parts[0])
	minor := sanitizeVersionField(parts[1])
	if major == "" || minor == "" {
		return "", ""
	}
	return major, minor
}
