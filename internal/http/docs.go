package httpapi

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nearby/booking-backend/api"
)

// docsPage renders api/openapi.yaml with Swagger UI.
//
// Swagger UI rather than a read-only viewer because the "Try it out" panel is
// what makes this useful while developing: it sends real requests, and the
// bearer scheme declared in the spec means an access token pasted into
// Authorize applies to every call.
//
// The assets come from a CDN, so the page needs network access the first time
// a browser loads it. The spec itself is embedded in the binary and served
// locally, so the contract is always available even when the CDN is not — see
// GET /openapi.yaml.
const docsPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Booking Platform API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: "/openapi.yaml",
        dom_id: "#swagger",
        deepLinking: true,
        persistAuthorization: true,
        displayRequestDuration: true,
        docExpansion: "none",
        filter: true,
        tryItOutEnabled: true,
      });
    };
  </script>
</body>
</html>`

// mountDocs adds the documentation routes.
//
// Both are registered together: a docs page whose spec 404s is worse than no
// docs page at all.
func mountDocs(r chi.Router) {
	r.Get("/openapi.yaml", serveSpec)
	r.Get("/docs", serveDocs)
	// Trailing slash is the shape people type after a redirect or a copy-paste.
	r.Get("/docs/", serveDocs)
}

func serveSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// The spec is compiled in, so it changes only when the binary does.
	http.ServeContent(w, r, api.SpecFilename, time.Time{}, bytes.NewReader(api.Spec))
}

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
