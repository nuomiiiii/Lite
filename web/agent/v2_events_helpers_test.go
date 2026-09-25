package agent

func RemoveV2EventsByMethods(uuid string, methods ...string) {
	if len(methods) == 0 {
		return
	}
	blocked := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		blocked[method] = struct{}{}
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	q := v2EventQueues[uuid]
	if q == nil {
		return
	}
	filtered := q.events[:0]
	for _, event := range q.events {
		if _, remove := blocked[event.Method]; !remove {
			filtered = append(filtered, event)
		}
	}
	q.events = filtered
}
