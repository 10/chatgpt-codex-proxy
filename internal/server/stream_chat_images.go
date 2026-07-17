package server

import (
	"crypto/sha256"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

type chatImageStreamer struct {
	indexByItemID map[string]int
	lastHash      map[string][32]byte
	nextIndex     int
}

func newChatImageStreamer() *chatImageStreamer {
	return &chatImageStreamer{
		indexByItemID: make(map[string]int),
		lastHash:      make(map[string][32]byte),
	}
}

func (s *chatImageStreamer) imagesForEvent(event *codex.StreamEvent) []map[string]any {
	if event == nil {
		return nil
	}
	switch event.Type {
	case "response.image_generation_call.partial_image":
		image := s.image(
			jsonutil.StringValue(event.Raw["item_id"]),
			jsonutil.StringValue(event.Raw["output_format"]),
			jsonutil.StringValue(event.Raw["partial_image_b64"]),
		)
		if image != nil {
			return []map[string]any{image}
		}
	case "response.output_item.done":
		item := jsonutil.FirstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item"))
		if jsonutil.StringValue(item["type"]) == "image_generation_call" {
			image := s.image(jsonutil.StringValue(item["id"]), jsonutil.StringValue(item["output_format"]), jsonutil.StringValue(item["result"]))
			if image != nil {
				return []map[string]any{image}
			}
		}
	case "response.completed":
		response := jsonutil.MapValue(event.Raw, "response")
		var images []map[string]any
		for _, item := range jsonutil.SliceOfMaps(response["output"]) {
			if jsonutil.StringValue(item["type"]) != "image_generation_call" {
				continue
			}
			if image := s.image(jsonutil.StringValue(item["id"]), jsonutil.StringValue(item["output_format"]), jsonutil.StringValue(item["result"])); image != nil {
				images = append(images, image)
			}
		}
		return images
	}
	return nil
}

func (s *chatImageStreamer) image(itemID, outputFormat, base64Data string) map[string]any {
	if strings.TrimSpace(base64Data) == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(base64Data))
	key := strings.TrimSpace(itemID)
	if key == "" {
		key = string(hash[:])
	}
	if previous, ok := s.lastHash[key]; ok && previous == hash {
		return nil
	}
	s.lastHash[key] = hash
	index, ok := s.indexByItemID[key]
	if !ok {
		index = s.nextIndex
		s.nextIndex++
		s.indexByItemID[key] = index
	}
	return turn.ChatImage(index, outputFormat, base64Data)
}
