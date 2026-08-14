// Package integration runs the API against a real Postgres+PostGIS container.
//
// These tests exist because the interesting guarantees in this backend are not
// Go-level ones: the exclusion constraint that prevents double booking, the
// membership check behind `x-organization-id`, and the attribute validator all
// have to be exercised against the real schema to mean anything.
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nearby/booking-backend/internal/auth"
	"github.com/nearby/booking-backend/internal/booking"
	"github.com/nearby/booking-backend/internal/catalog"
	httpapi "github.com/nearby/booking-backend/internal/http"
	"github.com/nearby/booking-backend/internal/media"
	"github.com/nearby/booking-backend/internal/platform/config"
	"github.com/nearby/booking-backend/internal/platform/database"
	"github.com/nearby/booking-backend/internal/scheduling"
	"github.com/nearby/booking-backend/internal/tenant"
)

// The image must be the PostGIS one: the schema needs postgis and btree_gist.
const postgresImage = "postgis/postgis:16-3.4"

var (
	testPool        *pgxpool.Pool
	testDatabaseURL string
)

func TestMain(m *testing.M) {
	// Match the API process, which pins UTC so the wire format is stable.
	time.Local = time.UTC

	// testing.Short() is only readable after flag parsing, which TestMain must
	// trigger itself.
	flag.Parse()
	if testing.Short() {
		// `make test` runs -short and must not need Docker, so the container
		// is never started. The tests still run and skip themselves through
		// newServer — exiting straight to a zero code here would report a pass
		// for work that was never attempted.
		os.Exit(m.Run())
	}

	ctx := context.Background()

	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("booking_test"),
		postgres.WithUsername("booking"),
		postgres.WithPassword("booking"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		os.Exit(1)
	}

	code := func() int {
		defer func() {
			_ = testcontainers.TerminateContainer(container)
		}()

		testDatabaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
			return 1
		}

		// The migrations create their own extensions, so no init script is
		// needed here — which is exactly the property migration 00001 exists
		// to guarantee.
		if err := migrate(testDatabaseURL); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			return 1
		}

		testPool, err = database.Connect(ctx, testDatabaseURL, 10)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect pool: %v\n", err)
			return 1
		}
		defer testPool.Close()

		return m.Run()
	}()

	os.Exit(code)
}

func migrate(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, "../../migrations")
}

// --- server -----------------------------------------------------------------

// testServer is an httptest server wired exactly like cmd/api, so the tests
// exercise the same middleware chains that production does.
type testServer struct {
	*httptest.Server
	pool  *pgxpool.Pool
	codes *capturedCodes
}

// capturedCodes is the test's auth.CodeSender: it keeps what would have been
// sent, so a test can act on a verification code the way a real recipient
// would. Guarded by a mutex because sign-ups happen on server goroutines.
type capturedCodes struct {
	mu   sync.Mutex
	sent []sentCode
}

type sentCode struct {
	Channel     string
	Destination string
	Code        string
}

func (c *capturedCodes) SendCode(_ context.Context, channel, destination, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentCode{Channel: channel, Destination: destination, Code: code})
	return nil
}

// latestFor returns the most recent code sent to destination, which is the
// only one still live: issuing a code retires the ones before it.
func (c *capturedCodes) latestFor(t *testing.T, destination string) string {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	for i := len(c.sent) - 1; i >= 0; i-- {
		if strings.EqualFold(c.sent[i].Destination, destination) {
			return c.sent[i].Code
		}
	}

	t.Fatalf("no verification code was sent to %s", destination)
	return ""
}

// newServer truncates the database and returns a fresh API.
//
// Truncating per test rather than sharing state keeps each case readable, and
// the container is reused so the cost is a few milliseconds.
func newServer(t *testing.T) *testServer {
	t.Helper()

	// Set only when TestMain started a container. Every test enters through
	// here, so this one guard is what makes `-short` skip the whole suite
	// visibly rather than silently.
	if testPool == nil {
		t.Skip("integration test: needs Docker; run without -short")
	}

	truncateAll(t)

	cfg := config.Config{
		Env:                   "test",
		LogLevel:              "error",
		JWTSecret:             "test-secret-that-is-long-enough",
		JWTIssuer:             "booking-api-test",
		AccessTokenTTL:        15 * time.Minute,
		RefreshTokenTTLWeb:    720 * time.Hour,
		RefreshTokenTTLMobile: 2160 * time.Hour,
		DefaultTimezone:       "Africa/Addis_Ababa",
		SlotStepMinutes:       15,
		MinLeadMinutes:        30,
		// Disabled in effect: a cached slot list would mask the very races
		// these tests are here to check.
		AvailabilityCacheTTL: time.Nanosecond,
		CORSAllowedOrigins:   []string{"*"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.AccessTokenTTL)

	// Stands in for the SMS/email provider, and lets a test read the code that
	// production would have delivered.
	codes := &capturedCodes{}

	authService := auth.NewService(testPool, issuer, codes, logger,
		cfg.RefreshTokenTTLWeb, cfg.RefreshTokenTTLMobile)
	tenantService := tenant.NewService(testPool, cfg.DefaultTimezone, cfg.MinLeadMinutes)
	catalogService := catalog.New(testPool)
	scheduler := scheduling.New(testPool, catalogService, scheduling.Options{
		SlotStepMinutes: cfg.SlotStepMinutes,
		CacheTTL:        cfg.AvailabilityCacheTTL,
	})
	bookingService := booking.NewService(testPool, catalogService, scheduler)

	// Uploads land in the test's own temp dir, so nothing leaks between runs.
	localMedia, err := media.NewLocalStorage(t.TempDir(), "/media")
	require.NoError(t, err)
	mediaService := media.NewService(testPool, localMedia, logger)

	handler := httpapi.NewRouter(cfg, logger, testPool, httpapi.Services{
		Auth:       authService,
		Tenant:     tenantService,
		Catalog:    catalogService,
		Scheduler:  scheduler,
		Booking:    bookingService,
		Media:      mediaService,
		LocalMedia: localMedia,
		Issuer:     issuer,
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &testServer{Server: server, pool: testPool, codes: codes}
}

// truncateAll empties every table. `migrations` is excluded so goose does not
// re-run.
func truncateAll(t *testing.T) {
	t.Helper()

	_, err := testPool.Exec(context.Background(), `
		TRUNCATE
			booking_options, bookings,
			schedule_exceptions, business_hours, resource_services, resources,
			service_options, option_groups, services,
			category_attributes, provider_categories, categories,
			invitations, members, providers,
			refresh_tokens, users
		RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
}

// --- HTTP helpers -----------------------------------------------------------

// response is a decoded API reply, kept deliberately loose so a test can assert
// on the error envelope and the success body with the same helper.
type response struct {
	Status int
	Body   map[string]any
	Raw    string
}

// Code returns the error envelope's code, or "" for a success.
func (r response) Code() string {
	if code, ok := r.Body["code"].(string); ok {
		return code
	}
	return ""
}

// Message returns the error envelope's message.
func (r response) Message() string {
	if message, ok := r.Body["message"].(string); ok {
		return message
	}
	return ""
}

// String makes a failed assertion show what the server actually said.
func (r response) String() string {
	return fmt.Sprintf("HTTP %d %s", r.Status, r.Raw)
}

// requestOptions carries the headers that distinguish the four surfaces.
type requestOptions struct {
	token   string
	orgID   string
	headers map[string]string
}

type option func(*requestOptions)

func withToken(token string) option { return func(o *requestOptions) { o.token = token } }
func withOrg(orgID string) option   { return func(o *requestOptions) { o.orgID = orgID } }

// withHeader sets one header, for the requests that are not JSON — multipart
// uploads carry a generated boundary in their content type.
func withHeader(name, value string) option {
	return func(o *requestOptions) {
		if o.headers == nil {
			o.headers = map[string]string{}
		}
		o.headers[name] = value
	}
}

func (s *testServer) do(t *testing.T, method, path string, body any, opts ...option) response {
	t.Helper()

	var options requestOptions
	for _, opt := range opts {
		opt(&options)
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequest(method, s.URL+path, reader)
	require.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if options.token != "" {
		req.Header.Set("Authorization", "Bearer "+options.token)
	}
	if options.orgID != "" {
		req.Header.Set("x-organization-id", options.orgID)
	}

	for name, value := range options.headers {
		req.Header.Set(name, value)
	}

	resp, err := s.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := response{Status: resp.StatusCode, Raw: string(raw)}
	if len(raw) > 0 {
		// A non-JSON body is a failure worth seeing verbatim, so decoding
		// errors are ignored here and surface through Raw.
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

func (s *testServer) get(t *testing.T, path string, opts ...option) response {
	t.Helper()
	return s.do(t, http.MethodGet, path, nil, opts...)
}

func (s *testServer) post(t *testing.T, path string, body any, opts ...option) response {
	t.Helper()
	return s.do(t, http.MethodPost, path, body, opts...)
}

func (s *testServer) patch(t *testing.T, path string, body any, opts ...option) response {
	t.Helper()
	return s.do(t, http.MethodPatch, path, body, opts...)
}

func (s *testServer) delete(t *testing.T, path string, opts ...option) response {
	t.Helper()
	return s.do(t, http.MethodDelete, path, nil, opts...)
}

// doRaw sends a pre-encoded body, for requests that are not JSON.
func (s *testServer) doRaw(t *testing.T, method, path string, body []byte, opts ...option) response {
	t.Helper()

	var options requestOptions
	for _, opt := range opts {
		opt(&options)
	}

	req, err := http.NewRequest(method, s.URL+path, bytes.NewReader(body))
	require.NoError(t, err)

	if options.token != "" {
		req.Header.Set("Authorization", "Bearer "+options.token)
	}
	if options.orgID != "" {
		req.Header.Set("x-organization-id", options.orgID)
	}
	for name, value := range options.headers {
		req.Header.Set(name, value)
	}

	resp, err := s.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := response{Status: resp.StatusCode, Raw: string(raw)}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

// getRaw fetches a non-API path (an uploaded image, say) and hands back the
// response itself, headers included.
func (s *testServer) getRaw(t *testing.T, path string) *http.Response {
	t.Helper()

	resp, err := s.Client().Get(s.URL + path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// --- assertions -------------------------------------------------------------

func requireStatus(t *testing.T, resp response, want int) {
	t.Helper()
	require.Equal(t, want, resp.Status, "unexpected status: %s", resp)
}

// requireError asserts both halves of the error envelope contract at once.
func requireError(t *testing.T, resp response, wantStatus int, wantCode string) {
	t.Helper()
	require.Equal(t, wantStatus, resp.Status, "unexpected status: %s", resp)
	require.Equal(t, wantCode, resp.Code(), "unexpected code: %s", resp)
	require.NotEmpty(t, resp.Message(), "error envelope must carry a message: %s", resp)
}

// --- data helpers -----------------------------------------------------------

type testUser struct {
	ID           string
	Email        string
	AccessToken  string
	RefreshToken string
}

// signUp registers a customer account and returns its tokens.
func (s *testServer) signUp(t *testing.T, name, email string) testUser {
	t.Helper()

	resp := s.post(t, "/api/v1/auth/sign-up", map[string]any{
		"name":     name,
		"email":    email,
		"password": "password123",
	})
	requireStatus(t, resp, http.StatusCreated)

	user := resp.Body["user"].(map[string]any)
	tokens := resp.Body["tokens"].(map[string]any)

	return testUser{
		ID:           user["id"].(string),
		Email:        email,
		AccessToken:  tokens["accessToken"].(string),
		RefreshToken: tokens["refreshToken"].(string),
	}
}

// signIn authenticates an existing account created by this harness.
func (s *testServer) signIn(t *testing.T, email string) testUser {
	t.Helper()
	return s.signInWith(t, email, "password123")
}

// signInWith authenticates with an explicit password, for accounts whose
// credentials the harness did not choose.
func (s *testServer) signInWith(t *testing.T, email, password string) testUser {
	t.Helper()

	resp := s.post(t, "/api/v1/auth/sign-in", map[string]any{
		"email":    email,
		"password": password,
	})
	requireStatus(t, resp, http.StatusOK)

	user := resp.Body["user"].(map[string]any)
	tokens := resp.Body["tokens"].(map[string]any)

	return testUser{
		ID:           user["id"].(string),
		Email:        email,
		AccessToken:  tokens["accessToken"].(string),
		RefreshToken: tokens["refreshToken"].(string),
	}
}

// promoteToAdmin makes a user a platform admin and re-authenticates so the new
// role is in the token.
func (s *testServer) promoteToAdmin(t *testing.T, user testUser) testUser {
	t.Helper()

	_, err := testPool.Exec(context.Background(),
		`UPDATE users SET platform_role = 'admin' WHERE id = $1`, user.ID)
	require.NoError(t, err)

	return s.signIn(t, user.Email)
}

// setProviderStatus flips a provider's status directly, for tests that need an
// active tenant without going through the admin approval flow.
func setProviderStatus(t *testing.T, providerID, status string) {
	t.Helper()

	_, err := testPool.Exec(context.Background(),
		`UPDATE providers SET status = $2 WHERE id = $1`, providerID, status)
	require.NoError(t, err)
}
