package server

import (
	"net/http"
	"strings"
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
	Value                    any        `json:"value"`
	ValidUntil               *time.Time `json:"valid_until"`
	Reason                   string     `json:"reason"`
	AcknowledgePaidReduction bool       `json:"acknowledge_paid_reduction"`
	Reference                string     `json:"reference"`
}

// PaidReductionVO is the 409 body when an override would lower a value below what the user paid
// for and the request did not acknowledge it. Values are booleans for flags, integers for limits.
type PaidReductionVO struct {
	Error    string    `json:"error"`
	Message  string    `json:"message"`
	Key      string    `json:"key"`
	Paid     any       `json:"paid"`
	Proposed any       `json:"proposed"`
	Payments []GrantVO `json:"payments"`
}

func toPaidReductionVO(e *catalog.PaidReductionError) PaidReductionVO {
	t := e.Key.Type
	vo := PaidReductionVO{
		Error: "paid_reduction_not_acknowledged", Message: e.Error(), Key: e.Key.Key,
		Paid: models.FormatValue(t, e.Paid), Proposed: models.FormatValue(t, e.Proposed), Payments: []GrantVO{},
	}
	for _, g := range e.Payments {
		vo.Payments = append(vo.Payments, toGrantVO(catalog.GrantView{Grant: g, Type: t}))
	}
	return vo
}

// PaidReductionEntryVO is one acknowledged override that lowered a paid value.
type PaidReductionEntryVO struct {
	At            time.Time `json:"at"`
	Administrator string    `json:"administrator"`
	UserID        string    `json:"user_id"`
	Key           string    `json:"key"`
	Paid          any       `json:"paid"`
	NewValue      any       `json:"new_value"`
	Reason        string    `json:"reason"`
	Reference     string    `json:"reference"`
}

func toPaidReductionEntryVO(e models.AuditEntry) PaidReductionEntryVO {
	user, key := e.Subject, ""
	if i := strings.LastIndex(e.Subject, "/"); i >= 0 {
		user, key = e.Subject[:i], e.Subject[i+1:]
	}
	return PaidReductionEntryVO{
		At: e.At, Administrator: e.Actor, UserID: user, Key: key, Paid: e.Paid, NewValue: e.After,
		Reason: e.Reason, Reference: e.Reference,
	}
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
	a.GET("/users/:user_id/entitlements", func(c *gin.Context) { respondEntitlements(c, svc) })
	a.GET("/users/:user_id/audit", func(c *gin.Context) {
		limit, skip := paging(c)
		entries, err := svc.UserAudit(c.Request.Context(), c.Param("user_id"), limit, skip)
		if entries == nil {
			entries = []models.AuditEntry{}
		}
		respond(c, http.StatusOK, entries, err)
	})
	a.GET("/paid-reductions", func(c *gin.Context) {
		limit, skip := paging(c)
		entries, err := svc.PaidReductions(c.Request.Context(), limit, skip)
		vos := make([]PaidReductionEntryVO, len(entries))
		for i, e := range entries {
			vos[i] = toPaidReductionEntryVO(e)
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
			catalog.OverrideInput{
				Value: b.Value, ValidUntil: b.ValidUntil, Reason: b.Reason,
				AcknowledgePaidReduction: b.AcknowledgePaidReduction, Reference: b.Reference,
			})
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

// TierGrantVO is a tier grant's value for the key being explained.
type TierGrantVO struct {
	TierID string `json:"tier_id"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// EntitlementVO explains one key for one user. Values are booleans for flags and integers for
// limits. Paid is the value from payment-sourced grants alone, Payments the grants behind it.
type EntitlementVO struct {
	Key        string        `json:"key"`
	Type       string        `json:"type"`
	Default    any           `json:"default"`
	TierGrants []TierGrantVO `json:"tier_grants"`
	Grants     []GrantVO     `json:"grants"`
	Override   *GrantVO      `json:"override"`
	Paid       any           `json:"paid"`
	Payments   []GrantVO     `json:"payments"`
	Effective  any           `json:"effective"`
}

func toEntitlementVO(e catalog.Explanation) EntitlementVO {
	t := e.Key.Type
	vo := EntitlementVO{
		Key: e.Key.Key, Type: string(t), TierGrants: []TierGrantVO{}, Grants: []GrantVO{}, Payments: []GrantVO{},
		Paid: models.FormatValue(t, e.Paid), Effective: models.FormatValue(t, e.Effective),
	}
	if e.Default != nil {
		vo.Default = models.FormatValue(t, *e.Default)
	}
	for _, tg := range e.TierGrants {
		vo.TierGrants = append(vo.TierGrants, TierGrantVO{TierID: tg.TierID, Value: models.FormatValue(t, tg.Value), Source: tg.Source})
	}
	for _, g := range e.KeyGrants {
		vo.Grants = append(vo.Grants, toGrantVO(catalog.GrantView{Grant: g, Type: t}))
	}
	for _, g := range e.Payments {
		vo.Payments = append(vo.Payments, toGrantVO(catalog.GrantView{Grant: g, Type: t}))
	}
	if e.Override != nil {
		o := toGrantVO(catalog.GrantView{Grant: *e.Override, Type: t})
		vo.Override = &o
	}
	return vo
}

func respondEntitlements(c *gin.Context, svc *catalog.Service) {
	list, err := svc.ExplainUser(c.Request.Context(), c.Param("user_id"), c.Query("prefix"))
	vos := make([]EntitlementVO, len(list))
	for i, e := range list {
		vos[i] = toEntitlementVO(e)
	}
	respond(c, http.StatusOK, vos, err)
}
