package booking

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/catalog"
	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
	"github.com/nearby/booking-backend/internal/scheduling"
)

// Catalog is booking's outbound port to the catalog: prices a selection and
// validates booking-scoped attributes. Nothing here exposes a category.
type Catalog interface {
	GetServiceWithProvider(ctx context.Context, serviceID uuid.UUID) (*catalog.ServiceWithProvider, error)
	QuoteFor(ctx context.Context, service catalog.Service, optionIDs []uuid.UUID) (*catalog.Quote, error)
	ValidateBookingAttributes(ctx context.Context, categoryID uuid.UUID, values map[string]any) (map[string]any, error)
}

// Slots is booking's outbound port to scheduling: re-derives what is actually
// free so a client-sent start can be checked rather than trusted.
type Slots interface {
	SlotsForBooking(ctx context.Context, serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID) ([]scheduling.Slot, error)
	InvalidateProvider(providerID uuid.UUID)
}

// Service implements the booking use cases.
type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	catalog Catalog
	slots   Slots
}

// NewService wires the booking service.
func NewService(pool *pgxpool.Pool, cat Catalog, slots Slots) *Service {
	return &Service{pool: pool, queries: db.New(pool), catalog: cat, slots: slots}
}

// CreateInput is a reservation request.
//
// Note what it does not carry: no price, no duration, no end time. The client
// picks a service, some options and a start; the server derives everything else.
type CreateInput struct {
	ServiceID  uuid.UUID      `json:"serviceId"`
	StartsAt   time.Time      `json:"startsAt"`
	OptionIDs  []uuid.UUID    `json:"optionIds"`
	Attributes map[string]any `json:"attributes"`
	Note       *string        `json:"note"`

	// Staff-entered walk-ins only. A customer booking themselves in leaves
	// these nil and is identified by their user id.
	ResourceID    *uuid.UUID `json:"resourceId"`
	CustomerName  *string    `json:"customerName"`
	CustomerPhone *string    `json:"customerPhone"`
}

// Create reserves a slot.
//
// The order matters. Everything cheap and deterministic happens first — price,
// duration, attribute validation, then a fresh availability check to confirm
// the start is real. Only then do we insert, and the exclusion constraint makes
// the final call. Two customers tapping the same slot both reach the INSERT;
// one wins, the other gets SLOT_TAKEN. No locking, no race.
func (s *Service) Create(ctx context.Context, customerID *uuid.UUID, in CreateInput) (*Booking, error) {
	if in.StartsAt.IsZero() {
		return nil, httpx.Validation("startsAt is required")
	}

	service, err := s.catalog.GetServiceWithProvider(ctx, in.ServiceID)
	if err != nil {
		return nil, err
	}
	if !service.IsActive {
		return nil, httpx.ServiceInactive()
	}
	if service.ProviderStatus != "active" {
		return nil, httpx.ProviderInactive()
	}

	location, err := time.LoadLocation(service.Timezone)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	// Booking-scoped attributes (e.g. the customer's vehicle size) are checked
	// against the category's definitions, same validator as services use.
	attributes, err := s.catalog.ValidateBookingAttributes(ctx, service.CategoryID, in.Attributes)
	if err != nil {
		return nil, err
	}
	encodedAttributes, err := json.Marshal(attributes)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	// Price and duration come from the catalog, never from the request.
	quote, err := s.catalog.QuoteFor(ctx, service.Service, in.OptionIDs)
	if err != nil {
		return nil, err
	}

	startsAt := in.StartsAt.UTC().Truncate(time.Second)
	endsAt := startsAt.Add(time.Duration(quote.DurationMinutes+service.BufferMinutes) * time.Minute)

	// Re-derive the day's slots and require the requested start to be one of
	// them. This is what enforces opening hours, the minimum lead time and the
	// slot step in a single check, using exactly the code that produced the
	// list the client was shown.
	candidates, err := s.resourcesForStart(ctx, in.ServiceID, startsAt, location, in.OptionIDs)
	if err != nil {
		return nil, err
	}

	if in.ResourceID != nil {
		if !containsID(candidates, *in.ResourceID) {
			return nil, httpx.SlotTaken()
		}
		candidates = []uuid.UUID{*in.ResourceID}
	}

	status := StatusPending
	if service.BookingMode == "instant" {
		// `instant` providers skip `pending` and land on `confirmed`.
		status = StatusConfirmed
	}

	booking, err := s.insertBooking(ctx, insertParams{
		service:     service,
		quote:       quote,
		candidates:  candidates,
		customerID:  customerID,
		startsAt:    startsAt,
		endsAt:      endsAt,
		status:      status,
		attributes:  encodedAttributes,
		note:        in.Note,
		walkInName:  in.CustomerName,
		walkInPhone: in.CustomerPhone,
	})
	if err != nil {
		return nil, err
	}

	s.slots.InvalidateProvider(service.ProviderID)
	booking.Attributes = attributes
	return booking, nil
}

// resourcesForStart returns the resources free at startsAt, or OUTSIDE_HOURS.
func (s *Service) resourcesForStart(
	ctx context.Context,
	serviceID uuid.UUID,
	startsAt time.Time,
	location *time.Location,
	optionIDs []uuid.UUID,
) ([]uuid.UUID, error) {
	// The slot list is generated per local calendar day, so ask for the day the
	// requested start falls on in the provider's zone — not the caller's.
	localDay := startsAt.In(location)

	slots, err := s.slots.SlotsForBooking(ctx, serviceID, localDay, optionIDs)
	if err != nil {
		return nil, err
	}

	for _, slot := range slots {
		if slot.StartsAt.Equal(startsAt) {
			if len(slot.ResourceIDs) == 0 {
				return nil, httpx.SlotTaken()
			}
			return slot.ResourceIDs, nil
		}
	}

	// The start is not on the grid: outside opening hours, inside the lead
	// time, not on a step boundary, or already fully booked.
	return nil, httpx.OutsideHours()
}

type insertParams struct {
	service     *catalog.ServiceWithProvider
	quote       *catalog.Quote
	candidates  []uuid.UUID
	customerID  *uuid.UUID
	startsAt    time.Time
	endsAt      time.Time
	status      string
	attributes  []byte
	note        *string
	walkInName  *string
	walkInPhone *string
}

// insertBooking writes the booking and its option snapshot in one transaction,
// trying each candidate resource in turn.
//
// Walking the candidates matters at a multi-bay provider: if two customers race
// for 09:00 and there are three bays, the loser of the first bay should get the
// second, not a SLOT_TAKEN. Only when every candidate is gone is the slot
// genuinely taken.
func (s *Service) insertBooking(ctx context.Context, p insertParams) (*Booking, error) {
	for _, resourceID := range p.candidates {
		booking, err := s.tryResource(ctx, p, resourceID)
		if err != nil {
			return nil, err
		}
		if booking != nil {
			return booking, nil
		}
		// This bay went between the availability read and the insert. Try the
		// next one before giving up on the slot as a whole.
	}

	return nil, httpx.SlotTaken()
}

// tryResource attempts the insert against one resource.
//
// It returns (booking, nil) on success and (nil, nil) when this particular
// resource is taken — which is a signal to try the next candidate, not an
// error. Only a genuine failure comes back in the error slot.
func (s *Service) tryResource(ctx context.Context, p insertParams, resourceID uuid.UUID) (*Booking, error) {
	const maxAttempts = 3

	for attempt := range maxAttempts {
		created, err := s.insertOnce(ctx, p, resourceID)
		switch {
		case err == nil:
			return created, nil

		case database.IsExclusionViolation(err):
			// Someone committed an overlapping booking on this resource.
			return nil, nil

		case database.IsRetryable(err):
			// Deadlocked against a competing inserter. Nothing is wrong with
			// the request, so back off briefly and try the same resource again.
			if attempt == maxAttempts-1 {
				// Persistently contended: report it as the slot being gone
				// rather than as an internal error, and let the caller move on
				// to the next resource.
				return nil, nil
			}
			select {
			case <-ctx.Done():
				return nil, httpx.Internal(ctx.Err())
			case <-time.After(backoffFor(attempt)):
			}

		default:
			var apiErr *httpx.Error
			if errors.As(err, &apiErr) {
				return nil, apiErr
			}
			return nil, httpx.Internal(err)
		}
	}

	return nil, nil
}

// backoffFor spreads retries out so two deadlocking transactions do not simply
// collide again in lockstep.
func backoffFor(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * 10 * time.Millisecond
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(base)))
	if err != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())
}

// insertOnce writes the booking and its option snapshot in one transaction.
// The raw database error is returned unclassified so the caller can decide
// whether it means "try another resource", "retry", or "give up".
func (s *Service) insertOnce(ctx context.Context, p insertParams, resourceID uuid.UUID) (*Booking, error) {
	var created *Booking

	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		code, err := s.reserveCode(ctx, q)
		if err != nil {
			return err
		}

		row, err := q.CreateBooking(ctx, db.CreateBookingParams{
			Code:            code,
			ProviderID:      p.service.ProviderID,
			ServiceID:       &p.service.ID,
			ResourceID:      resourceID,
			CustomerID:      p.customerID,
			StartsAt:        p.startsAt,
			EndsAt:          p.endsAt,
			Status:          p.status,
			ServiceName:     p.service.Name,
			PriceCents:      p.quote.PriceCents,
			Currency:        p.quote.Currency,
			DurationMinutes: p.quote.DurationMinutes,
			Attributes:      p.attributes,
			CustomerNote:    trimPtr(p.note),
			CustomerName:    trimPtr(p.walkInName),
			CustomerPhone:   trimPtr(p.walkInPhone),
		})
		if err != nil {
			return err // classified by the caller
		}

		options := make([]BookedOption, 0, len(p.quote.Options))
		for i, option := range p.quote.Options {
			optionID := option.OptionID
			if err := q.AddBookingOption(ctx, db.AddBookingOptionParams{
				BookingID:            row.ID,
				OptionID:             &optionID,
				Name:                 option.Name,
				PriceDeltaCents:      option.PriceDeltaCents,
				DurationDeltaMinutes: option.DurationDeltaMinutes,
				SortOrder:            int32(i),
			}); err != nil {
				return httpx.Internal(err)
			}
			options = append(options, BookedOption{
				OptionID:             &optionID,
				Name:                 option.Name,
				PriceDeltaCents:      option.PriceDeltaCents,
				DurationDeltaMinutes: option.DurationDeltaMinutes,
			})
		}

		created = bookingFromRow(row)
		created.Options = options
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// reserveCode finds a booking code not already in use.
func (s *Service) reserveCode(ctx context.Context, q *db.Queries) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newBookingCode()
		if err != nil {
			return "", httpx.Internal(err)
		}
		exists, err := q.BookingCodeExists(ctx, code)
		if err != nil {
			return "", httpx.Internal(err)
		}
		if !exists {
			return code, nil
		}
	}
	return "", httpx.Internal(errors.New("booking: exhausted attempts generating a unique code"))
}

// --- transitions ----------------------------------------------------------

// TransitionInput carries the optional context of a cancellation.
type TransitionInput struct {
	Reason *string `json:"reason"`
}

// Transition applies a lifecycle event to a booking.
//
// The UPDATE is guarded on the status we read, so two staff members tapping
// "confirm" at once cannot both succeed: the second updates zero rows and is
// told the transition is no longer valid.
func (s *Service) Transition(ctx context.Context, bookingID uuid.UUID, event Event, in TransitionInput) (*Booking, error) {
	current, err := s.queries.GetBooking(ctx, bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Booking")
		}
		return nil, httpx.Internal(err)
	}

	next, err := Next(current.Status, event)
	if err != nil {
		return nil, err
	}

	params := db.TransitionBookingParams{
		ID:         bookingID,
		FromStatus: current.Status,
		ToStatus:   next,
	}
	if next == StatusCancelledByCustomer || next == StatusCancelledByProvider {
		actor := ActorCustomer
		if next == StatusCancelledByProvider {
			actor = ActorProvider
		}
		params.CancelledBy = &actor
		params.CancelReason = trimPtr(in.Reason)
	}

	row, err := s.queries.TransitionBooking(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Someone else moved it between our read and our write.
			return nil, httpx.InvalidTransition(current.Status, string(event))
		}
		return nil, httpx.Internal(err)
	}

	// A booking that stopped occupying its resource frees a slot.
	if IsActive(current.Status) && !IsActive(next) {
		s.slots.InvalidateProvider(row.ProviderID)
	}

	booking := bookingFromRow(row)
	if err := s.attachOptions(ctx, []*Booking{booking}); err != nil {
		return nil, err
	}
	return booking, nil
}

// CancelAsCustomer cancels a booking the caller owns.
func (s *Service) CancelAsCustomer(ctx context.Context, customerID, bookingID uuid.UUID, in TransitionInput) (*Booking, error) {
	row, err := s.queries.GetBooking(ctx, bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Booking")
		}
		return nil, httpx.Internal(err)
	}
	if row.CustomerID == nil || *row.CustomerID != customerID {
		// Do not reveal that someone else's booking exists.
		return nil, httpx.NotFound("Booking")
	}
	return s.Transition(ctx, bookingID, EventCancelByCustomer, in)
}

// --- reads ----------------------------------------------------------------

// GetForCustomer reads one of the caller's own bookings.
func (s *Service) GetForCustomer(ctx context.Context, customerID, bookingID uuid.UUID) (*Booking, error) {
	row, err := s.queries.GetBooking(ctx, bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Booking")
		}
		return nil, httpx.Internal(err)
	}
	if row.CustomerID == nil || *row.CustomerID != customerID {
		return nil, httpx.NotFound("Booking")
	}

	booking := bookingFromRow(row)
	if err := s.attachOptions(ctx, []*Booking{booking}); err != nil {
		return nil, err
	}
	return booking, nil
}

// GetForProvider reads one booking belonging to the caller's org.
func (s *Service) GetForProvider(ctx context.Context, providerID, bookingID uuid.UUID) (*Booking, error) {
	row, err := s.queries.GetBooking(ctx, bookingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Booking")
		}
		return nil, httpx.Internal(err)
	}
	if row.ProviderID != providerID {
		return nil, httpx.NotFound("Booking")
	}

	booking := bookingFromRow(row)
	if err := s.attachOptions(ctx, []*Booking{booking}); err != nil {
		return nil, err
	}
	return booking, nil
}

// ListForCustomer returns the caller's bookings, newest first.
func (s *Service) ListForCustomer(ctx context.Context, customerID uuid.UUID, status string, page httpx.Page) ([]Booking, int64, error) {
	rows, err := s.queries.ListBookingsByCustomer(ctx, db.ListBookingsByCustomerParams{
		CustomerID:   &customerID,
		Status:       status,
		ResultLimit:  page.Limit,
		ResultOffset: page.Offset,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	total, err := s.queries.CountBookingsByCustomer(ctx, db.CountBookingsByCustomerParams{
		CustomerID: &customerID,
		Status:     status,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	bookings := make([]Booking, 0, len(rows))
	pointers := make([]*Booking, 0, len(rows))
	for _, row := range rows {
		booking := Booking{
			ID: row.ID, Code: row.Code, ProviderID: row.ProviderID,
			ServiceID: row.ServiceID, ResourceID: row.ResourceID, CustomerID: row.CustomerID,
			StartsAt: row.StartsAt, EndsAt: row.EndsAt, Status: row.Status,
			ServiceName: row.ServiceName, PriceCents: row.PriceCents, Currency: row.Currency,
			DurationMinutes: row.DurationMinutes, Attributes: decodeJSON(row.Attributes),
			CustomerNote: row.CustomerNote, CancelledBy: row.CancelledBy,
			CancelReason: row.CancelReason,
			Provider: &ProviderSummary{
				ID: row.ProviderID, Slug: row.ProviderSlug, Name: row.ProviderName,
				LogoURL: row.ProviderLogoUrl, City: row.ProviderCity, Timezone: row.ProviderTimezone,
			},
			Options:   []BookedOption{},
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		bookings = append(bookings, booking)
	}
	for i := range bookings {
		pointers = append(pointers, &bookings[i])
	}
	if err := s.attachOptions(ctx, pointers); err != nil {
		return nil, 0, err
	}
	return bookings, total, nil
}

// ListForProviderInput filters the provider's bookings inbox.
type ListForProviderInput struct {
	Status     string
	ResourceID *uuid.UUID
	From       *time.Time
	To         *time.Time
	Page       httpx.Page
}

// ListForProvider returns an org's bookings in chronological order.
func (s *Service) ListForProvider(ctx context.Context, providerID uuid.UUID, in ListForProviderInput) ([]Booking, int64, error) {
	rows, err := s.queries.ListBookingsByProvider(ctx, db.ListBookingsByProviderParams{
		ProviderID:   providerID,
		Status:       in.Status,
		ResourceID:   in.ResourceID,
		FromTime:     in.From,
		ToTime:       in.To,
		ResultLimit:  in.Page.Limit,
		ResultOffset: in.Page.Offset,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	total, err := s.queries.CountBookingsByProvider(ctx, db.CountBookingsByProviderParams{
		ProviderID: providerID,
		Status:     in.Status,
		ResourceID: in.ResourceID,
		FromTime:   in.From,
		ToTime:     in.To,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	bookings := make([]Booking, 0, len(rows))
	for _, row := range rows {
		// A walk-in has no user row, so fall back to the name staff typed in.
		name, phone := row.CustomerUserName, row.CustomerUserPhone
		if name == nil {
			name = row.CustomerName
		}
		if phone == nil {
			phone = row.CustomerPhone
		}

		resourceName := row.ResourceName
		bookings = append(bookings, Booking{
			ID: row.ID, Code: row.Code, ProviderID: row.ProviderID,
			ServiceID: row.ServiceID, ResourceID: row.ResourceID, CustomerID: row.CustomerID,
			StartsAt: row.StartsAt, EndsAt: row.EndsAt, Status: row.Status,
			ServiceName: row.ServiceName, PriceCents: row.PriceCents, Currency: row.Currency,
			DurationMinutes: row.DurationMinutes, Attributes: decodeJSON(row.Attributes),
			CustomerNote: row.CustomerNote, CustomerName: name, CustomerPhone: phone,
			CancelledBy: row.CancelledBy, CancelReason: row.CancelReason,
			ResourceName: &resourceName, Options: []BookedOption{},
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}

	pointers := make([]*Booking, 0, len(bookings))
	for i := range bookings {
		pointers = append(pointers, &bookings[i])
	}
	if err := s.attachOptions(ctx, pointers); err != nil {
		return nil, 0, err
	}
	return bookings, total, nil
}

// Stats summarizes an org's bookings over an optional window.
func (s *Service) Stats(ctx context.Context, providerID uuid.UUID, from, to *time.Time) (*Stats, error) {
	row, err := s.queries.ProviderBookingStats(ctx, db.ProviderBookingStatsParams{
		ProviderID: providerID,
		FromTime:   from,
		ToTime:     to,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return &Stats{
		Pending:               row.PendingCount,
		Confirmed:             row.ConfirmedCount,
		InProgress:            row.InProgressCount,
		Completed:             row.CompletedCount,
		Cancelled:             row.CancelledCount,
		NoShow:                row.NoShowCount,
		CompletedRevenueCents: row.CompletedRevenueCents,
	}, nil
}

// attachOptions loads the option snapshots for a page of bookings in one query.
func (s *Service) attachOptions(ctx context.Context, bookings []*Booking) error {
	if len(bookings) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(bookings))
	byID := make(map[uuid.UUID]*Booking, len(bookings))
	for _, booking := range bookings {
		ids = append(ids, booking.ID)
		byID[booking.ID] = booking
		if booking.Options == nil {
			booking.Options = []BookedOption{}
		}
	}

	rows, err := s.queries.ListBookingOptions(ctx, ids)
	if err != nil {
		return httpx.Internal(err)
	}

	for _, row := range rows {
		booking, ok := byID[row.BookingID]
		if !ok {
			continue
		}
		booking.Options = append(booking.Options, BookedOption{
			OptionID:             row.OptionID,
			Name:                 row.Name,
			PriceDeltaCents:      row.PriceDeltaCents,
			DurationDeltaMinutes: row.DurationDeltaMinutes,
		})
	}
	return nil
}

func bookingFromRow(row db.Booking) *Booking {
	return &Booking{
		ID: row.ID, Code: row.Code, ProviderID: row.ProviderID,
		ServiceID: row.ServiceID, ResourceID: row.ResourceID, CustomerID: row.CustomerID,
		StartsAt: row.StartsAt, EndsAt: row.EndsAt, Status: row.Status,
		ServiceName: row.ServiceName, PriceCents: row.PriceCents, Currency: row.Currency,
		DurationMinutes: row.DurationMinutes, Attributes: decodeJSON(row.Attributes),
		CustomerNote: row.CustomerNote, CustomerName: row.CustomerName,
		CustomerPhone: row.CustomerPhone, CancelledBy: row.CancelledBy,
		CancelReason: row.CancelReason, Options: []BookedOption{},
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func decodeJSON(raw []byte) map[string]any {
	values := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	return values
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
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
