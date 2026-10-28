package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v87/webhook"
	apiv "github.com/sweetrpg/api-core.go/vo"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/billing"
	"github.com/sweetrpg/entitlements-api/constants"
)

// SessionVO is the response to a checkout or portal request: the Stripe-hosted URL to send the
// user to.
type SessionVO struct {
	URL string `json:"url"`
}

type checkoutBody struct {
	TierID string `json:"tier_id"`
}

// setupBillingHandlers registers the user-facing billing routes. Like checks, they authenticate
// with the end user's own bearer token, so a session is always created for the verified caller.
func setupBillingHandlers(g *gin.Engine, svc *billing.Service, client *authz.Client) {
	s := g.Group("/billing", authz.RequireAnyRole(client, constants.ServiceName, anyRole...))

	s.POST("/checkout", func(c *gin.Context) {
		var b checkoutBody
		if c.ShouldBindJSON(&b) != nil || b.TierID == "" {
			badRequest(c, "tier_id is required")
			return
		}
		url, err := svc.Checkout(c.Request.Context(), authz.Viewer(c), tokenEmail(authz.Token(c)), b.TierID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, SessionVO{URL: url})
	})

	s.POST("/portal", func(c *gin.Context) {
		url, err := svc.Portal(c.Request.Context(), authz.Viewer(c))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, SessionVO{URL: url})
	})
}

// setupWebhookHandlers registers the public Stripe webhook. It sits outside the user-token
// groups on purpose: Stripe cannot present a user token, so the signature over the raw body is
// the only authentication, and nothing is read or changed before it verifies.
func setupWebhookHandlers(g *gin.Engine, svc *billing.Service, signingSecret string) {
	g.POST("/webhooks/stripe", func(c *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, constants.MaxWebhookBodyBytes))
		if err != nil {
			badRequest(c, "unreadable body")
			return
		}
		// Event objects are decoded by the handlers' own minimal structs, so the SDK's API-version
		// compatibility check would only reject events for fields we never read.
		ev, err := webhook.ConstructEventWithOptions(body, c.GetHeader("Stripe-Signature"), signingSecret,
			webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
		if err != nil {
			c.JSON(http.StatusBadRequest, apiv.ErrorVO{Error: "invalid_signature", Message: "signature verification failed"})
			return
		}
		err = svc.HandleEvent(c.Request.Context(), billing.Event{ID: ev.ID, Type: string(ev.Type), Object: ev.Data.Raw})
		if err != nil {
			logging.Logger.Error("stripe event failed", "event", ev.ID, "type", string(ev.Type), "error", err.Error())
			c.JSON(http.StatusInternalServerError, apiv.ErrorVO{Error: "internal_error", Message: "event not processed"})
			return
		}
		c.Status(http.StatusOK)
	})
}

// tokenEmail reads the email claim from a token auth-api has already verified, to prefill
// Checkout. It does not verify anything itself and returns "" when the claim is absent.
func tokenEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Email
}
