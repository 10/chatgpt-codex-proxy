package anthropic

import "net/http"

type ErrorResponse struct {
	Type      string    `json:"type"`
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

type ErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func ErrorPayload(errorType, message, requestID string) ErrorResponse {
	return ErrorResponse{
		Type:      "error",
		Error:     ErrorBody{Type: errorType, Message: message},
		RequestID: requestID,
	}
}

func ErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}
