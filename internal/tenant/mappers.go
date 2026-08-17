package tenant

import "github.com/nearby/booking-backend/internal/db"

// sqlc emits a distinct row type per query, so each provider-shaped result
// needs its own conversion. They are mechanical on purpose: the compiler
// catches a column added to one query and forgotten in another.

func providerFromCreateRow(row db.CreateProviderRow) *Provider {
	return &Provider{
		ID:             row.ID,
		Slug:           row.Slug,
		Name:           row.Name,
		Phone:          row.Phone,
		Email:          row.Email,
		Description:    row.Description,
		City:           row.City,
		Address:        row.Address,
		Location:       locationFrom(row.HasLocation, row.Lat, row.Lng),
		Timezone:       row.Timezone,
		LogoURL:        row.LogoUrl,
		CoverURL:       row.CoverUrl,
		LicenseNumber:  row.LicenseNumber,
		Status:         row.Status,
		RatingAvg:      row.RatingAvg,
		RatingCount:    row.RatingCount,
		BookingMode:    row.BookingMode,
		MinLeadMinutes: row.MinLeadMinutes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func providerFromGetRow(row db.GetProviderByIDRow) *Provider {
	return &Provider{
		ID:             row.ID,
		Slug:           row.Slug,
		Name:           row.Name,
		Phone:          row.Phone,
		Email:          row.Email,
		Description:    row.Description,
		City:           row.City,
		Address:        row.Address,
		Location:       locationFrom(row.HasLocation, row.Lat, row.Lng),
		Timezone:       row.Timezone,
		LogoURL:        row.LogoUrl,
		CoverURL:       row.CoverUrl,
		LicenseNumber:  row.LicenseNumber,
		Status:         row.Status,
		RatingAvg:      row.RatingAvg,
		RatingCount:    row.RatingCount,
		BookingMode:    row.BookingMode,
		MinLeadMinutes: row.MinLeadMinutes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func providerFromSlugRow(row db.GetProviderBySlugRow) *Provider {
	return &Provider{
		ID:             row.ID,
		Slug:           row.Slug,
		Name:           row.Name,
		Phone:          row.Phone,
		Email:          row.Email,
		Description:    row.Description,
		City:           row.City,
		Address:        row.Address,
		Location:       locationFrom(row.HasLocation, row.Lat, row.Lng),
		Timezone:       row.Timezone,
		LogoURL:        row.LogoUrl,
		CoverURL:       row.CoverUrl,
		LicenseNumber:  row.LicenseNumber,
		Status:         row.Status,
		RatingAvg:      row.RatingAvg,
		RatingCount:    row.RatingCount,
		BookingMode:    row.BookingMode,
		MinLeadMinutes: row.MinLeadMinutes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func providerFromUpdateRow(row db.UpdateProviderRow) *Provider {
	return &Provider{
		ID:             row.ID,
		Slug:           row.Slug,
		Name:           row.Name,
		Phone:          row.Phone,
		Email:          row.Email,
		Description:    row.Description,
		City:           row.City,
		Address:        row.Address,
		Location:       locationFrom(row.HasLocation, row.Lat, row.Lng),
		Timezone:       row.Timezone,
		LogoURL:        row.LogoUrl,
		CoverURL:       row.CoverUrl,
		LicenseNumber:  row.LicenseNumber,
		Status:         row.Status,
		RatingAvg:      row.RatingAvg,
		RatingCount:    row.RatingCount,
		BookingMode:    row.BookingMode,
		MinLeadMinutes: row.MinLeadMinutes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
