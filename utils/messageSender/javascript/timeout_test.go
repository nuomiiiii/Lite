package javascript

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTimeoutAbandonsRuntimeAndRetryUsesAFreshOne(t *testing.T) {
	previousScript := scriptTimeout
	scriptTimeout = 80 * time.Millisecond
	t.Cleanup(func() { scriptTimeout = previousScript })

	sender := &JavaScriptSender{}
	sender.Addition.Script = `function sendMessage(){ return new Promise(function(){}); }`
	started := time.Now()
	err := sender.SendTextMessage("slow", "title")
	if err == nil || !strings.Contains(err.Error(), "JavaScript execution timeout") {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("timeout took %s", time.Since(started))
	}
	sender.mu.Lock()
	abandoned := sender.vm == nil
	sender.mu.Unlock()
	if !abandoned {
		t.Fatal("timed out script kept the runtime")
	}

	sender.Addition.Script = `function sendMessage(){ return true; }`
	if err := sender.SendTextMessage("ok", "title"); err != nil {
		t.Fatal(err)
	}
}

func TestSlowSendDoesNotBlockTheRuntimeForALaterSend(t *testing.T) {
	previousScript := scriptTimeout
	scriptTimeout = 150 * time.Millisecond
	t.Cleanup(func() { scriptTimeout = previousScript })

	sender := &JavaScriptSender{}
	sender.Addition.Script = `function sendMessage(message){
		if (message === "slow") { return new Promise(function(){}); }
		return true;
	}`

	var wg sync.WaitGroup
	wg.Add(2)
	var slowErr, fastErr error
	go func() {
		defer wg.Done()
		slowErr = sender.SendTextMessage("slow", "title")
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		defer wg.Done()
		fastErr = sender.SendTextMessage("fast", "title")
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("second send stayed blocked on the timed out runtime")
	}
	if slowErr == nil || fastErr != nil {
		t.Fatalf("slow=%v fast=%v", slowErr, fastErr)
	}
}
