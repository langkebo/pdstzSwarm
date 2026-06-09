package reports

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// APIError is the wire-format error returned by handlers.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Handler is the Fiber handler bundle for /api/v1/reports. It depends
// on a Service plus a thin CampaignProvider so it can resolve a
// campaign id (passed in the request body) to a CampaignSnapshot.
type Handler struct {
	Service *Service
	// Campaigns is a function returning the live in-memory campaign
	// state. It is intentionally a function (not an interface) so the
	// reports package doesn't have to import the api package — which
	// would create an import cycle.
	Campaigns func(ctx context.Context, id uuid.UUID) (*CampaignSnapshot, error)
	// Findings optionally supplies a list of finding snapshots for
	// the campaign. If nil, the handler uses the in-memory
	// state.Findings; if that is also nil, an empty list is used.
	Findings func(ctx context.Context, campaignID uuid.UUID) ([]FindingSnapshot, error)
	// Board optionally provides a blackboard for live findings. If
	// nil, Findings is the only source.
	Board BoardSnapshotter
	// Generator is the label written into BuildInput.Generator.
	Generator string
}

// NewHandler builds a Handler with the given service. The Generator
// is written into BuildInput.Generator on every report so the report
// can be filtered / audited by source. The handler is safe to share
// across goroutines.
func NewHandler(svc *Service, generator string) *Handler {
	return &Handler{
		Service:   svc,
		Generator: generator,
	}
}

// Register attaches the report routes to the given router group.
//
//	api := app.Group("/api/v1")
//	reports.Register(api, h)
//
// The following routes are added:
//
//	POST   /reports                 generate a new report
//	GET    /reports                 list recent reports
//	GET    /reports/:id             fetch metadata
//	GET    /reports/:id/markdown    raw markdown body
//	GET    /reports/:id/json        structured JSON (sections + summary)
//	GET    /reports/by-campaign/:cid list reports for a campaign
//	DELETE /reports/:id             delete a report
func Register(router fiber.Router, h *Handler) {
	if h == nil {
		return
	}
	if h.Generator == "" {
		h.Generator = "pentest-swarm-ai"
	}

	router.Post("/reports", h.createReport)
	router.Get("/reports", h.listReports)
	router.Get("/reports/by-campaign/:id", h.listByCampaign)
	router.Get("/reports/:id", h.getReport)
	router.Get("/reports/:id/markdown", h.getReportMarkdown)
	router.Get("/reports/:id/json", h.getReportJSON)
	router.Delete("/reports/:id", h.deleteReport)
}

// CreateReportRequest is the POST body for generating a report. The
// caller supplies the campaign id; findings are pulled automatically
// from the in-memory state, the Board (if available), or the
// caller-provided Findings function — in that order.
type CreateReportRequest struct {
	CampaignID string `json:"campaign_id"`
	Title      string `json:"title,omitempty"`
	// Generator overrides the default generator label.
	Generator string `json:"generator,omitempty"`
}

func (h *Handler) createReport(c *fiber.Ctx) error {
	var req CreateReportRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "invalid body: " + err.Error()},
		})
	}
	if strings.TrimSpace(req.CampaignID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "campaign_id is required"},
		})
	}
	campaignID, err := uuid.Parse(req.CampaignID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "campaign_id is not a valid uuid"},
		})
	}
	if h.Campaigns == nil {
		return c.Status(fiber.StatusFailedDependency).JSON(fiber.Map{
			"error": APIError{Code: "NO_CAMPAIGN_PROVIDER", Message: "report handler is not wired to a campaign source"},
		})
	}

	snap, err := h.Campaigns(c.Context(), campaignID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": APIError{Code: "NOT_FOUND", Message: "campaign not found: " + err.Error()},
		})
	}

	// Resolve findings: custom provider → blackboard → empty.
	var findings []FindingSnapshot
	switch {
	case h.Findings != nil:
		findings, err = h.Findings(c.Context(), campaignID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": APIError{Code: "FINDINGS_LOOKUP_FAILED", Message: err.Error()},
			})
		}
	case h.Board != nil:
		findings, err = SnapshotFindingsForCampaign(c.Context(), h.Board, campaignID, 500)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": APIError{Code: "BOARD_QUERY_FAILED", Message: err.Error()},
			})
		}
	}

	gen := h.Generator
	if req.Generator != "" {
		gen = req.Generator
	}

	rep, err := h.Service.GenerateForCampaign(c.Context(), BuildInput{
		Campaign:    *snap,
		Findings:    findings,
		GeneratedAt: time.Now(),
		Generator:   gen,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "BUILD_FAILED", Message: err.Error()},
		})
	}
	if req.Title != "" {
		rep.Title = req.Title
	}
	return c.Status(fiber.StatusCreated).JSON(rep)
}

func (h *Handler) listReports(c *fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	if h.Service == nil || h.Service.store == nil {
		return c.JSON(fiber.Map{"data": []any{}, "meta": fiber.Map{"total": 0}})
	}
	reports, err := h.Service.ListRecent(c.Context(), limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "LIST_FAILED", Message: err.Error()},
		})
	}
	if reports == nil {
		reports = []*Report{}
	}
	return c.JSON(fiber.Map{"data": reports, "meta": fiber.Map{"total": len(reports)}})
}

func (h *Handler) listByCampaign(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "id is not a valid uuid"},
		})
	}
	if h.Service == nil || h.Service.store == nil {
		return c.JSON(fiber.Map{"data": []any{}, "meta": fiber.Map{"total": 0}})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	reports, err := h.Service.ListByCampaign(c.Context(), id, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "LIST_FAILED", Message: err.Error()},
		})
	}
	if reports == nil {
		reports = []*Report{}
	}
	return c.JSON(fiber.Map{"data": reports, "meta": fiber.Map{"total": len(reports)}})
}

func (h *Handler) getReport(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "id is not a valid uuid"},
		})
	}
	if h.Service == nil || h.Service.store == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": APIError{Code: "NOT_FOUND", Message: "report store not configured"},
		})
	}
	r, err := h.Service.Get(c.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": APIError{Code: "NOT_FOUND", Message: "report not found"},
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "GET_FAILED", Message: err.Error()},
		})
	}
	return c.JSON(r)
}

func (h *Handler) getReportMarkdown(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "id is not a valid uuid"},
		})
	}
	r, err := h.Service.Get(c.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": APIError{Code: "NOT_FOUND", Message: "report not found"},
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "GET_FAILED", Message: err.Error()},
		})
	}
	c.Set("Content-Type", "text/markdown; charset=utf-8")
	c.Set("Content-Disposition", `attachment; filename="`+safeFilename(r.Title, r.ID.String())+`.md"`)
	return c.SendString(r.Markdown)
}

func (h *Handler) getReportJSON(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "id is not a valid uuid"},
		})
	}
	r, err := h.Service.Get(c.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": APIError{Code: "NOT_FOUND", Message: "report not found"},
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "GET_FAILED", Message: err.Error()},
		})
	}
	return c.JSON(fiber.Map{
		"id":          r.ID,
		"campaign_id": r.CampaignID,
		"title":       r.Title,
		"summary":     r.Summary,
		"sections":    r.Sections,
		"created_at":  r.CreatedAt,
		"byte_size":   r.ByteSize,
	})
}

func (h *Handler) deleteReport(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": APIError{Code: "BAD_REQUEST", Message: "id is not a valid uuid"},
		})
	}
	if err := h.Service.Delete(c.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": APIError{Code: "NOT_FOUND", Message: "report not found"},
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": APIError{Code: "DELETE_FAILED", Message: err.Error()},
		})
	}
	return c.JSON(fiber.Map{"status": "deleted", "id": id})
}

func safeFilename(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return r
		default:
			return -1
		}
	}, title)
	if out == "" {
		return fallback
	}
	return out
}

// Ensure unused imports stay referenced when Handler is compiled alone.
var (
	_ = http.StatusOK
	_ = (*blackboard.Finding)(nil)
	_ pipeline.CampaignStatus = pipeline.StatusReporting
)
