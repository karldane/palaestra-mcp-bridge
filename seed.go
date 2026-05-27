package main

import (
	"encoding/json"

	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// seedDefaultUser creates a test user (admin@localhost / admin) if no users
// exist in the database. This is for local development and testing only.
func seedDefaultUser(st *store.Store) {
	if existing, err := st.GetUserByEmail("admin@localhost"); err == nil {
		if existing.Role != "admin" {
			existing.Role = "admin"
			st.UpdateUser(existing)
			shared.Info("seed: upgraded admin@localhost to role=admin")
		} else {
			shared.Info("seed: user admin@localhost already exists, skipping")
		}
		return
	}

	user := &store.User{
		Name:     "Admin",
		Email:    "admin@localhost",
		Password: "admin",
		Role:     "admin",
	}
	if err := st.CreateUser(user); err != nil {
		shared.Errorf("seed: failed to create user: %v", err)
		return
	}
	shared.Infof("seed: created user admin@localhost (id=%s, password=admin)", user.ID)
}

// seedBackendsFromConfig imports backends from the config file into the SQLite
// database if the DB has no backends yet. This is a one-time migration: once
// backends exist in the DB, the config file is no longer consulted for backend
// definitions (the DB is authoritative).
func seedBackendsFromConfig(st *store.Store, cfg *config.InternalConfig) {
	existing, err := st.ListBackends()
	if err != nil {
		shared.Errorf("seed-backends: list: %v", err)
		return
	}
	if len(existing) > 0 {
		return
	}

	mcpbridgeBackend := &store.Backend{
		ID:            "mcpbridge",
		Command:       "mcp-bridge-builtin",
		PoolSize:      1,
		ToolPrefix:    "",
		Enabled:       true,
		IsSystem:      true,
		SelfReporting: true,
	}
	if err := st.CreateBackend(mcpbridgeBackend); err != nil {
		shared.Errorf("seed-backends: create mcpbridge: %v", err)
	} else {
		shared.Info("seed-backends: created built-in mcpbridge backend")
	}

	count := 0
	for id, bc := range cfg.Backends {
		envJSON := "{}"
		if len(bc.Env) > 0 {
			if data, err := json.Marshal(bc.Env); err == nil {
				envJSON = string(data)
			}
		}

		envMappingsJSON := "{}"
		if len(bc.Secrets) > 0 {
			m := make(map[string]string)
			for _, s := range bc.Secrets {
				m[s.Name] = s.EnvKey
			}
			if data, err := json.Marshal(m); err == nil {
				envMappingsJSON = string(data)
			}
		}

		enabled := true
		if bc.Enabled != nil {
			enabled = *bc.Enabled
		}

		b := &store.Backend{
			ID:                id,
			Command:           bc.Command,
			PoolSize:          bc.PoolSize,
			MinPoolSize:       bc.MinPoolSize,
			MaxPoolSize:       bc.MaxPoolSize,
			ToolPrefix:        bc.ToolPrefix,
			Env:               envJSON,
			EnvMappings:       envMappingsJSON,
			Enabled:           enabled,
			IsSystem:          bc.IsSystem,
			SelfReporting:     bc.SelfReporting,
			NoKeysRequired:    bc.NoKeysRequired,
			SkipJustification: bc.SkipJustification,
			StdioFraming:      bc.StdioFraming,
		}
		if b.MinPoolSize == 0 {
			b.MinPoolSize = 1
		}
		if b.StdioFraming == "" && b.Command != "mcp-bridge-builtin" {
			b.StdioFraming = "newline"
		}

		if err := st.CreateBackend(b); err != nil {
			shared.Errorf("seed-backends: create %s: %v", id, err)
			continue
		}
		count++
	}
	if count > 0 {
		shared.Infof("seed-backends: imported %d backends from config into DB", count)
	}
}

// seedGlobalHints seeds the system-level agent hints if not already present.
func seedGlobalHints(st *store.Store) {
	existing, err := st.GetSetting("global_hints")
	if err != nil {
		shared.Errorf("seed-global-hints: get: %v", err)
		return
	}
	if existing != "" {
		shared.Info("seed-global-hints: already seeded, skipping")
		return
	}

	hints := `Welcome to Tusker MCP Bridge.

This system provides tooling for access to various tooling backends, including github and jira/confluence. Its intention is to allow coding agents such as yourself to be the 'spider in the web' - able to access various systems allowing you to be a full-stack developer and able to partake in all aspects of the software development lifecycle.

Jira tickets are named DEV-XXXXX. The majority of tickets will be found in the 'Development' namespace. Pull requests referenced in tickets can be found via the github backend.

Github: our organisation is 'tusker-direct' - limit searches to this space.

NewRelic provides access to all our logs, and the newrelic backend provides access to this.

About MCP Bridge
MCP Bridge acts as a unified gateway to multiple backend systems, presenting them to coding agents as a single cohesive toolset. It aggregates tools from configured backends (such as GitHub and Atlassian) and augments them with system-level tools to help agents discover, manage, and effectively use the available capabilities.

Available System Tools
MCP Bridge provides the following system tools to aid navigation and troubleshooting:
mcpbridge_0_README - This tool. Read it first. Contains essential usage guidance, hints for all configured backends, and company-specific information.
mcpbridge_ping - A simple connectivity check. Returns "pong" with a timestamp. Use this to verify the bridge is responsive.
mcpbridge_version - Returns the current version of the MCP Bridge server. Useful for debugging and support requests.
mcpbridge_list_backends - Lists all configured backend integrations and their status (enabled/disabled). Shows which systems are available for use.
mcpbridge_capabilities - Provides a comprehensive overview of available tools across all backends, including tool counts and configuration status. This is the quickest way to see what's available without triggering full tool enumeration.
mcpbridge_refresh_tools - Forces a refresh of the tool list from all backends. Use this if you suspect the available tools may have changed or been updated.
mcpbridge_pool_status - Shows the status of warm process pools. Displays memory usage and process counts per backend. Useful for diagnosing performance issues or resource constraints.

Best Practices
- Start with mcpbridge_0_README to understand the system and get context-specific guidance before making queries.
- Use mcpbridge_capabilities to discover available tools when beginning a new task.
- Call backend tools directly once you know what you need - MCP Bridge handles routing to the correct backend automatically.
- Prefix conventions: Backend tools are prefixed with their source (e.g., github_pr_search, atlassian_jira_search_issues).`

	if err := st.SetSetting("global_hints", hints); err != nil {
		shared.Errorf("seed-global-hints: set: %v", err)
		return
	}
	shared.Info("seed-global-hints: seeded system hints")
}

// seedDefaultPolicies creates safety policies from production config if none exist.
func seedDefaultPolicies(st *store.Store) {
	enforcerStore := store.NewEnforcerStore(st.DB())

	policies, err := enforcerStore.ListPolicies()
	if err != nil {
		shared.Errorf("seed-policies: failed to list policies: %v", err)
		return
	}
	if len(policies) > 0 {
		shared.Info("seed-policies: policies already exist, skipping")
		return
	}

	defaultPolicies := []enforcer.PolicyRow{
		// Backend-specific: allow reads by default
		{ID: "aws_allow_reads", Name: "AWS Read Operations", Description: "Allow AWS read-only operations", Scope: "global", Expression: `backend_id == "aws" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "AWS read operation allowed", Enabled: true, Priority: 10},
		{ID: "github_allow_reads", Name: "GitHub Read Operations", Description: "Allow GitHub read-only operations", Scope: "global", Expression: `backend_id == "github" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "GitHub read operation allowed", Enabled: true, Priority: 10},
		{ID: "atlassian_allow_reads", Name: "Atlassian Read Operations", Description: "Allow Atlassian read-only operations", Scope: "global", Expression: `backend_id == "atlassian" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "Atlassian read operation allowed", Enabled: true, Priority: 10},
		{ID: "k8s_allow_reads", Name: "Kubernetes Read Operations", Description: "Allow Kubernetes read-only operations", Scope: "global", Expression: `backend_id == "k8s" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "Kubernetes read operation allowed", Enabled: true, Priority: 10},
		{ID: "circleci_allow_reads", Name: "CircleCI Read Operations", Description: "Allow CircleCI read-only operations", Scope: "global", Expression: `backend_id == "circleci" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "CircleCI read operation allowed", Enabled: true, Priority: 10},
		{ID: "argocd_allow_reads", Name: "ArgoCD Read Operations", Description: "Allow read-only operations on ArgoCD resources", Scope: "global", Expression: `backend_id == "argocd" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Message: "Read operations are allowed", Enabled: true, Priority: 100},

		// Specific: block destructive high-risk tools
		{ID: "github_block_repo_delete", Name: "Block Repository Deletion", Description: "Prevent accidental deletion of GitHub repositories", Scope: "global", Expression: `tool.contains('github') && tool.contains('delete') && tool.contains('repo')`, Action: "PENDING_ADMIN_APPROVAL", Severity: "CRITICAL", Message: "Repository deletion is permanent and will delete all code, issues, and history. This requires senior approval.", Enabled: true, Priority: 1},
		{ID: "global_pii_protection", Name: "PII Protection", Description: "Flag tools that may expose PII for review", Scope: "global", Expression: `backend_id != "qdrant" && safety.pii_exposure && (args.query.contains("ssn") || args.query.contains("password") || args.query.contains("credit") || args.query.contains("secret"))`, Action: "PENDING_USER_APPROVAL", Severity: "CRITICAL", Message: "This query may access sensitive PII data. Please review carefully before approving.", Enabled: true, Priority: 5},
		{ID: "github_block_force_push", Name: "Block Force Pushes", Description: "Prevent force pushes that rewrite git history", Scope: "global", Expression: `tool.contains('github') && tool.contains('force')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Force pushes rewrite git history and can cause data loss for other developers. Avoid unless absolutely necessary.", Enabled: true, Priority: 25},
		{ID: "atlassian_block_delete_issues", Name: "Block Jira Issue Deletion", Description: "Prevent accidental deletion of Jira issues", Scope: "global", Expression: `tool.contains('jira') && tool.contains('delete') && tool.contains('issue')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting Jira issues is permanent and can break audit trails. Please confirm with your team lead before proceeding.", Enabled: true, Priority: 15},
		{ID: "atlassian_block_confluence_delete", Name: "Block Confluence Page Deletion", Description: "Prevent accidental deletion of Confluence pages", Scope: "global", Expression: `tool.contains('confluence') && tool.contains('delete') && tool.contains('page')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting Confluence pages may break documentation links. Consider archiving instead.", Enabled: true, Priority: 20},
		{ID: "newrelic_block_alert_delete", Name: "Block Alert Policy Deletion", Description: "Prevent accidental deletion of monitoring alerts", Scope: "global", Expression: `tool.contains('newrelic') && tool.contains('delete') && tool.contains('alert')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting alert policies may cause you to miss critical production issues. Consider disabling instead.", Enabled: true, Priority: 30},
		{ID: "newrelic_block_dashboard_delete", Name: "Block Dashboard Deletion", Description: "Prevent accidental deletion of monitoring dashboards", Scope: "global", Expression: `tool.contains('newrelic') && tool.contains('delete') && tool.contains('dashboard')`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "Dashboards contain valuable monitoring configurations. Please confirm before deleting.", Enabled: true, Priority: 40},
		{ID: "mcpbridge_block_backend_delete", Name: "Block Backend Deletion", Description: "Prevent accidental deletion of configured MCP backends", Scope: "global", Expression: `tool.contains('mcpbridge') && tool.contains('backend') && tool.contains('delete')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting a backend will break integrations for all users. This requires admin approval.", Enabled: true, Priority: 12},
		{ID: "mcpbridge_block_pool_changes", Name: "Block Pool Configuration Changes", Description: "Prevent changes to process pool settings that could affect stability", Scope: "global", Expression: `tool.contains('mcpbridge') && tool.contains('pool')`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "Changing pool configuration can affect system performance and stability. Please test in dev first.", Enabled: true, Priority: 45},
		{ID: "b0b8d9b409167e38389d5edb99239337", Name: "AppScan Block Scan Cancel", Description: "Block scan cancellation as it effectively deletes scan data", Scope: "global", Expression: `tool.contains("appscan") && tool.contains("scan") && tool.contains("cancel")`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Scan cancellation must be approved", Enabled: true, Priority: 14},
		{ID: "github_block_force_push", Name: "Block Force Pushes", Description: "Prevent force pushes that rewrite git history", Scope: "global", Expression: `tool.contains('github') && tool.contains('force')`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Force pushes rewrite git history and can cause data loss for other developers. Avoid unless absolutely necessary.", Enabled: true, Priority: 25},

		// Impact-scope-based policies per backend
		{ID: "aws_delete_requires_approval", Name: "AWS Delete Operations", Description: "AWS delete operations require approval", Scope: "global", Expression: `backend_id == "aws" && safety.impact_scope == "delete"`, Action: "PENDING_ADMIN_APPROVAL", Severity: "CRITICAL", Message: "AWS delete operations require admin approval", Enabled: true, Priority: 15},
		{ID: "aws_write_requires_approval", Name: "AWS Write Operations", Description: "AWS write operations require approval", Scope: "global", Expression: `backend_id == "aws" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "HIGH", Message: "AWS write operations require admin approval", Enabled: true, Priority: 20},
		{ID: "aws_admin_requires_approval", Name: "AWS Admin Operations", Description: "Route AWS admin/configuration tools to user approval", Scope: "global", Expression: `backend_id == "aws" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 20},
		{ID: "aws_warn_high_cost", Name: "AWS High Resource Cost", Description: "Warn on expensive AWS operations", Scope: "global", Expression: `backend_id == "aws" && safety.cost >= 5`, Action: "WARN", Severity: "MEDIUM", Message: "This AWS operation has high resource cost", Enabled: true, Priority: 25},

		{ID: "github_delete_requires_approval", Name: "GitHub Delete Operations", Description: "GitHub delete operations require approval", Scope: "global", Expression: `backend_id == "github" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "GitHub delete operations require admin approval", Enabled: true, Priority: 15},
		{ID: "github_write_requires_approval", Name: "GitHub Write Operations", Description: "GitHub write operations require approval", Scope: "global", Expression: `backend_id == "github" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "HIGH", Message: "GitHub write operations require admin approval", Enabled: true, Priority: 20},
		{ID: "github_admin_requires_approval", Name: "GitHub Admin Operations", Description: "Route GitHub admin/configuration tools to user approval", Scope: "global", Expression: `backend_id == "github" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 20},
		{ID: "github_warn_merge_no_review", Name: "Warn on Unreviewed PR Merges", Description: "Flag PR merges that may not have proper review", Scope: "global", Expression: `tool.contains('github') && tool.contains('merge') && safety.risk_level == 'high'`, Action: "WARN", Severity: "MEDIUM", Message: "Please ensure this PR has been properly reviewed before merging. Unreviewed code can introduce bugs and security issues.", Enabled: true, Priority: 60},

		{ID: "k8s_delete_requires_approval", Name: "Kubernetes Delete Operations", Description: "Kubernetes delete operations require approval", Scope: "global", Expression: `backend_id == "k8s" && safety.impact_scope == "delete"`, Action: "PENDING_ADMIN_APPROVAL", Severity: "HIGH", Message: "Kubernetes delete operations require admin approval", Enabled: true, Priority: 15},
		{ID: "k8s_write_requires_approval", Name: "Kubernetes Write Operations", Description: "Kubernetes write operations require approval", Scope: "global", Expression: `backend_id == "k8s" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "HIGH", Message: "Kubernetes write operations require admin approval", Enabled: true, Priority: 20},
		{ID: "k8s_admin_requires_approval", Name: "Kubernetes Admin Operations", Description: "Route Kubernetes admin/configuration tools to user approval", Scope: "global", Expression: `backend_id == "k8s" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 20},

		{ID: "atlassian_delete_requires_approval", Name: "Atlassian Delete Operations", Description: "Route Atlassian delete-impact tools to user approval", Scope: "global", Expression: `backend_id == "atlassian" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 20},
		{ID: "atlassian_write_requires_approval", Name: "Atlassian Write Operations", Description: "Atlassian write operations require approval", Scope: "global", Expression: `backend_id == "atlassian" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "MEDIUM", Message: "Atlassian write operations require admin approval", Enabled: true, Priority: 20},
		{ID: "atlassian_admin_requires_approval", Name: "Atlassian Admin Operations", Description: "Route Atlassian admin/configuration tools to user approval", Scope: "global", Expression: `backend_id == "atlassian" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 20},
		{ID: "atlassian_warn_bulk_ops", Name: "Warn on Bulk Jira Operations", Description: "Require confirmation for bulk operations that affect many issues", Scope: "global", Expression: `tool.contains('jira') && tool.contains('bulk') && safety.risk_level == 'high'`, Action: "WARN", Severity: "MEDIUM", Message: "This operation will affect multiple Jira issues. Please verify your JQL query and the scope of changes.", Enabled: true, Priority: 50},

		{ID: "circleci_delete_requires_approval", Name: "CircleCI Delete Operations", Description: "Route CircleCI delete-impact tools to user approval", Scope: "global", Expression: `backend_id == "circleci" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "", Enabled: true, Priority: 15},
		{ID: "circleci_write_requires_approval", Name: "CircleCI Write Operations", Description: "CircleCI write operations require approval", Scope: "global", Expression: `backend_id == "circleci" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "MEDIUM", Message: "CircleCI write operations require admin approval", Enabled: true, Priority: 20},
		{ID: "circleci_admin_requires_approval", Name: "CircleCI Admin Operations", Description: "Route CircleCI admin/configuration tools to user approval", Scope: "global", Expression: `backend_id == "circleci" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "", Enabled: true, Priority: 20},

		// ArgoCD
		{ID: "argocd_write_requires_approval", Name: "ArgoCD Write Operations", Description: "Write operations on ArgoCD require user approval", Scope: "global", Expression: `backend_id == "argocd" && safety.impact_scope == "write"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "Write operations require user approval", Enabled: true, Priority: 100},
		{ID: "argocd_delete_requires_approval", Name: "ArgoCD Delete Operations", Description: "Delete operations on ArgoCD require user approval", Scope: "global", Expression: `backend_id == "argocd" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Delete operations require user approval", Enabled: true, Priority: 100},
		{ID: "argocd_admin_requires_approval", Name: "ArgoCD Admin Operations", Description: "Admin operations on ArgoCD require user approval", Scope: "global", Expression: `backend_id == "argocd" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Admin operations require user approval", Enabled: true, Priority: 100},
		{ID: "argocd_block_app_delete", Name: "Block ArgoCD Application Deletion", Description: "Block deletion of ArgoCD applications", Scope: "global", Expression: `tool.contains("argocd") && tool.contains("delete") && tool.contains("app")`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting ArgoCD applications requires approval", Enabled: true, Priority: 100},
		{ID: "argocd_block_project_delete", Name: "Block ArgoCD Project Deletion", Description: "Block deletion of ArgoCD projects", Scope: "global", Expression: `tool.contains("argocd") && tool.contains("delete") && tool.contains("project")`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Message: "Deleting ArgoCD projects requires approval", Enabled: true, Priority: 100},

		// Cross-cutting
		{ID: "newrelic_warn_writes", Name: "Warn on New Relic Write Operations", Description: "Require explicit confirmation for write operations in New Relic", Scope: "global", Expression: `tool.contains('newrelic') && safety.impact_scope == 'write'`, Action: "WARN", Severity: "LOW", Message: "This operation will modify New Relic configuration. Please verify this is intentional.", Enabled: true, Priority: 80},
		{ID: "restrict_junior_high_risk", Name: "Junior Dev High-Risk Restriction", Description: "Require approval for high-risk operations by junior developers", Scope: "global", Expression: `backend_id != "qdrant" && user.trust_level < 60 && (safety.risk_level == "high" || safety.risk_level == "critical")`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "High-risk operations require approval for junior developers.", Enabled: true, Priority: 35},
		{ID: "global_resource_protection", Name: "Resource Protection", Description: "Block extremely resource-intensive operations when system is under load", Scope: "global", Expression: `safety.resource_cost >= 10`, Action: "DENY", Severity: "MEDIUM", Message: "This operation is extremely resource-intensive. Try a more targeted approach or contact an administrator.", Enabled: true, Priority: 100},
		{ID: "prevent_resource_exhaustion", Name: "Prevent Resource Exhaustion", Description: "Block extremely resource-intensive operations (bulk exports, full table scans, etc.)", Scope: "global", Expression: "safety.resource_cost >= 10", Action: "DENY", Severity: "HIGH", Message: "This operation is extremely resource-intensive. Try a more targeted approach.", Enabled: true, Priority: 100},
	}

	for _, policy := range defaultPolicies {
		if err := enforcerStore.CreatePolicy(policy); err != nil {
			shared.Errorf("seed-policies: failed to create policy %s: %v", policy.ID, err)
		} else {
			shared.Infof("seed-policies: created policy %s", policy.ID)
		}
	}

	shared.Infof("seed-policies: created %d default policies", len(defaultPolicies))
}
