// Package config validates the settings the service cannot run without, so a bad deployment
// fails at startup with a message naming the problem instead of at the first request.
package config

import (
	"errors"
	"strings"

	"github.com/sweetrpg/entitlements-api/constants"
)

// Config holds validated settings.
type Config struct {
	AuthAPIURL  string
	UsersAPIURL string
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

	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}
