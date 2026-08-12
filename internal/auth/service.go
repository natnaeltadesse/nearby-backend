// Package auth owns users, credentials, sessions and the middleware that
// turns a bearer token into an identity. It knows nothing about providers
// beyond reading membership rows to build the JWT claim.
package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

const minPasswordLength = 8

// User is the public shape of a user account. password_hash never appears here.
type User struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Phone         *string   `json:"phone,omitempty"`
	PlatformRole  string    `json:"platformRole"`
	EmailVerified bool      `json:"emailVerified"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Tokens is a freshly issued credential pair.
type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	TokenType    string    `json:"tokenType"`
}

// Session is what sign-up, sign-in and refresh all return.
type Session struct {
	User        User         `json:"user"`
	Memberships []Membership `json:"memberships"`
	Tokens      Tokens       `json:"tokens"`
}

// Service implements the auth use cases.
type Service struct {
	pool          *pgxpool.Pool
	queries       *db.Queries
	issuer        *TokenIssuer
	refreshTTLWeb time.Duration
	refreshTTLMob time.Duration
}

// NewService wires the auth service.
func NewService(pool *pgxpool.Pool, issuer *TokenIssuer, refreshTTLWeb, refreshTTLMobile time.Duration) *Service {
	return &Service{
		pool:          pool,
		queries:       db.New(pool),
		issuer:        issuer,
		refreshTTLWeb: refreshTTLWeb,
		refreshTTLMob: refreshTTLMobile,
	}
}

// SignUpInput is a new-account request. Every account starts as a customer;
// becoming provider staff happens by accepting an invitation.
type SignUpInput struct {
	Name       string  `json:"name"`
	Email      string  `json:"email"`
	Phone      *string `json:"phone"`
	Password   string  `json:"password"`
	ClientType string  `json:"clientType"`
}

// SignUp creates an account and returns a session for it.
func (s *Service) SignUp(ctx context.Context, in SignUpInput) (*Session, error) {
	name := strings.TrimSpace(in.Name)
	email := normalizeEmail(in.Email)

	if name == "" {
		return nil, httpx.Validation("Name is required")
	}
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	row, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Name:         name,
		Email:        email,
		Phone:        trimPtr(in.Phone),
		PasswordHash: hash,
		PlatformRole: RoleUser,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, httpx.EmailTaken()
		}
		return nil, httpx.Internal(err)
	}

	user := User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		Phone:         row.Phone,
		PlatformRole:  row.PlatformRole,
		EmailVerified: row.EmailVerified,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}

	// A brand-new account has no memberships by construction, so there is no
	// point querying for them.
	return s.newSession(ctx, user, []Membership{}, NormalizeClientType(in.ClientType))
}

// SignInInput is a credential check.
type SignInInput struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	ClientType string `json:"clientType"`
}

// SignIn verifies credentials and returns a session.
func (s *Service) SignIn(ctx context.Context, in SignInInput) (*Session, error) {
	email := normalizeEmail(in.Email)
	if email == "" || in.Password == "" {
		return nil, httpx.InvalidCredentials()
	}

	row, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Spend the same time as a real verification so the response does
			// not reveal whether the address is registered.
			BurnTimingBudget()
			return nil, httpx.InvalidCredentials()
		}
		return nil, httpx.Internal(err)
	}

	ok, err := VerifyPassword(in.Password, row.PasswordHash)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if !ok {
		return nil, httpx.InvalidCredentials()
	}

	memberships, err := s.membershipsFor(ctx, row.ID)
	if err != nil {
		return nil, err
	}

	user := User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		Phone:         row.Phone,
		PlatformRole:  row.PlatformRole,
		EmailVerified: row.EmailVerified,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}

	return s.newSession(ctx, user, memberships, NormalizeClientType(in.ClientType))
}

// Refresh rotates a refresh token and mints a new access token.
//
// Rotation is unconditional: the presented token is revoked whether or not the
// caller ever uses the replacement. Presenting an already-revoked token is
// treated as theft and revokes the user's whole token family.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*Session, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, httpx.InvalidToken()
	}

	// Deliberately outside the transaction below. Replaying a consumed token
	// has to *persist* the revocation of the whole family, and a transaction
	// that ends in an error rolls back — which would quietly undo the very
	// defence this branch exists to apply.
	stored, err := s.queries.GetRefreshTokenByHash(ctx, HashRefreshToken(refreshToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.InvalidToken()
		}
		return nil, httpx.Internal(err)
	}

	if stored.RevokedAt != nil {
		// Someone is replaying a token we already rotated away. We cannot tell
		// the thief from the victim, so we invalidate both and make everyone
		// sign in again.
		if err := s.queries.RevokeAllUserRefreshTokens(ctx, stored.UserID); err != nil {
			return nil, httpx.Internal(err)
		}
		return nil, httpx.InvalidToken()
	}
	if time.Now().After(stored.ExpiresAt) {
		return nil, httpx.TokenExpired()
	}

	var session *Session

	err = database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		// Consume the token. The UPDATE is guarded on revoked_at IS NULL, so if
		// two requests raced past the read above only one changes a row; the
		// loser is treated as a replay.
		consumed, err := q.RevokeRefreshToken(ctx, stored.ID)
		if err != nil {
			return httpx.Internal(err)
		}
		if consumed == 0 {
			return httpx.InvalidToken()
		}

		userRow, err := q.GetUserByID(ctx, stored.UserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.InvalidToken()
			}
			return httpx.Internal(err)
		}

		memberships, err := membershipsFromQuerier(ctx, q, stored.UserID)
		if err != nil {
			return err
		}

		user := User{
			ID:            userRow.ID,
			Name:          userRow.Name,
			Email:         userRow.Email,
			Phone:         userRow.Phone,
			PlatformRole:  userRow.PlatformRole,
			EmailVerified: userRow.EmailVerified,
			CreatedAt:     userRow.CreatedAt,
			UpdatedAt:     userRow.UpdatedAt,
		}

		// The replacement keeps the client type of the token it replaces: a
		// mobile session must not silently shorten itself to the web TTL.
		session, err = s.issueSession(ctx, q, user, memberships, stored.ClientType)
		return err
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// SignOut revokes a single refresh token. It is deliberately idempotent: a
// client signing out twice, or with a token that already expired, still gets
// a clean result.
func (s *Service) SignOut(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil
	}
	_, err := s.queries.RevokeRefreshTokenByHash(ctx, HashRefreshToken(refreshToken))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return httpx.Internal(err)
	}
	return nil
}

// SignOutEverywhere revokes every refresh token a user holds.
func (s *Service) SignOutEverywhere(ctx context.Context, userID uuid.UUID) error {
	if err := s.queries.RevokeAllUserRefreshTokens(ctx, userID); err != nil {
		return httpx.Internal(err)
	}
	return nil
}

// CurrentSession re-reads the user and memberships behind an access token.
// Used by GET /auth/session so a client can rehydrate without a refresh.
func (s *Service) CurrentSession(ctx context.Context, userID uuid.UUID) (User, []Membership, error) {
	row, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, nil, httpx.NotFound("User")
		}
		return User{}, nil, httpx.Internal(err)
	}

	memberships, err := s.membershipsFor(ctx, userID)
	if err != nil {
		return User{}, nil, err
	}

	return User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		Phone:         row.Phone,
		PlatformRole:  row.PlatformRole,
		EmailVerified: row.EmailVerified,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, memberships, nil
}

// UpdateProfileInput patches the fields a user may change about themselves.
type UpdateProfileInput struct {
	Name  *string `json:"name"`
	Phone *string `json:"phone"`
}

// UpdateProfile applies a partial update to the caller's own account.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, in UpdateProfileInput) (User, error) {
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return User{}, httpx.Validation("Name cannot be empty")
	}

	row, err := s.queries.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
		ID:    userID,
		Name:  trimPtr(in.Name),
		Phone: trimPtr(in.Phone),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, httpx.NotFound("User")
		}
		return User{}, httpx.Internal(err)
	}

	return User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		Phone:         row.Phone,
		PlatformRole:  row.PlatformRole,
		EmailVerified: row.EmailVerified,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// RegisterDevice stores an FCM token for push delivery (milestone 10 uses it).
func (s *Service) RegisterDevice(ctx context.Context, userID uuid.UUID, fcmToken string) error {
	fcmToken = strings.TrimSpace(fcmToken)
	if fcmToken == "" {
		return httpx.Validation("fcmToken is required")
	}
	if err := s.queries.SetUserFCMToken(ctx, db.SetUserFCMTokenParams{
		ID:       userID,
		FcmToken: &fcmToken,
	}); err != nil {
		return httpx.Internal(err)
	}
	return nil
}

// --- internals ------------------------------------------------------------

func (s *Service) newSession(ctx context.Context, user User, memberships []Membership, clientType string) (*Session, error) {
	return s.issueSession(ctx, s.queries, user, memberships, clientType)
}

func (s *Service) issueSession(ctx context.Context, q *db.Queries, user User, memberships []Membership, clientType string) (*Session, error) {
	accessToken, expiresAt, err := s.issuer.IssueAccessToken(
		user.ID, user.Email, user.Name, user.PlatformRole, memberships)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	refreshToken, refreshHash, err := NewRefreshToken()
	if err != nil {
		return nil, httpx.Internal(err)
	}

	if _, err := q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		UserID:     user.ID,
		TokenHash:  refreshHash,
		ClientType: clientType,
		ExpiresAt:  time.Now().Add(s.refreshTTL(clientType)),
	}); err != nil {
		return nil, httpx.Internal(err)
	}

	return &Session{
		User:        user,
		Memberships: memberships,
		Tokens: Tokens{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresAt:    expiresAt,
			TokenType:    "Bearer",
		},
	}, nil
}

func (s *Service) refreshTTL(clientType string) time.Duration {
	if clientType == ClientMobile {
		return s.refreshTTLMob
	}
	return s.refreshTTLWeb
}

func (s *Service) membershipsFor(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	return membershipsFromQuerier(ctx, s.queries, userID)
}

func membershipsFromQuerier(ctx context.Context, q *db.Queries, userID uuid.UUID) ([]Membership, error) {
	rows, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	memberships := make([]Membership, 0, len(rows))
	for _, row := range rows {
		memberships = append(memberships, Membership{ProviderID: row.ProviderID, Role: row.Role})
	}
	return memberships, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateEmail(email string) error {
	if email == "" {
		return httpx.Validation("Email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return httpx.Validation("That does not look like a valid email address")
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return httpx.Validation("Password must be at least 8 characters")
	}
	return nil
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
