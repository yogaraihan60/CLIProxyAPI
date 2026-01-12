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
	BaseModel string
	ImageSize string // "2K", "4K", or "" for default
}

// ParseImageModelSuffixes parses model name suffixes to extract resolution
// e.g., "gemini-3-pro-image-preview-4k" -> baseModel, "4K"
func ParseImageModelSuffixes(model string) ImageConfig {
	config := ImageConfig{
		BaseModel: model,
		ImageSize: "",
	}

	// Detect resolution/size from suffix
	if strings.Contains(model, "-4k") || strings.Contains(model, "-hd") {
		config.ImageSize = "4K"
	} else if strings.Contains(model, "-2k") {
		config.ImageSize = "2K"
	}

	// Normalize base model name by stripping suffixes
	baseModel := model
	suffixesToStrip := []string{"-4k", "-2k", "-hd"}
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
