package scheduling

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// availabilityCache memoizes slot lists per (service, date, option set) for a
// short window, as spec §9 calls for.
//
// Correctness never depends on it: a stale hit can only cost a customer one
// rejected booking, because the exclusion constraint is what actually decides
// who gets a slot. That is why a plain in-process map is enough — entries do
// not need to be consistent across replicas, only fresh-ish.
type availabilityCache struct {
	ttl time.Duration

	mu      sync.RWMutex
	entries map[string]cacheEntry
	// Reverse index so a booking write can drop everything for one provider
	// without walking the whole map.
	byProvider map[uuid.UUID]map[string]struct{}
}

type cacheEntry struct {
	slots      []Slot
	providerID uuid.UUID
	expiresAt  time.Time
}

func newAvailabilityCache(ttl time.Duration) *availabilityCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &availabilityCache{
		ttl:        ttl,
		entries:    make(map[string]cacheEntry),
		byProvider: make(map[uuid.UUID]map[string]struct{}),
	}
}

func (c *availabilityCache) get(serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID) ([]Slot, bool) {
	key := cacheKey(serviceID, date, optionIDs)

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}

	// Copy: callers may sort or filter, and a cached slice is shared.
	slots := make([]Slot, len(entry.slots))
	copy(slots, entry.slots)
	return slots, true
}

func (c *availabilityCache) put(serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID, providerID uuid.UUID, slots []Slot) {
	key := cacheKey(serviceID, date, optionIDs)

	stored := make([]Slot, len(slots))
	copy(stored, slots)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		slots:      stored,
		providerID: providerID,
		expiresAt:  time.Now().Add(c.ttl),
	}
	if c.byProvider[providerID] == nil {
		c.byProvider[providerID] = make(map[string]struct{})
	}
	c.byProvider[providerID][key] = struct{}{}

	c.evictExpiredLocked()
}

func (c *availabilityCache) invalidateProvider(providerID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.byProvider[providerID] {
		delete(c.entries, key)
	}
	delete(c.byProvider, providerID)
}

// evictExpiredLocked keeps the map from growing without bound. Called on write
// under the lock; the cost is proportional to the map, which stays small
// because entries live for a minute.
func (c *availabilityCache) evictExpiredLocked() {
	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
			delete(c.byProvider[entry.providerID], key)
		}
	}
}

// cacheKey is order-independent in the option set: picking wax then wax-and-
// interior in the other order is the same availability question.
func cacheKey(serviceID uuid.UUID, date time.Time, optionIDs []uuid.UUID) string {
	ids := make([]string, 0, len(optionIDs))
	for _, id := range optionIDs {
		ids = append(ids, id.String())
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString(serviceID.String())
	b.WriteByte('|')
	b.WriteString(date.Format(time.DateOnly))
	b.WriteByte('|')
	b.WriteString(strings.Join(ids, ","))
	return b.String()
}
