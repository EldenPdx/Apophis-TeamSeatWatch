package runtime

import "context"

// A reserved Owner login survives the response connection, but service shutdown
// still cancels its queue, browser and heartbeat. Publication keeps its existing
// short cleanup deadline and account/Owner fences.
func (h *OwnerAuthHandler) personalRefreshContext(request context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(request))
	if h.serviceContext == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(h.serviceContext, cancel)
	if h.serviceContext.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
