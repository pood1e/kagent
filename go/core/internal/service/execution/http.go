package execution

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

// RuntimeAuthorization checks the live initiator, UID-bound pinned dependencies
// and immutable resource ceiling on every gateway callback. Credentials are
// exported only after those checks, for the authenticated destination gateway.
func (s *Service) RuntimeAuthorization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	required := r.URL.Query().Get("scope")
	workspace := r.URL.Query().Get("workspace")
	server, model, credential := r.URL.Query().Get("server"), r.URL.Query().Get("model"), r.URL.Query().Get("credential")
	if len(required) > 256 || strings.ContainsFunc(required, unicode.IsSpace) ||
		len(server) > 512 || len(model) > 253 || len(workspace) > 63 || server == "" && workspace == "" || server != "" && model != "" ||
		credential != "" && (credential != "subject_token" || server == "") {
		http.Error(w, "invalid runtime target", http.StatusBadRequest)
		return
	}
	result, err := s.RuntimeAuthority(r.Context())
	if err != nil || required != "" && !slices.Contains(result.EffectiveScopes, required) {
		http.Error(w, "runtime execution denied", http.StatusForbidden)
		return
	}
	resolved, err := s.authority.Resolve(r.Context(), result.Authority.SessionId)
	if err != nil || required != "" && !slices.Contains(resolved.Principal().Scopes, required) {
		http.Error(w, "runtime authority unavailable", http.StatusForbidden)
		return
	}
	session, err := s.sessions.Get(r.Context(), result.Authority.SessionId)
	if err != nil {
		http.Error(w, "runtime target denied", http.StatusForbidden)
		return
	}
	revision, err := s.store.GetRuntimeRevision(r.Context(), session.PreparedRevision)
	if err != nil {
		http.Error(w, "runtime revision unavailable", http.StatusServiceUnavailable)
		return
	}
	if workspace != "" && workspace != revision.Namespace {
		http.Error(w, "runtime destination workspace denied", http.StatusForbidden)
		return
	}
	selection, err := s.runtimeSelection(r.Context(), resolved.Principal(), revision, server, model)
	if err != nil {
		code := http.StatusForbidden
		if serviceerrors.IsCode(err, serviceerrors.CodeUnavailable) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, "runtime resource denied", code)
		return
	}
	scopes, err := json.Marshal(resolved.Principal().Scopes)
	if err != nil {
		http.Error(w, "runtime scopes unavailable", http.StatusForbidden)
		return
	}
	if credential != "" {
		source, ok := resolved.(auth.TokenSource)
		if !ok {
			http.Error(w, "runtime credential unavailable", http.StatusForbidden)
			return
		}
		token, err := source.Token(r.Context())
		if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
			http.Error(w, "runtime credential unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Authorization", "Bearer "+token)
	}
	w.Header().Set("X-Kagent-Scopes", string(scopes))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(selection); err != nil {
		logging.FromContext(r.Context()).ErrorContext(r.Context(), "failed to encode runtime target authorization", "error", err)
	}
}
