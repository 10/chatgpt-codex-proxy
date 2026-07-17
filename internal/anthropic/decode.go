package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func DecodeMessages(data []byte) (MessagesRequest, error) {
	var request MessagesRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return MessagesRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return MessagesRequest{}, errors.New("request body must contain one JSON object")
		}
		return MessagesRequest{}, err
	}
	return request, nil
}
