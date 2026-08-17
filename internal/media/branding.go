package media

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Kind names one of the two pictures a provider has of itself.
//
// A gallery image is one of many and lives in its own table; these two are
// singular columns on `providers`, so writing one replaces what was there.
type Kind string

// The provider pictures.
const (
	KindLogo  Kind = "logo"
	KindCover Kind = "cover"
)

// ParseKind turns a path segment into a Kind, refusing anything else.
func ParseKind(value string) (Kind, error) {
	switch Kind(value) {
	case KindLogo:
		return KindLogo, nil
	case KindCover:
		return KindCover, nil
	default:
		return "", httpx.Validation("image must be 'logo' or 'cover'")
	}
}

// SetProviderImage replaces a provider's logo or cover and returns the new URL.
//
// The order is the same as a gallery upload's and for the same reason: bytes
// first, then the column, then the bytes that were there before. A failure
// between the steps leaves a file nothing points at rather than a column
// pointing at a file that is gone.
func (s *Service) SetProviderImage(
	ctx context.Context,
	providerID uuid.UUID,
	kind Kind,
	filename string,
	data []byte,
) (string, error) {
	previous, err := s.currentBranding(ctx, providerID, kind)
	if err != nil {
		return "", err
	}

	contentType, _, err := Inspect(data)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return "", httpx.Validation("That image is larger than 5 MB")
		}
		return "", httpx.Validation("Only JPEG, PNG, WebP and GIF images are accepted")
	}

	stored, err := s.storage.Save(ctx, filename, contentType, data)
	if err != nil {
		return "", httpx.Internal(err)
	}

	if err := s.writeBranding(ctx, providerID, kind, &stored.URL, &stored.PublicID); err != nil {
		// Nothing references the new file now, so it would only ever be litter.
		s.discard(ctx, stored.PublicID)
		return "", err
	}

	// Only once the column has moved: until it did, this was still the picture
	// being served.
	if previous != nil {
		s.discard(ctx, *previous)
	}

	return stored.URL, nil
}

// ClearProviderImage removes a provider's logo or cover.
func (s *Service) ClearProviderImage(ctx context.Context, providerID uuid.UUID, kind Kind) error {
	previous, err := s.currentBranding(ctx, providerID, kind)
	if err != nil {
		return err
	}

	if err := s.writeBranding(ctx, providerID, kind, nil, nil); err != nil {
		return err
	}

	if previous != nil {
		s.discard(ctx, *previous)
	}
	return nil
}

// currentBranding reads the storage key the column points at today, so the
// bytes behind it can be dropped once it has been replaced.
func (s *Service) currentBranding(ctx context.Context, providerID uuid.UUID, kind Kind) (*string, error) {
	row, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Provider")
		}
		return nil, httpx.Internal(err)
	}

	if kind == KindLogo {
		return row.LogoPublicID, nil
	}
	return row.CoverPublicID, nil
}

// writeBranding points the column at a stored image, or at nothing.
func (s *Service) writeBranding(
	ctx context.Context,
	providerID uuid.UUID,
	kind Kind,
	url, publicID *string,
) error {
	var (
		affected int64
		err      error
	)

	if kind == KindLogo {
		affected, err = s.queries.SetProviderLogo(ctx, db.SetProviderLogoParams{
			ID: providerID, LogoUrl: url, LogoPublicID: publicID,
		})
	} else {
		affected, err = s.queries.SetProviderCover(ctx, db.SetProviderCoverParams{
			ID: providerID, CoverUrl: url, CoverPublicID: publicID,
		})
	}

	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Provider")
	}
	return nil
}

// discard deletes stored bytes nothing points at any more. A failure here is
// logged rather than returned: the column has already moved, so the caller's
// request did succeed and a retry would delete nothing.
func (s *Service) discard(ctx context.Context, publicID string) {
	if err := s.storage.Delete(ctx, publicID); err != nil {
		s.logger.ErrorContext(ctx, "could not delete replaced provider image",
			slog.String("publicId", publicID),
			slog.String("error", err.Error()))
	}
}
