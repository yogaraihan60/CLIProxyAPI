// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is stored in model_definitions_static_data.go.
package registry

import (
	"sort"
	"strings"
)

// GetStaticModelDefinitionsByChannel returns static model definitions for a given channel/provider.
// It returns nil when the channel is unknown.
//
// Supported channels:
//   - claude
//   - gemini
//   - vertex
//   - gemini-cli
//   - aistudio
//   - codex
//   - qwen
//   - iflow
//   - antigravity (returns static overrides only)
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "claude":
		return GetClaudeModels()
	case "gemini":
		return GetGeminiModels()
	case "vertex":
		return GetGeminiVertexModels()
	case "gemini-cli":
		return GetGeminiCLIModels()
	case "aistudio":
		return GetAIStudioModels()
	case "codex":
		return GetOpenAIModels()
	case "qwen":
		return GetQwenModels()
	case "iflow":
		return GetIFlowModels()
	case "antigravity":
		cfg := GetAntigravityModelConfig()
		if len(cfg) == 0 {
			return nil
		}
		models := make([]*ModelInfo, 0, len(cfg))
		for modelID, entry := range cfg {
			if modelID == "" || entry == nil {
				continue
			}
			models = append(models, &ModelInfo{
				ID:                  modelID,
				Object:              "model",
				OwnedBy:             "antigravity",
				Type:                "antigravity",
				Thinking:            entry.Thinking,
				MaxCompletionTokens: entry.MaxCompletionTokens,
			})
		}
		sort.Slice(models, func(i, j int) bool {
			return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
		})
		return models
	default:
		return nil
	}
}

// LookupStaticModelInfo searches all static model definitions for a model by ID.
// Returns nil if no matching model is found.
func LookupStaticModelInfo(modelID string) *ModelInfo {
	if modelID == "" {
		return nil
	}

	allModels := [][]*ModelInfo{
		GetClaudeModels(),
		GetGeminiModels(),
		GetGeminiVertexModels(),
		GetGeminiCLIModels(),
		GetAIStudioModels(),
		GetOpenAIModels(),
		GetQwenModels(),
		GetIFlowModels(),
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && m.ID == modelID {
				return m
			}
		}
	}

	// Check Antigravity static config
	if cfg := GetAntigravityModelConfig()[modelID]; cfg != nil {
		return &ModelInfo{
			ID:                  modelID,
			Thinking:            cfg.Thinking,
			MaxCompletionTokens: cfg.MaxCompletionTokens,
		}
	}

	return nil
}

// IsImageGenerationModel checks if a model ID represents an image generation model
// that should have resolution/aspect ratio variants generated.
func IsImageGenerationModel(modelID string) bool {
	// Check for known image generation model patterns
	// Includes both aliased names (-image-preview) and upstream names (-image, -pro-image)
	imageModelPatterns := []string{
		"-image-preview",
		"-image-generation",
		"-pro-image", // upstream format: gemini-3-pro-image
		"imagen-",    // imagen models
	}
	modelLower := strings.ToLower(modelID)
	for _, pattern := range imageModelPatterns {
		if strings.Contains(modelLower, pattern) {
			return true
		}
	}
	return false
}

// GenerateImageModelVariants generates resolution variants for an image model
// e.g., gemini-3-pro-image-preview -> gemini-3-pro-image-preview-2k, gemini-3-pro-image-preview-4k
func GenerateImageModelVariants(baseModel *ModelInfo) []*ModelInfo {
	if baseModel == nil {
		return nil
	}

	resolutions := []string{"-2k", "-4k"}
	var variants []*ModelInfo

	for _, res := range resolutions {
		variantID := baseModel.ID + res
		variant := &ModelInfo{
			ID:                         variantID,
			Object:                     baseModel.Object,
			Created:                    baseModel.Created,
			OwnedBy:                    baseModel.OwnedBy,
			Type:                       baseModel.Type,
			Name:                       baseModel.Name, // Keep same backend name
			Version:                    baseModel.Version,
			DisplayName:                baseModel.DisplayName + " (" + res[1:] + ")",
			Description:                baseModel.Description + " with " + res[1:] + " resolution",
			InputTokenLimit:            baseModel.InputTokenLimit,
			OutputTokenLimit:           baseModel.OutputTokenLimit,
			SupportedGenerationMethods: baseModel.SupportedGenerationMethods,
			Thinking:                   baseModel.Thinking,
		}
		variants = append(variants, variant)
	}

	return variants
}
