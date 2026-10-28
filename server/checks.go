package server

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
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
	UserID  string    `json:"user_id"`
	Results []CheckVO `json:"results"`
}

type batchBody struct {
	UserID string   `json:"user_id"`
	Keys   []string `json:"keys"`
	Prefix string   `json:"prefix"`
}

func toCheckVO(r catalog.Result) CheckVO {
	return CheckVO{Key: r.Key, Type: r.Type, Value: models.FormatValue(r.Type, r.Value), TTLSeconds: r.TTLSeconds}
}

// setupCheckHandlers registers the service-to-service check routes. Browsers and native clients
// have no service credential and cannot reach them.
func setupCheckHandlers(g *gin.Engine, svc *catalog.Service, tokens map[string]string) {
	s := g.Group("/check", requireService(tokens))

	s.GET("", func(c *gin.Context) {
		userID, key := c.Query("user_id"), c.Query("key")
		if userID == "" || key == "" {
			badRequest(c, "user_id and key are required")
			return
		}
		results, err := svc.Check(c.Request.Context(), userID, []string{key})
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
		if c.ShouldBindJSON(&b) != nil || b.UserID == "" {
			badRequest(c, "user_id is required")
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
			results, err = svc.CheckPrefix(c.Request.Context(), b.UserID, b.Prefix)
		} else {
			results, err = svc.Check(c.Request.Context(), b.UserID, b.Keys)
		}
		if err != nil {
			writeError(c, err)
			return
		}
		out := BatchCheckVO{UserID: b.UserID, Results: make([]CheckVO, len(results))}
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
