package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
)

const (
	defaultCodexImageSize            = "auto"
	defaultCodexImageQuality         = "auto"
	defaultCodexImageOutputFormat    = "png"
	defaultCodexImageBackground      = "auto"
	defaultCodexImageModeration      = "auto"
	defaultCodexImageCompression     = 100
	maxCodexImageCount               = 10
	maxCodexInputImages              = 16
	maxCodexImageDataURLChars        = 20_971_520
	gptImage2MinPixels               = 655_360
	gptImage2MaxPixels               = 8_294_400
	gptImage2MaxEdge                 = 3840
	gptImage2MaxAspectRatioNumerator = 3
)

var ImageModelList = []string{"gpt-image-2"}

type codexImageRequest struct {
	Prompt            string          `json:"prompt"`
	Model             string          `json:"model"`
	N                 uint            `json:"n"`
	Size              string          `json:"size"`
	Quality           string          `json:"quality"`
	OutputFormat      string          `json:"output_format"`
	Background        string          `json:"background,omitempty"`
	Moderation        string          `json:"moderation,omitempty"`
	OutputCompression *int            `json:"output_compression,omitempty"`
	User              json.RawMessage `json:"user,omitempty"`
	Images            []codexImageRef `json:"images,omitempty"`
	Mask              *codexImageRef  `json:"mask,omitempty"`
}

type codexImageRef struct {
	ImageURL string `json:"image_url"`
}

func isImageRelayMode(relayMode int) bool {
	return relayMode == relayconstant.RelayModeImagesGenerations ||
		relayMode == relayconstant.RelayModeImagesEdits
}

func imageRequestPath(relayMode int) string {
	if relayMode == relayconstant.RelayModeImagesEdits {
		return "/backend-api/codex/images/edits"
	}
	return "/backend-api/codex/images/generations"
}

func convertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	if info == nil || !isImageRelayMode(info.RelayMode) {
		return nil, errors.New("codex channel: image endpoint not supported")
	}

	if isMultipartRequest(c) {
		if err := populateMultipartImageFields(c, &request); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("model is required")
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "b64_json" {
		return nil, errors.New("codex image endpoint only supports response_format=b64_json")
	}

	n := uint(1)
	if request.N != nil {
		n = *request.N
	}
	if n < 1 || n > maxCodexImageCount {
		return nil, fmt.Errorf("n must be between 1 and %d", maxCodexImageCount)
	}

	size := defaultString(request.Size, defaultCodexImageSize)
	quality := defaultString(request.Quality, defaultCodexImageQuality)
	background, err := rawStringOrDefault(request.Background, "background", defaultCodexImageBackground)
	if err != nil {
		return nil, err
	}
	moderation, err := rawStringOrDefault(request.Moderation, "moderation", defaultCodexImageModeration)
	if err != nil {
		return nil, err
	}
	outputFormat, err := rawStringOrDefault(request.OutputFormat, "output_format", defaultCodexImageOutputFormat)
	if err != nil {
		return nil, err
	}
	outputFormat = strings.ToLower(outputFormat)
	if outputFormat == "jpg" {
		outputFormat = "jpeg"
	}
	outputCompression, err := rawInt(request.OutputCompression, "output_compression")
	if err != nil {
		return nil, err
	}
	if outputCompression == nil && (outputFormat == "jpeg" || outputFormat == "webp") {
		value := defaultCodexImageCompression
		outputCompression = &value
	}

	if err := validateCodexImageParameters(request.Model, size, quality, background, moderation, outputFormat, outputCompression); err != nil {
		return nil, err
	}

	converted := &codexImageRequest{
		Prompt:            request.Prompt,
		Model:             request.Model,
		N:                 n,
		Size:              size,
		Quality:           quality,
		OutputFormat:      outputFormat,
		Background:        background,
		Moderation:        moderation,
		OutputCompression: outputCompression,
		User:              request.User,
	}

	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		images, mask, err := buildCodexEditImages(c, request)
		if err != nil {
			return nil, err
		}
		converted.Images = images
		converted.Mask = mask
	}

	return converted, nil
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func rawStringOrDefault(raw json.RawMessage, field string, fallback string) (string, error) {
	if len(raw) == 0 {
		return fallback, nil
	}
	var value string
	if err := common.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return defaultString(value, fallback), nil
}

func rawInt(raw json.RawMessage, field string) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value int
	if err := common.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be an integer", field)
	}
	return &value, nil
}

func validateCodexImageParameters(model string, size string, quality string, background string, moderation string, outputFormat string, outputCompression *int) error {
	if !isOneOf(quality, "low", "medium", "high", "auto") {
		return errors.New("quality must be low, medium, high, or auto")
	}
	if !isOneOf(background, "opaque", "auto") {
		return errors.New("background must be opaque or auto")
	}
	if !isOneOf(moderation, "low", "auto") {
		return errors.New("moderation must be low or auto")
	}
	if !isOneOf(outputFormat, "png", "jpeg", "webp") {
		return errors.New("output_format must be png, jpeg, or webp")
	}
	if outputCompression != nil {
		if *outputCompression < 0 || *outputCompression > 100 {
			return errors.New("output_compression must be between 0 and 100")
		}
		if outputFormat != "jpeg" && outputFormat != "webp" {
			return errors.New("output_compression is only supported for jpeg or webp output")
		}
	}
	return validateCodexImageSize(model, size)
}

func validateCodexImageSize(model string, size string) error {
	if size == "auto" {
		return nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return errors.New("size must be auto or WIDTHxHEIGHT, for example 1024x1024")
	}
	width, err := strconv.Atoi(parts[0])
	if err != nil || width <= 0 {
		return errors.New("size must be auto or WIDTHxHEIGHT, for example 1024x1024")
	}
	height, err := strconv.Atoi(parts[1])
	if err != nil || height <= 0 {
		return errors.New("size must be auto or WIDTHxHEIGHT, for example 1024x1024")
	}

	if !strings.Contains(strings.ToLower(model), "gpt-image-2") {
		if !isOneOf(size, "1024x1024", "1536x1024", "1024x1536") {
			return errors.New("this image model only supports 1024x1024, 1536x1024, 1024x1536, or auto")
		}
		return nil
	}

	if width%16 != 0 || height%16 != 0 {
		return errors.New("gpt-image-2 width and height must be multiples of 16")
	}
	if width > gptImage2MaxEdge || height > gptImage2MaxEdge {
		return fmt.Errorf("gpt-image-2 max edge must be <= %d", gptImage2MaxEdge)
	}
	longEdge := width
	shortEdge := height
	if height > width {
		longEdge, shortEdge = height, width
	}
	if longEdge > shortEdge*gptImage2MaxAspectRatioNumerator {
		return fmt.Errorf("gpt-image-2 long-to-short ratio must be <= %d:1", gptImage2MaxAspectRatioNumerator)
	}
	pixels := int64(width) * int64(height)
	if pixels < gptImage2MinPixels || pixels > gptImage2MaxPixels {
		return fmt.Errorf("gpt-image-2 total pixels must be between %d and %d", gptImage2MinPixels, gptImage2MaxPixels)
	}
	return nil
}

func isOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func isMultipartRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && strings.Contains(strings.ToLower(c.Request.Header.Get("Content-Type")), "multipart/form-data")
}

func populateMultipartImageFields(c *gin.Context, request *dto.ImageRequest) error {
	if c == nil || c.Request == nil {
		return errors.New("missing image request context")
	}
	form, err := c.MultipartForm()
	if err != nil {
		return fmt.Errorf("failed to parse image edit form request: %w", err)
	}

	if request.Prompt == "" {
		request.Prompt = firstFormValue(form, "prompt")
	}
	if request.Model == "" {
		request.Model = firstFormValue(form, "model")
	}
	if request.N == nil {
		if raw := firstFormValue(form, "n"); raw != "" {
			value, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return errors.New("n must be an integer")
			}
			n := uint(value)
			request.N = &n
		}
	}
	if request.Size == "" {
		request.Size = firstFormValue(form, "size")
	}
	if request.Quality == "" {
		request.Quality = firstFormValue(form, "quality")
	}
	if request.ResponseFormat == "" {
		request.ResponseFormat = firstFormValue(form, "response_format")
	}

	var errSet error
	setRawStringFromForm := func(target *json.RawMessage, key string) {
		if errSet != nil || len(*target) > 0 {
			return
		}
		value := firstFormValue(form, key)
		if value == "" {
			return
		}
		encoded, err := common.Marshal(value)
		if err != nil {
			errSet = err
			return
		}
		*target = encoded
	}
	setRawStringFromForm(&request.Background, "background")
	setRawStringFromForm(&request.Moderation, "moderation")
	setRawStringFromForm(&request.OutputFormat, "output_format")
	setRawStringFromForm(&request.User, "user")
	if errSet != nil {
		return errSet
	}
	if len(request.OutputCompression) == 0 {
		if value := firstFormValue(form, "output_compression"); value != "" {
			compression, err := strconv.Atoi(value)
			if err != nil {
				return errors.New("output_compression must be an integer")
			}
			encoded, err := common.Marshal(compression)
			if err != nil {
				return err
			}
			request.OutputCompression = encoded
		}
	}
	return nil
}

func firstFormValue(form *multipart.Form, key string) string {
	if form == nil || len(form.Value[key]) == 0 {
		return ""
	}
	return form.Value[key][0]
}

func buildCodexEditImages(c *gin.Context, request dto.ImageRequest) ([]codexImageRef, *codexImageRef, error) {
	if isMultipartRequest(c) {
		return buildMultipartCodexEditImages(c)
	}

	images, err := parseCodexImageRefs(request.Images, "images")
	if err != nil {
		return nil, nil, err
	}
	if len(images) == 0 && len(request.Image) > 0 {
		images, err = parseCodexImageRefs(request.Image, "image")
		if err != nil {
			return nil, nil, err
		}
	}
	if len(images) == 0 {
		return nil, nil, errors.New("images are required for codex image edits")
	}
	if len(images) > maxCodexInputImages {
		return nil, nil, fmt.Errorf("at most %d input images are supported", maxCodexInputImages)
	}
	for _, image := range images {
		if err := validateCodexImageDataURL(image.ImageURL); err != nil {
			return nil, nil, fmt.Errorf("invalid input image: %w", err)
		}
	}

	var mask *codexImageRef
	if len(request.Mask) > 0 {
		masks, err := parseCodexImageRefs(request.Mask, "mask")
		if err != nil {
			return nil, nil, err
		}
		if len(masks) != 1 {
			return nil, nil, errors.New("mask must contain exactly one image")
		}
		if err := validateCodexImageDataURL(masks[0].ImageURL); err != nil {
			return nil, nil, fmt.Errorf("invalid mask image: %w", err)
		}
		mask = &masks[0]
	}
	return images, mask, nil
}

func buildMultipartCodexEditImages(c *gin.Context) ([]codexImageRef, *codexImageRef, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse image edit form request: %w", err)
	}
	files := multipartImageFiles(form)
	if len(files) == 0 {
		return nil, nil, errors.New("image is required for codex image edits")
	}
	if len(files) > maxCodexInputImages {
		return nil, nil, fmt.Errorf("at most %d input images are supported", maxCodexInputImages)
	}

	images := make([]codexImageRef, 0, len(files))
	for _, file := range files {
		imageURL, err := multipartFileToDataURL(file)
		if err != nil {
			return nil, nil, err
		}
		images = append(images, codexImageRef{ImageURL: imageURL})
	}

	var mask *codexImageRef
	if form != nil && len(form.File["mask"]) > 0 {
		imageURL, err := multipartFileToDataURL(form.File["mask"][0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid mask image: %w", err)
		}
		mask = &codexImageRef{ImageURL: imageURL}
	}
	return images, mask, nil
}

func multipartImageFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil || form.File == nil {
		return nil
	}
	if files := form.File["image"]; len(files) > 0 {
		return files
	}
	if files := form.File["image[]"]; len(files) > 0 {
		return files
	}
	keys := make([]string, 0)
	for key := range form.File {
		if strings.HasPrefix(key, "image[") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var files []*multipart.FileHeader
	for _, key := range keys {
		files = append(files, form.File[key]...)
	}
	return files
}

func multipartFileToDataURL(header *multipart.FileHeader) (string, error) {
	if header == nil {
		return "", errors.New("image file is missing")
	}
	file, err := header.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open image %q: %w", header.Filename, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxCodexImageDataURLChars+1))
	if err != nil {
		return "", fmt.Errorf("failed to read image %q: %w", header.Filename, err)
	}
	if len(data) > maxCodexImageDataURLChars {
		return "", fmt.Errorf("input image %q is too large", header.Filename)
	}
	mimeType, err := detectCodexImageMIME(header.Filename, data)
	if err != nil {
		return "", err
	}
	prefix := "data:" + mimeType + ";base64,"
	if len(prefix)+base64.StdEncoding.EncodedLen(len(data)) > maxCodexImageDataURLChars {
		return "", fmt.Errorf("input image %q exceeds data URL limit", header.Filename)
	}
	return prefix + base64.StdEncoding.EncodeToString(data), nil
}

func detectCodexImageMIME(filename string, data []byte) (string, error) {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", nil
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if semicolon := strings.IndexByte(mimeType, ';'); semicolon >= 0 {
		mimeType = mimeType[:semicolon]
	}
	if isOneOf(mimeType, "image/png", "image/jpeg", "image/webp") {
		return mimeType, nil
	}
	return "", fmt.Errorf("input image must be PNG, JPEG, or WebP: %s", filename)
}

func parseCodexImageRefs(raw json.RawMessage, field string) ([]codexImageRef, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var refs []codexImageRef
	if err := common.Unmarshal(raw, &refs); err == nil && len(refs) > 0 {
		return refs, nil
	}
	var ref codexImageRef
	if err := common.Unmarshal(raw, &ref); err == nil && ref.ImageURL != "" {
		return []codexImageRef{ref}, nil
	}
	var values []string
	if err := common.Unmarshal(raw, &values); err == nil && len(values) > 0 {
		refs = make([]codexImageRef, 0, len(values))
		for _, value := range values {
			refs = append(refs, codexImageRef{ImageURL: value})
		}
		return refs, nil
	}
	var value string
	if err := common.Unmarshal(raw, &value); err == nil && value != "" {
		return []codexImageRef{{ImageURL: value}}, nil
	}
	return nil, fmt.Errorf("%s must be an image reference or an array of image references", field)
}

func validateCodexImageDataURL(value string) error {
	if len(value) > maxCodexImageDataURLChars {
		return errors.New("image data URL exceeds size limit")
	}
	comma := strings.IndexByte(value, ',')
	if comma <= 0 {
		return errors.New("image must be a base64 data URL")
	}
	header := value[:comma]
	if !isOneOf(header, "data:image/png;base64", "data:image/jpeg;base64", "data:image/webp;base64") {
		return errors.New("image data URL must contain PNG, JPEG, or WebP data")
	}
	if value[comma+1:] == "" {
		return errors.New("image data URL payload is empty")
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(value[comma+1:]))
	if _, err := io.Copy(io.Discard, decoder); err != nil {
		return errors.New("image data URL payload is not valid base64")
	}
	return nil
}

func applyImageRequestHeaders(req *http.Header, info *relaycommon.RelayInfo) {
	if req == nil || info == nil || !isImageRelayMode(info.RelayMode) {
		return
	}
	req.Del("OpenAI-Beta")
	req.Set("Content-Type", "application/json")
	req.Set("Accept", "application/json")
}

func validateImagePassThrough(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil || !isImageRelayMode(info.RelayMode) || !isMultipartRequest(c) {
		return nil
	}
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		return errors.New("codex image edits do not support multipart request pass-through; disable pass-through so files can be converted to JSON data URLs")
	}
	return nil
}
