package main

// reserveLegacyRemuxSlot enforces both the legacy endpoint cap and the same
// process budget used by HLS. Denial never leaks a reservation.
func reserveLegacyRemuxSlot(legacy, shared chan struct{}) (func(), bool) {
	select {
	case legacy <- struct{}{}:
	default:
		return nil, false
	}
	select {
	case shared <- struct{}{}:
		return func() {
			<-shared
			<-legacy
		}, true
	default:
		<-legacy
		return nil, false
	}
}
