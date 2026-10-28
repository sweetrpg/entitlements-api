package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
)

// TierVO is the API shape of a tier: values are rendered per key type (flags as booleans).
type TierVO struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Values        map[string]any `json:"values"`
	StripePriceID string         `json:"stripe_price_id,omitempty"`
	mcore.Auditable
}

func toTierVO(t models.Tier) TierVO {
	values := make(map[string]any, len(t.Values))
	for _, v := range t.Values {
		values[v.Key] = models.FormatValue(v.Type, v.Value)
	}
	return TierVO{ID: t.Slug, Name: t.Name, Description: t.Description, Values: values, StripePriceID: t.StripePriceID, Auditable: t.Auditable}
}

type keyBody struct {
	Key         string         `json:"key"`
	Type        models.KeyType `json:"type"`
	Description string         `json:"description"`
	TTLSeconds  int            `json:"ttl_seconds"`
}

type tierBody struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Values        map[string]any `json:"values"`
	StripePriceID string         `json:"stripe_price_id"`
}

func (b tierBody) input(slug string) catalog.TierInput {
	return catalog.TierInput{Slug: slug, Name: b.Name, Description: b.Description, Values: b.Values, StripePriceID: b.StripePriceID}
}

// paging reads limit/skip query parameters, clamping limit to a sane page.
func paging(c *gin.Context) (limit, skip int64) {
	limit, _ = strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 64)
	skip, _ = strconv.ParseInt(c.DefaultQuery("skip", "0"), 10, 64)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if skip < 0 {
		skip = 0
	}
	return limit, skip
}

// setupAdminHandlers registers the catalog write and read routes. Every route requires a user
// token carrying the admin role; the resolved canonical user ID is the audit actor.
func setupAdminHandlers(g *gin.Engine, svc *catalog.Service, client *authz.Client) {
	a := g.Group("/admin", authz.RequireAnyRole(client, constants.ServiceName, authz.RoleAdmin))

	a.GET("/keys", func(c *gin.Context) {
		limit, skip := paging(c)
		keys, err := svc.ListKeys(c.Request.Context(), c.Query("prefix"), limit, skip)
		respond(c, http.StatusOK, keys, err)
	})
	a.POST("/keys", func(c *gin.Context) {
		var b keyBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		k, err := svc.CreateKey(c.Request.Context(), authz.Viewer(c), catalog.KeyInput{Key: b.Key, Type: b.Type, Description: b.Description, TTLSeconds: b.TTLSeconds})
		respond(c, http.StatusCreated, k, err)
	})
	a.PUT("/keys/:key", func(c *gin.Context) {
		var b keyBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		k, err := svc.UpdateKey(c.Request.Context(), authz.Viewer(c), c.Param("key"), b.Description, b.TTLSeconds)
		respond(c, http.StatusOK, k, err)
	})
	a.DELETE("/keys/:key", func(c *gin.Context) {
		respondEmpty(c, svc.DeleteKey(c.Request.Context(), authz.Viewer(c), c.Param("key")))
	})

	a.GET("/tiers", func(c *gin.Context) {
		limit, skip := paging(c)
		tiers, err := svc.ListTiers(c.Request.Context(), limit, skip)
		vos := make([]TierVO, len(tiers))
		for i, t := range tiers {
			vos[i] = toTierVO(t)
		}
		respond(c, http.StatusOK, vos, err)
	})
	a.POST("/tiers", func(c *gin.Context) {
		var b tierBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		t, err := svc.CreateTier(c.Request.Context(), authz.Viewer(c), b.input(b.ID))
		respondTier(c, http.StatusCreated, t, err)
	})
	a.PUT("/tiers/:id", func(c *gin.Context) {
		var b tierBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		t, err := svc.UpdateTier(c.Request.Context(), authz.Viewer(c), b.input(c.Param("id")))
		respondTier(c, http.StatusOK, t, err)
	})
	a.DELETE("/tiers/:id", func(c *gin.Context) {
		respondEmpty(c, svc.DeleteTier(c.Request.Context(), authz.Viewer(c), c.Param("id")))
	})

	a.GET("/default-tier", func(c *gin.Context) {
		id, err := svc.DefaultTier(c.Request.Context())
		respond(c, http.StatusOK, gin.H{"id": id}, err)
	})
	a.PUT("/default-tier", func(c *gin.Context) {
		var b struct {
			ID string `json:"id"`
		}
		if c.ShouldBindJSON(&b) != nil || b.ID == "" {
			badRequest(c, "id is required")
			return
		}
		respondEmpty(c, svc.SetDefaultTier(c.Request.Context(), authz.Viewer(c), b.ID))
	})

	a.GET("/audit", func(c *gin.Context) {
		limit, skip := paging(c)
		entries, err := svc.ListAudit(c.Request.Context(), limit, skip)
		respond(c, http.StatusOK, entries, err)
	})
}

func respond(c *gin.Context, status int, body any, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(status, body)
}

func respondTier(c *gin.Context, status int, t *models.Tier, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(status, toTierVO(*t))
}

func respondEmpty(c *gin.Context, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
