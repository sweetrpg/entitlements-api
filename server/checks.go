package server

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
)

// CheckVO is one key's effective value. Consumers may cache it for TTLSeconds.
type CheckVO struct {
	Key        string         `json:"key"`
	Type       models.KeyType `json:"type"`
	Value      any            `json:"value"`
	TTLSeconds int            `json:"ttl_seconds"`
}

// BatchCheckVO is the response to a batch check.
type BatchCheckVO struct {
	Results []CheckVO `json:"results"`
}

type batchBody struct {
	Keys   []string `json:"keys"`
	Prefix string   `json:"prefix"`
}

func toCheckVO(r catalog.Result) CheckVO {
	return CheckVO{Key: r.Key, Type: r.Type, Value: models.FormatValue(r.Type, r.Value), TTLSeconds: r.TTLSeconds}
}

// anyRole lists every platform role: a check needs a verified user, not a particular role.
var anyRole = []string{authz.RoleUser, authz.RoleSubmitter, authz.RoleEditor, authz.RoleModerator, authz.RoleApprover, authz.RoleAdmin}

// setupCheckHandlers registers the check routes. The caller passes the end user's own bearer
// token; auth-api verifies it against Auth0 and users-api resolves the canonical user ID, so the
// answer is always about the verified caller and no user ID is accepted from the request.
func setupCheckHandlers(g *gin.Engine, svc *catalog.Service, client *authz.Client) {
	s := g.Group("/check", authz.RequireAnyRole(client, constants.ServiceName, anyRole...))

	s.GET("", func(c *gin.Context) {
		key := c.Query("key")
		if key == "" {
			badRequest(c, "key is required")
			return
		}
		results, err := svc.Check(c.Request.Context(), authz.Viewer(c), []string{key})
		if err != nil {
			writeError(c, err)
			return
		}
		vo := toCheckVO(results[0])
		c.Header("Cache-Control", cacheControl(vo.TTLSeconds))
		c.JSON(http.StatusOK, vo)
	})

	s.POST("/batch", func(c *gin.Context) {
		var b batchBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		if (len(b.Keys) == 0) == (b.Prefix == "") {
			badRequest(c, "provide either keys or prefix")
			return
		}
		if len(b.Keys) > constants.MaxBatchKeys {
			badRequest(c, fmt.Sprintf("at most %d keys per request", constants.MaxBatchKeys))
			return
		}
		var results []catalog.Result
		var err error
		if b.Prefix != "" {
			results, err = svc.CheckPrefix(c.Request.Context(), authz.Viewer(c), b.Prefix)
		} else {
			results, err = svc.Check(c.Request.Context(), authz.Viewer(c), b.Keys)
		}
		if err != nil {
			writeError(c, err)
			return
		}
		out := BatchCheckVO{Results: make([]CheckVO, len(results))}
		for i, r := range results {
			out.Results[i] = toCheckVO(r)
		}
		c.JSON(http.StatusOK, out)
	})
}

func cacheControl(ttl int) string {
	if ttl <= 0 {
		return "no-store"
	}
	return fmt.Sprintf("private, max-age=%d", ttl)
}
