package display

import (
	"context"
	"errors"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
)

var ErrNotRunning = errors.New("display is not running")

type Controller interface {
	Start(context.Context) error
	Stop(context.Context) error
	IsRunning(context.Context) (bool, error)
	RenderNews(context.Context, []*screenv1.NewsItem) error
}
