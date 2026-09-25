package api

import (
	"context"
	"errors"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productFeedbackDesc = "Record the outcome of a product_search/product_match call: which " +
	"listing the user picked plus an optional verdict. Appends one JSONL line to the feedback " +
	"log (FEEDBACK_FILE) — the outcome half of the ADR-6 calibration pair joined to the " +
	"jeff_gate events on request_id. POST /api/v1/feedback accepts the same body."

// productFeedbackInput is the product_feedback tool argument shape — and
// the POST /api/v1/feedback JSON body (shared decode).
type productFeedbackInput struct {
	RequestID string `json:"request_id"          jsonschema:"Correlation id of the search being rated (the request_id on the jeff_gate log events for that call)"`
	PickedURL string `json:"picked_url"          jsonschema:"Listing URL the user picked or visited"`
	Verdict   string `json:"verdict,omitempty"   jsonschema:"Optional outcome verdict (e.g. bought, wrong_price, out_of_stock, bad_match)"`
}

// feedbackResponse is the product_feedback tool payload.
type feedbackResponse struct {
	OK        bool `json:"ok"`
	Persisted bool `json:"persisted"` // false → store is in log-only mode
}

func registerProductFeedback(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        toolProductFeedback,
		Description: productFeedbackDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in productFeedbackInput) (*mcp.CallToolResult, error) {
		return handleProductFeedback(d, in)
	})
}

// handleProductFeedback appends the outcome record. A nil store (unit
// tests) degrades to log-only — the tool still acks.
func handleProductFeedback(d deps, in productFeedbackInput) (*mcp.CallToolResult, error) {
	store := d.feedback
	if store == nil {
		store = NewFeedbackStore("")
	}
	if err := store.appendValidated(in); err != nil {
		var fe feedbackError
		if errors.As(err, &fe) {
			return errResult(fe.msg), nil
		}
		logToolError(toolProductFeedback, err)
		return errResult("feedback write failed"), nil
	}
	return jsonResult(feedbackResponse{OK: true, Persisted: store.persisted()})
}
