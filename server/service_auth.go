package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	apiv "github.com/sweetrpg/api-core.go/vo"
	"github.com/sweetrpg/common.go/logging"
)

const callerContextKey = "entitlements.caller"

// requireService admits only callers presenting a registered service token and records the
// service name in the context. Every registered token is compared (no early exit) so timing
// does not reveal which one matched.
func requireService(tokens map[string]string) gin.HandlerFunc {
	return func(c *gin.Context) {
		presented, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		caller := ""
		if ok && presented != "" {
			for token, name := range tokens {
				if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
					caller = name
				}
			}
		}
		if caller == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiv.ErrorVO{Error: "unauthorized", Message: "missing or invalid service credential"})
			return
		}
		logging.Logger.Debug("entitlement check caller", "service", caller)
		c.Set(callerContextKey, caller)
		c.Next()
	}
}
