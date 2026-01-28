package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

type modelAliasEntry interface {
	GetName() string
	GetAlias() string
}

type oauthModelAliasTable struct {
	// reverse maps channel -> alias (lower) -> original upstream model name.
	reverse map[string]map[string]string
}

func compileOAuthModelAliasTable(aliases map[string][]internalconfig.OAuthModelAlias) *oauthModelAliasTable {
	if len(aliases) == 0 {
		return &oauthModelAliasTable{}
	}
	out := &oauthModelAliasTable{
		reverse: make(map[string]map[string]string, len(aliases)),
	}
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string]string, len(entries))
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = name
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
	}
	return out
}

// SetOAuthModelAlias updates the OAuth model name alias table used during execution.
// The alias is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetOAuthModelAlias(aliases map[string][]internalconfig.OAuthModelAlias) {
	if m == nil {
		return
	}
	table := compileOAuthModelAliasTable(aliases)
	// atomic.Value requires non-nil store values.
	if table == nil {
		table = &oauthModelAliasTable{}
	}
	m.oauthModelAlias.Store(table)
}

// applyOAuthModelAlias resolves the upstream model from OAuth model alias.
// If an alias exists, the returned model is the upstream model.
func (m *Manager) applyOAuthModelAlias(auth *Auth, requestedModel string) string {
	upstreamModel := m.resolveOAuthUpstreamModel(auth, requestedModel)
	if upstreamModel == "" {
		return requestedModel
	}
	return upstreamModel
}

func resolveModelAliasFromConfigModels(requestedModel string, models []modelAliasEntry) string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	if len(models) == 0 {
		return ""
	}

	requestResult := thinking.ParseSuffix(requestedModel)
	base := requestResult.ModelName

	// Also strip image suffixes (e.g., -4k, -2k) to find the base model for alias lookup
	imgConfig := util.ParseImageModelSuffixes(base)
	baseNoImageSuffix := imgConfig.BaseModel
	imageSuffix := "" // Store the image suffix to re-add later
	if imgConfig.ImageSize != "" {
		// Determine the original suffix string from the model name
		if strings.Contains(base, "-4k") {
			imageSuffix = "-4k"
		} else if strings.Contains(base, "-2k") {
			imageSuffix = "-2k"
		} else if strings.Contains(base, "-hd") {
			imageSuffix = "-hd"
		}
	}

	// Build candidates: base without image suffix first, then with image suffix, then full model
	candidates := []string{baseNoImageSuffix}
	if baseNoImageSuffix != base {
		candidates = append(candidates, base)
	}
	if base != requestedModel {
		candidates = append(candidates, requestedModel)
	}

	preserveSuffix := func(resolved string) string {
		resolved = strings.TrimSpace(resolved)
		if resolved == "" {
			return ""
		}
		result := resolved
		// Add image suffix first (it's part of the model name)
		if imageSuffix != "" {
			result = result + imageSuffix
		}
		// If config already has thinking suffix, it takes priority
		if thinking.ParseSuffix(resolved).HasSuffix {
			return result
		}
		// Then add thinking suffix (it's in parentheses)
		if requestResult.HasSuffix && requestResult.RawSuffix != "" {
			result = result + "(" + requestResult.RawSuffix + ")"
		}
		return result
	}

	for i := range models {
		name := strings.TrimSpace(models[i].GetName())
		alias := strings.TrimSpace(models[i].GetAlias())
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			if alias != "" && strings.EqualFold(alias, candidate) {
				if name != "" {
					return preserveSuffix(name)
				}
				return preserveSuffix(candidate)
			}
			if name != "" && strings.EqualFold(name, candidate) {
				return preserveSuffix(name)
			}
		}
	}
	return ""
}

// resolveOAuthUpstreamModel resolves the upstream model name from OAuth model alias.
// If an alias exists, returns the original (upstream) model name that corresponds
// to the requested alias.
//
// If the requested model contains a thinking suffix (e.g., "gemini-2.5-pro(8192)"),
// the suffix is preserved in the returned model name. However, if the alias's
// original name already contains a suffix, the config suffix takes priority.
func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	return resolveUpstreamModelFromAliasTable(m, auth, requestedModel, modelAliasChannel(auth))
}

func resolveUpstreamModelFromAliasTable(m *Manager, auth *Auth, requestedModel, channel string) string {
	if m == nil || auth == nil {
		return ""
	}
	if channel == "" {
		return ""
	}

	// Extract thinking suffix from requested model using ParseSuffix
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName

	// Also strip image suffixes (e.g., -4k, -2k) to find the base model for alias lookup
	imgConfig := util.ParseImageModelSuffixes(baseModel)
	baseModelNoImageSuffix := imgConfig.BaseModel
	imageSuffix := "" // Store the image suffix to re-add later
	if imgConfig.ImageSize != "" {
		// Determine the original suffix string from the model name
		if strings.Contains(baseModel, "-4k") {
			imageSuffix = "-4k"
		} else if strings.Contains(baseModel, "-2k") {
			imageSuffix = "-2k"
		} else if strings.Contains(baseModel, "-hd") {
			imageSuffix = "-hd"
		}
	}

	// Candidate keys to match: base model without image suffix, base model with thinking suffix stripped,
	// and raw input (handles suffix-parsing edge cases).
	candidates := []string{baseModelNoImageSuffix}
	if baseModelNoImageSuffix != baseModel {
		candidates = append(candidates, baseModel)
	}
	if baseModel != requestedModel {
		candidates = append(candidates, requestedModel)
	}

	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	rev := table.reverse[channel]
	if rev == nil {
		return ""
	}

	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		original := strings.TrimSpace(rev[key])
		if original == "" {
			continue
		}
		if strings.EqualFold(original, baseModel) {
			return ""
		}

		// If config already has suffix, it takes priority (but still add image suffix).
		if thinking.ParseSuffix(original).HasSuffix {
			// Add image suffix if present
			if imageSuffix != "" {
				return original + imageSuffix
			}
			return original
		}
		// Build the final model name preserving both thinking and image suffixes
		result := original
		// Add image suffix first (it's part of the model name)
		if imageSuffix != "" {
			result = result + imageSuffix
		}
		// Then add thinking suffix (it's in parentheses)
		if requestResult.HasSuffix && requestResult.RawSuffix != "" {
			result = result + "(" + requestResult.RawSuffix + ")"
		}
		return result
	}

	return ""
}

// modelAliasChannel extracts the OAuth model alias channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelAliasChannel for the actual channel resolution.
func modelAliasChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := ""
	if auth.Attributes != nil {
		authKind = strings.ToLower(strings.TrimSpace(auth.Attributes["auth_kind"]))
	}
	if authKind == "" {
		if kind, _ := auth.AccountInfo(); strings.EqualFold(kind, "api_key") {
			authKind = "apikey"
		}
	}
	return OAuthModelAliasChannel(provider, authKind)
}

// OAuthModelAliasChannel returns the OAuth model alias channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model alias (e.g., API key authentication).
//
// Supported channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, qwen, iflow.
func OAuthModelAliasChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch provider {
	case "gemini":
		// gemini provider uses gemini-api-key config, not oauth-model-alias.
		// OAuth-based gemini auth is converted to "gemini-cli" by the synthesizer.
		return ""
	case "vertex":
		if authKind == "apikey" {
			return ""
		}
		return "vertex"
	case "claude":
		if authKind == "apikey" {
			return ""
		}
		return "claude"
	case "codex":
		if authKind == "apikey" {
			return ""
		}
		return "codex"
	case "gemini-cli", "aistudio", "antigravity", "qwen", "iflow":
		return provider
	default:
		return ""
	}
}
