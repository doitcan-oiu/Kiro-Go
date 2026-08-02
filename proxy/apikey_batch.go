package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"kiro-go/auth"
	"kiro-go/config"
	"kiro-go/logger"
)

// firstImportableApiKeyKey returns the first non-empty, non-duplicate key in
// rawText, used as the batch region probe. Duplicates already present in existing
// and repeats within rawText are skipped. Returns "" when there is no importable
// key. This is a focused early-out scan; it does not replicate the create loop's
// per-key create/refresh/async work.
func firstImportableApiKeyKey(rawText string, existing map[string]bool) string {
	seenInBatch := make(map[string]bool)
	for _, raw := range strings.Split(rawText, "\n") {
		key := strings.TrimSpace(raw)
		if key == "" || existing[key] || seenInBatch[key] {
			continue
		}
		seenInBatch[key] = true
		return key
	}
	return ""
}

// ApiKeyImportResult is the per-key outcome of a bulk API-key import.
type ApiKeyImportResult struct {
	MaskedKey    string  `json:"maskedKey"`
	AccountID    string  `json:"accountId,omitempty"`
	Email        string  `json:"email,omitempty"`
	Subscription string  `json:"subscription,omitempty"`
	UsageCurrent float64 `json:"usageCurrent,omitempty"`
	UsageLimit   float64 `json:"usageLimit,omitempty"`
	Imported     bool    `json:"imported"`
	Skipped      bool    `json:"skipped"`
	InfoOK       bool    `json:"infoOk,omitempty"`
	Error        string  `json:"error,omitempty"`
}

// ApiKeyImportSummary aggregates a bulk import run.
type ApiKeyImportSummary struct {
	Total      int                  `json:"total"`
	Imported   int                  `json:"imported"`
	Skipped    int                  `json:"skipped"`
	InfoFailed int                  `json:"infoFailed"`
	Results    []ApiKeyImportResult `json:"results"`
}

// maskKey returns head-6 … tail-4 of a key (or "****" when the key is too short
// to safely mask).
func maskKey(key string) string {
	if len(key) <= 10 {
		return "****"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

// ImportApiKeys parses newline-separated Kiro API keys and imports each as an
// enabled api_key account. Dedups against the existing snapshot (by KiroApiKey)
// and within the batch itself. For each new key it best-effort fetches usage
// info (email/subscription/credit) via RefreshAccountInfo. Returns a per-key
// result summary; never aborts the whole batch on a single key's failure.
func (h *Handler) ImportApiKeys(rawText, region, authRegion, apiRegion string) ApiKeyImportSummary {
	return h.ImportApiKeysWithCost(rawText, region, authRegion, apiRegion, 0)
}

// ImportApiKeysWithCost 与 ImportApiKeys 相同，但为本批每个 Key 记录采购成本
// （美元 / 个）。补号流程用它把供应商单价绑定到 Key 上，成为利润核算的成本项。
//
// cost <= 0 表示未知成本（人工导入的 Key 没有采购价），此时不写该字段，面板据此
// 显示「—」而不是把利润当成「等于收入」——后者会把自有 Key 的收益虚报为纯利润。
func (h *Handler) ImportApiKeysWithCost(rawText, region, authRegion, apiRegion string, cost float64) ApiKeyImportSummary {
	summary := ApiKeyImportSummary{Results: []ApiKeyImportResult{}}

	// Snapshot existing keys to dedup against.
	existing := make(map[string]bool)
	for _, a := range config.GetAccounts() {
		if a.KiroApiKey != "" {
			existing[a.KiroApiKey] = true
		}
	}
	seenInBatch := make(map[string]bool)

	if region == "" {
		region = "us-east-1"
	}

	// Probe-once-per-batch: detect the batch region from the first importable key.
	// Read-only; the probe account is thrown away (no persist, no double-create).
	// On HIT override region with the detected one; on failure keep the hint/default.
	if first := firstImportableApiKeyKey(rawText, existing); first != "" {
		probeAccount := config.Account{
			AuthMethod:  "api_key",
			KiroApiKey:  first,
			AccessToken: first,
			MachineId:   config.GenerateMachineId(),
		}
		if _, detected, err := refreshApiKeyInfoWithRegionProbe(&probeAccount, region); err == nil && detected != "" {
			region = detected
		}
	}

	for _, raw := range strings.Split(rawText, "\n") {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		summary.Total++
		masked := maskKey(key)

		if existing[key] || seenInBatch[key] {
			summary.Skipped++
			summary.Results = append(summary.Results, ApiKeyImportResult{MaskedKey: masked, Skipped: true})
			continue
		}
		seenInBatch[key] = true

		account := config.Account{
			ID:          auth.GenerateAccountID(),
			AuthMethod:  "api_key",
			KiroApiKey:  key,
			AccessToken: key, // pool/dispatch/metrics compatibility
			Region:      region,
			AuthRegion:  authRegion,
			ApiRegion:   apiRegion,
			ExpiresAt:   0, // api_key: never refresh
			Enabled:     true,
			MachineId:   config.GenerateMachineId(),
			Cost:        cost,
		}
		if err := config.AddAccount(account); err != nil {
			summary.Results = append(summary.Results, ApiKeyImportResult{MaskedKey: masked, Error: err.Error()})
			continue
		}
		summary.Imported++

		result := ApiKeyImportResult{MaskedKey: masked, Imported: true, AccountID: account.ID}
		// Best-effort usage info. RefreshAccountInfo uses KiroApiKey (mirrored into
		// AccessToken) as the bearer; profile-ARN resolution is skipped for api_key.
		if info, err := RefreshAccountInfo(&account); err != nil {
			summary.InfoFailed++
			logger.Warnf("[ApiKeyBatch] RefreshAccountInfo failed for %s: %v", masked, err)
		} else if info != nil {
			result.InfoOK = true
			result.Email = info.Email
			result.Subscription = info.SubscriptionTitle
			if result.Subscription == "" {
				result.Subscription = info.SubscriptionType
			}
			result.UsageCurrent = info.UsageCurrent
			result.UsageLimit = info.UsageLimit
		}
		// Async model-cache refresh (non-blocking).
		go func(acc config.Account) {
			if err := h.fetchAndCacheAccountModels(&acc); err != nil {
				logger.Warnf("[ModelsCache] Auto-refresh failed for api_key account %s: %v", accountEmailForLog(&acc), err)
			}
		}(account)
		summary.Results = append(summary.Results, result)
	}

	if summary.Imported > 0 {
		h.pool.Reload()
	}
	return summary
}

// apiImportApiKeys handles POST /auth/apikeys-batch: bulk import of newline-
// separated Kiro API keys.
func (h *Handler) apiImportApiKeys(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20) // 2 MB cap on the keys payload
	var req struct {
		Keys       string `json:"keys"`
		Region     string `json:"region"`
		AuthRegion string `json:"authRegion"`
		ApiRegion  string `json:"apiRegion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Keys) == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "keys is required"})
		return
	}

	summary := h.ImportApiKeys(req.Keys, req.Region, req.AuthRegion, req.ApiRegion)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    true,
		"total":      summary.Total,
		"imported":   summary.Imported,
		"skipped":    summary.Skipped,
		"infoFailed": summary.InfoFailed,
		"results":    summary.Results,
	})
}
