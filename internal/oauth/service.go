package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Service provides OAuth 2.0 authorization server capabilities for MCP.
type Service struct {
	hmacKey     []byte
	issuer      string
	tokenExpiry time.Duration

	// Static clients (client_id -> client config)
	clients map[string]*Client

	// Authorization codes (code -> auth code data)
	authCodes map[string]*authCode

	// Access tokens in memory (for validation)
	tokens map[string]*tokenRecord
}

// Client represents an OAuth 2.0 client registration.
type Client struct {
	ID           string
	Name         string
	Secret       string // For confidential clients
	RedirectURIs []string
	Public       bool   // true = public client (no secret, PKCE required)
}

// authCode holds authorization code data.
type authCode struct {
	Code        string
	ClientID    string
	UserID      string
	RedirectURI string
	PKCEChallenge string
	PKCEMethod  string
	ExpiresAt   time.Time
	Used        bool
}

// tokenRecord holds access token metadata.
type tokenRecord struct {
	Token     string
	UserID    string
	ClientID  string
	Scopes    []string
	ExpiresAt time.Time
}

// Claims for JWT access tokens.
type AccessTokenClaims struct {
	jwt.RegisteredClaims
	UserID   string   `json:"sub"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scope,omitempty"`
}

// NewService creates an OAuth service.
func NewService(hmacKey []byte, issuer string) *Service {
	s := &Service{
		hmacKey:     hmacKey,
		issuer:      issuer,
		tokenExpiry: 24 * time.Hour,
		clients:     make(map[string]*Client),
		authCodes:   make(map[string]*authCode),
		tokens:      make(map[string]*tokenRecord),
	}

	// Register a default public client for ChatGPT MCP
	// ChatGPT will use this client ID or we can support dynamic registration later
	s.clients["chatgpt-mcp"] = &Client{
		ID:           "chatgpt-mcp",
		Name:         "ChatGPT MCP",
		RedirectURIs: []string{}, // Will accept any redirect URI for testing
		Public:       true,
	}

	return s
}

// RegisterClient adds a new OAuth client.
func (s *Service) RegisterClient(client *Client) {
	s.clients[client.ID] = client
}

// GetClient returns a registered client by ID.
func (s *Service) GetClient(id string) *Client {
	return s.clients[id]
}

// AuthorizationServerMetadata returns the OAuth 2.0 Authorization Server Metadata.
func (s *Service) AuthorizationServerMetadata() map[string]interface{} {
	return map[string]interface{}{
		"issuer":                 s.issuer,
		"authorization_endpoint": s.issuer + "/oauth/authorize",
		"token_endpoint":         s.issuer + "/oauth/token",
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                        []string{"memory.read", "memory.search", "memory.propose", "memory.write"},
	}
}

// ProtectedResourceMetadata returns the OAuth 2.0 Protected Resource Metadata.
func (s *Service) ProtectedResourceMetadata() map[string]interface{} {
	return map[string]interface{}{
		"resource":              s.issuer + "/mcp",
		"authorization_servers": []string{s.issuer},
		"scopes_supported":      []string{"memory.read", "memory.search", "memory.propose", "memory.write"},
	}
}

// GenerateAuthCode creates an authorization code for the authorization code flow.
func (s *Service) GenerateAuthCode(clientID, userID, redirectURI, pkceChallenge, pkceMethod string) (string, error) {
	client := s.GetClient(clientID)
	if client == nil {
		return "", fmt.Errorf("invalid client_id")
	}

	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		return "", fmt.Errorf("failed to generate code: %w", err)
	}
	code := base64.RawURLEncoding.EncodeToString(codeBytes)

	ac := &authCode{
		Code:          code,
		ClientID:      clientID,
		UserID:        userID,
		RedirectURI:   redirectURI,
		PKCEChallenge: pkceChallenge,
		PKCEMethod:    pkceMethod,
		ExpiresAt:     time.Now().Add(10 * time.Minute),
	}

	s.authCodes[code] = ac

	return code, nil
}

// ExchangeAuthCode exchanges an authorization code for tokens.
func (s *Service) ExchangeAuthCode(code, clientID, redirectURI, codeVerifier string) (*TokenResponse, error) {
	ac, ok := s.authCodes[code]
	if !ok || ac.Used || time.Now().After(ac.ExpiresAt) {
		return nil, fmt.Errorf("invalid authorization code")
	}

	// Verify client_id matches
	if ac.ClientID != clientID {
		return nil, fmt.Errorf("client_id mismatch")
	}

	// Verify PKCE
	if ac.PKCEChallenge != "" {
		if codeVerifier == "" {
			return nil, fmt.Errorf("missing code_verifier")
		}
		if ac.PKCEMethod == "S256" {
			hash := sha256.Sum256([]byte(codeVerifier))
			challenge := base64.RawURLEncoding.EncodeToString(hash[:])
			if subtle.ConstantTimeCompare([]byte(challenge), []byte(ac.PKCEChallenge)) != 1 {
				return nil, fmt.Errorf("invalid code_verifier")
			}
		} else if ac.PKCEMethod == "plain" {
			if subtle.ConstantTimeCompare([]byte(codeVerifier), []byte(ac.PKCEChallenge)) != 1 {
				return nil, fmt.Errorf("invalid code_verifier")
			}
		}
	}

	ac.Used = true

	// Generate access token
	token, err := s.GenerateAccessToken(ac.UserID, clientID, []string{"memory.read", "memory.search", "memory.propose"})
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(s.tokenExpiry.Seconds()),
	}, nil
}

// GenerateAccessToken creates a new JWT access token.
func (s *Service) GenerateAccessToken(userID, clientID string, scopes []string) (string, error) {
	now := time.Now()
	claims := AccessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{s.issuer + "/mcp"},
			ExpiresAt: jwt.NewNumericDate(now.Add(s.tokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        generateTokenID(),
		},
		UserID:   userID,
		ClientID: clientID,
		Scopes:   scopes,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.hmacKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	// Record token for validation
	s.tokens[claims.ID] = &tokenRecord{
		Token:     signed,
		UserID:    userID,
		ClientID:  clientID,
		Scopes:    scopes,
		ExpiresAt: now.Add(s.tokenExpiry),
	}

	return signed, nil
}

// ValidateAccessToken validates a JWT access token and returns the claims.
func (s *Service) ValidateAccessToken(tokenString string) (*AccessTokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &AccessTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.hmacKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*AccessTokenClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Check expiry
	if time.Now().After(claims.ExpiresAt.Time) {
		return nil, fmt.Errorf("token expired")
	}

	// Check audience
	audValid := false
	targetAudience := s.issuer + "/mcp"
	for _, aud := range claims.Audience {
		if aud == targetAudience {
			audValid = true
			break
		}
	}
	if !audValid {
		return nil, fmt.Errorf("invalid token audience")
	}

	return claims, nil
}

// TokenResponse represents the OAuth 2.0 token endpoint response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// ErrorResponse represents an OAuth 2.0 error response.
type ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// WriteTokenResponse writes a successful token response.
func WriteTokenResponse(w http.ResponseWriter, resp *TokenResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// WriteErrorResponse writes an OAuth error response.
func WriteErrorResponse(w http.ResponseWriter, errCode, description string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error:            errCode,
		ErrorDescription: description,
	})
}

func generateTokenID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ParseBasicAuth extracts client_id and client_secret from Authorization header.
func ParseBasicAuth(r *http.Request) (clientID, clientSecret string, ok bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", "", false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
