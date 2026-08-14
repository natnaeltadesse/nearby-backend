package tenant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/db"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/platform/httpx"
)

const invitationTTL = 7 * 24 * time.Hour

// Service implements provider, membership and invitation use cases.
type Service struct {
	pool            *pgxpool.Pool
	queries         *db.Queries
	defaultTimezone string
	defaultLead     int32
}

// NewService wires the tenant service.
func NewService(pool *pgxpool.Pool, defaultTimezone string, defaultMinLeadMinutes int) *Service {
	return &Service{
		pool:            pool,
		queries:         db.New(pool),
		defaultTimezone: defaultTimezone,
		defaultLead:     int32(defaultMinLeadMinutes),
	}
}

// CreateProviderInput registers a new tenant.
type CreateProviderInput struct {
	Name           string      `json:"name"`
	Slug           *string     `json:"slug"`
	Phone          *string     `json:"phone"`
	Email          *string     `json:"email"`
	Description    *string     `json:"description"`
	City           *string     `json:"city"`
	Address        *string     `json:"address"`
	Location       *Location   `json:"location"`
	Timezone       *string     `json:"timezone"`
	LicenseNumber  *string     `json:"licenseNumber"`
	BookingMode    *string     `json:"bookingMode"`
	MinLeadMinutes *int32      `json:"minLeadMinutes"`
	CategoryIDs    []uuid.UUID `json:"categoryIds"`
}

// CreateProvider creates a tenant and, when ownerID is set, makes that user its
// owner in the same transaction — a provider without an owner is unreachable.
//
// New providers always start `pending`: going live is a platform-admin decision.
func (s *Service) CreateProvider(ctx context.Context, in CreateProviderInput, ownerID *uuid.UUID) (*Provider, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, httpx.Validation("Name is required")
	}

	timezone := s.defaultTimezone
	if in.Timezone != nil && strings.TrimSpace(*in.Timezone) != "" {
		timezone = strings.TrimSpace(*in.Timezone)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, httpx.Validation(fmt.Sprintf("%q is not a known IANA timezone", timezone))
	}

	bookingMode := ModeRequest
	if in.BookingMode != nil {
		bookingMode = strings.TrimSpace(*in.BookingMode)
		if bookingMode != ModeRequest && bookingMode != ModeInstant {
			return nil, httpx.Validation("bookingMode must be 'request' or 'instant'")
		}
	}

	minLead := s.defaultLead
	if in.MinLeadMinutes != nil {
		if *in.MinLeadMinutes < 0 {
			return nil, httpx.Validation("minLeadMinutes cannot be negative")
		}
		minLead = *in.MinLeadMinutes
	}

	if in.Location != nil {
		if err := validateLocation(*in.Location); err != nil {
			return nil, err
		}
	}

	slug, err := s.resolveSlug(ctx, in.Slug, name)
	if err != nil {
		return nil, err
	}

	params := db.CreateProviderParams{
		Slug:           slug,
		Name:           name,
		Phone:          trimPtr(in.Phone),
		Email:          lowerPtr(in.Email),
		Description:    trimPtr(in.Description),
		City:           trimPtr(in.City),
		Address:        trimPtr(in.Address),
		Timezone:       timezone,
		LicenseNumber:  trimPtr(in.LicenseNumber),
		BookingMode:    bookingMode,
		MinLeadMinutes: minLead,
	}
	if in.Location != nil {
		params.Lat = &in.Location.Lat
		params.Lng = &in.Location.Lng
	}

	var provider *Provider

	err = database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		row, err := q.CreateProvider(ctx, params)
		if err != nil {
			if database.IsUniqueViolation(err) {
				return httpx.Conflict(fmt.Sprintf("The slug %q is already taken", slug))
			}
			return httpx.Internal(err)
		}

		if ownerID != nil {
			if _, err := q.AddMember(ctx, db.AddMemberParams{
				ProviderID: row.ID,
				UserID:     *ownerID,
				Role:       RoleOwner,
			}); err != nil {
				return httpx.Internal(err)
			}
		}

		for _, categoryID := range in.CategoryIDs {
			if err := q.AddProviderCategory(ctx, db.AddProviderCategoryParams{
				ProviderID: row.ID,
				CategoryID: categoryID,
			}); err != nil {
				if database.IsForeignKeyViolation(err) {
					return httpx.Validation("One of the supplied categoryIds does not exist")
				}
				return httpx.Internal(err)
			}
		}

		provider = providerFromCreateRow(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// GetProvider reads one provider by id.
func (s *Service) GetProvider(ctx context.Context, id uuid.UUID) (*Provider, error) {
	row, err := s.queries.GetProviderByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Provider")
		}
		return nil, httpx.Internal(err)
	}
	return providerFromGetRow(row), nil
}

// GetProviderBySlug reads one provider by its public slug.
func (s *Service) GetProviderBySlug(ctx context.Context, slug string) (*Provider, error) {
	row, err := s.queries.GetProviderBySlug(ctx, strings.TrimSpace(slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Provider")
		}
		return nil, httpx.Internal(err)
	}
	return providerFromSlugRow(row), nil
}

// UpdateProviderInput patches a provider profile. Nil fields are left alone.
type UpdateProviderInput struct {
	Name           *string   `json:"name"`
	Phone          *string   `json:"phone"`
	Email          *string   `json:"email"`
	Description    *string   `json:"description"`
	City           *string   `json:"city"`
	Address        *string   `json:"address"`
	Location       *Location `json:"location"`
	Timezone       *string   `json:"timezone"`
	LicenseNumber  *string   `json:"licenseNumber"`
	BookingMode    *string   `json:"bookingMode"`
	MinLeadMinutes *int32    `json:"minLeadMinutes"`
}

// UpdateProvider applies a partial update to a provider profile.
func (s *Service) UpdateProvider(ctx context.Context, id uuid.UUID, in UpdateProviderInput) (*Provider, error) {
	if in.Timezone != nil {
		if _, err := time.LoadLocation(strings.TrimSpace(*in.Timezone)); err != nil {
			return nil, httpx.Validation(fmt.Sprintf("%q is not a known IANA timezone", *in.Timezone))
		}
	}
	if in.BookingMode != nil {
		mode := strings.TrimSpace(*in.BookingMode)
		if mode != ModeRequest && mode != ModeInstant {
			return nil, httpx.Validation("bookingMode must be 'request' or 'instant'")
		}
	}
	if in.MinLeadMinutes != nil && *in.MinLeadMinutes < 0 {
		return nil, httpx.Validation("minLeadMinutes cannot be negative")
	}
	if in.Location != nil {
		if err := validateLocation(*in.Location); err != nil {
			return nil, err
		}
	}

	params := db.UpdateProviderParams{
		ID:             id,
		Name:           trimPtr(in.Name),
		Phone:          trimPtr(in.Phone),
		Email:          lowerPtr(in.Email),
		Description:    trimPtr(in.Description),
		City:           trimPtr(in.City),
		Address:        trimPtr(in.Address),
		Timezone:       trimPtr(in.Timezone),
		LicenseNumber:  trimPtr(in.LicenseNumber),
		BookingMode:    trimPtr(in.BookingMode),
		MinLeadMinutes: in.MinLeadMinutes,
	}
	if in.Location != nil {
		params.Lat = &in.Location.Lat
		params.Lng = &in.Location.Lng
	}

	row, err := s.queries.UpdateProvider(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Provider")
		}
		return nil, httpx.Internal(err)
	}
	return providerFromUpdateRow(row), nil
}

// SetStatus approves or suspends a provider. Platform-admin only.
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, status string) (*Provider, error) {
	switch status {
	case StatusPending, StatusActive, StatusSuspended:
	default:
		return nil, httpx.Validation("status must be 'pending', 'active' or 'suspended'")
	}

	if _, err := s.queries.SetProviderStatus(ctx, db.SetProviderStatusParams{ID: id, Status: status}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("Provider")
		}
		return nil, httpx.Internal(err)
	}
	return s.GetProvider(ctx, id)
}

// ListProvidersInput filters the admin provider list.
type ListProvidersInput struct {
	Status string
	Search string
	Limit  int32
	Offset int32
}

// ListProviders is the platform-admin listing. Customer-facing search lives in
// discovery/, which has entirely different filters.
func (s *Service) ListProviders(ctx context.Context, in ListProvidersInput) ([]Provider, int64, error) {
	rows, err := s.queries.ListProviders(ctx, db.ListProvidersParams{
		Status:       in.Status,
		Search:       in.Search,
		ResultLimit:  in.Limit,
		ResultOffset: in.Offset,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	total, err := s.queries.CountProviders(ctx, db.CountProvidersParams{
		Status: in.Status,
		Search: in.Search,
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}

	providers := make([]Provider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, Provider{
			ID:          row.ID,
			Slug:        row.Slug,
			Name:        row.Name,
			Phone:       row.Phone,
			Email:       row.Email,
			City:        row.City,
			Address:     row.Address,
			Location:    locationFrom(row.HasLocation, row.Lat, row.Lng),
			Timezone:    row.Timezone,
			LogoURL:     row.LogoUrl,
			Status:      row.Status,
			RatingAvg:   row.RatingAvg,
			RatingCount: row.RatingCount,
			BookingMode: row.BookingMode,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return providers, total, nil
}

// --- membership -----------------------------------------------------------

// Membership returns the caller's role at providerID, or NOT_A_MEMBER.
//
// This is the database check behind every /org/* request. The JWT carries a
// memberships claim too, but that snapshot can be up to one access-token TTL
// stale, so it is never the authority for access decisions.
func (s *Service) Membership(ctx context.Context, providerID, userID uuid.UUID) (string, error) {
	row, err := s.queries.GetMembership(ctx, db.GetMembershipParams{
		ProviderID: providerID,
		UserID:     userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", httpx.NotAMember()
		}
		return "", httpx.Internal(err)
	}
	return row.Role, nil
}

// ListMembers returns a provider's staff.
func (s *Service) ListMembers(ctx context.Context, providerID uuid.UUID) ([]Member, error) {
	rows, err := s.queries.ListMembers(ctx, providerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	members := make([]Member, 0, len(rows))
	for _, row := range rows {
		members = append(members, Member{
			ProviderID: row.ProviderID,
			UserID:     row.UserID,
			Role:       row.Role,
			Name:       row.Name,
			Email:      row.Email,
			Phone:      row.Phone,
			CreatedAt:  row.CreatedAt,
		})
	}
	return members, nil
}

// CreateMemberInput provisions a staff account from the org side.
type CreateMemberInput struct {
	Name  string  `json:"name"`
	Email string  `json:"email"`
	Phone *string `json:"phone"`
	// Password is optional: leaving it out has the server generate one and
	// return it in the response, which is the only time it is ever readable.
	Password *string `json:"password"`
	Role     string  `json:"role"`
}

// CreateMember creates a login for someone the org is hiring and makes them a
// member, both in one transaction — a user row created here has no purpose
// outside the membership, so a half-applied version of this is worse than none.
//
// This is the counterpart to CreateInvitation, for the common case where the
// owner is sitting next to the new employee and will hand over the credentials
// themselves. Invitations remain the right tool when the person already has an
// account, which is why an address that is already registered is refused here
// rather than quietly attached: joining an org is the account holder's
// decision, not the owner's.
//
// Promotion to owner deliberately is not available at creation time. Handing
// out full control of a tenant should be a deliberate second step through
// UpdateMemberRole, not a field on a form filled in during onboarding.
func (s *Service) CreateMember(ctx context.Context, providerID uuid.UUID, in CreateMemberInput) (*Member, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", httpx.Validation("Name is required")
	}

	email := auth.NormalizeEmail(in.Email)
	if err := auth.ValidateEmail(email); err != nil {
		return nil, "", err
	}

	switch in.Role {
	case RoleAdmin, RoleStaff:
	case RoleOwner:
		return nil, "", httpx.Validation(
			"A new account cannot start as an owner; create them as a manager first, then promote")
	default:
		return nil, "", httpx.Validation("role must be 'admin' or 'staff'")
	}

	// A generated password is returned to the caller; a supplied one never is,
	// because whoever typed it already knows it.
	password := ""
	generated := ""
	if in.Password != nil {
		password = *in.Password
		if err := auth.ValidatePassword(password); err != nil {
			return nil, "", err
		}
	} else {
		var err error
		if generated, err = auth.GeneratePassword(); err != nil {
			return nil, "", httpx.Internal(err)
		}
		password = generated
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, "", httpx.Internal(err)
	}

	var member *Member

	err = database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		user, err := q.CreateUser(ctx, db.CreateUserParams{
			Name:         name,
			Email:        email,
			Phone:        trimPtr(in.Phone),
			PasswordHash: hash,
			PlatformRole: auth.RoleUser,
		})
		if err != nil {
			if database.IsUniqueViolation(err) {
				return httpx.Conflict(
					"Someone already has an account with that email — invite them instead")
			}
			return httpx.Internal(err)
		}

		row, err := q.AddMember(ctx, db.AddMemberParams{
			ProviderID: providerID,
			UserID:     user.ID,
			Role:       in.Role,
		})
		if err != nil {
			return httpx.Internal(err)
		}

		member = &Member{
			ProviderID: row.ProviderID,
			UserID:     row.UserID,
			Role:       row.Role,
			Name:       user.Name,
			Email:      user.Email,
			Phone:      user.Phone,
			CreatedAt:  row.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return member, generated, nil
}

// UpdateMemberRole changes a member's role, refusing to demote the last owner.
func (s *Service) UpdateMemberRole(ctx context.Context, providerID, userID uuid.UUID, role string) (*Member, error) {
	if !ValidRole(role) {
		return nil, httpx.Validation("role must be 'owner', 'admin' or 'staff'")
	}

	var member *Member

	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		current, err := q.GetMembership(ctx, db.GetMembershipParams{ProviderID: providerID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("Member")
			}
			return httpx.Internal(err)
		}

		if current.Role == RoleOwner && role != RoleOwner {
			owners, err := q.CountOwners(ctx, providerID)
			if err != nil {
				return httpx.Internal(err)
			}
			if owners <= 1 {
				return httpx.Conflict("A provider must keep at least one owner")
			}
		}

		row, err := q.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{
			ProviderID: providerID,
			UserID:     userID,
			Role:       role,
		})
		if err != nil {
			return httpx.Internal(err)
		}

		member = &Member{
			ProviderID: row.ProviderID,
			UserID:     row.UserID,
			Role:       row.Role,
			CreatedAt:  row.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return member, nil
}

// RemoveMember detaches a user from a provider, refusing to strand it ownerless.
func (s *Service) RemoveMember(ctx context.Context, providerID, userID uuid.UUID) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		current, err := q.GetMembership(ctx, db.GetMembershipParams{ProviderID: providerID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("Member")
			}
			return httpx.Internal(err)
		}

		if current.Role == RoleOwner {
			owners, err := q.CountOwners(ctx, providerID)
			if err != nil {
				return httpx.Internal(err)
			}
			if owners <= 1 {
				return httpx.Conflict("A provider must keep at least one owner")
			}
		}

		affected, err := q.RemoveMember(ctx, db.RemoveMemberParams{ProviderID: providerID, UserID: userID})
		if err != nil {
			return httpx.Internal(err)
		}
		if affected == 0 {
			return httpx.NotFound("Member")
		}
		return nil
	})
}

// --- invitations ----------------------------------------------------------

// CreateInvitation issues a membership invitation and returns the one-time
// token. Only the token's hash is stored, so this is the only moment the
// plaintext exists — the caller is responsible for delivering it.
func (s *Service) CreateInvitation(ctx context.Context, providerID uuid.UUID, email, role string) (*Invitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, httpx.Validation("Email is required")
	}
	if !ValidRole(role) {
		return nil, httpx.Validation("role must be 'owner', 'admin' or 'staff'")
	}

	token, hash, err := auth.NewRefreshToken() // same 256-bit opaque-token generator
	if err != nil {
		return nil, httpx.Internal(err)
	}

	row, err := s.queries.CreateInvitation(ctx, db.CreateInvitationParams{
		ProviderID: providerID,
		Email:      email,
		Role:       role,
		TokenHash:  hash,
		ExpiresAt:  time.Now().Add(invitationTTL),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, httpx.Conflict("There is already a pending invitation for that email")
		}
		return nil, httpx.Internal(err)
	}

	return &Invitation{
		ID:         row.ID,
		ProviderID: row.ProviderID,
		Email:      row.Email,
		Role:       row.Role,
		ExpiresAt:  row.ExpiresAt,
		AcceptedAt: row.AcceptedAt,
		CreatedAt:  row.CreatedAt,
		Token:      token,
	}, nil
}

// ListInvitations returns a provider's invitations, newest first.
func (s *Service) ListInvitations(ctx context.Context, providerID uuid.UUID) ([]Invitation, error) {
	rows, err := s.queries.ListInvitations(ctx, providerID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	invitations := make([]Invitation, 0, len(rows))
	for _, row := range rows {
		invitations = append(invitations, Invitation{
			ID:         row.ID,
			ProviderID: row.ProviderID,
			Email:      row.Email,
			Role:       row.Role,
			ExpiresAt:  row.ExpiresAt,
			AcceptedAt: row.AcceptedAt,
			CreatedAt:  row.CreatedAt,
		})
	}
	return invitations, nil
}

// RevokeInvitation deletes a pending invitation.
func (s *Service) RevokeInvitation(ctx context.Context, providerID, invitationID uuid.UUID) error {
	affected, err := s.queries.DeleteInvitation(ctx, db.DeleteInvitationParams{
		ID:         invitationID,
		ProviderID: providerID,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	if affected == 0 {
		return httpx.NotFound("Invitation")
	}
	return nil
}

// AcceptInvitation turns a signed-in user into a member. The invitation's
// email must match the accepting account, so a leaked token cannot be redeemed
// by whoever finds it.
func (s *Service) AcceptInvitation(ctx context.Context, token string, userID uuid.UUID, userEmail string) (*Member, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, httpx.Validation("token is required")
	}

	var member *Member

	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		invitation, err := q.GetInvitationByTokenHash(ctx, auth.HashRefreshToken(token))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.InvalidToken()
			}
			return httpx.Internal(err)
		}

		if invitation.AcceptedAt != nil {
			return httpx.Conflict("That invitation has already been accepted")
		}
		if time.Now().After(invitation.ExpiresAt) {
			return httpx.TokenExpired()
		}
		if !strings.EqualFold(invitation.Email, userEmail) {
			return httpx.Forbidden("That invitation was issued to a different email address")
		}

		if _, err := q.AcceptInvitation(ctx, invitation.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.Conflict("That invitation is no longer valid")
			}
			return httpx.Internal(err)
		}

		row, err := q.AddMember(ctx, db.AddMemberParams{
			ProviderID: invitation.ProviderID,
			UserID:     userID,
			Role:       invitation.Role,
		})
		if err != nil {
			return httpx.Internal(err)
		}

		member = &Member{
			ProviderID: row.ProviderID,
			UserID:     row.UserID,
			Role:       row.Role,
			CreatedAt:  row.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return member, nil
}

// --- helpers --------------------------------------------------------------

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// resolveSlug uses the caller's slug when given, otherwise derives one from
// the name and appends a counter until it is free.
func (s *Service) resolveSlug(ctx context.Context, requested *string, name string) (string, error) {
	if requested != nil {
		slug := Slugify(*requested)
		if slug == "" {
			return "", httpx.Validation("slug must contain at least one letter or digit")
		}
		return slug, nil
	}

	base := Slugify(name)
	if base == "" {
		base = "provider"
	}

	candidate := base
	for attempt := 2; attempt < 100; attempt++ {
		exists, err := s.queries.SlugExists(ctx, candidate)
		if err != nil {
			return "", httpx.Internal(err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", httpx.Conflict("Could not derive a free slug from that name; please supply one")
}

// Slugify reduces a string to a lowercase, hyphen-separated identifier.
func Slugify(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	lowered = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) && r > unicode.MaxASCII {
			return -1 // drop non-ASCII letters rather than mangle them
		}
		return r
	}, lowered)
	return strings.Trim(slugUnsafe.ReplaceAllString(lowered, "-"), "-")
}

func validateLocation(loc Location) error {
	if loc.Lat < -90 || loc.Lat > 90 {
		return httpx.Validation("location.lat must be between -90 and 90")
	}
	if loc.Lng < -180 || loc.Lng > 180 {
		return httpx.Validation("location.lng must be between -180 and 180")
	}
	return nil
}

func locationFrom(hasLocation bool, lat, lng float64) *Location {
	if !hasLocation {
		return nil
	}
	return &Location{Lat: lat, Lng: lng}
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

func lowerPtr(s *string) *string {
	trimmed := trimPtr(s)
	if trimmed == nil {
		return nil
	}
	lowered := strings.ToLower(*trimmed)
	return &lowered
}
