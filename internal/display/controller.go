package display

import (
	"context"
	"errors"
)

var ErrNotRunning = errors.New("display is not running")

type Controller interface {
	Start(context.Context) error
	Stop(context.Context) error
	IsRunning(context.Context) (bool, error)
}
