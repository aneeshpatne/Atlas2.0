package alert

import (
	"context"
	"time"
)

type Alert struct {
	OperationID, Color, Message string
	ReceivedAt                  time.Time
}

type Presenter interface {
	Show(context.Context, Alert) error
	Clear(context.Context) error
}
