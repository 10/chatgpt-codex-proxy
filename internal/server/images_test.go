package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accountmanager"
	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

func TestImagesGenerationsUsesDirectCodexEndpoint(t *testing.T) {
	t.Parallel()

	app := newImagesTestApp(t, func(turn.NormalizedRequest) eventStream {
		t.Fatal("tool fallback should not be used")
		return nil
	})
	var gotPath string
	var gotPayload []byte
	var gotStream bool
	app.directImageOpen = func(_ context.Context, _ accounts.Record, path string, payload []byte, stream bool) (*http.Response, error) {
		gotPath = path
		gotPayload = append([]byte(nil), payload...)
		gotStream = stream
		return &http.Response{
			Body:   io.NopCloser(strings.NewReader(`{"created":123,"data":[{"url":"https://example.com/image.png"}]}`)),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{
		"model":"gpt-image-2",
		"prompt":"A blue circle",
		"n":2,
		"response_format":"url"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/codex/images/generations" || gotStream {
		t.Fatalf("direct call = path %q stream %v", gotPath, gotStream)
	}
	var upstream map[string]any
	if err := json.Unmarshal(gotPayload, &upstream); err != nil {
		t.Fatalf("decode direct payload: %v", err)
	}
	if upstream["model"] != "gpt-image-2" || upstream["n"] != float64(2) || upstream["response_format"] != "url" {
		t.Fatalf("direct payload = %#v", upstream)
	}
	if recorder.Body.String() != `{"created":123,"data":[{"url":"https://example.com/image.png"}]}` {
		t.Fatalf("response body = %s", recorder.Body.String())
	}
}

func TestImagesGenerationsStreamsDirectCodexResponse(t *testing.T) {
	t.Parallel()

	app := newImagesTestApp(t, nil)
	const upstream = "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"partial\"}\n\n" +
		"event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"final\"}\n\n"
	var gotPayload []byte
	app.directImageOpen = func(_ context.Context, _ accounts.Record, path string, payload []byte, stream bool) (*http.Response, error) {
		if path != "/codex/images/generations" || !stream {
			t.Fatalf("direct call = path %q stream %v", path, stream)
		}
		gotPayload = append([]byte(nil), payload...)
		return &http.Response{
			Body:   io.NopCloser(strings.NewReader(upstream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}, nil
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"prompt":"A blue circle","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Body.String() != upstream {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(gotPayload, &payload); err != nil {
		t.Fatalf("decode direct payload: %v", err)
	}
	if payload["model"] != defaultImageModel || payload["stream"] != true {
		t.Fatalf("direct payload = %#v", payload)
	}
}

func TestImagesEditsConvertsMultipartForDirectCodexEndpoint(t *testing.T) {
	t.Parallel()

	app := newImagesTestApp(t, nil)
	var gotPayload []byte
	app.directImageOpen = func(_ context.Context, _ accounts.Record, path string, payload []byte, stream bool) (*http.Response, error) {
		if path != "/codex/images/edits" || stream {
			t.Fatalf("direct call = path %q stream %v", path, stream)
		}
		gotPayload = append([]byte(nil), payload...)
		return &http.Response{
			Body:   io.NopCloser(strings.NewReader(`{"created":123,"data":[{"b64_json":"edited"}]}`)),
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "Turn it green")
	_ = writer.WriteField("n", "2")
	imagePart, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = imagePart.Write([]byte("input-png"))
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = maskPart.Write([]byte("mask-png"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		N      int              `json:"n"`
		Images []imageReference `json:"images"`
		Mask   *imageReference  `json:"mask"`
	}
	if err := json.Unmarshal(gotPayload, &payload); err != nil {
		t.Fatalf("decode direct payload: %v", err)
	}
	if payload.N != 2 || len(payload.Images) != 1 || !strings.HasPrefix(payload.Images[0].ImageURL, "data:") {
		t.Fatalf("direct edit payload = %#v", payload)
	}
	if payload.Mask == nil || !strings.HasPrefix(payload.Mask.ImageURL, "data:") {
		t.Fatalf("direct edit mask = %#v", payload.Mask)
	}
}

func TestImagesGenerationsReturnsOpenAIImageResponse(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return &fakeEventStream{events: []*codex.StreamEvent{
			{
				Type: "response.output_item.done",
				Raw: map[string]any{
					"output_index": 0,
					"item": map[string]any{
						"type": "image_generation_call", "id": "ig_1", "result": "cG5n",
						"background": "opaque", "output_format": "png", "size": "1024x1024", "quality": "low", "revised_prompt": "A blue circle",
					},
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{"response": map[string]any{
					"id": "resp_image_1", "model": "gpt-5.6-sol", "created_at": 123,
					"status": "completed", "output": []any{},
					"tool_usage": map[string]any{"image_gen": map[string]any{"total_tokens": 9}},
				}},
			},
		}}
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{
		"model":"gpt-image-2",
		"prompt":"A blue circle",
		"size":"1024x1024",
		"quality":"low"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Background   string `json:"background"`
		Created      int64  `json:"created"`
		OutputFormat string `json:"output_format"`
		Quality      string `json:"quality"`
		Size         string `json:"size"`
		Data         []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Created != 123 || len(response.Data) != 1 {
		t.Fatalf("response = %#v, want one image created at 123", response)
	}
	if response.Background != "opaque" || response.OutputFormat != "png" || response.Quality != "low" || response.Size != "1024x1024" {
		t.Fatalf("image metadata = %#v", response)
	}
	if response.Data[0].B64JSON != "cG5n" || response.Data[0].RevisedPrompt != "A blue circle" {
		t.Fatalf("data[0] = %#v", response.Data[0])
	}
	if response.Usage["total_tokens"] != float64(9) {
		t.Fatalf("usage = %#v", response.Usage)
	}

	encodedRequest, err := json.Marshal(upstreamRequest)
	if err != nil {
		t.Fatalf("marshal upstream request: %v", err)
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(encodedRequest, &upstreamBody); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	tools, _ := upstreamBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("upstream tools = %#v, want one image_generation tool", upstreamBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "image_generation" || tool["action"] != "generate" || tool["model"] != "gpt-image-2" {
		t.Fatalf("upstream tool = %#v", tool)
	}
	input, _ := upstreamBody["input"].([]any)
	message, _ := input[0].(map[string]any)
	if message["content"] != "A blue circle" {
		t.Fatalf("upstream input = %#v", upstreamBody["input"])
	}
	if instructions, ok := upstreamBody["instructions"]; !ok || instructions != "" {
		t.Fatalf("upstream instructions = %#v, present = %v; want explicit empty string", instructions, ok)
	}
}

func TestImagesGenerationsDefaultsImageModel(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return completedImageStream("generated")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"prompt":"A blue circle"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := string(upstreamRequest.Tools[0].ExtraFields["model"]); got != `"gpt-image-2"` {
		t.Fatalf("upstream image model = %s, want gpt-image-2", got)
	}
}

func TestImagesGenerationsRejectsUnsupportedImageModel(t *testing.T) {
	t.Parallel()

	app := newImagesTestApp(t, func(turn.NormalizedRequest) eventStream {
		return completedImageStream("unexpected")
	})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{
		"model":"not-an-image-model",
		"prompt":"A blue circle"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", recorder.Code, recorder.Body.String())
	}
	assertOpenAIErrorCode(t, recorder.Body.Bytes(), "model_not_found")
}

func TestImagesEditsAcceptsJSONImageReferences(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return completedImageStream("edited")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewBufferString(`{
		"model":"gpt-image-2",
		"prompt":"Make the sky purple",
		"images":[
			{"image_url":"https://example.com/input.png"},
			{"file_id":"file_input"}
		],
		"mask":{"image_url":"https://example.com/mask.png"},
		"input_fidelity":"high"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response imagesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].B64JSON != "edited" {
		t.Fatalf("response = %#v", response)
	}

	if len(upstreamRequest.Input) != 1 || len(upstreamRequest.Input[0].Content) != 3 {
		t.Fatalf("upstream input = %#v", upstreamRequest.Input)
	}
	content := upstreamRequest.Input[0].Content
	if content[0].Text != "Make the sky purple" || content[1].ImageURL != "https://example.com/input.png" || content[2].FileID != "file_input" {
		t.Fatalf("upstream content = %#v", content)
	}
	tool := upstreamRequest.Tools[0]
	if string(tool.ExtraFields["action"]) != `"edit"` || string(tool.ExtraFields["input_fidelity"]) != `"high"` {
		t.Fatalf("upstream tool = %#v", tool.ExtraFields)
	}
	var mask map[string]any
	if err := json.Unmarshal(tool.ExtraFields["input_image_mask"], &mask); err != nil {
		t.Fatalf("decode input_image_mask: %v", err)
	}
	if mask["image_url"] != "https://example.com/mask.png" {
		t.Fatalf("mask = %#v", mask)
	}
}

func TestImagesEditsAcceptsMultipartUploads(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return completedImageStream("edited-upload")
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "Turn it green"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("quality", "low"); err != nil {
		t.Fatal(err)
	}
	imagePart, err := writer.CreateFormFile("image[]", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = imagePart.Write([]byte("input-png"))
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = maskPart.Write([]byte("mask-png"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	content := upstreamRequest.Input[0].Content
	if len(content) != 2 || !strings.HasPrefix(content[1].ImageURL, "data:") || !strings.HasSuffix(content[1].ImageURL, "aW5wdXQtcG5n") {
		t.Fatalf("upstream content = %#v", content)
	}
	tool := upstreamRequest.Tools[0]
	if string(tool.ExtraFields["action"]) != `"edit"` || string(tool.ExtraFields["quality"]) != `"low"` {
		t.Fatalf("upstream tool = %#v", tool.ExtraFields)
	}
	var mask map[string]any
	if err := json.Unmarshal(tool.ExtraFields["input_image_mask"], &mask); err != nil {
		t.Fatalf("decode input_image_mask: %v", err)
	}
	maskURL, _ := mask["image_url"].(string)
	if !strings.HasPrefix(maskURL, "data:") || !strings.HasSuffix(maskURL, "bWFzay1wbmc=") {
		t.Fatalf("mask = %#v", mask)
	}
}

func TestImagesGenerationsStreamsPartialAndCompletedEvents(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return &fakeEventStream{events: []*codex.StreamEvent{
			{
				Type: "response.image_generation_call.partial_image",
				Raw: map[string]any{
					"partial_image_b64": "partial", "partial_image_index": 0, "output_format": "png",
				},
			},
			{
				Type: "response.output_item.done",
				Raw: map[string]any{"output_index": 0, "item": map[string]any{
					"type": "image_generation_call", "id": "ig_stream", "result": "final", "output_format": "png",
				}},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{"response": map[string]any{
					"id": "resp_stream", "created_at": 789, "status": "completed", "output": []any{},
					"tool_usage": map[string]any{"image_gen": map[string]any{"total_tokens": 11}},
				}},
			},
		}}
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{
		"model":"gpt-image-2",
		"prompt":"A blue circle",
		"stream":true,
		"partial_images":1,
		"output_format":"png"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "image_generation.partial_image", "image_generation.completed")
	if events[0].Data["b64_json"] != "partial" || events[0].Data["partial_image_index"] != float64(0) {
		t.Fatalf("partial event = %#v", events[0].Data)
	}
	if events[1].Data["b64_json"] != "final" {
		t.Fatalf("completed event = %#v", events[1].Data)
	}
	if !upstreamRequest.Stream || string(upstreamRequest.Tools[0].ExtraFields["partial_images"]) != "1" {
		t.Fatalf("upstream request = %#v", upstreamRequest)
	}
}

func TestImagesEditsStreamsMultipartRequests(t *testing.T) {
	t.Parallel()

	var upstreamRequest codex.Request
	app := newImagesTestApp(t, func(normalized turn.NormalizedRequest) eventStream {
		upstreamRequest = normalized.Request
		return &fakeEventStream{events: []*codex.StreamEvent{
			{
				Type: "response.image_generation_call.partial_image",
				Raw:  map[string]any{"partial_image_b64": "edit-partial", "partial_image_index": 0, "output_format": "webp"},
			},
			{
				Type: "response.output_item.done",
				Raw: map[string]any{"output_index": 0, "item": map[string]any{
					"type": "image_generation_call", "result": "edit-final", "output_format": "webp",
				}},
			},
			{
				Type: "response.completed",
				Raw:  map[string]any{"response": map[string]any{"id": "resp_edit_stream", "status": "completed", "output": []any{}}},
			},
		}}
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "Make it green")
	_ = writer.WriteField("stream", "true")
	_ = writer.WriteField("partial_images", "1")
	imagePart, err := writer.CreateFormFile("image", "input.webp")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = imagePart.Write([]byte("input-webp"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q; body=%s", contentType, recorder.Body.String())
	}
	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "image_edit.partial_image", "image_edit.completed")
	if events[0].Data["b64_json"] != "edit-partial" || events[1].Data["b64_json"] != "edit-final" {
		t.Fatalf("events = %#v", events)
	}
	if !upstreamRequest.Stream || string(upstreamRequest.Tools[0].ExtraFields["partial_images"]) != "1" {
		t.Fatalf("upstream request = %#v", upstreamRequest)
	}
}

func completedImageStream(result string) eventStream {
	return &fakeEventStream{events: []*codex.StreamEvent{
		{
			Type: "response.output_item.done",
			Raw: map[string]any{"output_index": 0, "item": map[string]any{
				"type": "image_generation_call", "id": "ig_done", "result": result, "output_format": "png",
			}},
		},
		{
			Type: "response.completed",
			Raw: map[string]any{"response": map[string]any{
				"id": "resp_done", "model": "gpt-5.6-sol", "created_at": 456, "status": "completed", "output": []any{},
			}},
		},
	}}
}

func newImagesTestApp(t *testing.T, opener func(turn.NormalizedRequest) eventStream) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_images",
		AccountID: "upstream_images",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	cfg := config.Config{
		ProxyAPIKey:     "test-key",
		CodexBaseURL:    "https://example.invalid",
		DefaultModel:    "gpt-5.6-sol",
		ContinuationTTL: time.Minute,
		RequestTimeout:  5 * time.Second,
	}
	httpClient := codex.NewHTTPClient(cfg)
	t.Cleanup(httpClient.Close)
	catalog := models.NewCatalog(models.BootstrapEntries())
	app := &App{
		cfg:           cfg,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		engine:        gin.New(),
		accounts:      accountsSvc,
		httpClient:    httpClient,
		continuations: conversation.NewContinuationManager(time.Minute),
		models:        catalog,
	}
	app.directImageOpen = func(context.Context, accounts.Record, string, []byte, bool) (*http.Response, error) {
		return nil, &codex.UpstreamError{StatusCode: http.StatusNotFound, Body: "direct images unavailable"}
	}
	if opener != nil {
		record := mustGetAccount(t, accountsSvc, "acct_images")
		app.imageOpener = func(_ *gin.Context, _ string, normalized turn.NormalizedRequest) (openedRequest, bool) {
			return openedRequest{
				Resolution: sessionResolution{Request: normalized},
				Account:    record,
				Stream:     opener(normalized),
			}, true
		}
	}
	app.accountMgr = accountmanager.NewAccountManager(cfg, accountsSvc, nil, httpClient, catalog)
	app.routes()
	return app
}
