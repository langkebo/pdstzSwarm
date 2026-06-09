package api

// WithFindingHook registers a FindingHook to be invoked on every
// finding republished by every campaign's FindingsBridge. The
// hook is invoked after the WebSocket publish, so observability
// adapters (LangFuse, OpenTelemetry, etc.) see findings in
// real-time alongside dashboard subscribers.
//
// WithFindingHook is additive: calling it multiple times
// accumulates hooks. Hooks run in registration order. Nil hooks
// are silently dropped. Hooks must be registered before
// (or concurrently with) the first campaign start; hooks added
// after a campaign has already started do not retroactively fire
// for findings republished before the hook was added.
//
// This is a fluent-style helper on Server; it's safe to chain
// during construction:
//
//   s := api.NewServer(...).WithFindingHook(obs.OnFinding)
func (s *Server) WithFindingHook(hook FindingHook) *Server {
	if s == nil || hook == nil {
		return s
	}
	s.findingHooks = append(s.findingHooks, hook)
	return s
}
