package server

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	apiv "github.com/sweetrpg/api-core.go/vo"
	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
)

// MaxExceededVO is the 409 body when a purchase would pass a key's maximum.
type MaxExceededVO struct {
	Error          string `json:"error"`
	Message        string `json:"message"`
	Key            string `json:"key"`
	UnitsRemaining int64  `json:"units_remaining"`
}

// writeError maps a service error to a response. Unexpected errors are logged and reported
// without detail.
func writeError(c *gin.Context, err error) {
	var unknown catalog.UnknownKeysError
	var exceeded catalog.MaxExceededError
	switch {
	case errors.As(err, &exceeded):
		c.JSON(http.StatusConflict, MaxExceededVO{Error: "exceeds_maximum", Message: err.Error(), Key: exceeded.Key, UnitsRemaining: exceeded.Remaining})
	case errors.Is(err, models.ErrInvalid):
		c.JSON(http.StatusBadRequest, apiv.ErrorVO{Error: "invalid_request", Message: err.Error()})
	case errors.As(err, &unknown):
		c.JSON(http.StatusNotFound, apiv.ErrorVO{Error: "unknown_key", Message: err.Error()})
	case errors.Is(err, catalog.ErrNotFound):
		c.JSON(http.StatusNotFound, apiv.ErrorVO{Error: "not_found", Message: "not found"})
	case errors.Is(err, catalog.ErrDuplicate):
		c.JSON(http.StatusConflict, apiv.ErrorVO{Error: "duplicate", Message: "already exists"})
	case errors.Is(err, catalog.ErrInUse):
		c.JSON(http.StatusConflict, apiv.ErrorVO{Error: "in_use", Message: err.Error()})
	default:
		logging.Logger.Error("request failed", "error", err.Error())
		c.JSON(http.StatusInternalServerError, apiv.ErrorVO{Error: "internal_error", Message: "internal error"})
	}
}

func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, apiv.ErrorVO{Error: "invalid_request", Message: msg})
}
