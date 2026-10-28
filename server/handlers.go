package server

import (
	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/entitlements-api/billing"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/config"
)

// SetupHandlers wires every route.
func SetupHandlers(g *gin.Engine, svc *catalog.Service, bill *billing.Service, authzClient *authz.Client, cfg *config.Config) {
	setupAdminHandlers(g, svc, authzClient)
	setupCheckHandlers(g, svc, authzClient)
	setupBillingHandlers(g, bill, authzClient)
	setupWebhookHandlers(g, bill, cfg.StripeWebhookSecret)
	setupStatusHandlers(g)
}
