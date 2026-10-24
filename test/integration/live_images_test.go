//go:build live

package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLiveImages(t *testing.T) {
	cfg := loadLiveConfig(t)
	fixture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			fixture.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var fixturePNG bytes.Buffer
	if err := png.Encode(&fixturePNG, fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		edit       bool
	}{
		{name: "generation JSON", path: "/images/generations"},
		{name: "edit multipart SSE", path: "/images/edits", edit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := "A plain blue square on a white background. Minimal flat graphic."
			if tc.edit {
				prompt = "Change the red square to blue. Return the edited image."
			}
			payload := map[string]any{"model": "gpt-image-2", "prompt": prompt, "quality": "low", "size": "1024x1024", "output_format": "png", "response_format": "b64_json", "stream": tc.edit}
			var body bytes.Buffer
			contentType := "application/json"
			if tc.edit {
				writer := multipart.NewWriter(&body)
				for k, v := range map[string]string{"model": "gpt-image-2", "prompt": prompt, "quality": "low", "size": "1024x1024", "output_format": "png", "response_format": "b64_json", "stream": "true"} {
					if err := writer.WriteField(k, v); err != nil {
						t.Fatal(err)
					}
				}
				file, err := writer.CreateFormFile("image", "square.png")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write(fixturePNG.Bytes()); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = writer.FormDataContentType()
			} else if err := json.NewEncoder(&body).Encode(payload); err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+tc.path, &body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			req.Header.Set("Content-Type", contentType)
			response, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 {
				t.Fatalf("HTTP %d: %.1000s", response.StatusCode, data)
			}
			var encoded string
			if tc.edit {
				if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
					t.Fatal("missing SSE content type")
				}
				for _, event := range liveSSEObjects(t, data) {
					if event["type"] == "error" || event["error"] != nil {
						t.Fatalf("stream error: %v", event)
					}
					if strings.HasSuffix(liveString(event["type"]), ".completed") {
						encoded = liveString(event["b64_json"])
					}
				}
			} else {
				var decoded struct {
					Data []struct {
						B64JSON string `json:"b64_json"`
					} `json:"data"`
				}
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				if len(decoded.Data) != 1 {
					t.Fatalf("expected one image, got %d", len(decoded.Data))
				}
				encoded = decoded.Data[0].B64JSON
			}
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			output, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("invalid generated PNG (%d bytes): %v", len(raw), err)
			}
			if output.Bounds().Empty() {
				t.Fatal("generated image has no pixels")
			}
			t.Logf("valid PNG (requested 1024x1024): %dx%d, %d bytes", output.Bounds().Dx(), output.Bounds().Dy(), len(raw))
		})
	}
}
