package enforcer

import (
	"testing"
)

// TestPromoteRisk verifies the risk promotion chain including the no-op at critical.
func TestPromoteRisk(t *testing.T) {
	tests := []struct {
		input    RiskLevel
		expected RiskLevel
	}{
		{RiskLow, RiskMedium},
		{RiskMedium, RiskHigh},
		{RiskHigh, RiskCritical},
		{RiskCritical, RiskCritical}, // no-op
	}
	for _, tt := range tests {
		got := PromoteRisk(tt.input)
		if got != tt.expected {
			t.Errorf("PromoteRisk(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestHeuristicProfile_Priority1_CriticalDestructive verifies priority-1 patterns.
func TestHeuristicProfile_Priority1_CriticalDestructive(t *testing.T) {
	tools := []string{
		"delete_all_users",
		"purge_records",
		"drop_database",
		"drop_table_logs",
		"wipe_storage",
		"terminate_cluster",
		"decommission_node",
		"factory_reset_device",
		"hard_delete_entry",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskCritical {
			t.Errorf("tool %q: Risk = %q, want critical", name, p.Risk)
		}
		if p.Impact != ImpactDelete {
			t.Errorf("tool %q: Impact = %q, want ImpactDelete", name, p.Impact)
		}
		if !p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = false, want true", name)
		}
		if p.Idempotent {
			t.Errorf("tool %q: Idempotent = true, want false", name)
		}
		if p.Cost != 20 {
			t.Errorf("tool %q: Cost = %d, want 20", name, p.Cost)
		}
		if p.Source != "inferred" {
			t.Errorf("tool %q: Source = %q, want inferred", name, p.Source)
		}
	}
}

// TestHeuristicProfile_Priority2_HighDestructive verifies priority-2 patterns.
func TestHeuristicProfile_Priority2_HighDestructive(t *testing.T) {
	tools := []string{
		"delete_ticket",
		"remove_user",
		"destroy_resource",
		"revoke_token",
		"deactivate_account",
		"disable_service",
		"ban_user",
		"block_ip",
		"archive_project",
		"unsubscribe_user",
		"cancel_subscription",
		"terminate_session",
		"kill_process",
		"stop_service_instance",
		"deprovision_vm",
		"offboard_employee",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskHigh {
			t.Errorf("tool %q: Risk = %q, want high", name, p.Risk)
		}
		if p.Impact != ImpactDelete {
			t.Errorf("tool %q: Impact = %q, want ImpactDelete", name, p.Impact)
		}
		if p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = true, want false", name)
		}
		if p.Cost != 10 {
			t.Errorf("tool %q: Cost = %d, want 10", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority3_HighAuth verifies priority-3 auth/access patterns.
func TestHeuristicProfile_Priority3_HighAuth(t *testing.T) {
	tools := []string{
		"grant_permission",
		"assign_role_admin",
		"elevate_privileges",
		"promote_user",
		"impersonate_account",
		"sudo_exec",
		"reset_password",
		"rotate_key",
		"generate_token",
		"create_api_key",
		"set_permission",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskHigh {
			t.Errorf("tool %q: Risk = %q, want high", name, p.Risk)
		}
		if p.Impact != ImpactAdmin {
			t.Errorf("tool %q: Impact = %q, want ImpactAdmin", name, p.Impact)
		}
		if p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = true, want false", name)
		}
		if p.Cost != 10 {
			t.Errorf("tool %q: Cost = %d, want 10", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority4_HighFinancial verifies priority-4 financial patterns.
func TestHeuristicProfile_Priority4_HighFinancial(t *testing.T) {
	tools := []string{
		"charge_customer",
		"invoice_client",
		"refund_payment",
		"transfer_funds",
		"withdraw_balance",
		"purchase_license",
		"pay_invoice",
		"billing_update",
		"subscribe_plan",
		"checkout_cart",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskHigh {
			t.Errorf("tool %q: Risk = %q, want high", name, p.Risk)
		}
		if p.Impact != ImpactAdmin {
			t.Errorf("tool %q: Impact = %q, want ImpactAdmin", name, p.Impact)
		}
		if !p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = false, want true", name)
		}
		if p.Cost != 10 {
			t.Errorf("tool %q: Cost = %d, want 10", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority5_MediumWrite verifies priority-5 write patterns.
func TestHeuristicProfile_Priority5_MediumWrite(t *testing.T) {
	tools := []string{
		"create_ticket",
		"add_user",
		"insert_record",
		"write_file",
		"post_message",
		"publish_article",
		"send_notification",
		"submit_form",
		"upload_document",
		"import_data",
		"deploy_service",
		"provision_resource",
		"onboard_user",
		"register_device",
		"invite_member",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskMedium {
			t.Errorf("tool %q: Risk = %q, want medium", name, p.Risk)
		}
		if p.Impact != ImpactWrite {
			t.Errorf("tool %q: Impact = %q, want ImpactWrite", name, p.Impact)
		}
		if p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = true, want false", name)
		}
		if p.Cost != 5 {
			t.Errorf("tool %q: Cost = %d, want 5", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority6_MediumUpdate verifies priority-6 update patterns.
func TestHeuristicProfile_Priority6_MediumUpdate(t *testing.T) {
	tools := []string{
		"update_user",
		"edit_ticket",
		"modify_config",
		"patch_record",
		"set_status",
		"change_password",
		"rename_project",
		"move_file",
		"migrate_data",
		"replace_content",
		"upsert_record",
		"merge_branches",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskMedium {
			t.Errorf("tool %q: Risk = %q, want medium", name, p.Risk)
		}
		if p.Impact != ImpactWrite {
			t.Errorf("tool %q: Impact = %q, want ImpactWrite", name, p.Impact)
		}
		if p.Cost != 5 {
			t.Errorf("tool %q: Cost = %d, want 5", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority7_MediumTrigger verifies priority-7 trigger patterns.
func TestHeuristicProfile_Priority7_MediumTrigger(t *testing.T) {
	tools := []string{
		"run_pipeline",
		"execute_job",
		"trigger_build",
		"start_workflow",
		"launch_task",
		"fire_event",
		"invoke_function",
		"call_webhook",
		"process_queue",
		"enqueue_job",
		"schedule_task",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskMedium {
			t.Errorf("tool %q: Risk = %q, want medium", name, p.Risk)
		}
		if p.Impact != ImpactWrite {
			t.Errorf("tool %q: Impact = %q, want ImpactWrite", name, p.Impact)
		}
		if p.Cost != 7 {
			t.Errorf("tool %q: Cost = %d, want 7", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_Priority8_LowRead verifies priority-8 read patterns.
func TestHeuristicProfile_Priority8_LowRead(t *testing.T) {
	tools := []string{
		"get_user",
		"fetch_record",
		"read_file",
		"list_tickets",
		"search_users",
		"find_resource",
		"query_database",
		"lookup_entry",
		"retrieve_data",
		"describe_instance",
		"explain_query",
		"show_config",
		"view_logs",
		"inspect_pod",
		"check_status",
		"status_check",
		"health_check",
		"ping_server",
		"count_records",
		"summarize_report",
		"report_metrics",
		"export_data",
		"download_file",
	}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskLow {
			t.Errorf("tool %q: Risk = %q, want low", name, p.Risk)
		}
		if p.Impact != ImpactRead {
			t.Errorf("tool %q: Impact = %q, want ImpactRead", name, p.Impact)
		}
		if p.RequiresHITL {
			t.Errorf("tool %q: RequiresHITL = true, want false", name)
		}
		if !p.Idempotent {
			t.Errorf("tool %q: Idempotent = false, want true", name)
		}
		if p.Cost != 1 {
			t.Errorf("tool %q: Cost = %d, want 1", name, p.Cost)
		}
	}
}

// TestHeuristicProfile_FallThroughSentinel verifies unknown tool names get a safe default.
func TestHeuristicProfile_FallThroughSentinel(t *testing.T) {
	tools := []string{"xyzzy_frobnicate", "zap_widget", "unknown_operation", "flibbertigibbet"}
	for _, name := range tools {
		p := HeuristicProfile(name, "", nil)
		if p.Risk != RiskHigh {
			t.Errorf("sentinel %q: Risk = %q, want high", name, p.Risk)
		}
		if p.Impact != ImpactDelete {
			t.Errorf("sentinel %q: Impact = %q, want ImpactDelete", name, p.Impact)
		}
		if !p.RequiresHITL {
			t.Errorf("sentinel %q: RequiresHITL = false, want true", name)
		}
		if p.Idempotent {
			t.Errorf("sentinel %q: Idempotent = true, want false", name)
		}
		if p.Cost != 10 {
			t.Errorf("sentinel %q: Cost = %d, want 10", name, p.Cost)
		}
		if p.Source != "inferred" {
			t.Errorf("sentinel %q: Source = %q, want inferred", name, p.Source)
		}
	}
}

// TestHeuristicProfile_BulkPromotion verifies bulk signals promote risk and multiply cost.
func TestHeuristicProfile_BulkPromotion(t *testing.T) {
	// delete (high) + bulk → critical; cost 10 × 3 = 30
	p := HeuristicProfile("bulk_delete_tickets", "", nil)
	if p.Risk != RiskCritical {
		t.Errorf("bulk_delete_tickets: Risk = %q, want critical", p.Risk)
	}
	if !p.RequiresHITL {
		t.Errorf("bulk_delete_tickets: RequiresHITL = false, want true")
	}
	if p.Cost != 30 {
		t.Errorf("bulk_delete_tickets: Cost = %d, want 30", p.Cost)
	}

	// create (medium) + batch → high; cost 5 × 3 = 15
	p2 := HeuristicProfile("batch_create_users", "", nil)
	if p2.Risk != RiskHigh {
		t.Errorf("batch_create_users: Risk = %q, want high", p2.Risk)
	}
	if p2.Cost != 15 {
		t.Errorf("batch_create_users: Cost = %d, want 15", p2.Cost)
	}
}

// TestHeuristicProfile_IrreversibilityPromotion verifies irreversibility signals in description.
func TestHeuristicProfile_IrreversibilityPromotion(t *testing.T) {
	// get (low) + irreversibility description → medium; RequiresHITL = true
	p := HeuristicProfile("get_record", "This action is permanent and cannot be undone.", nil)
	if p.Risk != RiskMedium {
		t.Errorf("irreversible read: Risk = %q, want medium", p.Risk)
	}
	if !p.RequiresHITL {
		t.Errorf("irreversible read: RequiresHITL = false, want true")
	}
	if p.Idempotent {
		t.Errorf("irreversible read: Idempotent = true, want false")
	}

	// delete (high) + irreversibility → critical; RequiresHITL = true
	p2 := HeuristicProfile("delete_user", "This will irreversibly destroy all user data.", nil)
	if p2.Risk != RiskCritical {
		t.Errorf("irreversible delete: Risk = %q, want critical", p2.Risk)
	}
	if !p2.RequiresHITL {
		t.Errorf("irreversible delete: RequiresHITL = false, want true")
	}
}

// TestHeuristicProfile_PIIDetection_SchemaField verifies schema field PII detection.
func TestHeuristicProfile_PIIDetection_SchemaField(t *testing.T) {
	schema := map[string]interface{}{
		"email":  map[string]interface{}{"type": "string"},
		"name":   map[string]interface{}{"type": "string"},
	}
	// get (low) + PII schema field → medium
	p := HeuristicProfile("get_user_profile", "", schema)
	if !p.PIIExposure {
		t.Error("get_user_profile with email field: PIIExposure = false, want true")
	}
	if p.Risk != RiskMedium {
		t.Errorf("get_user_profile with email field: Risk = %q, want medium", p.Risk)
	}
}

// TestHeuristicProfile_PIIDetection_Description verifies description PII signals.
func TestHeuristicProfile_PIIDetection_Description(t *testing.T) {
	p := HeuristicProfile("get_record", "Returns personal data including email address and date of birth.", nil)
	if !p.PIIExposure {
		t.Error("PIIExposure = false, want true")
	}
	if p.Risk != RiskMedium {
		t.Errorf("Risk = %q, want medium (promoted from low)", p.Risk)
	}
}

// TestHeuristicProfile_PIIHighRisk verifies PII on a high-risk tool does not demote risk.
func TestHeuristicProfile_PIIHighRisk(t *testing.T) {
	schema := map[string]interface{}{
		"password": map[string]interface{}{"type": "string"},
	}
	p := HeuristicProfile("delete_user", "", schema)
	if p.Risk != RiskHigh {
		t.Errorf("delete_user with password field: Risk = %q, want high (PII does not demote)", p.Risk)
	}
	if !p.PIIExposure {
		t.Error("PIIExposure = false, want true")
	}
}

// TestHeuristicProfile_SourceAlwaysInferred verifies Source is always "inferred".
func TestHeuristicProfile_SourceAlwaysInferred(t *testing.T) {
	names := []string{"get_user", "delete_record", "xyzzy_unknown", "bulk_update_all"}
	for _, name := range names {
		p := HeuristicProfile(name, "", nil)
		if p.Source != "inferred" {
			t.Errorf("%q: Source = %q, want inferred", name, p.Source)
		}
	}
}

// TestHeuristicProfile_ToolNameAndBackendIDNotSet verifies callers must set identity fields.
func TestHeuristicProfile_ToolNameAndBackendIDNotSet(t *testing.T) {
	p := HeuristicProfile("get_user", "", nil)
	if p.ToolName != "" {
		t.Errorf("ToolName = %q, want empty (caller sets this)", p.ToolName)
	}
	if p.BackendID != "" {
		t.Errorf("BackendID = %q, want empty (caller sets this)", p.BackendID)
	}
}

// TestHeuristicProfile_NilSchemaAndEmptyDescription verifies graceful handling of absent context.
func TestHeuristicProfile_NilSchemaAndEmptyDescription(t *testing.T) {
	// Should not panic
	p := HeuristicProfile("update_config", "", nil)
	if p.Risk == "" {
		t.Error("Risk is empty, expected a value")
	}
}

// TestHeuristicProfile_CaseInsensitiveMatching verifies tool names are lowercased before matching.
func TestHeuristicProfile_CaseInsensitiveMatching(t *testing.T) {
	p1 := HeuristicProfile("DELETE_TICKET", "", nil)
	p2 := HeuristicProfile("delete_ticket", "", nil)
	if p1.Risk != p2.Risk {
		t.Errorf("case mismatch: DELETE_TICKET risk=%q, delete_ticket risk=%q", p1.Risk, p2.Risk)
	}
	if p1.Impact != p2.Impact {
		t.Errorf("case mismatch: DELETE_TICKET impact=%q, delete_ticket impact=%q", p1.Impact, p2.Impact)
	}
}

// TestHeuristicProfile_BulkInNameNotDescription verifies bulk detection in tool name.
func TestHeuristicProfile_BulkInNameNotDescription(t *testing.T) {
	// "batch" in name triggers bulk
	p := HeuristicProfile("batch_update_records", "", nil)
	if !p.RequiresHITL {
		t.Error("batch_update_records: RequiresHITL = false, want true (bulk in name)")
	}
}

// TestHeuristicProfile_Priority1VsPriority2_DeleteAll verifies priority 1 wins over priority 2.
func TestHeuristicProfile_Priority1VsPriority2_DeleteAll(t *testing.T) {
	// "delete_all" matches priority 1; "delete" alone would match priority 2
	p := HeuristicProfile("delete_all_records", "", nil)
	if p.Risk != RiskCritical {
		t.Errorf("delete_all_records: Risk = %q, want critical (priority 1)", p.Risk)
	}
	if p.Cost != 20 {
		t.Errorf("delete_all_records: Cost = %d, want 20", p.Cost)
	}
}
