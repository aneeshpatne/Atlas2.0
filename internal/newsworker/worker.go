package newsworker

import (
	"context"
)

type Worker interface {
	Run(context.Context, <-chan struct{}) error
}
