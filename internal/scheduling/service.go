package scheduling

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// Scheduler implements the scheduling use cases.
type Scheduler struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	services ServiceResolver
	step     time.Duration
	cache    *availabilityCache
}

// Options configure a Scheduler.
type Options struct {
	SlotStepMinutes int
	CacheTTL        time.Duration
}

// New wires the scheduler.
func New(pool *pgxpool.Pool, services ServiceResolver, opts Options) *Scheduler {
	step := time.Duration(opts.SlotStepMinutes) * time.Minute
	if step <= 0 {
		step = 15 * time.Minute
	}
	return &Scheduler{
		pool:     pool,
		queries:  db.New(pool),
		services: services,
		step:     step,
		cache:    newAvailabilityCache(opts.CacheTTL),
	}
}

// --- availability ---------------------------------------------------------

// Availability implements spec §9 end to end.
//
// The returned slots carry only start times and candidate resources. The
// client picks a start; the server re-derives everything at booking time and
// lets the exclusion constraint arbitrate. A slot list that is a minute stale
// is therefore harmless — it can cost a customer a retry, never a double
// booking.
func (s *Scheduler) Availability(ctx context.Context, serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID) ([]Slot, error) {
	return s.availability(ctx, serviceID, date, optionIDs, false)
}

// SlotsForBooking is Availability with the cache bypassed.
//
// booking/ re-derives the slot list to check the start a client sent back. A
// cached list could be up to a TTL stale, and while a stale hit costs a browsing
// customer nothing, here it would reject a slot that has since freed up. The
// exclusion constraint still has the final word either way.
func (s *Scheduler) SlotsForBooking(ctx context.Context, serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID) ([]Slot, error) {
	return s.availability(ctx, serviceID, date, optionIDs, true)
}

// availability generates a day's slots.
//
// forBooking distinguishes the two callers. Browsing wants a cached list of
// what can be taken; reserving wants a fresh list that also includes positions
// which are inside opening hours but full, so the caller can answer
// SLOT_TAKEN rather than OUTSIDE_HOURS.
func (s *Scheduler) availability(ctx context.Context, serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID, forBooking bool) ([]Slot, error) {
	// 1. how long the resource is occupied for, options and buffer included
	resolved, err := s.services.ResolveScheduling(ctx, serviceID, optionIDs)
	if err != nil {
		return nil, err
	}
	if !resolved.Bookable {
		switch resolved.UnbookableCode {
		case httpx.CodeServiceInactive:
			return nil, httpx.ServiceInactive()
		default:
			return nil, httpx.ProviderInactive()
		}
	}

	location, err := time.LoadLocation(resolved.Timezone)
	if err != nil {
		return nil, httpx.Internal(fmt.Errorf("provider timezone %q: %w", resolved.Timezone, err))
	}

	if !forBooking {
		if cached, ok := s.cache.get(serviceID, date, optionIDs); ok {
			return cached, nil
		}
	}

	// 2. resources that can actually perform this service
	resourceRows, err := s.queries.ListResourcesForService(ctx, serviceID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if len(resourceRows) == 0 {
		// No resource is assigned to this service, so nothing is bookable.
		// Not an error: an empty day is a legitimate answer.
		return []Slot{}, nil
	}

	resourceIDs := make([]uuid.UUID, 0, len(resourceRows))
	for _, row := range resourceRows {
		resourceIDs = append(resourceIDs, row.ID)
	}

	year, month, day := date.In(location).Date()
	dayStart := time.Date(year, month, day, 0, 0, 0, 0, location)
	dayEnd := dayStart.AddDate(0, 0, 1)

	// 3a. the day's windows: exceptions override the weekly pattern
	hourRows, err := s.queries.ListBusinessHoursForDay(ctx, db.ListBusinessHoursForDayParams{
		ProviderID:  resolved.ProviderID,
		Weekday:     int32(dayStart.Weekday()),
		ResourceIds: resourceIDs,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	exceptionRows, err := s.queries.ListScheduleExceptionsForDate(ctx, db.ListScheduleExceptionsForDateParams{
		ProviderID: resolved.ProviderID,
		Date:       dayStart,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	hours := make([]HoursRule, 0, len(hourRows))
	for _, row := range hourRows {
		hours = append(hours, HoursRule{
			ID:         row.ID,
			ProviderID: row.ProviderID,
			ResourceID: row.ResourceID,
			Weekday:    int(row.Weekday),
			Opens:      TimeOfDay(row.OpensMinute),
			Closes:     TimeOfDay(row.ClosesMinute),
		})
	}

	exceptions := make([]ExceptionRule, 0, len(exceptionRows))
	for _, row := range exceptionRows {
		exceptions = append(exceptions, ExceptionRule{
			ID:         row.ID,
			ProviderID: row.ProviderID,
			ResourceID: row.ResourceID,
			IsClosed:   row.IsClosed,
			Opens:      timeOfDayPtr(row.OpensAt),
			Closes:     timeOfDayPtr(row.ClosesAt),
			Reason:     row.Reason,
		})
	}

	windows := ResolveWindows(resourceIDs, hours, exceptions, int(dayStart.Weekday()))
	if len(windows) == 0 {
		return []Slot{}, nil
	}

	// 3b. what already occupies those resources
	busyRows, err := s.queries.ListBusyIntervals(ctx, db.ListBusyIntervalsParams{
		ResourceIds: resourceIDs,
		WindowStart: dayStart,
		WindowEnd:   dayEnd,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	busy := make([]BusyInterval, 0, len(busyRows))
	for _, row := range busyRows {
		busy = append(busy, BusyInterval{
			ResourceID: row.ResourceID,
			Start:      row.StartsAt,
			End:        row.EndsAt,
		})
	}

	// 4 & 5. generate, drop anything inside the lead time, return sorted starts
	slots := GenerateSlots(SlotRequest{
		Date:               dayStart,
		Location:           location,
		TotalDuration:      resolved.TotalDuration,
		Step:               s.step,
		EarliestStart:      time.Now().Add(time.Duration(resolved.MinLeadMinutes) * time.Minute),
		Windows:            windows,
		Busy:               busy,
		IncludeFullyBooked: forBooking,
	})

	if !forBooking {
		s.cache.put(serviceID, date, optionIDs, resolved.ProviderID, slots)
	}
	return slots, nil
}

// InvalidateProvider drops cached availability for a provider. booking/ calls
// this after any write that changes what is free.
func (s *Scheduler) InvalidateProvider(providerID uuid.UUID) {
	s.cache.invalidateProvider(providerID)
}

// --- resources ------------------------------------------------------------

// CreateResourceInput describes a new bookable resource.
type CreateResourceInput struct {
	Name       string      `json:"name"`
	UserID     *uuid.UUID  `json:"userId"`
	IsActive   *bool       `json:"isActive"`
	ServiceIDs []uuid.UUID `json:"serviceIds"`
}

// CreateResource adds a resource and optionally attaches it to services.
func (s *Scheduler) CreateResource(ctx context.Context, providerID uuid.UUID, in CreateResourceInput) (*Resource, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}

	row, err := s.queries.CreateResource(ctx, db.CreateResourceParams{
		ProviderID: providerID,
		Name:       name,
		UserID:     in.UserID,
		IsActive:   valueOr(in.IsActive, true),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	for _, serviceID := range in.ServiceIDs {
		if err := s.queries.AddResourceService(ctx, db.AddResourceServiceParams{
			ResourceID: row.ID,
			ServiceID:  serviceID,
		}); err != nil {
			return nil, httpx.Internal(err)
		}
	}

	s.cache.invalidateProvider(providerID)

	return &Resource{
		ID: row.ID, ProviderID: row.ProviderID, Name: row.Name,
		UserID: row.UserID, IsActive: row.IsActive,
		ServiceIDs: in.ServiceIDs,
		CreatedAt:  row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// ListResources returns a provider's resources with their service attachments.
func (s *Scheduler) ListResources(ctx context.Context, providerID uuid.UUID, activeOnly bool) ([]Resource, error) {
	rows, err := s.queries.ListResourcesByProvider(ctx, db.ListResourcesByProviderParams{
		ProviderID: providerID,
		ActiveOnly: activeOnly,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	resources := make([]Resource, 0, len(rows))
	for _, row := range rows {
		serviceIDs, err := s.queries.ListServiceIDsForResource(ctx, row.ID)
		if err != nil {
			return nil, httpx.Internal(err)
		}
		resources = append(resources, Resource{
			ID: row.ID, ProviderID: row.ProviderID, Name: row.Name,
			UserID: row.UserID, IsActive: row.IsActive, ServiceIDs: serviceIDs,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return resources, nil
}

// UpdateResourceInput patches a resource.
//
// UnassignUser exists because every other field here means "leave it alone"
// when omitted, which leaves no way to say "belongs to nobody now". Setting it
// detaches the staff login and overrides UserID.
type UpdateResourceInput struct {
	Name         *string    `json:"name"`
	UserID       *uuid.UUID `json:"userId"`
	UnassignUser bool       `json:"unassignUser"`
	IsActive     *bool      `json:"isActive"`
}

// UpdateResource applies a partial update.
func (s *Scheduler) UpdateResource(ctx context.Context, providerID, id uuid.UUID, in UpdateResourceInput) (*Resource, error) {
	row, err := s.queries.UpdateResource(ctx, db.UpdateResourceParams{
		ID:           id,
		ProviderID:   providerID,
		Name:         trimPtr(in.Name),
		UserID:       in.UserID,
		UnassignUser: in.UnassignUser,
		IsActive:     in.IsActive,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Resource")
		}
		return nil, httpx.Internal(err)
	}

	serviceIDs, err := s.queries.ListServiceIDsForResource(ctx, row.ID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	s.cache.invalidateProvider(providerID)

	return &Resource{
		ID: row.ID, ProviderID: row.ProviderID, Name: row.Name,
		UserID: row.UserID, IsActive: row.IsActive, ServiceIDs: serviceIDs,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// DeleteResource removes a resource. Bookings reference resources with
// ON DELETE RESTRICT, so one with history cannot be deleted — deactivate it.
func (s *Scheduler) DeleteResource(ctx context.Context, providerID, id uuid.UUID) error {
	affected, err := s.queries.DeleteResource(ctx, db.DeleteResourceParams{ID: id, ProviderID: providerID})
	if err != nil {
		return httpx.Conflict("That resource has bookings; deactivate it instead")
	}
	if affected == 0 {
		return httpx.NotFound("Resource")
	}
	s.cache.invalidateProvider(providerID)
	return nil
}

// AttachService makes a resource able to perform a service.
func (s *Scheduler) AttachService(ctx context.Context, providerID, resourceID, serviceID uuid.UUID) error {
	if err := s.assertResourceOwned(ctx, providerID, resourceID); err != nil {
		return err
	}
	if err := s.queries.AddResourceService(ctx, db.AddResourceServiceParams{
		ResourceID: resourceID,
		ServiceID:  serviceID,
	}); err != nil {
		return httpx.Validation("serviceId does not refer to an existing service")
	}
	s.cache.invalidateProvider(providerID)
	return nil
}

// DetachService stops a resource from performing a service.
func (s *Scheduler) DetachService(ctx context.Context, providerID, resourceID, serviceID uuid.UUID) error {
	if err := s.assertResourceOwned(ctx, providerID, resourceID); err != nil {
		return err
	}
	affected, err := s.queries.RemoveResourceService(ctx, db.RemoveResourceServiceParams{
		ResourceID: resourceID,
		ServiceID:  serviceID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Service attachment")
	}
	s.cache.invalidateProvider(providerID)
	return nil
}

// --- business hours -------------------------------------------------------

// HoursInput describes one weekly opening span.
type HoursInput struct {
	ResourceID *uuid.UUID `json:"resourceId"`
	Weekday    int        `json:"weekday"`
	Opens      TimeOfDay  `json:"opensAt"`
	Closes     TimeOfDay  `json:"closesAt"`
}

// CreateHours adds a weekly opening span.
func (s *Scheduler) CreateHours(ctx context.Context, providerID uuid.UUID, in HoursInput) (*HoursRule, error) {
	if in.Weekday < 0 || in.Weekday > 6 {
		return nil, httpx.Validation("weekday must be between 0 (Sunday) and 6")
	}
	if !in.Opens.Valid() || !in.Closes.Valid() {
		return nil, httpx.Validation("opensAt and closesAt must be times of day")
	}
	if in.Closes <= in.Opens {
		return nil, httpx.Validation("closesAt must be after opensAt")
	}
	if in.ResourceID != nil {
		if err := s.assertResourceOwned(ctx, providerID, *in.ResourceID); err != nil {
			return nil, err
		}
	}

	row, err := s.queries.CreateBusinessHours(ctx, db.CreateBusinessHoursParams{
		ProviderID:   providerID,
		ResourceID:   in.ResourceID,
		Weekday:      int32(in.Weekday),
		OpensMinute:  int32(in.Opens),
		ClosesMinute: int32(in.Closes),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	s.cache.invalidateProvider(providerID)

	return &HoursRule{
		ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
		Weekday: int(row.Weekday), Opens: TimeOfDay(row.OpensMinute), Closes: TimeOfDay(row.ClosesMinute),
	}, nil
}

// ListHours returns a provider's whole weekly pattern.
func (s *Scheduler) ListHours(ctx context.Context, providerID uuid.UUID) ([]HoursRule, error) {
	rows, err := s.queries.ListBusinessHours(ctx, providerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	hours := make([]HoursRule, 0, len(rows))
	for _, row := range rows {
		hours = append(hours, HoursRule{
			ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
			Weekday: int(row.Weekday), Opens: TimeOfDay(row.OpensMinute), Closes: TimeOfDay(row.ClosesMinute),
		})
	}
	return hours, nil
}

// ReplaceHoursInput rewrites one scope's whole weekly pattern.
//
// A null ResourceID targets the provider-wide default; a set one targets that
// resource's override. The other scopes are left untouched either way, so
// saving the shop's week does not silently wipe a barber's own hours.
type ReplaceHoursInput struct {
	ResourceID *uuid.UUID   `json:"resourceId"`
	Hours      []HoursInput `json:"hours"`
}

// ReplaceHours swaps a scope's weekly pattern for the one supplied.
//
// An hours editor saves a week, not a row, and doing that as a stream of
// creates and deletes would leave the schedule visibly wrong in between — a
// customer refreshing mid-save could find the shop closed all week. The delete
// and the inserts therefore share one transaction: either the new week is in
// place or the old one still is.
func (s *Scheduler) ReplaceHours(ctx context.Context, providerID uuid.UUID, in ReplaceHoursInput) ([]HoursRule, error) {
	for _, span := range in.Hours {
		if span.Weekday < 0 || span.Weekday > 6 {
			return nil, httpx.Validation("weekday must be between 0 (Sunday) and 6")
		}
		if !span.Opens.Valid() || !span.Closes.Valid() {
			return nil, httpx.Validation("opensAt and closesAt must be times of day")
		}
		if span.Closes <= span.Opens {
			return nil, httpx.Validation("closesAt must be after opensAt")
		}
	}

	// Two spans on one day that overlap would generate the same slot twice.
	// The database has no constraint for this — it is a rule about a set of
	// rows, not about one — so it is enforced here.
	if err := assertNoOverlap(in.Hours); err != nil {
		return nil, err
	}

	if in.ResourceID != nil {
		if err := s.assertResourceOwned(ctx, providerID, *in.ResourceID); err != nil {
			return nil, err
		}
	}

	rules := make([]HoursRule, 0, len(in.Hours))

	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		if _, err := q.DeleteBusinessHoursForScope(ctx, db.DeleteBusinessHoursForScopeParams{
			ProviderID: providerID,
			ResourceID: in.ResourceID,
		}); err != nil {
			return httpx.Internal(err)
		}

		for _, span := range in.Hours {
			row, err := q.CreateBusinessHours(ctx, db.CreateBusinessHoursParams{
				ProviderID:   providerID,
				ResourceID:   in.ResourceID,
				Weekday:      int32(span.Weekday),
				OpensMinute:  int32(span.Opens),
				ClosesMinute: int32(span.Closes),
			})
			if err != nil {
				return httpx.Internal(err)
			}

			rules = append(rules, HoursRule{
				ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
				Weekday: int(row.Weekday),
				Opens:   TimeOfDay(row.OpensMinute), Closes: TimeOfDay(row.ClosesMinute),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.cache.invalidateProvider(providerID)
	return rules, nil
}

// assertNoOverlap refuses two spans on the same weekday that share a minute.
func assertNoOverlap(spans []HoursInput) error {
	byDay := make(map[int][]HoursInput, 7)
	for _, span := range spans {
		byDay[span.Weekday] = append(byDay[span.Weekday], span)
	}

	for _, day := range byDay {
		sort.Slice(day, func(i, j int) bool { return day[i].Opens < day[j].Opens })
		for i := 1; i < len(day); i++ {
			if day[i].Opens < day[i-1].Closes {
				return httpx.Validation("two opening times on the same day overlap")
			}
		}
	}
	return nil
}

// UpdateHoursInput patches one weekly opening span.
type UpdateHoursInput struct {
	Weekday *int       `json:"weekday"`
	Opens   *TimeOfDay `json:"opensAt"`
	Closes  *TimeOfDay `json:"closesAt"`
}

// UpdateHours applies a partial update to a weekly opening span.
func (s *Scheduler) UpdateHours(ctx context.Context, providerID, id uuid.UUID, in UpdateHoursInput) (*HoursRule, error) {
	if in.Weekday != nil && (*in.Weekday < 0 || *in.Weekday > 6) {
		return nil, httpx.Validation("weekday must be between 0 (Sunday) and 6")
	}

	var weekday *int32
	if in.Weekday != nil {
		value := int32(*in.Weekday)
		weekday = &value
	}

	row, err := s.queries.UpdateBusinessHours(ctx, db.UpdateBusinessHoursParams{
		ID:           id,
		ProviderID:   providerID,
		Weekday:      weekday,
		OpensMinute:  minutePtr(in.Opens),
		ClosesMinute: minutePtr(in.Closes),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Business hours")
		}
		if strings.Contains(err.Error(), "business_hours_opens_before_closes") {
			return nil, httpx.Validation("closesAt must be after opensAt")
		}
		return nil, httpx.Internal(err)
	}

	s.cache.invalidateProvider(providerID)

	return &HoursRule{
		ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
		Weekday: int(row.Weekday), Opens: TimeOfDay(row.OpensMinute), Closes: TimeOfDay(row.ClosesMinute),
	}, nil
}

// DeleteHours removes a weekly opening span.
func (s *Scheduler) DeleteHours(ctx context.Context, providerID, id uuid.UUID) error {
	affected, err := s.queries.DeleteBusinessHours(ctx, db.DeleteBusinessHoursParams{ID: id, ProviderID: providerID})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Business hours")
	}
	s.cache.invalidateProvider(providerID)
	return nil
}

// --- exceptions -----------------------------------------------------------

// ExceptionInput describes a one-date override.
type ExceptionInput struct {
	ResourceID *uuid.UUID `json:"resourceId"`
	Date       string     `json:"date"`
	IsClosed   *bool      `json:"isClosed"`
	Opens      *TimeOfDay `json:"opensAt"`
	Closes     *TimeOfDay `json:"closesAt"`
	Reason     *string    `json:"reason"`
}

// CreateException adds a one-date override to the weekly pattern.
func (s *Scheduler) CreateException(ctx context.Context, providerID uuid.UUID, in ExceptionInput) (*ExceptionRule, error) {
	date, err := time.Parse(time.DateOnly, strings.TrimSpace(in.Date))
	if err != nil {
		return nil, httpx.Validation("date must be formatted as YYYY-MM-DD")
	}

	isClosed := valueOr(in.IsClosed, true)
	if isClosed {
		if in.Opens != nil || in.Closes != nil {
			return nil, httpx.Validation("a closed exception cannot carry opening times")
		}
	} else {
		if in.Opens == nil || in.Closes == nil {
			return nil, httpx.Validation("an open exception needs both opensAt and closesAt")
		}
		if *in.Closes <= *in.Opens {
			return nil, httpx.Validation("closesAt must be after opensAt")
		}
	}

	if in.ResourceID != nil {
		if err := s.assertResourceOwned(ctx, providerID, *in.ResourceID); err != nil {
			return nil, err
		}
	}

	row, err := s.queries.CreateScheduleException(ctx, db.CreateScheduleExceptionParams{
		ProviderID:   providerID,
		ResourceID:   in.ResourceID,
		Date:         date,
		IsClosed:     isClosed,
		OpensMinute:  minutePtr(in.Opens),
		ClosesMinute: minutePtr(in.Closes),
		Reason:       trimPtr(in.Reason),
	})
	if err != nil {
		if strings.Contains(err.Error(), "schedule_exceptions_") {
			return nil, httpx.Conflict("There is already an exception for that date and scope")
		}
		return nil, httpx.Internal(err)
	}

	s.cache.invalidateProvider(providerID)

	return &ExceptionRule{
		ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
		Date: row.Date.Format(time.DateOnly), IsClosed: row.IsClosed,
		Opens: timeOfDayPtr(row.OpensAt), Closes: timeOfDayPtr(row.ClosesAt),
		Reason: row.Reason,
	}, nil
}

// ListExceptions returns a provider's overrides, optionally within a range.
func (s *Scheduler) ListExceptions(ctx context.Context, providerID uuid.UUID, from, to *time.Time) ([]ExceptionRule, error) {
	rows, err := s.queries.ListScheduleExceptions(ctx, db.ListScheduleExceptionsParams{
		ProviderID: providerID,
		FromDate:   from,
		ToDate:     to,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	exceptions := make([]ExceptionRule, 0, len(rows))
	for _, row := range rows {
		exceptions = append(exceptions, ExceptionRule{
			ID: row.ID, ProviderID: row.ProviderID, ResourceID: row.ResourceID,
			Date: row.Date.Format(time.DateOnly), IsClosed: row.IsClosed,
			Opens: timeOfDayPtr(row.OpensAt), Closes: timeOfDayPtr(row.ClosesAt),
			Reason: row.Reason,
		})
	}
	return exceptions, nil
}

// DeleteException removes a one-date override.
func (s *Scheduler) DeleteException(ctx context.Context, providerID, id uuid.UUID) error {
	affected, err := s.queries.DeleteScheduleException(ctx, db.DeleteScheduleExceptionParams{
		ID:         id,
		ProviderID: providerID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Schedule exception")
	}
	s.cache.invalidateProvider(providerID)
	return nil
}

// --- helpers --------------------------------------------------------------

func (s *Scheduler) assertResourceOwned(ctx context.Context, providerID, resourceID uuid.UUID) error {
	row, err := s.queries.GetResource(ctx, resourceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("Resource")
		}
		return httpx.Internal(err)
	}
	if row.ProviderID != providerID {
		return httpx.NotFound("Resource")
	}
	return nil
}

// timeOfDayPtr converts a nullable `time` column into a TimeOfDay.
//
// Exception times are nullable — a closed exception carries none — so they
// cross the sqlc boundary as pgtype.Time rather than as the integer minutes
// used for the NOT NULL business_hours columns.
func timeOfDayPtr(t pgtype.Time) *TimeOfDay {
	if !t.Valid {
		return nil
	}
	value := TimeOfDay(t.Microseconds / int64(time.Minute/time.Microsecond))
	return &value
}

func minutePtr(t *TimeOfDay) *int32 {
	if t == nil {
		return nil
	}
	value := int32(*t)
	return &value
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

func valueOr[T any](ptr *T, fallback T) T {
	if ptr == nil {
		return fallback
	}
	return *ptr
}
