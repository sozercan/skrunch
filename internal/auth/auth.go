package auth

import (
	"fmt"
	"net/url"
	"strings"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
)

const githubHost = "github.com"

type Resolved struct {
	Host   string
	Token  string
	Source string
}

func Resolve(host string) (Resolved, error) {
	normalized, err := NormalizeHost(host)
	if err != nil {
		return Resolved{}, err
	}

	token, source := ghauth.TokenFromEnvOrConfig(normalized)
	return Resolved{
		Host:   normalized,
		Token:  token,
		Source: source,
	}, nil
}

func NormalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		defaultHost, _ := ghauth.DefaultHost()
		host = defaultHost
	}
	if host == "" {
		host = githubHost
	}

	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("parse host %q: %w", host, err)
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("invalid host %q", host)
		}
		host = parsed.Host
	}

	host = strings.TrimSuffix(host, "/")
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	if host == "" {
		return "", fmt.Errorf("invalid host")
	}

	return ghauth.NormalizeHostname(host), nil
}

func APIBaseURL(host string) string {
	if ghauth.NormalizeHostname(host) == githubHost {
		return "https://api.github.com/"
	}
	return fmt.Sprintf("https://%s/api/v3/", host)
}

func UploadBaseURL(host string) string {
	if ghauth.NormalizeHostname(host) == githubHost {
		return "https://uploads.github.com/"
	}
	return fmt.Sprintf("https://%s/api/uploads/", host)
}

func IsEnterprise(host string) bool {
	return ghauth.IsEnterprise(host)
}
