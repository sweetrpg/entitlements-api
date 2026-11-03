package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
)

// GrantVO is the API shape of one active grant or override. Value is a boolean for a flag and an
// integer for a limit; it is absent on a tier grant, whose values come from the tier.
type GrantVO struct {
	TierID     string     `json:"tier_id,omitempty"`
	Key        string     `json:"key,omitempty"`
	Value      any        `json:"value,omitempty"`
	Mode       string     `json:"mode,omitempty"`
	Source     string     `json:"source"`
	ProductID  string     `json:"product_id,omitempty"`
	Quantity   int64      `json:"quantity,omitempty"`
	Override   bool       `json:"override"`
	Reason     string     `json:"reason,omitempty"`
	SetBy      string     `json:"set_by,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

func toGrantVO(g catalog.GrantView) GrantVO {
	vo := GrantVO{
		TierID: g.TierID, Key: g.Key, Mode: g.Mode, Source: g.Source, ProductID: g.ProductID,
		Quantity: g.Quantity, Override: g.Mode == models.ModeOverride, Reason: g.Reason, ValidUntil: g.ValidUntil,
	}
	if g.Key != "" {
		vo.Value = models.FormatValue(g.Type, g.Value)
	}
	if vo.Override {
		vo.SetBy = g.UpdatedBy
		if vo.SetBy == "" {
			vo.SetBy = g.CreatedBy
		}
	}
	return vo
}

type overrideBody struct {
	Value      any        `json:"value"`
	ValidUntil *time.Time `json:"valid_until"`
	Reason     string     `json:"reason"`
}

type removeOverrideBody struct {
	Reason string `json:"reason"`
}

// setupAdminOverrideHandlers registers the per-user routes on the admin group. The user is the
// canonical user ID in the path. Writes need a reason and are audited; none of them deletes or
// ends anything except removing the override itself.
func setupAdminOverrideHandlers(a *gin.RouterGroup, svc *catalog.Service) {
	a.GET("/users/:user_id/grants", func(c *gin.Context) {
		grants, err := svc.UserGrants(c.Request.Context(), c.Param("user_id"))
		vos := make([]GrantVO, len(grants))
		for i, g := range grants {
			vos[i] = toGrantVO(g)
		}
		respond(c, http.StatusOK, vos, err)
	})
	a.PUT("/users/:user_id/overrides/:key", func(c *gin.Context) {
		var b overrideBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		_, err := svc.SetOverride(c.Request.Context(), authz.Viewer(c), c.Param("user_id"), c.Param("key"),
			catalog.OverrideInput{Value: b.Value, ValidUntil: b.ValidUntil, Reason: b.Reason})
		if err != nil {
			writeError(c, err)
			return
		}
		respondOverride(c, svc, c.Param("user_id"), c.Param("key"))
	})
	a.DELETE("/users/:user_id/overrides/:key", func(c *gin.Context) {
		var b removeOverrideBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		respondEmpty(c, svc.RemoveOverride(c.Request.Context(), authz.Viewer(c), c.Param("user_id"), c.Param("key"), b.Reason))
	})
}

// respondOverride answers a set with the user's override for key as stored.
func respondOverride(c *gin.Context, svc *catalog.Service, userID, key string) {
	grants, err := svc.UserGrants(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}
	for _, g := range grants {
		if g.Key == key && g.Mode == models.ModeOverride {
			c.JSON(http.StatusOK, toGrantVO(g))
			return
		}
	}
	writeError(c, catalog.ErrNotFound)
}
