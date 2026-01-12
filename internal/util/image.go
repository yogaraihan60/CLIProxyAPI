package util

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	"image/png"
	"strings"
)

// ImageConfig holds parsed image generation configuration from model name suffixes
type ImageConfig struct {
	BaseModel   string
	AspectRatio string
	ImageSize   string // "2K", "4K", or "" for default
}

// ParseImageModelSuffixes parses model name suffixes to extract aspect ratio and resolution
// e.g., "gemini-3-pro-image-preview-4k-16x9" -> baseModel, "16:9", "4K"
func ParseImageModelSuffixes(model string) ImageConfig {
	config := ImageConfig{
		BaseModel:   model,
		AspectRatio: "",
		ImageSize:   "",
	}

	// Detect aspect ratio from suffix
	switch {
	case strings.Contains(model, "-21x9"), strings.Contains(model, "-21-9"):
		config.AspectRatio = "21:9"
	case strings.Contains(model, "-16x9"), strings.Contains(model, "-16-9"):
		config.AspectRatio = "16:9"
	case strings.Contains(model, "-9x16"), strings.Contains(model, "-9-16"):
		config.AspectRatio = "9:16"
	case strings.Contains(model, "-4x3"), strings.Contains(model, "-4-3"):
		config.AspectRatio = "4:3"
	case strings.Contains(model, "-3x4"), strings.Contains(model, "-3-4"):
		config.AspectRatio = "3:4"
	case strings.Contains(model, "-1x1"), strings.Contains(model, "-1-1"):
		config.AspectRatio = "1:1"
	}

	// Detect resolution/size from suffix
	if strings.Contains(model, "-4k") || strings.Contains(model, "-hd") {
		config.ImageSize = "4K"
	} else if strings.Contains(model, "-2k") {
		config.ImageSize = "2K"
	}

	// Normalize base model name by stripping suffixes
	// Keep the core model name (e.g., "gemini-3-pro-image-preview")
	baseModel := model
	suffixesToStrip := []string{
		"-4k", "-2k", "-hd",
		"-21x9", "-21-9", "-16x9", "-16-9", "-9x16", "-9-16",
		"-4x3", "-4-3", "-3x4", "-3-4", "-1x1", "-1-1",
	}
	for _, suffix := range suffixesToStrip {
		baseModel = strings.ReplaceAll(baseModel, suffix, "")
	}
	config.BaseModel = baseModel

	return config
}

func CreateWhiteImageBase64(aspectRatio string) (string, error) {
	width := 1024
	height := 1024

	switch aspectRatio {
	case "1:1":
		width = 1024
		height = 1024
	case "2:3":
		width = 832
		height = 1248
	case "3:2":
		width = 1248
		height = 832
	case "3:4":
		width = 864
		height = 1184
	case "4:3":
		width = 1184
		height = 864
	case "4:5":
		width = 896
		height = 1152
	case "5:4":
		width = 1152
		height = 896
	case "9:16":
		width = 768
		height = 1344
	case "16:9":
		width = 1344
		height = 768
	case "21:9":
		width = 1536
		height = 672
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)

	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}

	base64String := base64.StdEncoding.EncodeToString(buf.Bytes())
	return base64String, nil
}
