package execution

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"

	corev1 "k8s.io/api/core/v1"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

// RuntimeAuthorization serves uncached HTTP ext-authz decisions for a trusted
// gateway. Its dedicated Actor audience is authenticated at the application
// boundary. Target selection is bound to the Actor Session's pinned revision.
// subject_token exports the live initiator credential solely to that gateway,
// which exchanges it for a destination-specific credential before forwarding.
func (s *Service) RuntimeAuthorization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	required := r.URL.Query().Get("scope")
	server, model, credential := r.URL.Query().Get("server"), r.URL.Query().Get("model"), r.URL.Query().Get("credential")
	if len(required) > 256 || strings.ContainsFunc(required, unicode.IsSpace) ||
		len(server) > 512 || len(model) > 253 || server != "" && model != "" ||
		credential != "" && (credential != "subject_token" || server == "") {
		http.Error(w, "invalid runtime target", http.StatusBadRequest)
		return
	}
	result, err := s.RuntimeAuthority(r.Context())
	if err != nil || required != "" && !slices.Contains(result.EffectiveScopes, required) {
		http.Error(w, "runtime execution denied", http.StatusForbidden)
		return
	}
	scopes, err := json.Marshal(result.EffectiveScopes)
	if err != nil {
		http.Error(w, "runtime scopes unavailable", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Kagent-Scopes", string(scopes))
	var selection any
	{
		session, err := s.sessions.Get(r.Context(), result.Authority.SessionId)
		if err != nil {
			http.Error(w, "runtime target denied", http.StatusForbidden)
			return
		}
		revision, err := s.store.GetRuntimeRevision(r.Context(), session.PreparedRevision)
		if err != nil {
			http.Error(w, "runtime revision unavailable", http.StatusForbidden)
			return
		}
		selection = revision.Models
		if model != "" {
			if !slices.ContainsFunc(revision.Models, func(ref corev1.LocalObjectReference) bool { return ref.Name == model }) {
				http.Error(w, "runtime model target denied", http.StatusForbidden)
				return
			}
		} else if server != "" {
			var found bool
			selection, found = selectedMCPTools(revision.Namespace, revision.MCPTools, server)
			if !found {
				http.Error(w, "runtime MCP target denied", http.StatusForbidden)
				return
			}
		}
	}
	if credential != "" {
		resolved, err := s.authority.Resolve(r.Context(), result.Authority.SessionId)
		if err != nil || required != "" && !slices.Contains(resolved.Principal().Scopes, required) {
			http.Error(w, "runtime credential unavailable", http.StatusForbidden)
			return
		}
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
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(selection); err != nil {
		logging.FromContext(r.Context()).ErrorContext(r.Context(), "failed to encode runtime target authorization", "error", err)
	}
}

func selectedMCPTools(namespace string, bindings []v1alpha3.MCPToolBinding, target string) (v1alpha3.MCPToolBinding, bool) {
	var result v1alpha3.MCPToolBinding
	found, all := false, false
	for _, binding := range bindings {
		if namespace+"/"+binding.Server.Name != target {
			continue
		}
		result.Server, found = binding.Server, true
		all = all || len(binding.Tools) == 0
		result.Tools = append(result.Tools, binding.Tools...)
	}
	if all {
		result.Tools = nil
	} else {
		slices.Sort(result.Tools)
		result.Tools = slices.Compact(result.Tools)
	}
	return result, found
}
