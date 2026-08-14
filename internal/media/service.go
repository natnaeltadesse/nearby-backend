package media

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Image is one photo attached to a service.
type Image struct {
	ID        uuid.UUID `json:"id"`
	ServiceID uuid.UUID `json:"serviceId"`
	ImageURL  string    `json:"imageUrl"`
	Caption   *string   `json:"caption,omitempty"`
	SortOrder int32     `json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
}

// Service owns a service's gallery: the rows and the bytes behind them.
type Service struct {
	queries *db.Queries
	storage Storage
	logger  *slog.Logger
}

// NewService wires the gallery.
func NewService(pool *pgxpool.Pool, storage Storage, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{queries: db.New(pool), storage: storage, logger: logger}
}

// List returns a service's gallery in display order.
func (s *Service) List(ctx context.Context, providerID, serviceID uuid.UUID) ([]Image, error) {
	if err := s.assertOwned(ctx, providerID, serviceID); err != nil {
		return nil, err
	}

	rows, err := s.queries.ListServiceMedia(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	images := make([]Image, 0, len(rows))
	for _, row := range rows {
		images = append(images, Image{
			ID:        row.ID,
			ServiceID: row.ServiceID,
			ImageURL:  row.ImageUrl,
			Caption:   row.Caption,
			SortOrder: row.SortOrder,
			CreatedAt: row.CreatedAt,
		})
	}
	return images, nil
}

// Upload validates bytes, stores them, and records the row.
//
// The bytes are written before the row, so a failure between the two leaves an
// orphaned file rather than a row pointing at nothing. That is the cheaper of
// the two mistakes: a stray file costs disk, a broken row shows the customer a
// missing image.
func (s *Service) Upload(
	ctx context.Context,
	providerID, serviceID uuid.UUID,
	filename string,
	data []byte,
	caption *string,
) (*Image, error) {
	if err := s.assertOwned(ctx, providerID, serviceID); err != nil {
		return nil, err
	}

	count, err := s.queries.CountServiceMedia(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if count >= MaxPerService {
		return nil, httpx.Conflict("This service already has the maximum number of photos")
	}

	contentType, _, err := Inspect(data)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooLarge):
			return nil, httpx.Validation("That image is larger than 5 MB")
		default:
			return nil, httpx.Validation("Only JPEG, PNG, WebP and GIF images are accepted")
		}
	}

	stored, err := s.storage.Save(ctx, filename, contentType, data)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	sortOrder, err := s.queries.NextServiceMediaSortOrder(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	row, err := s.queries.CreateServiceMedia(ctx, db.CreateServiceMediaParams{
		ServiceID:     serviceID,
		ProviderID:    providerID,
		ImageUrl:      stored.URL,
		ImagePublicID: stored.PublicID,
		Caption:       trimPtr(caption),
		SortOrder:     sortOrder,
	})
	if err != nil {
		// The row is what makes the file reachable, so drop the bytes rather
		// than leaving something nothing points at.
		if cleanup := s.storage.Delete(ctx, stored.PublicID); cleanup != nil {
			s.logger.ErrorContext(ctx, "could not clean up orphaned upload",
				slog.String("publicId", stored.PublicID),
				slog.String("error", cleanup.Error()))
		}
		return nil, httpx.Internal(err)
	}

	return &Image{
		ID:        row.ID,
		ServiceID: row.ServiceID,
		ImageURL:  row.ImageUrl,
		Caption:   row.Caption,
		SortOrder: row.SortOrder,
		CreatedAt: row.CreatedAt,
	}, nil
}

// UpdateInput patches a gallery entry.
type UpdateInput struct {
	Caption   *string `json:"caption"`
	SortOrder *int32  `json:"sortOrder"`
}

// Update changes a caption or the display order.
func (s *Service) Update(ctx context.Context, providerID, mediaID uuid.UUID, in UpdateInput) (*Image, error) {
	row, err := s.queries.UpdateServiceMedia(ctx, db.UpdateServiceMediaParams{
		ID:         mediaID,
		ProviderID: providerID,
		Caption:    trimPtr(in.Caption),
		SortOrder:  in.SortOrder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Image")
		}
		return nil, httpx.Internal(err)
	}

	return &Image{
		ID:        row.ID,
		ServiceID: row.ServiceID,
		ImageURL:  row.ImageUrl,
		Caption:   row.Caption,
		SortOrder: row.SortOrder,
		CreatedAt: row.CreatedAt,
	}, nil
}

// Delete removes the row and then the bytes.
//
// A storage failure is logged, not returned: the row is already gone, so the
// caller's request did succeed, and reporting an error would invite a retry
// that deletes nothing.
func (s *Service) Delete(ctx context.Context, providerID, mediaID uuid.UUID) error {
	publicID, err := s.queries.DeleteServiceMedia(ctx, db.DeleteServiceMediaParams{
		ID:         mediaID,
		ProviderID: providerID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Image")
		}
		return httpx.Internal(err)
	}

	if err := s.storage.Delete(ctx, publicID); err != nil {
		s.logger.ErrorContext(ctx, "could not delete stored image",
			slog.String("publicId", publicID),
			slog.String("error", err.Error()))
	}
	return nil
}

// assertOwned refuses a service id belonging to another tenant, so a leaked id
// cannot be used to read or write someone else's gallery.
func (s *Service) assertOwned(ctx context.Context, providerID, serviceID uuid.UUID) error {
	ok, err := s.queries.ServiceExistsForProvider(ctx, db.ServiceExistsForProviderParams{
		ID:         serviceID,
		ProviderID: providerID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if !ok {
		return httpx.NotFound("Service")
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
