package notify

import (
	"strings"
	"testing"
)

func TestQuotaWatchAlertsOnceWhenCrossingFloor(t *testing.T) {
	sender := &memSender{}
	w := NewQuotaWatch(sender, 25)

	w.Observe(400, 100)
	w.Observe(30, 470)
	if len(sender.sent) != 0 {
		t.Fatalf("no alert above floor, got %d", len(sender.sent))
	}
	w.Observe(20, 480)
	w.Observe(10, 490)
	if len(sender.sent) != 1 {
		t.Fatalf("alerts = %d, want exactly 1 while below floor", len(sender.sent))
	}
	e := sender.sent[0]
	if !strings.Contains(e.Title, "quota") || !strings.Contains(strings.ToLower(e.Title+fieldsText(e)), "20") {
		t.Errorf("alert should name the quota and remaining credits: %+v", e)
	}

	// Recovery (monthly reset) re-arms the alert
	w.Observe(500, 0)
	w.Observe(5, 495)
	if len(sender.sent) != 2 {
		t.Errorf("alerts = %d, want 2 after recovery and a second dip", len(sender.sent))
	}
}

func TestQuotaWatchNilSenderOnlyLogs(t *testing.T) {
	w := NewQuotaWatch(nil, 25)
	w.Observe(1, 499) // must not panic
}

func TestQuotaWatchZeroFloorDisabled(t *testing.T) {
	sender := &memSender{}
	NewQuotaWatch(sender, 0).Observe(0, 500)
	if len(sender.sent) != 0 {
		t.Error("floor 0 disables alerts")
	}
}

func fieldsText(e Embed) string {
	var b strings.Builder
	for _, f := range e.Fields {
		b.WriteString(f.Name + " " + f.Value + " ")
	}
	return b.String()
}
