package server

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	apiv "github.com/sweetrpg/api-core.go/vo"
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

	s.GET("", checkOne(svc))
	s.GET("/batch", checkBatch(svc))
}

// checkOne returns the caller's effective value for one key.
//
//	@Summary		Check one key
//	@Description	Effective value for one key for the verified caller. Cacheable for ttl_seconds.
//	@Tags			check
//	@Produce		json
//	@Param			key	query		string	true	"Entitlement key, e.g. initiative.theme_slots"
//	@Success		200	{object}	CheckVO
//	@Failure		400	{object}	vo.ErrorVO
//	@Failure		401	{object}	vo.ErrorVO
//	@Failure		404	{object}	vo.ErrorVO
//	@Router			/check [get]
func checkOne(svc *catalog.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
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
	}
}

// checkBatch returns effective values for several keys.
//
//	@Summary		Check several keys
//	@Description	Exactly one of repeated key params or a prefix. An unknown key fails the whole batch with 404.
//	@Tags			check
//	@Produce		json
//	@Param			key		query		[]string	false	"Entitlement key; repeat for several (max 200)"	collectionFormat(multi)
//	@Param			prefix	query		string		false	"Key prefix, e.g. initiative. (max 128 characters)"
//	@Success		200		{object}	BatchCheckVO
//	@Failure		400		{object}	vo.ErrorVO
//	@Failure		401		{object}	vo.ErrorVO
//	@Failure		404		{object}	vo.ErrorVO
//	@Router			/check/batch [get]
func checkBatch(svc *catalog.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		keys, prefix := c.QueryArray("key"), c.Query("prefix")
		if code, msg := validateBatchSelector(keys, prefix); code != "" {
			c.JSON(http.StatusBadRequest, apiv.ErrorVO{Error: code, Message: msg})
			return
		}
		var results []catalog.Result
		var err error
		if prefix != "" {
			results, err = svc.CheckPrefix(c.Request.Context(), authz.Viewer(c), prefix)
		} else {
			results, err = svc.Check(c.Request.Context(), authz.Viewer(c), keys)
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
	}
}

// validateBatchSelector requires exactly one of repeated key params or a prefix, within the
// caps in constants. It returns an error code and message, or "" when the selector is valid.
func validateBatchSelector(keys []string, prefix string) (code, msg string) {
	switch {
	case (len(keys) == 0) == (prefix == ""):
		return "invalid_selector", "provide either key (repeatable) or prefix, not both and not neither"
	case len(keys) > constants.MaxBatchKeys:
		return "too_many_keys", fmt.Sprintf("at most %d keys per request", constants.MaxBatchKeys)
	case len(prefix) > constants.MaxPrefixLength:
		return "prefix_too_long", fmt.Sprintf("prefix may be at most %d characters", constants.MaxPrefixLength)
	}
	return "", ""
}

func cacheControl(ttl int) string {
	if ttl <= 0 {
		return "no-store"
	}
	return fmt.Sprintf("private, max-age=%d", ttl)
}
