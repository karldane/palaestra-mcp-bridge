package newrelic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mcp-bridge/mcp-framework/framework"
)

const (
	defaultNREndpoint   = "https://api.newrelic.com/graphql"
	defaultNREndpointEU = "https://api.eu.newrelic.com/graphql"
)

type Client struct {
	apiKey     string
	endpoint   string
	httpClient *http.Client
	accountID  string
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		endpoint:   defaultNREndpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func NewClientWithRegion(apiKey, region string) *Client {
	endpoint := defaultNREndpoint
	if region == "eu" {
		endpoint = defaultNREndpointEU
	}
	return &Client{
		apiKey:     apiKey,
		endpoint:   endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func NewClientWithEndpoint(apiKey, endpoint string) *Client {
	client := NewClient(apiKey)
	client.endpoint = endpoint
	return client
}

func (c *Client) GetAccountID(ctx context.Context) (string, error) {
	if c.accountID != "" {
		return c.accountID, nil
	}
	query := `query { actor { accounts { id name } } }`
	result, err := c.Query(ctx, query, nil)
	if err != nil {
		return "", err
	}
	data, _ := result["data"].(map[string]interface{})
	actor, _ := data["actor"].(map[string]interface{})
	accounts, _ := actor["accounts"].([]interface{})
	if len(accounts) > 0 {
		account, _ := accounts[0].(map[string]interface{})
		id, _ := account["id"].(float64)
		c.accountID = fmt.Sprintf("%.0f", id)
	}
	return c.accountID, nil
}

func (c *Client) Query(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	requestBody := map[string]interface{}{"query": query, "variables": variables}
	jsonBody, _ := json.Marshal(requestBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("API-Key", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return result, nil
}

type Server struct {
	*framework.Server
	client *Client
}

func NewServer(apiKey string, writeEnabled ...bool) *Server {
	enabled := false
	if len(writeEnabled) > 0 {
		enabled = writeEnabled[0]
	}
	return newServerWithClient(NewClient(apiKey), enabled)
}

func NewServerWithRegion(apiKey, region string, writeEnabled ...bool) *Server {
	enabled := false
	if len(writeEnabled) > 0 {
		enabled = writeEnabled[0]
	}
	return newServerWithClient(NewClientWithRegion(apiKey, region), enabled)
}

func NewServerWithEndpoint(apiKey, endpoint string, writeEnabled ...bool) *Server {
	enabled := false
	if len(writeEnabled) > 0 {
		enabled = writeEnabled[0]
	}
	return newServerWithClient(NewClientWithEndpoint(apiKey, endpoint), enabled)
}

func newServerWithClient(client *Client, writeEnabled bool) *Server {
	config := &framework.Config{
		Name:         "newrelic-mcp",
		Version:      "1.0.0",
		Instructions: "New Relic MCP Server with tools for querying data and managing alerts.",
	}
	s := &Server{Server: framework.NewServerWithConfig(config), client: client}
	s.SetWriteEnabled(writeEnabled)
	s.registerTools()
	return s
}

func (s *Server) registerTools() {
	s.RegisterTool(&NRQLQueryTool{client: s.client})
	s.RegisterTool(&ListAlertsTool{client: s.client})
	s.RegisterTool(&GetAPMMetricsTool{client: s.client})
	s.RegisterTool(&SearchLogsTool{client: s.client})
	s.RegisterTool(&ListApplicationsTool{client: s.client})
	s.RegisterTool(&GetAlertConditionsTool{client: s.client})
	s.RegisterTool(&QueryTracesTool{client: s.client})
	s.RegisterTool(&GetApplicationMetricsTool{client: s.client})
	s.RegisterTool(&GetAlertViolationsTool{client: s.client})
	s.RegisterTool(&GetTransactionTracesTool{client: s.client})
	s.RegisterTool(&GetTraceDetailsTool{client: s.client})
	s.RegisterTool(&TailLogsTool{client: s.client})
	s.RegisterTool(&GetInfrastructureMetricsTool{client: s.client})
	s.RegisterTool(&ListDashboardsTool{client: s.client})
	s.RegisterTool(&GetDashboardDataTool{client: s.client})
	// Write tools - disabled by default
	s.RegisterTool(&AcknowledgeAlertViolationTool{client: s.client})
	s.RegisterTool(&CreateAlertConditionTool{client: s.client})
	s.RegisterTool(&AddDashboardWidgetTool{client: s.client})
}

type NRQLQueryTool struct{ client *Client }

func (t *NRQLQueryTool) Name() string        { return "nrql_query" }
func (t *NRQLQueryTool) Description() string { return "Execute NRQL queries" }
func (t *NRQLQueryTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"query": map[string]interface{}{"type": "string", "description": "NRQL query"},
		},
		Required: []string{"query"},
	}
}
func (t *NRQLQueryTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	return "Results for: " + query, nil
}
func (t *NRQLQueryTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(4),
		framework.WithPII(true),
	)
}

type ListAlertsTool struct{ client *Client }

func (t *ListAlertsTool) Name() string                { return "list_alerts" }
func (t *ListAlertsTool) Description() string         { return "List alert policies" }
func (t *ListAlertsTool) Schema() mcp.ToolInputSchema { return mcp.ToolInputSchema{Type: "object"} }
func (t *ListAlertsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Alert policies list", nil
}
func (t *ListAlertsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(2),
		framework.WithPII(false),
	)
}

type GetAPMMetricsTool struct{ client *Client }

func (t *GetAPMMetricsTool) Name() string                { return "get_apm_metrics" }
func (t *GetAPMMetricsTool) Description() string         { return "Get APM metrics" }
func (t *GetAPMMetricsTool) Schema() mcp.ToolInputSchema { return mcp.ToolInputSchema{Type: "object"} }
func (t *GetAPMMetricsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "APM metrics", nil
}
func (t *GetAPMMetricsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

type SearchLogsTool struct{ client *Client }

func (t *SearchLogsTool) Name() string                { return "search_logs" }
func (t *SearchLogsTool) Description() string         { return "Search logs" }
func (t *SearchLogsTool) Schema() mcp.ToolInputSchema { return mcp.ToolInputSchema{Type: "object"} }
func (t *SearchLogsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Log search results", nil
}
func (t *SearchLogsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(5),
		framework.WithPII(true),
	)
}
