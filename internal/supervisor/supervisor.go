package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/config"
	"github.com/aneeshpatne/atlas/internal/display"
	"github.com/aneeshpatne/atlas/internal/newsworker"
)

var (
	ErrShuttingDown        = errors.New("service is shutting down")
	ErrQueueFull           = errors.New("alert queue is full")
	ErrOutsideActiveWindow = errors.New("outside active window")
	ErrWorkerStopTimeout   = errors.New("worker stop timeout")
)

type LifecycleState string

const (
	StateOff      LifecycleState = "off"
	StateStarting LifecycleState = "starting"
	StateRunning  LifecycleState = "running"
	StatePausing  LifecycleState = "pausing"
	StateAlerting LifecycleState = "alerting"
	StateStopping LifecycleState = "stopping"
	StateFailed   LifecycleState = "failed"
)

type State struct {
	Lifecycle                                            LifecycleState
	DesiredOn, DisplayRunning, NewsRunning, AlertRunning bool
	QueuedAlerts                                         int
	CurrentAlertOperationID                              string
	LastStartedAt, LastStoppedAt, LastRefreshAt          time.Time
	LastError                                            string
}

type AlertResult struct{ OperationID, State string }
type commandKind uint8

const (
	cmdStart commandKind = iota
	cmdShutdown
	cmdAlert
	cmdNewsChanged
	cmdNewsPass
	cmdReconcile
	cmdSnapshot
	cmdStop
	cmdNewsStopped
	cmdAlertFinished
	cmdNewsStopTimedOut
)

type command struct {
	kind    commandKind
	alert   alert.Alert
	desired bool
	err     error
	run     uint64
	reply   chan response
}
type response struct {
	result   AlertResult
	snapshot State
	err      error
}

type internalState struct {
	State
	newsCancel                                        context.CancelFunc
	alertCancel                                       context.CancelFunc
	alerts                                            []alert.Alert
	newsRun, alertRun                                 uint64
	shuttingDown, temporaryAlertDisplay, newsStopping bool
}

type Supervisor struct {
	cfg        config.Config
	display    display.Controller
	news       newsworker.Worker
	presenter  alert.Presenter
	logger     *slog.Logger
	commands   chan command
	refreshNow chan struct{}
	done       chan struct{}
	appCtx     context.Context
	cancel     context.CancelFunc
	startOnce  sync.Once
}

func New(parent context.Context, cfg config.Config, d display.Controller, nw newsworker.Worker, p alert.Presenter, logger *slog.Logger) *Supervisor {
	ctx, cancel := context.WithCancel(parent)
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{cfg: cfg, display: d, news: nw, presenter: p, logger: logger, commands: make(chan command, cfg.CommandQueueCapacity), refreshNow: make(chan struct{}, 1), done: make(chan struct{}), appCtx: ctx, cancel: cancel}
}
func (s *Supervisor) Start()                    { s.startOnce.Do(func() { go s.loop() }) }
func (s *Supervisor) Done() <-chan struct{}     { return s.done }
func (s *Supervisor) RequestScheduledStart()    { s.trySend(command{kind: cmdStart}) }
func (s *Supervisor) RequestScheduledShutdown() { s.trySend(command{kind: cmdShutdown}) }

// RequestNewsPass asks the news worker to start a genre-wise pass at the next
// opportunity. Used by the wall-clock cron (:00/:15/:30/:45 by default).
// No-ops when news is not running (outside the active window or stopping).
func (s *Supervisor) RequestNewsPass() {
	s.trySend(command{kind: cmdNewsPass})
}

func (s *Supervisor) Reconcile(ctx context.Context, desired bool) error {
	_, err := s.request(ctx, command{kind: cmdReconcile, desired: desired})
	return err
}
func (s *Supervisor) NotifyNewsChanged(ctx context.Context) error {
	_, err := s.request(ctx, command{kind: cmdNewsChanged})
	return err
}
func (s *Supervisor) AddAlert(ctx context.Context, a alert.Alert) (AlertResult, error) {
	r, err := s.request(ctx, command{kind: cmdAlert, alert: a})
	return r.result, err
}
func (s *Supervisor) Snapshot(ctx context.Context) (State, error) {
	r, err := s.request(ctx, command{kind: cmdSnapshot})
	return r.snapshot, err
}
func (s *Supervisor) Stop(ctx context.Context) error {
	r, err := s.request(ctx, command{kind: cmdStop})
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) request(ctx context.Context, c command) (response, error) {
	c.reply = make(chan response, 1)
	select {
	case <-s.done:
		return response{}, ErrShuttingDown
	case <-ctx.Done():
		return response{}, ctx.Err()
	case s.commands <- c:
	}
	select {
	case r := <-c.reply:
		return r, r.err
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-s.done:
		return response{}, ErrShuttingDown
	}
}
func (s *Supervisor) trySend(c command) bool {
	select {
	case <-s.done:
		return false
	case s.commands <- c:
		return true
	default:
		s.logger.Error("supervisor command queue full")
		return false
	}
}
func reply(c command, r response) {
	if c.reply != nil {
		c.reply <- r
	}
}

func (s *Supervisor) loop() {
	defer close(s.done)
	st := internalState{State: State{Lifecycle: StateOff}}
	for {
		c := <-s.commands
		switch c.kind {
		case cmdStart:
			st.DesiredOn = true
		case cmdShutdown:
			st.DesiredOn = false
			s.cancelAll(&st, true)
		case cmdReconcile:
			st.DesiredOn = c.desired
			if !c.desired {
				s.cancelAll(&st, true)
			}
		case cmdNewsChanged, cmdNewsPass:
			// Soft signal: the worker finishes the current pass (if any) then
			// starts another. Buffered so a tick during an in-flight pass is
			// not lost, but multiple ticks coalesce to one follow-up pass.
			if st.NewsRunning && !st.newsStopping {
				select {
				case s.refreshNow <- struct{}{}:
				default:
				}
			}
		case cmdAlert:
			if st.shuttingDown {
				reply(c, response{err: ErrShuttingDown})
				continue
			}
			if !st.DesiredOn && !s.cfg.AllowAlertsOutsideActiveWindow {
				reply(c, response{err: ErrOutsideActiveWindow})
				continue
			}
			waiting := len(st.alerts)
			queued := waiting
			if !st.AlertRunning && waiting > 0 {
				queued--
			}
			if queued >= s.cfg.AlertQueueCapacity {
				reply(c, response{err: ErrQueueFull})
				continue
			}
			state := "queued"
			if !st.AlertRunning && waiting == 0 {
				state = "displaying"
			}
			st.alerts = append(st.alerts, c.alert)
			st.QueuedAlerts = len(st.alerts)
			if !st.AlertRunning && st.QueuedAlerts > 0 {
				st.QueuedAlerts-- // the head alert owns the in-progress display transition
			}
			if !st.DesiredOn {
				st.temporaryAlertDisplay = true
			}
			if st.NewsRunning && !st.newsStopping {
				s.stopNews(&st)
			}
			reply(c, response{result: AlertResult{OperationID: c.alert.OperationID, State: state}})
		case cmdNewsStopped:
			if c.run != st.newsRun {
				continue
			}
			st.NewsRunning = false
			st.newsStopping = false
			st.newsCancel = nil
			if c.err != nil && !errors.Is(c.err, context.Canceled) {
				st.LastError = c.err.Error()
			}
		case cmdNewsStopTimedOut:
			if c.run == st.newsRun && st.NewsRunning {
				st.Lifecycle = StateFailed
				st.LastError = ErrWorkerStopTimeout.Error()
			}
		case cmdAlertFinished:
			if c.run != st.alertRun {
				continue
			}
			st.AlertRunning = false
			st.alertCancel = nil
			st.CurrentAlertOperationID = ""
			if c.err != nil && !errors.Is(c.err, context.Canceled) {
				st.LastError = c.err.Error()
			}
		case cmdSnapshot:
			reply(c, response{snapshot: st.State})
			continue
		case cmdStop:
			st.shuttingDown = true
			st.DesiredOn = false
			s.cancelAll(&st, false)
			reply(c, response{})
			s.cancel()
		}
		s.reconcile(&st)
		switch c.kind {
		case cmdStart, cmdShutdown, cmdReconcile, cmdNewsChanged:
			reply(c, response{snapshot: st.State})
		}
		if st.shuttingDown && !st.NewsRunning && !st.AlertRunning {
			// Always clear the panel and zero brightness on process exit
			// (SIGTERM/SIGINT), matching scheduled window end.
			s.stopDisplay(&st)
			return
		}
	}
}

func (s *Supervisor) reconcile(st *internalState) {
	if st.shuttingDown {
		return
	}
	wantDisplay := st.DesiredOn || (st.temporaryAlertDisplay && (st.AlertRunning || len(st.alerts) > 0))
	if !wantDisplay {
		s.cancelAll(st, true)
		if st.NewsRunning || st.AlertRunning {
			return
		}
		s.stopDisplay(st)
		st.Lifecycle = StateOff
		return
	}
	if !st.DisplayRunning {
		if !s.startDisplay(st) {
			return
		}
	}
	if st.AlertRunning {
		st.Lifecycle = StateAlerting
		return
	}
	if len(st.alerts) > 0 {
		if st.NewsRunning {
			if !st.newsStopping {
				s.stopNews(st)
			}
			st.Lifecycle = StatePausing
			return
		}
		item := st.alerts[0]
		st.alerts = st.alerts[1:]
		st.QueuedAlerts = len(st.alerts)
		s.startAlert(st, item)
		return
	}
	if st.temporaryAlertDisplay {
		st.temporaryAlertDisplay = false
		s.stopDisplay(st)
		st.Lifecycle = StateOff
		return
	}
	if st.DesiredOn && !st.NewsRunning {
		s.startNews(st)
	}
}

func (s *Supervisor) startDisplay(st *internalState) bool {
	st.Lifecycle = StateStarting
	var err error
	for i := 0; i < s.cfg.StartupRetryCount; i++ {
		ctx, cancel := context.WithTimeout(s.appCtx, s.cfg.OperationTimeout)
		err = s.display.Start(ctx)
		cancel()
		if err == nil {
			st.DisplayRunning = true
			st.LastStartedAt = time.Now()
			return true
		}
		if !wait(s.appCtx, s.cfg.StartupRetryDelay<<i) {
			break
		}
	}
	st.Lifecycle = StateFailed
	st.LastError = fmt.Sprintf("start display: %v", err)
	return false
}
func (s *Supervisor) stopDisplay(st *internalState) {
	// Always invoke Stop so clear + brightness 0 run even if our flag is stale
	// (e.g. display was already on when the process started).
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OperationTimeout)
	err := s.display.Stop(ctx)
	cancel()
	if err != nil {
		st.LastError = err.Error()
		st.Lifecycle = StateFailed
		return
	}
	st.DisplayRunning = false
	st.LastStoppedAt = time.Now()
}
func (s *Supervisor) startNews(st *internalState) {
	if st.NewsRunning {
		return
	}
	ctx, cancel := context.WithCancel(s.appCtx)
	st.newsCancel = cancel
	st.newsRun++
	run := st.newsRun
	st.NewsRunning = true
	st.newsStopping = false
	st.Lifecycle = StateRunning
	// No immediate news kick: clock is the default until the next wall-clock
	// pass (or NotifyNewsChanged). startNews only starts the worker loop.
	go func() {
		err := s.news.Run(ctx, s.refreshNow)
		s.trySend(command{kind: cmdNewsStopped, err: err, run: run})
	}()
}
func (s *Supervisor) stopNews(st *internalState) {
	if !st.NewsRunning || st.newsStopping {
		return
	}
	st.newsStopping = true
	st.Lifecycle = StatePausing
	st.newsCancel()
	run := st.newsRun
	time.AfterFunc(s.cfg.WorkerStopTimeout, func() { s.trySend(command{kind: cmdNewsStopTimedOut, run: run}) })
}
func (s *Supervisor) startAlert(st *internalState, item alert.Alert) {
	ctx, cancel := context.WithCancel(s.appCtx)
	st.alertCancel = cancel
	st.alertRun++
	run := st.alertRun
	st.AlertRunning = true
	st.CurrentAlertOperationID = item.OperationID
	st.Lifecycle = StateAlerting
	go func() {
		err := alert.Run(ctx, s.presenter, item, s.cfg.AlertDisplayDuration, min(s.cfg.OperationTimeout, 2*time.Second))
		s.trySend(command{kind: cmdAlertFinished, err: err, run: run})
	}()
}
func (s *Supervisor) cancelAll(st *internalState, scheduled bool) {
	if st.newsCancel != nil {
		s.stopNews(st)
	}
	if st.alertCancel != nil {
		st.alertCancel()
	}
	st.alerts = nil
	st.QueuedAlerts = 0
	st.temporaryAlertDisplay = false
	if scheduled {
		st.Lifecycle = StateStopping
	}
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
