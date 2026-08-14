package integration

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes builds a real PNG, because the API sniffs content rather than
// trusting the declared type — a fake body would be rejected, correctly.
func pngBytes(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 10, G: 200, B: 90, A: 255})

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// uploadImage posts one file to a service's gallery.
func (s *testServer) uploadImage(
	t *testing.T, serviceID, filename string, data []byte, caption string, opts ...option,
) response {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	part, err := form.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(data)
	require.NoError(t, err)

	if caption != "" {
		require.NoError(t, form.WriteField("caption", caption))
	}
	require.NoError(t, form.Close())

	opts = append(opts, withHeader("Content-Type", form.FormDataContentType()))
	return s.doRaw(t, http.MethodPost,
		"/api/v1/org/services/"+serviceID+"/media", body.Bytes(), opts...)
}

func TestIntegrationServiceGallery(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")
	orgAuth := f.orgAuth()

	upload := s.uploadImage(t, f.serviceID, "work.png", pngBytes(t), "Fresh cut", orgAuth...)
	requireStatus(t, upload, http.StatusCreated)

	imageURL, _ := upload.Body["imageUrl"].(string)
	require.NotEmpty(t, imageURL)
	assert.Equal(t, "Fresh cut", upload.Body["caption"])
	assert.NotContains(t, upload.Raw, "imagePublicId",
		"the storage key is internal and must not leak to clients")

	t.Run("the bytes come back from the url it handed out", func(t *testing.T) {
		resp := s.getRaw(t, imageURL)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
			"user content served from our own origin must not be sniffed")
	})

	t.Run("it is listed in order", func(t *testing.T) {
		second := s.uploadImage(t, f.serviceID, "b.png", pngBytes(t), "", orgAuth...)
		requireStatus(t, second, http.StatusCreated)

		list := s.get(t, "/api/v1/org/services/"+f.serviceID+"/media", orgAuth...)
		requireStatus(t, list, http.StatusOK)

		images := list.Body["images"].([]any)
		require.Len(t, images, 2)
		assert.Equal(t, float64(0), images[0].(map[string]any)["sortOrder"])
		assert.Equal(t, float64(1), images[1].(map[string]any)["sortOrder"])
	})

	t.Run("a non-image is refused whatever it claims to be", func(t *testing.T) {
		resp := s.uploadImage(t, f.serviceID, "payload.png",
			[]byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"),
			"", orgAuth...)
		requireError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	})

	t.Run("deleting removes the row and the bytes", func(t *testing.T) {
		mediaID := upload.Body["id"].(string)

		requireStatus(t, s.delete(t,
			"/api/v1/org/services/"+f.serviceID+"/media/"+mediaID, orgAuth...),
			http.StatusNoContent)

		gone := s.getRaw(t, imageURL)
		assert.Equal(t, http.StatusNotFound, gone.StatusCode)
	})
}

// A gallery belongs to its tenant: a service id from another org must not open
// one, even for a signed-in owner of a different business.
func TestIntegrationGalleryIsTenantIsolated(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	outsider := s.signUp(t, "Outsider", "outsider@example.et")
	created := s.post(t, "/api/v1/me/providers",
		map[string]any{"name": "Rival Wash"}, withToken(outsider.AccessToken))
	requireStatus(t, created, http.StatusCreated)
	rivalID := created.Body["id"].(string)

	outsider = s.signIn(t, outsider.Email)
	rivalAuth := []option{withToken(outsider.AccessToken), withOrg(rivalID)}

	// f.serviceID belongs to the fixture's provider, not this one.
	requireError(t, s.get(t,
		"/api/v1/org/services/"+f.serviceID+"/media", rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")

	requireError(t, s.uploadImage(t, f.serviceID, "x.png", pngBytes(t), "", rivalAuth...),
		http.StatusNotFound, "NOT_FOUND")
}

// Staff may look at the gallery but not change it — the same gate as the rest
// of the catalog.
func TestIntegrationGalleryWritesNeedManager(t *testing.T) {
	s := newServer(t)
	f := newFixture(t, s, "instant")

	staff := s.signUp(t, "Staff", "gallery-staff@shinewash.et")
	_, err := testPool.Exec(t.Context(),
		`INSERT INTO members (provider_id, user_id, role) VALUES ($1, $2, 'staff')`,
		f.providerID, staff.ID)
	require.NoError(t, err)
	staff = s.signIn(t, staff.Email)
	staffAuth := []option{withToken(staff.AccessToken), withOrg(f.providerID)}

	requireStatus(t, s.get(t,
		"/api/v1/org/services/"+f.serviceID+"/media", staffAuth...), http.StatusOK)

	requireError(t, s.uploadImage(t, f.serviceID, "x.png", pngBytes(t), "", staffAuth...),
		http.StatusForbidden, "FORBIDDEN")
}
