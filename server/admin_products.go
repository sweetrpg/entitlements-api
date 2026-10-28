package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
)

// ProductVO is the API shape of a product: entitlement values are rendered per key type.
type ProductVO struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Billing       string         `json:"billing"`
	Repeatable    bool           `json:"repeatable"`
	StripePriceID string         `json:"stripe_price_id,omitempty"`
	Entitlements  map[string]any `json:"entitlements"`
	mcore.Auditable
}

func toProductVO(p models.Product) ProductVO {
	ents := make(map[string]any, len(p.Entitlements))
	for _, e := range p.Entitlements {
		ents[e.Key] = models.FormatValue(e.Type, e.Value)
	}
	return ProductVO{
		ID: p.Slug, Name: p.Name, Description: p.Description, Billing: p.Billing, Repeatable: p.Repeatable,
		StripePriceID: p.StripePriceID, Entitlements: ents, Auditable: p.Auditable,
	}
}

type productBody struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Billing       string         `json:"billing"`
	Repeatable    bool           `json:"repeatable"`
	StripePriceID string         `json:"stripe_price_id"`
	Entitlements  map[string]any `json:"entitlements"`
}

func (b productBody) input(slug string) catalog.ProductInput {
	return catalog.ProductInput{
		Slug: slug, Name: b.Name, Description: b.Description, Billing: b.Billing,
		Repeatable: b.Repeatable, StripePriceID: b.StripePriceID, Entitlements: b.Entitlements,
	}
}

func respondProduct(c *gin.Context, status int, p *models.Product, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(status, toProductVO(*p))
}

// setupAdminProductHandlers registers the product routes on the admin group.
func setupAdminProductHandlers(a *gin.RouterGroup, svc *catalog.Service) {
	a.GET("/products", func(c *gin.Context) {
		limit, skip := paging(c)
		products, err := svc.ListProducts(c.Request.Context(), limit, skip)
		vos := make([]ProductVO, len(products))
		for i, p := range products {
			vos[i] = toProductVO(p)
		}
		respond(c, http.StatusOK, vos, err)
	})
	a.POST("/products", func(c *gin.Context) {
		var b productBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		p, err := svc.CreateProduct(c.Request.Context(), authz.Viewer(c), b.input(b.ID))
		respondProduct(c, http.StatusCreated, p, err)
	})
	a.PUT("/products/:id", func(c *gin.Context) {
		var b productBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		p, err := svc.UpdateProduct(c.Request.Context(), authz.Viewer(c), b.input(c.Param("id")))
		respondProduct(c, http.StatusOK, p, err)
	})
	a.DELETE("/products/:id", func(c *gin.Context) {
		respondEmpty(c, svc.DeleteProduct(c.Request.Context(), authz.Viewer(c), c.Param("id")))
	})
}
