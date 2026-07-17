package accounts

import (
	"cmp"
	"strings"

	"chatgpt-codex-proxy/internal/jwtutil"
)

type tokenMetadata struct {
	Email    string
	PlanType string
	UserID   string
}

type jwtClaims struct {
	Email    string            `json:"email"`
	PlanType string            `json:"chatgpt_plan_type"`
	UserID   string            `json:"chatgpt_user_id"`
	Profile  *jwtProfileClaims `json:"https://api.openai.com/profile,omitempty"`
	Auth     *jwtAuthClaims    `json:"https://api.openai.com/auth,omitempty"`
}

type jwtProfileClaims struct {
	Email  string `json:"email"`
	UserID string `json:"chatgpt_user_id"`
}

type jwtAuthClaims struct {
	PlanType string `json:"chatgpt_plan_type"`
	UserID   string `json:"chatgpt_user_id"`
}

func metadataFromToken(token OAuthToken) tokenMetadata {
	claims, ok := jwtutil.DecodePayload[jwtClaims](token.AccessToken)
	if !ok {
		return tokenMetadata{}
	}

	profile := jwtProfileClaims{}
	if claims.Profile != nil {
		profile = *claims.Profile
	}
	auth := jwtAuthClaims{}
	if claims.Auth != nil {
		auth = *claims.Auth
	}

	return tokenMetadata{
		Email:    cmp.Or(strings.TrimSpace(claims.Email), strings.TrimSpace(profile.Email)),
		PlanType: cmp.Or(strings.TrimSpace(claims.PlanType), strings.TrimSpace(auth.PlanType)),
		UserID: cmp.Or(
			strings.TrimSpace(claims.UserID),
			strings.TrimSpace(profile.UserID),
			strings.TrimSpace(auth.UserID),
		),
	}
}
