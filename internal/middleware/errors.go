package middleware

type OpenAIErrorResponse struct {
	Error OpenAIErrorBody `json:"error"`
}

type OpenAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   string `json:"param,omitempty"`
}

func OpenAIErrorPayload(message, typ, code, param string) OpenAIErrorResponse {
	return OpenAIErrorResponse{
		Error: OpenAIErrorBody{
			Message: message,
			Type:    typ,
			Code:    code,
			Param:   param,
		},
	}
}
