package anthropic

import (
	"encoding/base64"
	"strings"
)

const maxCodexReasoningSignatureLength = 32 << 20

// IsValidCodexReasoningSignature validates the Fernet-like transport envelope
// used by Codex reasoning encrypted_content. It cannot prove that the upstream
// can decrypt the value, so callers must still handle signature rejection.
func IsValidCodexReasoningSignature(raw string) bool {
	signature := strings.TrimSpace(raw)
	if signature == "" || len(signature) > maxCodexReasoningSignatureLength || !strings.HasPrefix(signature, "gAAAA") {
		return false
	}
	for _, character := range signature {
		switch {
		case character >= 'A' && character <= 'Z':
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_' || character == '=':
		default:
			return false
		}
	}

	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(signature)
	}
	if err != nil || len(decoded) < 73 || decoded[0] != 0x80 {
		return false
	}

	ciphertextLength := len(decoded) - 1 - 8 - 16 - 32
	return ciphertextLength > 0 && ciphertextLength%16 == 0
}
