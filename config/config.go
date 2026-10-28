// Package config validates the settings the service cannot run without, so a bad deployment
// fails at startup with a message naming the problem instead of at the first request.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sweetrpg/entitlements-api/constants"
)

// minTokenLength rejects trivially guessable service tokens.
const minTokenLength = 32

// Config holds validated settings.
type Config struct {
	AuthAPIURL  string
	UsersAPIURL string
	// ServiceTokens maps a bearer token to the name of the registered service that owns it.
	ServiceTokens map[string]string
}

// Load reads and validates settings through getenv (os.Getenv in production). Every problem is
// reported at once.
func Load(getenv func(string) string) (*Config, error) {
	var problems []string

	cfg := &Config{
		AuthAPIURL:  getenv(constants.AUTH_API_URL),
		UsersAPIURL: getenv(constants.USERS_API_URL),
	}
	for _, name := range []string{constants.AUTH_API_URL, constants.USERS_API_URL} {
		if getenv(name) == "" {
			problems = append(problems, name+" is required")
		}
	}

	if getenv("DB_URI") == "" && getenv("DB_HOST") == "" {
		problems = append(problems, "DB_URI (or DB_HOST and DB_NAME) is required")
	}

	tokens, err := parseServiceTokens(getenv(constants.SERVICE_TOKENS))
	if err != nil {
		problems = append(problems, constants.SERVICE_TOKENS+": "+err.Error())
	}
	cfg.ServiceTokens = tokens

	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// parseServiceTokens parses "name=token,name=token". Names and tokens must be unique so a
// token always identifies exactly one service.
func parseServiceTokens(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("at least one name=token pair is required")
	}
	byToken := map[string]string{}
	names := map[string]bool{}
	for _, pair := range strings.Split(raw, ",") {
		name, token, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || name == "" {
			return nil, errors.New("entries must look like name=token")
		}
		if len(token) < minTokenLength {
			return nil, fmt.Errorf("token for %q is shorter than %d characters", name, minTokenLength)
		}
		if names[name] {
			return nil, fmt.Errorf("service %q is listed twice", name)
		}
		if _, dup := byToken[token]; dup {
			return nil, fmt.Errorf("service %q reuses another service's token", name)
		}
		names[name] = true
		byToken[token] = name
	}
	return byToken, nil
}
