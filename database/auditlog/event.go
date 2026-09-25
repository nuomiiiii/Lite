package auditlog

import "encoding/json"

// Event writes one audit row that the log page can show in the current language.
// Params stay language-neutral. A search copy in all four UI languages is stored
// with the row so the log search box matches the words on screen.
func Event(ip, actor, msgType, key string, params map[string]string) {
	Log(ip, actor, Message(key, params), msgType)
}

// Message is the stored text for an audit event. It does not touch the database.
func Message(key string, params map[string]string) string {
	if params == nil {
		params = map[string]string{}
	}
	payload := struct {
		K string            `json:"k"`
		P map[string]string `json:"p,omitempty"`
		S string            `json:"s,omitempty"`
	}{
		K: key,
		P: params,
		S: searchText(key, params),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return key
	}
	return string(raw)
}
