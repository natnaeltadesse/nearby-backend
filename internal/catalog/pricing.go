package catalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nearby/booking-backend/internal/platform/httpx"
)

// SelectedOption is one resolved add-on, priced from the catalog rather than
// from anything the client sent.
type SelectedOption struct {
	OptionID             uuid.UUID `json:"optionId"`
	GroupID              uuid.UUID `json:"groupId"`
	Name                 string    `json:"name"`
	PriceDeltaCents      int32     `json:"priceDeltaCents"`
	DurationDeltaMinutes int32     `json:"durationDeltaMinutes"`
}

// Quote is the server's computed total for a service plus a set of options.
type Quote struct {
	PriceCents int32 `json:"priceCents"`
	// Service duration plus option deltas. The scheduling buffer is added by
	// scheduling/, not here — buffer is a resource concern, not a price one.
	DurationMinutes int32            `json:"durationMinutes"`
	Currency        string           `json:"currency"`
	Options         []SelectedOption `json:"options"`
}

// BuildQuote resolves chosen option ids against a service's own option tree and
// computes the total price and duration.
//
// This is the "never trust client-sent totals" rule in code: the client sends
// ids and nothing else, and every number in the result is read from the catalog.
// It is a pure function, so the same computation backs both availability
// (which needs the duration) and booking creation (which needs both).
func BuildQuote(service Service, groups []OptionGroup, chosen []uuid.UUID) (*Quote, error) {
	optionsByID := make(map[uuid.UUID]Option)
	groupByID := make(map[uuid.UUID]OptionGroup, len(groups))
	for _, group := range groups {
		groupByID[group.ID] = group
		for _, option := range group.Options {
			optionsByID[option.ID] = option
		}
	}

	selected := make([]SelectedOption, 0, len(chosen))
	countByGroup := make(map[uuid.UUID]int, len(groups))
	seen := make(map[uuid.UUID]struct{}, len(chosen))

	for _, id := range chosen {
		if _, duplicate := seen[id]; duplicate {
			return nil, httpx.Validation("The same option was selected more than once")
		}
		seen[id] = struct{}{}

		option, ok := optionsByID[id]
		if !ok {
			// Either a bad id or an option belonging to a different service.
			// Both are the same mistake from the caller's side.
			return nil, httpx.Validation("One of the selected options does not belong to this service")
		}
		if !option.IsActive {
			return nil, httpx.Validation(fmt.Sprintf("The option %q is no longer available", option.Name))
		}

		countByGroup[option.GroupID]++
		selected = append(selected, SelectedOption{
			OptionID:             option.ID,
			GroupID:              option.GroupID,
			Name:                 option.Name,
			PriceDeltaCents:      option.PriceDeltaCents,
			DurationDeltaMinutes: option.DurationDeltaMinutes,
		})
	}

	if err := checkGroupRules(groups, countByGroup); err != nil {
		return nil, err
	}

	price := service.PriceCents
	duration := service.DurationMinutes
	for _, option := range selected {
		price += option.PriceDeltaCents
		duration += option.DurationDeltaMinutes
	}

	// A combination that prices below zero or collapses the duration means the
	// provider's option deltas are misconfigured. Refusing is better than
	// silently booking a free or zero-length appointment.
	if price < 0 {
		return nil, httpx.Validation("That combination of options is not purchasable; please contact the provider")
	}
	if duration <= 0 {
		return nil, httpx.Validation("That combination of options leaves no time to perform the service")
	}

	// Stable order so the booking_options snapshot reads the same way twice.
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].Name < selected[j].Name
	})

	return &Quote{
		PriceCents:      price,
		DurationMinutes: duration,
		Currency:        service.Currency,
		Options:         selected,
	}, nil
}

func checkGroupRules(groups []OptionGroup, countByGroup map[uuid.UUID]int) error {
	for _, group := range groups {
		count := countByGroup[group.ID]

		minimum := group.MinSelect
		if group.IsRequired && minimum < 1 {
			minimum = 1
		}

		if int32(count) < minimum {
			return httpx.Validation(fmt.Sprintf(
				"%s: choose at least %s", group.Name, plural(minimum, "option")))
		}

		if group.SelectionType == SelectionSingle && count > 1 {
			return httpx.Validation(fmt.Sprintf("%s: choose only one option", group.Name))
		}

		if group.MaxSelect != nil && int32(count) > *group.MaxSelect {
			return httpx.Validation(fmt.Sprintf(
				"%s: choose at most %s", group.Name, plural(*group.MaxSelect, "option")))
		}
	}
	return nil
}

func plural(n int32, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, strings.TrimSuffix(noun, "s"))
}
