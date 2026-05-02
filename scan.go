package main

import (
	"encoding/json"
	"os"
	"sync"
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
// Scans are run in parallel using goroutines for faster startup.
// It first tries scan-mode (--scan flag) which is faster and doesn't require env vars.
// If scan-mode fails, it falls back to the MCP handshake approach.
func scanSelfReportingBackends(st *store.Store, logf, warnf LogFn) {
	backends, err := st.ListBackends()
	if err != nil {
		warnf("scanSelfReporting: failed to list backends: %v", err)
		return
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	scanned := 0

	scanTimeout := 10 * time.Second

	for _, b := range backends {
		if !b.SelfReporting || !b.Enabled {
			continue
		}

		wg.Add(1)
		go func(b *store.Backend) {
			defer wg.Done()

			logf("scanSelfReporting: scanning backend %s (command=%q)", b.ID, b.Command)

			// Try scan-mode first (faster, no env vars needed)
			env := buildEnvForScan(b)
			output, err := poolmgr.ScanBackendToolsScanMode(b.Command, env, scanTimeout)
			if err != nil {
				logf("scanSelfReporting: backend %s scan-mode failed: %v, falling back to MCP handshake", b.ID, err)
				// Fall back to MCP handshake approach
				scanner := enforcer.NewToolProfileScanner(poolmgr.ScanBackendTools)
				profiles, err := scanner.ScanBackend(b.ID, b.Command, env)
				if err != nil {
					warnf("scanSelfReporting: backend %s scan failed: %v", b.ID, err)
					return
				}
				if len(profiles) == 0 {
					logf("scanSelfReporting: backend %s returned no self-reported profiles", b.ID)
					return
				}
				storeProfiles(st, b, profiles, &mu, &scanned, logf, warnf)
				return
			}

			// Process scan-mode output
			if len(output.Tools) == 0 {
				logf("scanSelfReporting: backend %s returned no tools in scan-mode", b.ID)
				return
			}

			stored := 0
			for _, tool := range output.Tools {
				if tool.Profile == nil {
					continue
				}
				rawJSON, _ := json.Marshal(tool.Profile)
				row := store.ToolProfileRow{
					ID:           uuid.New().String(),
					BackendID:    b.ID,
					ToolName:     tool.Name,
					RiskLevel:    string(tool.Profile.RiskLevel),
					ImpactScope:  tool.Profile.ImpactScope,
					ResourceCost: tool.Profile.ResourceCost,
					RequiresHITL: tool.Profile.ApprovalReq,
					PIIExposure:  tool.Profile.PIIExposure,
					Idempotent:   tool.Profile.Idempotent,
					RawProfile:   string(rawJSON),
					ScannedAt:    time.Now(),
				}
				if err := st.UpsertToolProfile(row); err != nil {
					warnf("scanSelfReporting: failed to store profile %s.%s: %v", b.ID, tool.Name, err)
					continue
				}
				stored++
			}

			logf("scanSelfReporting: backend %s: stored %d tool profiles (scan-mode)", b.ID, stored)

			mu.Lock()
			scanned += stored
			mu.Unlock()
		}(b)
	}

	wg.Wait()

	if scanned > 0 {
		logf("scanSelfReporting: total: stored %d tool profiles", scanned)
	} else {
		logf("scanSelfReporting: no self-reporting backends found or no profiles extracted")
	}
}

// storeProfiles stores the scanned profiles into the database
func storeProfiles(st *store.Store, b *store.Backend, profiles []enforcer.ScannedProfile, mu *sync.Mutex, scanned *int, logf, warnf LogFn) {
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

	mu.Lock()
	*scanned += stored
	mu.Unlock()
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
