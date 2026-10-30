package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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
// user to. A checkout that switched an existing subscription to another plan returns
// switched=true and no URL.
type SessionVO struct {
	URL      string `json:"url,omitempty"`
	Switched bool   `json:"switched,omitempty"`
}

type checkoutBody struct {
	TierID    string `json:"tier_id"`
	ProductID string `json:"product_id"`
	Quantity  int64  `json:"quantity"`
	// Confirm is the user's go-ahead to switch their subscription to another plan in the same
	// group. Without it a switch is answered with 409 confirmation_required and the quote.
	Confirm bool `json:"confirm"`
	// ProrationDate is the proration_date from the quote being confirmed.
	ProrationDate int64 `json:"proration_date"`
}

// ConfirmationRequiredVO is the 409 body for a plan switch that needs the user's go-ahead: show
// them Quote, then repeat the request with confirm=true (and the quote's proration_date).
type ConfirmationRequiredVO struct {
	Error   string         `json:"error"`
	Message string         `json:"message"`
	Quote   *billing.Quote `json:"quote"`
}

// setupBillingHandlers registers the user-facing billing routes. Like checks, they authenticate
// with the end user's own bearer token, so a session is always created for the verified caller.
func setupBillingHandlers(g *gin.Engine, svc *billing.Service, client *authz.Client) {
	s := g.Group("/billing", authz.RequireAnyRole(client, constants.ServiceName, anyRole...))
	s.POST("/checkout", checkoutHandler(svc))
	s.GET("/quote", quoteHandler(svc))
	s.POST("/portal", portalHandler(svc))
}

// checkoutHandler starts a purchase or switches a plan.
//
//	@Summary		Start a checkout or switch a plan
//	@Description	Returns a Stripe-hosted checkout URL, or switches the caller's existing subscription in the product's plan group. A switch needs confirm=true: without it the response is 409 confirmation_required carrying a quote to show the user; an upgrade is charged immediately and a failed payment is 402 payment_failed with nothing changed.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			body	body		checkoutBody	true	"tier_id or product_id (+ quantity, confirm, proration_date)"
//	@Success		200		{object}	SessionVO
//	@Failure		400		{object}	vo.ErrorVO
//	@Failure		402		{object}	vo.ErrorVO
//	@Failure		409		{object}	ConfirmationRequiredVO
//	@Failure		502		{object}	vo.ErrorVO
//	@Router			/billing/checkout [post]
func checkoutHandler(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var b checkoutBody
		if c.ShouldBindJSON(&b) != nil {
			badRequest(c, "malformed JSON body")
			return
		}
		res, err := svc.Checkout(c.Request.Context(), authz.Viewer(c), tokenEmail(authz.Token(c)), billing.CheckoutRequest{
			TierID: b.TierID, ProductID: b.ProductID, Quantity: b.Quantity, Confirm: b.Confirm, ProrationDate: b.ProrationDate,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, SessionVO{URL: res.URL, Switched: res.Switched})
	}
}

// quoteHandler prices a purchase or plan change without changing anything.
//
//	@Summary		Quote a purchase or plan change
//	@Description	What POST /billing/checkout would do and what it would cost, changing nothing. action is checkout, switch, or none. Amounts are minor units (cents). For a switch the figure comes from Stripe's invoice preview with the same proration the real switch uses; send proration_date back with confirm so the charge matches.
//	@Tags			billing
//	@Produce		json
//	@Param			product_id	query		string	true	"Product to buy or switch to"
//	@Param			quantity	query		int		false	"Quantity (default 1)"
//	@Success		200			{object}	billing.Quote
//	@Failure		400			{object}	vo.ErrorVO
//	@Failure		409			{object}	vo.ErrorVO
//	@Failure		502			{object}	vo.ErrorVO
//	@Router			/billing/quote [get]
func quoteHandler(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		qty, err := strconv.ParseInt(c.DefaultQuery("quantity", "1"), 10, 64)
		if err != nil {
			badRequest(c, "quantity must be an integer")
			return
		}
		q, err := svc.Quote(c.Request.Context(), authz.Viewer(c), billing.CheckoutRequest{ProductID: c.Query("product_id"), Quantity: qty})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, q)
	}
}

// portalHandler opens the Stripe billing portal.
//
//	@Summary		Open the billing portal
//	@Tags			billing
//	@Produce		json
//	@Success		200	{object}	SessionVO
//	@Failure		404	{object}	vo.ErrorVO
//	@Failure		502	{object}	vo.ErrorVO
//	@Router			/billing/portal [post]
func portalHandler(svc *billing.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		url, err := svc.Portal(c.Request.Context(), authz.Viewer(c))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, SessionVO{URL: url})
	}
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
