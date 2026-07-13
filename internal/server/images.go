package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/translate"
)

const defaultImageModel = "gpt-image-2"

type imageGenerationRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 *int   `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	ResponseFormat    string `json:"response_format,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	PartialImages     *int   `json:"partial_images,omitempty"`
	Stream            bool   `json:"stream,omitempty"`
}

type imageReference struct {
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

type imageEditRequest struct {
	imageGenerationRequest
	Images        []imageReference `json:"images"`
	Mask          *imageReference  `json:"mask,omitempty"`
	InputFidelity string           `json:"input_fidelity,omitempty"`
}

type imageResult struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type imagesResponse struct {
	Background   string         `json:"background,omitempty"`
	Created      int64          `json:"created"`
	Data         []imageResult  `json:"data"`
	OutputFormat string         `json:"output_format,omitempty"`
	Quality      string         `json:"quality,omitempty"`
	Size         string         `json:"size,omitempty"`
	Usage        map[string]any `json:"usage,omitempty"`
}

func (a *App) handleImageGenerations(c *gin.Context) {
	body, err := readRequestBody(c.Request)
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	var req imageGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		a.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required", "invalid_request_error")
		return
	}
	if !a.validateImageModel(c, req.Model) {
		return
	}
	req.Model = resolvedImageModel(req.Model)
	directPayload, err := prepareDirectImagePayload(body, req.Model, req.Stream)
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	if a.handleDirectImageResponse(c, "images_generations", "/codex/images/generations", directPayload, req.Stream) {
		return
	}

	normalized := normalizedImageRequest(req)
	a.handleImageResponse(c, "images_generations", "image_generation", req.ResponseFormat, normalized)
}

func (a *App) handleImageEdits(c *gin.Context) {
	req, directPayload, cleanup, err := decodeImageEditRequest(c.Request)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		a.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required", "invalid_request_error")
		return
	}
	if !a.validateImageModel(c, req.Model) {
		return
	}
	if len(req.Images) == 0 {
		a.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", "images are required", "invalid_request_error")
		return
	}
	req.Model = resolvedImageModel(req.Model)
	directPayload, err = prepareDirectImagePayload(directPayload, req.Model, req.Stream)
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	if a.handleDirectImageResponse(c, "images_edits", "/codex/images/edits", directPayload, req.Stream) {
		return
	}

	normalized := normalizedImageEditRequest(req)
	a.handleImageResponse(c, "images_edits", "image_edit", req.ResponseFormat, normalized)
}

func (a *App) validateImageModel(c *gin.Context, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || isCodexImageModel(model) {
		return true
	}
	a.writeOpenAIError(c, http.StatusNotFound, "model_not_found", "Model '"+model+"' not found", "invalid_request_error")
	return false
}

func decodeImageEditRequest(req *http.Request) (imageEditRequest, []byte, func(), error) {
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		body, err := readRequestBody(req)
		if err != nil {
			return imageEditRequest{}, nil, nil, err
		}
		var decoded imageEditRequest
		err = json.Unmarshal(body, &decoded)
		return decoded, body, nil, err
	}

	if err := req.ParseMultipartForm(32 << 20); err != nil {
		return imageEditRequest{}, nil, nil, err
	}
	cleanup := func() {
		if req.MultipartForm != nil {
			_ = req.MultipartForm.RemoveAll()
		}
	}
	form := req.MultipartForm
	decoded := imageEditRequest{imageGenerationRequest: imageGenerationRequest{
		Model:             multipartValue(form, "model"),
		Prompt:            multipartValue(form, "prompt"),
		N:                 multipartInt(form, "n"),
		Size:              multipartValue(form, "size"),
		Quality:           multipartValue(form, "quality"),
		Background:        multipartValue(form, "background"),
		OutputFormat:      multipartValue(form, "output_format"),
		ResponseFormat:    multipartValue(form, "response_format"),
		Moderation:        multipartValue(form, "moderation"),
		OutputCompression: multipartInt(form, "output_compression"),
		PartialImages:     multipartInt(form, "partial_images"),
		Stream:            multipartBool(form, "stream"),
	}, InputFidelity: multipartValue(form, "input_fidelity")}

	files := form.File["image[]"]
	if len(files) == 0 {
		files = form.File["image"]
	}
	for _, file := range files {
		dataURL, err := multipartImageDataURL(file)
		if err != nil {
			cleanup()
			return imageEditRequest{}, nil, nil, err
		}
		decoded.Images = append(decoded.Images, imageReference{ImageURL: dataURL})
	}
	if masks := form.File["mask"]; len(masks) > 0 {
		dataURL, err := multipartImageDataURL(masks[0])
		if err != nil {
			cleanup()
			return imageEditRequest{}, nil, nil, err
		}
		decoded.Mask = &imageReference{ImageURL: dataURL}
	}
	body, err := json.Marshal(decoded)
	if err != nil {
		cleanup()
		return imageEditRequest{}, nil, nil, err
	}
	return decoded, body, cleanup, nil
}

func resolvedImageModel(model string) string {
	if model = strings.TrimSpace(model); model != "" {
		return model
	}
	return defaultImageModel
}

func prepareDirectImagePayload(body []byte, model string, stream bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	payload["model"] = mustRawJSON(model)
	if stream {
		payload["stream"] = json.RawMessage("true")
	} else {
		delete(payload, "stream")
	}
	return json.Marshal(payload)
}

func (a *App) handleDirectImageResponse(c *gin.Context, endpoint, path string, payload []byte, stream bool) bool {
	account, response, err := a.openDirectImageWithFailover(c.Request.Context(), c, endpoint, path, payload, stream)
	if err != nil {
		if directImageEndpointUnavailable(err) {
			return false
		}
		a.setRequestAccount(c, account)
		a.handleOpenStreamError(c, endpoint, account.ID, account.ID, err)
		return true
	}
	defer response.Close()

	a.setRequestAccount(c, account)
	a.observeQuotaSnapshot(account.ID, codex.ParseQuotaFromHeaders(response.Headers))
	contentType := strings.TrimSpace(response.Headers.Get("Content-Type"))
	if stream {
		prepareStreamResponse(c)
		if contentType != "" {
			c.Header("Content-Type", contentType)
		}
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				_, _ = c.Writer.Write(buffer[:n])
				c.Writer.Flush()
			}
			if readErr != nil {
				if readErr != io.EOF {
					a.respondStreamError(c, endpoint, account.ID, "", "error", readErr)
					return true
				}
				break
			}
		}
	} else {
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			a.handleOpenStreamError(c, endpoint, account.ID, account.ID, readErr)
			return true
		}
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(http.StatusOK, contentType, body)
	}
	a.accounts.NoteSuccess(account.ID)
	return true
}

func (a *App) openDirectImageWithFailover(ctx context.Context, c *gin.Context, endpoint, path string, payload []byte, stream bool) (accounts.Record, *codex.RawImageResponse, error) {
	attempted := make(map[string]struct{})
	var lastAccount accounts.Record
	var lastErr error
	for {
		account, err := a.accounts.AcquireMatching("", func(record accounts.Record) bool {
			_, alreadyAttempted := attempted[record.ID]
			return !alreadyAttempted
		})
		if err != nil {
			if lastErr != nil && strings.Contains(strings.ToLower(err.Error()), "no active accounts") {
				return lastAccount, nil, lastErr
			}
			return account, nil, err
		}
		selected := account
		account, err = a.accountMgr.EnsureReady(ctx, selected.ID)
		if err != nil {
			account = selected
		}
		if err == nil {
			a.logUpstreamPayload(c, endpoint, "http", account.ID, json.RawMessage(payload))
			open := a.directImageOpen
			if open == nil {
				open = a.httpClient.OpenImage
			}
			var response *codex.RawImageResponse
			response, err = open(ctx, account, path, payload, stream)
			if err == nil {
				return account, response, nil
			}
		}
		if directImageEndpointUnavailable(err) || !shouldFailoverRequest(err) {
			return account, nil, err
		}
		attempted[account.ID] = struct{}{}
		lastAccount = account
		lastErr = err
		a.classifyUpstreamError(account.ID, err)
	}
}

func directImageEndpointUnavailable(err error) bool {
	var upstreamErr *codex.UpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	switch upstreamErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func multipartValue(form *multipart.Form, key string) string {
	if form == nil || len(form.Value[key]) == 0 {
		return ""
	}
	return strings.TrimSpace(form.Value[key][0])
}

func multipartInt(form *multipart.Form, key string) *int {
	value := multipartValue(form, key)
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func multipartBool(form *multipart.Form, key string) bool {
	parsed, _ := strconv.ParseBool(multipartValue(form, key))
	return parsed
}

func multipartImageDataURL(file *multipart.FileHeader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("image upload is missing")
	}
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, 50<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 50<<20 {
		return "", fmt.Errorf("image upload exceeds 50MB")
	}
	contentType := strings.TrimSpace(file.Header.Get("Content-Type"))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func normalizedImageRequest(req imageGenerationRequest) translate.NormalizedRequest {
	tool := newImageTool(req.Model, "generate")
	appendImageToolOptions(&tool, req)

	return normalizedImageToolRequest(req.Prompt, nil, tool, req.Stream)
}

func normalizedImageEditRequest(req imageEditRequest) translate.NormalizedRequest {
	tool := newImageTool(req.Model, "edit")
	appendImageToolOptions(&tool, req.imageGenerationRequest)
	appendImageToolString(&tool, "input_fidelity", req.InputFidelity)
	if req.Mask != nil {
		mask := map[string]any{}
		if value := strings.TrimSpace(req.Mask.ImageURL); value != "" {
			mask["image_url"] = value
		}
		if value := strings.TrimSpace(req.Mask.FileID); value != "" {
			mask["file_id"] = value
		}
		if len(mask) > 0 {
			tool.ExtraFields["input_image_mask"] = mustRawJSON(mask)
		}
	}

	images := make([]codex.ContentPart, 0, len(req.Images))
	for _, reference := range req.Images {
		part := codex.ContentPart{Type: "input_image"}
		part.ImageURL = strings.TrimSpace(reference.ImageURL)
		part.FileID = strings.TrimSpace(reference.FileID)
		if part.ImageURL != "" || part.FileID != "" {
			images = append(images, part)
		}
	}
	return normalizedImageToolRequest(req.Prompt, images, tool, req.Stream)
}

func newImageTool(model, action string) openai.ToolDefinition {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultImageModel
	}
	return openai.ToolDefinition{
		Type: "image_generation",
		ExtraFields: map[string]json.RawMessage{
			"action": mustRawJSON(action),
			"model":  mustRawJSON(model),
		},
	}
}

func normalizedImageToolRequest(prompt string, images []codex.ContentPart, tool openai.ToolDefinition, stream bool) translate.NormalizedRequest {
	content := make([]codex.ContentPart, 0, 1+len(images))
	content = append(content, codex.ContentPart{Type: "input_text", Text: strings.TrimSpace(prompt)})
	content = append(content, images...)
	return translate.NormalizedRequest{Request: codex.Request{
		Instructions: "You are a helpful assistant.",
		Stream:       stream,
		Input: []codex.InputItem{{
			Role:    "user",
			Content: content,
		}},
		Tools:      []codex.Tool{tool},
		ToolChoice: json.RawMessage(`{"type":"image_generation"}`),
	}}
}

func appendImageToolOptions(tool *openai.ToolDefinition, req imageGenerationRequest) {
	appendImageToolString(tool, "size", req.Size)
	appendImageToolString(tool, "quality", req.Quality)
	appendImageToolString(tool, "background", req.Background)
	appendImageToolString(tool, "output_format", req.OutputFormat)
	appendImageToolString(tool, "moderation", req.Moderation)
	appendImageToolInt(tool, "output_compression", req.OutputCompression)
	appendImageToolInt(tool, "partial_images", req.PartialImages)
}

func appendImageToolString(tool *openai.ToolDefinition, key, value string) {
	value = strings.TrimSpace(value)
	if tool == nil || value == "" {
		return
	}
	tool.ExtraFields[key] = mustRawJSON(value)
}

func appendImageToolInt(tool *openai.ToolDefinition, key string, value *int) {
	if tool == nil || value == nil {
		return
	}
	tool.ExtraFields[key] = mustRawJSON(*value)
}

func mustRawJSON(value any) json.RawMessage {
	payload, _ := json.Marshal(value)
	return payload
}

func (a *App) handleImageResponse(c *gin.Context, endpoint, streamPrefix, responseFormat string, normalized translate.NormalizedRequest) {
	if normalized.Stream {
		a.streamImageResponse(c, endpoint, streamPrefix, responseFormat, normalized)
		return
	}
	a.collectImageResponse(c, endpoint, responseFormat, normalized)
}

func (a *App) collectImageResponse(c *gin.Context, endpoint, responseFormat string, normalized translate.NormalizedRequest) {
	opened, ok := a.openImageRequest(c, endpoint, normalized)
	if !ok {
		return
	}
	defer opened.Stream.Close()

	accumulator := translate.NewAccumulator(opened.Resolution.Request)
	for {
		event, _, err := a.nextStreamEvent(opened.Account, accumulator, opened.Stream)
		if err != nil {
			if err == io.EOF {
				err = errIncompleteResponse
			}
			a.respondOpenAIUpstreamStreamError(c, endpoint, opened.Account.ID, accumulator.ResponseID, err)
			return
		}
		if event.Type == "response.completed" {
			break
		}
	}
	if !accumulator.IsCompleted() {
		a.respondOpenAIUpstreamStreamError(c, endpoint, opened.Account.ID, accumulator.ResponseID, errIncompleteResponse)
		return
	}

	results := imageResultsFromAccumulator(accumulator, responseFormat)
	if len(results) == 0 {
		a.writeOpenAIError(c, http.StatusBadGateway, "image_generation_failed", "upstream did not return image output", "api_error")
		return
	}
	metadata := imageMetadataFromAccumulator(accumulator)
	a.accounts.NoteSuccess(opened.Account.ID)
	c.JSON(http.StatusOK, imagesResponse{
		Background:   metadata.Background,
		Created:      imageCreatedAt(accumulator),
		Data:         results,
		OutputFormat: metadata.OutputFormat,
		Quality:      metadata.Quality,
		Size:         metadata.Size,
		Usage:        imageUsage(accumulator),
	})
}

func (a *App) streamImageResponse(c *gin.Context, endpoint, streamPrefix, responseFormat string, normalized translate.NormalizedRequest) {
	opened, ok := a.openImageRequest(c, endpoint, normalized)
	if !ok {
		return
	}
	defer opened.Stream.Close()
	prepareStreamResponse(c)

	accumulator := translate.NewAccumulator(opened.Resolution.Request)
	for {
		event, upstreamErr, err := a.nextStreamEvent(opened.Account, accumulator, opened.Stream)
		if err != nil {
			if err == io.EOF {
				err = errIncompleteResponse
			}
			if upstreamErr {
				a.respondClassifiedStreamError(c, endpoint, opened.Account.ID, accumulator.ResponseID, "error", err)
			} else {
				a.respondStreamError(c, endpoint, opened.Account.ID, accumulator.ResponseID, "error", err)
			}
			return
		}

		if event.Type == "response.image_generation_call.partial_image" {
			payload := imagePartialEventPayload(event, streamPrefix, responseFormat)
			if payload != nil {
				writeSSE(c.Writer, streamPrefix+".partial_image", translate.MustJSON(payload))
				c.Writer.Flush()
			}
		}
		if event.Type != "response.completed" {
			continue
		}

		results := imageResultsFromAccumulator(accumulator, responseFormat)
		if len(results) == 0 {
			a.respondStreamError(c, endpoint, opened.Account.ID, accumulator.ResponseID, "error", io.ErrUnexpectedEOF)
			return
		}
		for _, result := range results {
			payload := map[string]any{"type": streamPrefix + ".completed"}
			if result.URL != "" {
				payload["url"] = result.URL
			} else {
				payload["b64_json"] = result.B64JSON
			}
			if usage := imageUsage(accumulator); len(usage) > 0 {
				payload["usage"] = usage
			}
			writeSSE(c.Writer, streamPrefix+".completed", translate.MustJSON(payload))
			c.Writer.Flush()
		}
		a.accounts.NoteSuccess(opened.Account.ID)
		return
	}
}

func (a *App) openImageRequest(c *gin.Context, endpoint string, normalized translate.NormalizedRequest) (openedRequest, bool) {
	if a.imageOpener != nil {
		return a.imageOpener(c, endpoint, normalized)
	}
	return a.resolveAndOpenRequest(c, endpoint, normalized)
}

func imageResultsFromAccumulator(accumulator *translate.Accumulator, responseFormat string) []imageResult {
	if accumulator == nil {
		return nil
	}
	response := accumulator.ResponsesObject()
	output := jsonutil.SliceOfMaps(response["output"])
	results := make([]imageResult, 0, len(output))
	for _, item := range output {
		if jsonutil.StringValue(item["type"]) != "image_generation_call" {
			continue
		}
		result := strings.TrimSpace(jsonutil.StringValue(item["result"]))
		if result == "" {
			continue
		}
		image := imageResult{
			RevisedPrompt: strings.TrimSpace(jsonutil.StringValue(item["revised_prompt"])),
		}
		if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
			image.URL = "data:" + imageMIMEType(jsonutil.StringValue(item["output_format"])) + ";base64," + result
		} else {
			image.B64JSON = result
		}
		results = append(results, image)
	}
	return results
}

type imageMetadata struct {
	Background   string
	OutputFormat string
	Quality      string
	Size         string
}

func imageMetadataFromAccumulator(accumulator *translate.Accumulator) imageMetadata {
	if accumulator == nil {
		return imageMetadata{}
	}
	for _, item := range jsonutil.SliceOfMaps(accumulator.ResponsesObject()["output"]) {
		if jsonutil.StringValue(item["type"]) == "image_generation_call" {
			return imageMetadata{
				Background:   strings.TrimSpace(jsonutil.StringValue(item["background"])),
				OutputFormat: strings.TrimSpace(jsonutil.StringValue(item["output_format"])),
				Quality:      strings.TrimSpace(jsonutil.StringValue(item["quality"])),
				Size:         strings.TrimSpace(jsonutil.StringValue(item["size"])),
			}
		}
	}
	return imageMetadata{}
}

func imagePartialEventPayload(event *codex.StreamEvent, streamPrefix, responseFormat string) map[string]any {
	if event == nil {
		return nil
	}
	result := strings.TrimSpace(jsonutil.StringValue(event.Raw["partial_image_b64"]))
	if result == "" {
		return nil
	}
	payload := map[string]any{
		"type":                streamPrefix + ".partial_image",
		"partial_image_index": 0,
	}
	if index, ok := serverIntValue(event.Raw["partial_image_index"]); ok {
		payload["partial_image_index"] = index
	}
	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		payload["url"] = "data:" + imageMIMEType(jsonutil.StringValue(event.Raw["output_format"])) + ";base64," + result
	} else {
		payload["b64_json"] = result
	}
	return payload
}

func imageMIMEType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func imageCreatedAt(accumulator *translate.Accumulator) int64 {
	if accumulator != nil {
		response := jsonutil.MapValue(accumulator.RawFinal, "response")
		if created, ok := serverIntValue(response["created_at"]); ok && created > 0 {
			return int64(created)
		}
	}
	return time.Now().UTC().Unix()
}

func imageUsage(accumulator *translate.Accumulator) map[string]any {
	if accumulator == nil {
		return nil
	}
	response := jsonutil.MapValue(accumulator.RawFinal, "response")
	toolUsage := jsonutil.MapValue(response, "tool_usage")
	if usage := jsonutil.MapValue(toolUsage, "image_gen"); len(usage) > 0 {
		return jsonutil.CloneMap(usage)
	}
	return accumulator.ResponsesUsageObject()
}
