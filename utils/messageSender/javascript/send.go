package javascript

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/nuomiiiii/lite/database/models"
)

func (j *JavaScriptSender) SendTextMessage(message, title string) error {
	j.sendMu.Lock()
	defer j.sendMu.Unlock()
	return j.sendTextLocked(message, title)
}

func (j *JavaScriptSender) sendTextLocked(message, title string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ensureLocked(); err != nil {
		return err
	}
	return j.awaitLocked(func() (goja.Value, error) {
		fn, ok := goja.AssertFunction(j.vm.Get("sendMessage"))
		if !ok {
			return nil, errors.New("sendMessage is not a callable function")
		}
		return fn(goja.Undefined(), j.vm.ToValue(message), j.vm.ToValue(title))
	}, "sendMessage returned false")
}

func (j *JavaScriptSender) SendEvent(event models.EventMessage) error {
	j.sendMu.Lock()
	defer j.sendMu.Unlock()

	j.mu.Lock()
	if err := j.ensureLocked(); err != nil {
		j.mu.Unlock()
		return err
	}
	fn, ok := goja.AssertFunction(j.vm.Get("sendEvent"))
	if !ok {
		j.mu.Unlock()
		return j.sendTextLocked(fallbackMessage(event), event.Event)
	}
	payload, err := eventPayload(event)
	if err != nil {
		j.mu.Unlock()
		return err
	}
	err = j.awaitLocked(func() (goja.Value, error) {
		return fn(goja.Undefined(), j.vm.ToValue(payload))
	}, "sendEvent returned false")
	j.mu.Unlock()
	return err
}

func (j *JavaScriptSender) ensureLocked() error {
	if j.vm != nil {
		return nil
	}
	return j.initLocked()
}

// awaitLocked runs a script on the calling goroutine. The VM lock is released
// while waiting so fetch and timers can deliver their callbacks, and taken
// again before the runtime is touched. sendMu stays held, so a retry cannot
// enter the same runtime.
func (j *JavaScriptSender) awaitLocked(call func() (goja.Value, error), falseMessage string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			j.abandonLocked()
			err = fmt.Errorf("JavaScript panic: %v", recovered)
		}
	}()

	gen := j.gen
	result, callErr := call()
	if callErr != nil {
		return fmt.Errorf("JavaScript error: %v", callErr)
	}
	promise, isPromise := result.Export().(*goja.Promise)
	if !isPromise {
		if result != nil && result.ToBoolean() {
			return nil
		}
		return errors.New(falseMessage)
	}

	deadline := time.Now().Add(scriptTimeout)
	for {
		j.runMicrotasks()
		switch promise.State() {
		case goja.PromiseStateFulfilled:
			if !promise.Result().ToBoolean() {
				return errors.New(falseMessage)
			}
			return nil
		case goja.PromiseStateRejected:
			return fmt.Errorf("Promise rejected: %v", promise.Result())
		}
		if !time.Now().Before(deadline) {
			j.abandonLocked()
			return fmt.Errorf("JavaScript execution timeout after %s", scriptTimeout)
		}
		j.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		j.mu.Lock()
		if j.vm == nil || j.gen != gen {
			return fmt.Errorf("JavaScript execution timeout after %s", scriptTimeout)
		}
	}
}

func eventPayload(event models.EventMessage) (map[string]interface{}, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal event: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event: %v", err)
	}
	return payload, nil
}

func fallbackMessage(event models.EventMessage) string {
	message := fmt.Sprintf("%s%s%s\nEvent: %s\nMessage: %s\nTime: %s",
		event.Emoji, event.Emoji, event.Emoji,
		event.Event,
		event.Message,
		event.Time.UTC().Format(time.RFC3339Nano))
	if len(event.Clients) == 0 {
		return message
	}
	clientNames := make([]string, 0, len(event.Clients))
	for _, client := range event.Clients {
		name := client.Name
		if name == "" {
			name = client.UUID
		}
		clientNames = append(clientNames, name)
	}
	return fmt.Sprintf("%s%s%s\nEvent: %s\nClients: %s\nMessage: %s\nTime: %s",
		event.Emoji, event.Emoji, event.Emoji,
		event.Event,
		clientNames,
		event.Message,
		event.Time.UTC().Format(time.RFC3339Nano))
}
