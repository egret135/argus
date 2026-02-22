package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Auth handles JWT-based authentication with HttpOnly cookies.
type Auth struct {
	username     string
	passwordHash string
	jwtSecret    []byte

	csrfMu     sync.Mutex
	csrfTokens map[string]time.Time
}

// NewAuth creates a new Auth instance. If configSecret is non-empty, it is used
// as the JWT signing secret. Otherwise, the secret is read from (or generated
// into) dataDir/jwt_secret.
func NewAuth(username, passwordHash string, dataDir string, configSecret string) (*Auth, error) {
	a := &Auth{
		username:     username,
		passwordHash: passwordHash,
		csrfTokens:   make(map[string]time.Time),
	}

	if configSecret != "" {
		a.jwtSecret = []byte(configSecret)
		return a, nil
	}

	secretPath := filepath.Join(dataDir, "jwt_secret")
	data, err := os.ReadFile(secretPath)
	if err == nil {
		a.jwtSecret = data
		return a, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.WriteFile(secretPath, secret, 0o600); err != nil {
		return nil, err
	}
	a.jwtSecret = secret
	return a, nil
}

// CheckPassword compares a plaintext password against the stored bcrypt hash.
func (a *Auth) CheckPassword(password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password))
	return err == nil
}

// GenerateToken creates a signed JWT with a 24-hour expiry.
func (a *Auth) GenerateToken() (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   a.username,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.jwtSecret)
}

// ValidateToken parses and validates a JWT string.
func (a *Auth) ValidateToken(tokenStr string) bool {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return a.jwtSecret, nil
	})
	return err == nil && token.Valid
}

// AuthMiddleware checks for a valid JWT in the "argus_token" cookie.
// If the token is missing or invalid, the request is redirected to /login.
func (a *Auth) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("argus_token")
		if err != nil || !a.ValidateToken(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GenerateCSRFToken creates a random hex token and stores it with a TTL.
func (a *Auth) GenerateCSRFToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)

	a.csrfMu.Lock()
	// Purge expired tokens while holding the lock.
	now := time.Now()
	for k, exp := range a.csrfTokens {
		if now.After(exp) {
			delete(a.csrfTokens, k)
		}
	}
	a.csrfTokens[token] = now.Add(10 * time.Minute)
	a.csrfMu.Unlock()

	return token
}

// ValidateCSRFToken checks whether the token exists and has not expired.
func (a *Auth) ValidateCSRFToken(token string) bool {
	a.csrfMu.Lock()
	defer a.csrfMu.Unlock()

	exp, ok := a.csrfTokens[token]
	if !ok {
		return false
	}
	delete(a.csrfTokens, token)
	return time.Now().Before(exp)
}
