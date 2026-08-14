package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

const (
	// verificationCodeTTL is short because the code is short: six digits is a
	// guessable secret, and a narrow window is most of what makes it safe.
	verificationCodeTTL = 15 * time.Minute

	// maxVerificationAttempts caps guessing at a rate that makes a one-in-a-
	// million code worth roughly what it looks like.
	maxVerificationAttempts = 5

	verificationCodeDigits = 6
)

// IssueEmailVerification creates a code for a user's email address and hands
// it to the sender. Any earlier live code for that address is retired first,
// so a resend leaves exactly one code working.
//
// Delivery failures are logged, not returned: sign-up has already succeeded by
// the time this runs, and failing the request would leave the caller with an
// account they were told they do not have. Resending is always available.
func (s *Service) IssueEmailVerification(ctx context.Context, userID uuid.UUID, email string) error {
	email = NormalizeEmail(email)

	code, err := generateNumericCode(verificationCodeDigits)
	if err != nil {
		return httpx.Internal(err)
	}

	if err := s.queries.ConsumeVerificationCodesFor(ctx, db.ConsumeVerificationCodesForParams{
		Channel:     ChannelEmail,
		Destination: email,
	}); err != nil {
		return httpx.Internal(err)
	}

	if _, err := s.queries.CreateVerificationCode(ctx, db.CreateVerificationCodeParams{
		UserID:      userID,
		Channel:     ChannelEmail,
		Destination: email,
		CodeHash:    HashRefreshToken(code),
		ExpiresAt:   time.Now().Add(verificationCodeTTL),
	}); err != nil {
		return httpx.Internal(err)
	}

	if err := s.sender.SendCode(ctx, ChannelEmail, email, code); err != nil {
		s.logger.ErrorContext(ctx, "could not deliver verification code",
			slog.String("channel", ChannelEmail),
			slog.String("error", err.Error()))
	}
	return nil
}

// VerifyEmail checks a submitted code and marks the address verified.
//
// Every failure returns the same error whether the address is unknown, the
// code is wrong or it expired: telling them apart would turn this endpoint
// into a way to discover which addresses are registered.
func (s *Service) VerifyEmail(ctx context.Context, email, code string) error {
	email = NormalizeEmail(email)
	code = strings.TrimSpace(code)

	if email == "" || code == "" {
		return httpx.Validation("Email and code are both required")
	}

	row, err := s.queries.GetLiveVerificationCode(ctx, db.GetLiveVerificationCodeParams{
		Channel:     ChannelEmail,
		Destination: email,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.InvalidCode()
		}
		return httpx.Internal(err)
	}

	if time.Now().After(row.ExpiresAt) {
		return httpx.InvalidCode()
	}

	// Count the attempt before checking it, so a crash mid-request cannot be
	// used to guess for free.
	attempts, err := s.queries.IncrementVerificationAttempts(ctx, row.ID)
	if err != nil {
		return httpx.Internal(err)
	}
	if attempts > maxVerificationAttempts {
		// Burn the code rather than leaving it to be ground down.
		if _, err := s.queries.ConsumeVerificationCode(ctx, row.ID); err != nil {
			return httpx.Internal(err)
		}
		return httpx.TooManyAttempts()
	}

	if HashRefreshToken(code) != row.CodeHash {
		return httpx.InvalidCode()
	}

	consumed, err := s.queries.ConsumeVerificationCode(ctx, row.ID)
	if err != nil {
		return httpx.Internal(err)
	}
	if consumed == 0 {
		// Another request consumed it between the read and here.
		return httpx.InvalidCode()
	}

	if err := s.queries.MarkEmailVerified(ctx, row.UserID); err != nil {
		return httpx.Internal(err)
	}
	return nil
}

// ResendEmailVerification issues a fresh code for an address.
//
// It reports success even for an address with no account, for the same reason
// VerifyEmail does not distinguish its failures.
func (s *Service) ResendEmailVerification(ctx context.Context, email string) error {
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return err
	}

	user, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return httpx.Internal(err)
	}

	// Nothing to prove twice.
	if user.EmailVerified {
		return nil
	}

	return s.IssueEmailVerification(ctx, user.ID, user.Email)
}

// generateNumericCode returns a zero-padded decimal code of n digits, drawn
// uniformly so that every code including the ones with leading zeros is
// equally likely.
func generateNumericCode(digits int) (string, error) {
	max := big.NewInt(1)
	for range digits {
		max.Mul(max, big.NewInt(10))
	}

	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("auth: read verification code: %w", err)
	}
	return fmt.Sprintf("%0*d", digits, n), nil
}
