package newrelic

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mcp-bridge/mcp-framework/framework"
)

type ListApplicationsTool struct{ client *Client }

func (t *ListApplicationsTool) Name() string        { return "list_applications" }
func (t *ListApplicationsTool) Description() string { return "List APM applications" }
func (t *ListApplicationsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"limit": map[string]interface{}{"type": "number", "description": "Max results"},
		},
	}
}
func (t *ListApplicationsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Applications list", nil
}
func (t *ListApplicationsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(2),
		framework.WithPII(false),
	)
}

type GetAlertConditionsTool struct{ client *Client }

func (t *GetAlertConditionsTool) Name() string        { return "get_alert_conditions" }
func (t *GetAlertConditionsTool) Description() string { return "Get alert conditions" }
func (t *GetAlertConditionsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"policy_id": map[string]interface{}{"type": "string", "description": "Policy ID"},
		},
		Required: []string{"policy_id"},
	}
}
func (t *GetAlertConditionsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	policyID, _ := args["policy_id"].(string)
	if policyID == "" {
		return "", fmt.Errorf("missing required parameter: policy_id")
	}
	return "Alert conditions", nil
}
func (t *GetAlertConditionsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

type QueryTracesTool struct{ client *Client }

func (t *QueryTracesTool) Name() string        { return "query_traces" }
func (t *QueryTracesTool) Description() string { return "Query distributed traces" }
func (t *QueryTracesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"service_name": map[string]interface{}{"type": "string"},
			"error_only":   map[string]interface{}{"type": "boolean"},
		},
	}
}
func (t *QueryTracesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Trace results", nil
}
func (t *QueryTracesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(5),
		framework.WithPII(true),
	)
}

type GetApplicationMetricsTool struct{ client *Client }

func (t *GetApplicationMetricsTool) Name() string { return "get_application_metrics" }
func (t *GetApplicationMetricsTool) Description() string {
	return "Get comprehensive application metrics"
}
func (t *GetApplicationMetricsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"app_name": map[string]interface{}{"type": "string", "description": "Application name"},
		},
		Required: []string{"app_name"},
	}
}
func (t *GetApplicationMetricsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	appName, _ := args["app_name"].(string)
	if appName == "" {
		return "", fmt.Errorf("missing required parameter: app_name")
	}
	return fmt.Sprintf("Metrics for %s", appName), nil
}
func (t *GetApplicationMetricsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

type GetAlertViolationsTool struct{ client *Client }

func (t *GetAlertViolationsTool) Name() string        { return "get_alert_violations" }
func (t *GetAlertViolationsTool) Description() string { return "Get alert violations" }
func (t *GetAlertViolationsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{Type: "object"}
}
func (t *GetAlertViolationsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Alert violations", nil
}
func (t *GetAlertViolationsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(4),
		framework.WithPII(true),
	)
}

type GetTransactionTracesTool struct{ client *Client }

func (t *GetTransactionTracesTool) Name() string { return "get_transaction_traces" }
func (t *GetTransactionTracesTool) Description() string {
	return "Get slowest transaction traces for an application"
}
func (t *GetTransactionTracesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"app_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the APM application",
			},
			"duration": map[string]interface{}{
				"type":        "string",
				"description": "Time range (default: '1 hour')",
				"default":     "1 hour",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Number of traces (default: 10)",
				"default":     10,
			},
			"min_duration": map[string]interface{}{
				"type":        "number",
				"description": "Only traces slower than X milliseconds",
			},
		},
		Required: []string{"app_name"},
	}
}
func (t *GetTransactionTracesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	appName, _ := args["app_name"].(string)
	if appName == "" {
		return "", fmt.Errorf("missing required parameter: app_name")
	}
	return fmt.Sprintf("Transaction traces for %s", appName), nil
}
func (t *GetTransactionTracesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(6),
		framework.WithPII(true),
	)
}

type GetTraceDetailsTool struct{ client *Client }

func (t *GetTraceDetailsTool) Name() string { return "get_trace_details" }
func (t *GetTraceDetailsTool) Description() string {
	return "Get detailed span waterfall for a specific trace ID"
}
func (t *GetTraceDetailsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"trace_id": map[string]interface{}{
				"type":        "string",
				"description": "The trace ID to analyze",
			},
		},
		Required: []string{"trace_id"},
	}
}
func (t *GetTraceDetailsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	traceID, _ := args["trace_id"].(string)
	if traceID == "" {
		return "", fmt.Errorf("missing required parameter: trace_id")
	}
	return fmt.Sprintf("Trace details for %s", traceID), nil
}
func (t *GetTraceDetailsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(7),
		framework.WithPII(true),
	)
}

type TailLogsTool struct{ client *Client }

func (t *TailLogsTool) Name() string { return "tail_logs" }
func (t *TailLogsTool) Description() string {
	return "Tail logs in real-time (returns latest logs, use with polling)"
}
func (t *TailLogsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Log filter query (e.g., 'service:mystique level:ERROR')",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Number of lines to return (default: 50)",
				"default":     50,
			},
			"include_timestamp": map[string]interface{}{
				"type":        "boolean",
				"description": "Include timestamps in output",
				"default":     true,
			},
		},
	}
}
func (t *TailLogsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	return fmt.Sprintf("Latest logs for query: %s", query), nil
}
func (t *TailLogsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(4),
		framework.WithPII(true),
	)
}

type GetInfrastructureMetricsTool struct{ client *Client }

func (t *GetInfrastructureMetricsTool) Name() string { return "get_infrastructure_metrics" }
func (t *GetInfrastructureMetricsTool) Description() string {
	return "Get infrastructure metrics for hosts, containers, or Kubernetes"
}
func (t *GetInfrastructureMetricsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"hostname": map[string]interface{}{
				"type":        "string",
				"description": "Specific host to query",
			},
			"container_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific container to query",
			},
			"cluster_name": map[string]interface{}{
				"type":        "string",
				"description": "Kubernetes cluster name",
			},
			"metric_type": map[string]interface{}{
				"type":        "string",
				"description": "Type of metrics: cpu, memory, disk, network (default: all)",
			},
			"duration": map[string]interface{}{
				"type":        "string",
				"description": "Time range (default: '1 hour')",
				"default":     "1 hour",
			},
		},
	}
}
func (t *GetInfrastructureMetricsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	hostname, _ := args["hostname"].(string)
	if hostname != "" {
		return fmt.Sprintf("Infrastructure metrics for host: %s", hostname), nil
	}
	return "Infrastructure metrics", nil
}
func (t *GetInfrastructureMetricsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

type ListDashboardsTool struct{ client *Client }

func (t *ListDashboardsTool) Name() string { return "list_dashboards" }
func (t *ListDashboardsTool) Description() string {
	return "List all dashboards in your New Relic account"
}
func (t *ListDashboardsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum results (default 50)",
				"default":     50,
			},
		},
	}
}
func (t *ListDashboardsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	return "Dashboards list", nil
}
func (t *ListDashboardsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(2),
		framework.WithPII(false),
	)
}

type GetDashboardDataTool struct{ client *Client }

func (t *GetDashboardDataTool) Name() string { return "get_dashboard_data" }
func (t *GetDashboardDataTool) Description() string {
	return "Get data from a specific dashboard's widgets"
}
func (t *GetDashboardDataTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"dashboard_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the dashboard",
			},
			"duration": map[string]interface{}{
				"type":        "string",
				"description": "Time range (default: '1 hour')",
				"default":     "1 hour",
			},
		},
		Required: []string{"dashboard_name"},
	}
}
func (t *GetDashboardDataTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	dashboardName, _ := args["dashboard_name"].(string)
	if dashboardName == "" {
		return "", fmt.Errorf("missing required parameter: dashboard_name")
	}
	return fmt.Sprintf("Dashboard data for %s", dashboardName), nil
}
func (t *GetDashboardDataTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(5),
		framework.WithPII(true),
	)
}

// Write Tools - disabled by default unless --write-enabled flag is provided

type AcknowledgeAlertViolationTool struct{ client *Client }

func (t *AcknowledgeAlertViolationTool) Name() string { return "acknowledge_alert_violation" }
func (t *AcknowledgeAlertViolationTool) Description() string {
	return "Acknowledge an alert violation (disabled without --write-enabled)"
}
func (t *AcknowledgeAlertViolationTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"violation_id": map[string]interface{}{
				"type":        "string",
				"description": "The violation ID to acknowledge",
			},
			"comment": map[string]interface{}{
				"type":        "string",
				"description": "Optional comment for the acknowledgment",
			},
		},
		Required: []string{"violation_id"},
	}
}
func (t *AcknowledgeAlertViolationTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	violationID, _ := args["violation_id"].(string)
	comment, _ := args["comment"].(string)
	return fmt.Sprintf("Acknowledged violation %s with comment: %s", violationID, comment), nil
}
func (t *AcknowledgeAlertViolationTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactWrite),
		framework.WithResourceCost(2),
		framework.WithPII(false),
		framework.WithIdempotent(true),
	)
}

type CreateAlertConditionTool struct{ client *Client }

func (t *CreateAlertConditionTool) Name() string { return "create_alert_condition" }
func (t *CreateAlertConditionTool) Description() string {
	return "Create a new alert condition in an alert policy (disabled without --write-enabled)"
}
func (t *CreateAlertConditionTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"policy_id": map[string]interface{}{
				"type":        "string",
				"description": "The alert policy ID to add the condition to",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the alert condition",
			},
			"nrql_query": map[string]interface{}{
				"type":        "string",
				"description": "NRQL query for the condition",
			},
			"critical_threshold": map[string]interface{}{
				"type":        "number",
				"description": "Critical threshold value",
			},
			"duration_minutes": map[string]interface{}{
				"type":        "number",
				"description": "Duration in minutes before triggering",
				"default":     5,
			},
		},
		Required: []string{"policy_id", "name", "nrql_query", "critical_threshold"},
	}
}
func (t *CreateAlertConditionTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	name, _ := args["name"].(string)
	return fmt.Sprintf("Created alert condition: %s", name), nil
}
func (t *CreateAlertConditionTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactWrite),
		framework.WithResourceCost(3),
		framework.WithPII(false),
		framework.WithIdempotent(false),
	)
}

type AddDashboardWidgetTool struct{ client *Client }

func (t *AddDashboardWidgetTool) Name() string { return "add_dashboard_widget" }
func (t *AddDashboardWidgetTool) Description() string {
	return "Add a widget to an existing dashboard (disabled without --write-enabled)"
}
func (t *AddDashboardWidgetTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"dashboard_guid": map[string]interface{}{
				"type":        "string",
				"description": "GUID of the dashboard to add widget to",
			},
			"widget_title": map[string]interface{}{
				"type":        "string",
				"description": "Title of the widget",
			},
			"nrql_query": map[string]interface{}{
				"type":        "string",
				"description": "NRQL query for the widget data",
			},
			"visualization": map[string]interface{}{
				"type":        "string",
				"description": "Visualization type (e.g., 'line', 'bar', 'table')",
				"default":     "line",
			},
		},
		Required: []string{"dashboard_guid", "widget_title", "nrql_query"},
	}
}
func (t *AddDashboardWidgetTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	widgetTitle, _ := args["widget_title"].(string)
	return fmt.Sprintf("Added widget '%s' to dashboard", widgetTitle), nil
}
func (t *AddDashboardWidgetTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactWrite),
		framework.WithResourceCost(2),
		framework.WithPII(false),
		framework.WithIdempotent(false),
	)
}
