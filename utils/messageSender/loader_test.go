package messageSender

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/messageSender/factory"
)

type recordingSender struct {
	events int
	texts  int
}

func (r *recordingSender) GetName() string                         { return "recording" }
func (r *recordingSender) GetConfiguration() factory.Configuration { return &struct{}{} }
func (r *recordingSender) Init() error                             { return nil }
func (r *recordingSender) Destroy() error                          { return nil }
func (r *recordingSender) SendTextMessage(string, string) error {
	r.texts++
	return nil
}
func (r *recordingSender) SendEvent(models.EventMessage) error {
	r.events++
	return nil
}

func TestSendTestEventIgnoresNotificationSwitch(t *testing.T) {
	prevLoad := loadNotificationSettings
	prevAudit := writeSendAudit
	mu.Lock()
	prevProvider := currentProvider
	mu.Unlock()
	t.Cleanup(func() {
		loadNotificationSettings = prevLoad
		writeSendAudit = prevAudit
		mu.Lock()
		currentProvider = prevProvider
		mu.Unlock()
	})
	writeSendAudit = func(string, string, string, string, map[string]string) {}
	loadNotificationSettings = func(map[string]any) (map[string]any, error) {
		return map[string]any{
			config.NotificationEnabledKey:  false,
			config.NotificationTemplateKey: "Message: {{message}}",
		}, nil
	}
	sender := &recordingSender{}
	mu.Lock()
	currentProvider = sender
	mu.Unlock()

	if err := SendEvent(models.EventMessage{Event: "Offline", Message: "down"}); err != nil {
		t.Fatal(err)
	}
	if sender.events != 0 || sender.texts != 0 {
		t.Fatalf("disabled switch sent a real event: events=%d texts=%d", sender.events, sender.texts)
	}
	if err := SendTestEvent(models.EventMessage{Event: "Test", Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if sender.events != 1 {
		t.Fatalf("test message was not sent, events=%d", sender.events)
	}
}

func TestParseTemplateFormatsEventTimeAsUTCNanoseconds(t *testing.T) {
	local := time.FixedZone("UTC+8", 8*60*60)
	eventTime := time.Date(2026, 7, 17, 9, 30, 0, 123456789, local)
	got := parseTemplate("{{time}}", models.EventMessage{Time: eventTime})
	want := "2026-07-17T01:30:00.123456789Z"
	if got != want {
		t.Fatalf("formatted event time = %q, want %q", got, want)
	}
}

func Test(t *testing.T) {
	senders := factory.GetAllMessageSenders()
	if len(senders) == 0 {
		t.Error("No message senders found")
		return
	}
	cfg := factory.GetSenderConfigs()
	if len(cfg) == 0 {
		t.Error("No sender configs found")
		return
	}
	LoadProvider("email", `{"host":"smtp.example.com","port":587,"username":"user","password":"pass"}`)
	cp := CurrentProvider
	if cp() == nil {
		t.Error("Current provider is nil")
		return
	}
}
