package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func readRequestBody(req *http.Request) ([]byte, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	encodings := strings.Split(req.Header.Get("Content-Encoding"), ",")
	for i := len(encodings) - 1; i >= 0; i-- {
		encoding := strings.ToLower(strings.TrimSpace(encodings[i]))
		switch encoding {
		case "", "identity":
			continue
		case "zstd":
			decoder, err := zstd.NewReader(bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
			}
			body, err = io.ReadAll(decoder)
			decoder.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
			}
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", encoding)
		}
	}

	return body, nil
}
