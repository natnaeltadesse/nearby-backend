package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Client types decide the refresh-token TTL.
const (
	ClientWeb    = "web"
	ClientMobile = "mobile"
)

// Platform roles.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// Membership is one provider the user belongs to, as carried in the JWT.
// A customer's list is simply empty — that is the entire difference between a
// customer account and a provider account (spec §6).
type Membership struct {
	ProviderID uuid.UUID `json:"providerId"`
	Role       string    `json:"role"`
}

// MembershipDetail is a membership plus enough of the provider to name it in a
// UI, which a client needs to let someone who owns several businesses choose
// between them. It is what the API returns; the JWT keeps the smaller
// Membership, because a claim rides along on every single request and a display
// name is not worth the bytes.
type MembershipDetail struct {
	Membership
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// Claims narrows details down to what the token actually carries.
func claimsFor(details []MembershipDetail) []Membership {
	memberships := make([]Membership, 0, len(details))
	for _, detail := range details {
		memberships = append(memberships, detail.Membership)
	}
	return memberships
}

// Claims is the access-token payload. It is verified locally on every request,
// so nothing here may be security-critical beyond the signature itself: the
// org middleware still re-checks membership against the database.
type Claims struct {
	Email        string       `json:"email"`
	Name         string       `json:"name"`
	PlatformRole string       `json:"platformRole"`
	Memberships  []Membership `json:"memberships"`
	jwt.RegisteredClaims
}

// UserID returns the subject as a UUID.
func (c Claims) UserID() (uuid.UUID, error) {
	return uuid.Parse(c.Subject)
}

// IsAdmin reports whether the caller is a platform administrator.
func (c Claims) IsAdmin() bool { return c.PlatformRole == RoleAdmin }

// TokenIssuer mints and verifies access tokens.
type TokenIssuer struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

// NewTokenIssuer builds a TokenIssuer. secret must already be validated as
// long enough by config.
func NewTokenIssuer(secret, issuer string, accessTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), issuer: issuer, accessTTL: accessTTL}
}

// AccessTTL is the lifetime of the tokens this issuer mints.
func (t *TokenIssuer) AccessTTL() time.Duration { return t.accessTTL }

// IssueAccessToken signs a short-lived access token for user.
func (t *TokenIssuer) IssueAccessToken(userID uuid.UUID, email, name, platformRole string, memberships []Membership) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(t.accessTTL)

	if memberships == nil {
		memberships = []Membership{}
	}

	claims := Claims{
		Email:        email,
		Name:         name,
		PlatformRole: platformRole,
		Memberships:  memberships,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    t.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// ParseAccessToken verifies a token and returns its claims. Expiry is reported
// as TOKEN_EXPIRED so clients know to refresh rather than to sign in again.
func (t *TokenIssuer) ParseAccessToken(raw string) (*Claims, error) {
	var claims Claims

	_, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", token.Header["alg"])
		}
		return t.secret, nil
	},
		jwt.WithIssuer(t.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		switch {
		case isExpiry(err):
			return nil, httpx.TokenExpired()
		default:
			return nil, httpx.InvalidToken().WithCause(err)
		}
	}
	if _, err := claims.UserID(); err != nil {
		return nil, httpx.InvalidToken().WithCause(err)
	}
	return &claims, nil
}

// isExpiry distinguishes "refresh me" from "this token is junk". jwt/v5 joins
// its validation failures, and errors.Is walks a join, so no manual unwrapping
// is needed.
func isExpiry(err error) bool {
	return errors.Is(err, jwt.ErrTokenExpired) || errors.Is(err, jwt.ErrTokenNotValidYet)
}

// --- refresh tokens -------------------------------------------------------
//
// Opaque, never a JWT: the whole point is that the server can revoke one.
// Only the hash is stored, so a database leak does not yield usable tokens.

const refreshTokenBytes = 32

// NewRefreshToken returns a fresh opaque token and the hash to persist.
func NewRefreshToken() (token, hash string, err error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: read refresh token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashRefreshToken(token), nil
}

// HashRefreshToken is the one-way function used for storage and lookup.
// SHA-256 is right here where argon2 would be wrong: the input is 256 bits of
// entropy we generated, not a guessable human secret, so there is nothing for
// a slow hash to buy — and lookup happens on every refresh.
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NormalizeClientType maps a client-supplied value onto a known client type,
// defaulting to web.
func NormalizeClientType(raw string) string {
	if raw == ClientMobile {
		return ClientMobile
	}
	return ClientWeb
}
