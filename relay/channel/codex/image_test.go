package codex

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	appconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertImageGenerationRequest(t *testing.T) {
	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/generations", "application/json", nil)
	outputFormat := mustCodexImageJSON(t, "webp")
	compression := mustCodexImageJSON(t, 0)
	user := mustCodexImageJSON(t, "user-1")

	converted, err := convertImageRequest(c, codexImageRelayInfo(relayconstant.RelayModeImagesGenerations), dto.ImageRequest{
		Model:             "gpt-image-2",
		Prompt:            "a red circle",
		Size:              "1024x1024",
		Quality:           "low",
		OutputFormat:      outputFormat,
		OutputCompression: compression,
		User:              user,
	})
	require.NoError(t, err)

	request, ok := converted.(*codexImageRequest)
	require.True(t, ok)
	require.Equal(t, uint(1), request.N)
	require.Equal(t, "webp", request.OutputFormat)
	require.NotNil(t, request.OutputCompression)
	require.Equal(t, 0, *request.OutputCompression)
	require.Equal(t, defaultCodexImageBackground, request.Background)
	require.Equal(t, defaultCodexImageModeration, request.Moderation)

	data, err := common.Marshal(request)
	require.NoError(t, err)
	require.Contains(t, string(data), `"output_compression":0`)
	require.NotContains(t, string(data), "response_format")
}

func TestConvertImageGenerationRejectsURLResponse(t *testing.T) {
	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/generations", "application/json", nil)
	_, err := (&Adaptor{}).ConvertImageRequest(c, codexImageRelayInfo(relayconstant.RelayModeImagesGenerations), dto.ImageRequest{
		Model:          "gpt-image-2",
		Prompt:         "a red circle",
		ResponseFormat: "url",
	})
	require.ErrorContains(t, err, "only supports response_format=b64_json")
	var newAPIError *types.NewAPIError
	require.ErrorAs(t, err, &newAPIError)
	require.Equal(t, http.StatusBadRequest, newAPIError.StatusCode)
	require.True(t, types.IsSkipRetryError(newAPIError))
}

func TestValidateCodexImageSize(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		size    string
		wantErr bool
	}{
		{name: "auto", model: "gpt-image-2", size: "auto"},
		{name: "square", model: "gpt-image-2", size: "1024x1024"},
		{name: "wide", model: "gpt-image-2", size: "1536x1024"},
		{name: "custom", model: "gpt-image-2", size: "1920x1088"},
		{name: "not multiple of sixteen", model: "gpt-image-2", size: "1000x1000", wantErr: true},
		{name: "edge too long", model: "gpt-image-2", size: "4096x1024", wantErr: true},
		{name: "ratio too large", model: "gpt-image-2", size: "3072x800", wantErr: true},
		{name: "too few pixels", model: "gpt-image-2", size: "512x512", wantErr: true},
		{name: "legacy accepted", model: "gpt-image-1", size: "1536x1024"},
		{name: "legacy rejected", model: "gpt-image-1", size: "1920x1088", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCodexImageSize(test.model, test.size)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConvertMultipartImageEditRequest(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "make it blue"))
	require.NoError(t, writer.WriteField("size", "1024x1024"))
	require.NoError(t, writer.WriteField("quality", "low"))
	require.NoError(t, writer.WriteField("output_format", "webp"))
	require.NoError(t, writer.WriteField("output_compression", "0"))
	require.NoError(t, writer.WriteField("background", "opaque"))
	require.NoError(t, writer.WriteField("moderation", "low"))
	require.NoError(t, writer.WriteField("response_format", "b64_json"))
	require.NoError(t, writer.WriteField("user", "user-2"))

	imagePart, err := writer.CreateFormFile("image[]", "input.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("\x89PNG\r\n\x1a\nimage"))
	require.NoError(t, err)
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("\x89PNG\r\n\x1a\nmask"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/edits", writer.FormDataContentType(), &body)
	converted, err := convertImageRequest(c, codexImageRelayInfo(relayconstant.RelayModeImagesEdits), dto.ImageRequest{})
	require.NoError(t, err)
	request := converted.(*codexImageRequest)
	require.Equal(t, "gpt-image-2", request.Model)
	require.Equal(t, "make it blue", request.Prompt)
	require.Equal(t, "webp", request.OutputFormat)
	require.Equal(t, "opaque", request.Background)
	require.Equal(t, "low", request.Moderation)
	require.NotNil(t, request.OutputCompression)
	require.Equal(t, 0, *request.OutputCompression)
	require.Len(t, request.Images, 1)
	require.True(t, strings.HasPrefix(request.Images[0].ImageURL, "data:image/png;base64,"))
	require.NotNil(t, request.Mask)
	require.True(t, strings.HasPrefix(request.Mask.ImageURL, "data:image/png;base64,"))

	var user string
	require.NoError(t, common.Unmarshal(request.User, &user))
	require.Equal(t, "user-2", user)
}

func TestConvertJSONImageEditRequest(t *testing.T) {
	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/edits", "application/json", nil)
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	converted, err := convertImageRequest(c, codexImageRelayInfo(relayconstant.RelayModeImagesEdits), dto.ImageRequest{
		Model:  "gpt-image-2",
		Prompt: "edit this image",
		Images: mustCodexImageJSON(t, []map[string]string{{"image_url": dataURL}}),
		Mask:   mustCodexImageJSON(t, map[string]string{"image_url": dataURL}),
	})
	require.NoError(t, err)
	request := converted.(*codexImageRequest)
	require.Len(t, request.Images, 1)
	require.Equal(t, dataURL, request.Images[0].ImageURL)
	require.NotNil(t, request.Mask)
	require.Equal(t, dataURL, request.Mask.ImageURL)
}

func TestApplyImageRequestHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("OpenAI-Beta", "responses=experimental")
	header.Set("Accept", "text/event-stream")
	header.Set("Content-Type", "multipart/form-data")
	applyImageRequestHeaders(&header, codexImageRelayInfo(relayconstant.RelayModeImagesEdits))
	require.Empty(t, header.Get("OpenAI-Beta"))
	require.Equal(t, "application/json", header.Get("Accept"))
	require.Equal(t, "application/json", header.Get("Content-Type"))
}

func TestSetupRequestHeaderForImage(t *testing.T) {
	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/generations", "application/json; charset=utf-8", nil)
	info := codexImageRelayInfo(relayconstant.RelayModeImagesGenerations)
	info.ApiKey = string(mustCodexImageJSON(t, map[string]string{
		"access_token": "access-token",
		"account_id":   "account-id",
	}))
	info.ForceUpstreamStream = true
	header := http.Header{}

	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	require.Equal(t, "Bearer access-token", header.Get("Authorization"))
	require.Equal(t, "account-id", header.Get("chatgpt-account-id"))
	require.Equal(t, "application/json", header.Get("Content-Type"))
	require.Equal(t, "application/json", header.Get("Accept"))
	require.Empty(t, header.Get("OpenAI-Beta"))
}

func TestDoImageResponsePreservesBodyAndNormalizesUsage(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	responseBody := `{"data":[{"b64_json":"aGVsbG8="}],"usage":{"input_tokens":17,"input_tokens_details":{"image_tokens":0,"text_tokens":17},"output_tokens":229,"total_tokens":246}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}

	usageAny, newAPIError := (&Adaptor{}).DoResponse(c, resp, codexImageRelayInfo(relayconstant.RelayModeImagesGenerations))
	require.Nil(t, newAPIError)
	usage, ok := usageAny.(*dto.Usage)
	require.True(t, ok)
	require.Equal(t, 17, usage.PromptTokens)
	require.Equal(t, 229, usage.CompletionTokens)
	require.Equal(t, 246, usage.TotalTokens)
	require.Equal(t, 17, usage.PromptTokensDetails.TextTokens)
	require.JSONEq(t, responseBody, recorder.Body.String())
}

func TestCodexImageRequestURLAndModelList(t *testing.T) {
	adaptor := &Adaptor{}
	info := codexImageRelayInfo(relayconstant.RelayModeImagesGenerations)
	info.ChannelBaseUrl = "https://chatgpt.com"
	info.ChannelType = appconstant.ChannelTypeCodex
	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://chatgpt.com/backend-api/codex/images/generations", url)
	require.Contains(t, adaptor.GetModelList(), "gpt-image-2")
	require.NotContains(t, adaptor.GetModelList(), "gpt-image-2-compact")
}

func TestValidateMultipartImagePassThrough(t *testing.T) {
	c := newCodexImageTestContext(t, http.MethodPost, "/v1/images/edits", "multipart/form-data; boundary=test", nil)
	info := codexImageRelayInfo(relayconstant.RelayModeImagesEdits)
	info.ChannelSetting.PassThroughBodyEnabled = true
	require.ErrorContains(t, validateImagePassThrough(c, info), "disable pass-through")
}

func mustCodexImageJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}

func newCodexImageTestContext(t *testing.T, method string, path string, contentType string, body *bytes.Buffer) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		requestBody = bytes.NewReader(body.Bytes())
	}
	c.Request = httptest.NewRequest(method, path, requestBody)
	c.Request.Header.Set("Content-Type", contentType)
	return c
}

func codexImageRelayInfo(relayMode int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayMode: relayMode,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{},
		},
	}
}
