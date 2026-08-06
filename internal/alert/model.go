package alert

import (
	"context"
	"time"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Alert struct {
	OperationID, Color, Message string
	Severity                    Severity
	ReceivedAt                  time.Time
}

type Presenter interface {
	Show(context.Context, Alert) error
	Clear(context.Context) error
}
