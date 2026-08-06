package kindledisplay

import (
	"context"
	"strings"
	"testing"

	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/kindle"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(command string) (string, error) {
	r.commands = append(r.commands, command)
	if strings.Contains(command, "flIntensity") && strings.Contains(command, "get-prop") {
		return "0", nil
	}
	return "", nil
}

func TestControllerRendersCriticalAlertStyle(t *testing.T) {
	runner := &recordingRunner{}
	controller := New(kindle.New(runner))
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Show(context.Background(), alert.Alert{Message: "Battery low", Severity: alert.SeverityCritical}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, "⚠  Battery low") {
		t.Fatalf("critical alert style missing:\n%s", joined)
	}
}
