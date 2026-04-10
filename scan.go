package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// LogFn is a logging function with format string.
type LogFn func(format string, args ...interface{})

// scanSelfReportingBackends iterates over backends marked as self-reporting,
// spawns each one to enumerate their tools, extracts EnforcerProfile metadata
// from tool._meta, and stores the results in the enforcer_tool_profiles table.
func scanSelfReportingBackends(st *store.Store, logf, warnf LogFn) {
	backends, err := st.ListBackends()
	if err != nil {
		warnf("scanSelfReporting: failed to list backends: %v", err)
		return
	}

	scanner := enforcer.NewToolProfileScanner(poolmgr.ScanBackendTools)
	scanned := 0

	for _, b := range backends {
		if !b.SelfReporting || !b.Enabled {
			continue
		}

		logf("scanSelfReporting: scanning backend %s (command=%q)", b.ID, b.Command)

		env := buildEnvForScan(b)
		profiles, err := scanner.ScanBackend(b.ID, b.Command, env)
		if err != nil {
			warnf("scanSelfReporting: backend %s scan failed: %v", b.ID, err)
			continue
		}

		if len(profiles) == 0 {
			logf("scanSelfReporting: backend %s returned no self-reported profiles", b.ID)
			continue
		}

		stored := 0
		for _, p := range profiles {
			row := store.ToolProfileRow{
				ID:           uuid.New().String(),
				BackendID:    b.ID,
				ToolName:     p.ToolName,
				RiskLevel:    p.Profile.RiskLevel,
				ImpactScope:  p.Profile.ImpactScope,
				ResourceCost: p.Profile.ResourceCost,
				RequiresHITL: p.Profile.ApprovalReq,
				PIIExposure:  p.Profile.PIIExposure,
				Idempotent:   p.Profile.Idempotent,
				RawProfile:   p.RawJSON,
				ScannedAt:    time.Now(),
			}
			if err := st.UpsertToolProfile(row); err != nil {
				warnf("scanSelfReporting: failed to store profile %s.%s: %v", b.ID, p.ToolName, err)
				continue
			}
			stored++
		}

		logf("scanSelfReporting: backend %s: stored %d tool profiles", b.ID, stored)
		scanned += stored
	}

	if scanned > 0 {
		logf("scanSelfReporting: total: stored %d tool profiles", scanned)
	} else {
		logf("scanSelfReporting: no self-reporting backends found or no profiles extracted")
	}
}

// buildEnvForScan builds the environment for scanning a backend.
// It uses the backend's static Env (which contains API keys etc.)
// merged with the current process environment.
func buildEnvForScan(b *store.Backend) []string {
	env := append([]string(nil), os.Environ()...)

	if b.Env != "" && b.Env != "{}" {
		var envMap map[string]string
		if err := json.Unmarshal([]byte(b.Env), &envMap); err == nil {
			for k, v := range envMap {
				env = append(env, k+"="+v)
			}
		}
	}

	shared.Debugf("buildEnvForScan: backend=%s, env_vars=%d", b.ID, len(env))
	return env
}

// loadOverridesIntoResolver loads all manual overrides from the database into
// the enforcer's in-memory override map so they take effect at startup.
func loadOverridesIntoResolver(st *store.Store, enf *enforcer.Enforcer, logf, warnf LogFn) {
	es := store.NewEnforcerStore(st.DB())
	overrides, err := es.ListOverrides()
	if err != nil {
		warnf("loadOverridesIntoResolver: failed to list overrides: %v", err)
		return
	}
	loaded := 0
	for _, o := range overrides {
		profile := enforcer.SafetyProfile{
			ToolName:     o.ToolName,
			BackendID:    o.BackendID,
			Risk:         enforcer.RiskLevel(o.RiskLevel),
			Impact:       enforcer.ImpactScope(o.ImpactScope),
			Cost:         o.ResourceCost,
			RequiresHITL: o.RequiresHITL,
			PIIExposure:  o.PIIExposure,
			Source:       "override",
		}
		if err := enf.RegisterOverride(o.ToolName, o.BackendID, profile); err != nil {
			warnf("loadOverridesIntoResolver: failed to register override for %s: %v", o.ToolName, err)
			continue
		}
		loaded++
	}
	logf("loadOverridesIntoResolver: loaded %d overrides into resolver", loaded)
}
