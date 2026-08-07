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
	cmdNewsRetry
)

type command struct {
	kind    commandKind
	alert   alert.Alert
	desired bool
	err     error
	run     uint64
	reply   chan response
	ctx     context.Context
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
	newsFailures                                      int
	shuttingDown, temporaryAlertDisplay, newsStopping bool
	forceStopDisplay                                  bool
}

type Supervisor struct {
	cfg        config.Config
	display    display.Controller
	news       newsworker.Worker
	presenter  alert.Presenter
	logger     *slog.Logger
	commands   chan command
	internal   chan command
	refreshNow chan struct{}
	done       chan struct{}
	stopResult chan error
	appCtx     context.Context
	cancel     context.CancelFunc
	startOnce  sync.Once
}

func New(parent context.Context, cfg config.Config, d display.Controller, nw newsworker.Worker, p alert.Presenter, logger *slog.Logger) *Supervisor {
	ctx, cancel := context.WithCancel(parent)
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{cfg: cfg, display: d, news: nw, presenter: p, logger: logger, commands: make(chan command, cfg.CommandQueueCapacity), internal: make(chan command, 16), refreshNow: make(chan struct{}, 1), done: make(chan struct{}), stopResult: make(chan error, 1), appCtx: ctx, cancel: cancel}
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
	_, err := s.request(ctx, command{kind: cmdStop})
	if err != nil {
		return err
	}
	select {
	case err := <-s.stopResult:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) request(ctx context.Context, c command) (response, error) {
	c.reply = make(chan response, 1)
	c.ctx = ctx
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

func (s *Supervisor) sendInternal(c command) {
	select {
	case s.internal <- c:
	case <-s.done:
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
		var c command
		select {
		case c = <-s.internal:
		case c = <-s.commands:
		}
		if c.ctx != nil {
			if err := c.ctx.Err(); err != nil {
				reply(c, response{err: err})
				continue
			}
		}
		var reconcileErr error
		previousLifecycle := st.Lifecycle
		switch c.kind {
		case cmdStart:
			st.DesiredOn = true
		case cmdShutdown:
			st.DesiredOn = false
			st.forceStopDisplay = st.DisplayRunning
			s.cancelAll(&st, true)
		case cmdReconcile:
			st.DesiredOn = c.desired
			if !c.desired {
				st.forceStopDisplay = true
				s.cancelAll(&st, true)
			}
		case cmdNewsChanged, cmdNewsPass:
			// Soft signal: the worker finishes the current pass (if any) then
			// starts another. Buffered so a tick during an in-flight pass is
			// not lost, but multiple ticks coalesce to one follow-up pass.
			if st.NewsRunning && !st.newsStopping {
				select {
				case s.refreshNow <- struct{}{}:
					st.LastRefreshAt = time.Now()
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
				if st.DesiredOn && !st.shuttingDown {
					st.newsFailures++
					delay := retryDelay(st.newsFailures)
					run := st.newsRun
					time.AfterFunc(delay, func() { s.sendInternal(command{kind: cmdNewsRetry, run: run}) })
					st.Lifecycle = StateFailed
					s.logger.Error("news worker failed", "error", c.err, "retry_in", delay)
					continue
				}
			}
			st.newsFailures = 0
		case cmdNewsStopTimedOut:
			if c.run == st.newsRun && st.NewsRunning {
				st.Lifecycle = StateFailed
				st.LastError = ErrWorkerStopTimeout.Error()
			}
		case cmdNewsRetry:
			if c.run != st.newsRun || !st.DesiredOn || st.shuttingDown || st.AlertRunning || len(st.alerts) > 0 {
				continue
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
			st.forceStopDisplay = true
			s.cancelAll(&st, false)
			reply(c, response{})
			s.cancel()
		}
		reconcileErr = s.reconcile(&st)
		if st.Lifecycle != previousLifecycle {
			s.logger.Info("lifecycle transition", "from", previousLifecycle, "to", st.Lifecycle, "desired_on", st.DesiredOn, "queued_alerts", st.QueuedAlerts)
		}
		switch c.kind {
		case cmdStart, cmdShutdown, cmdReconcile, cmdNewsChanged:
			reply(c, response{snapshot: st.State, err: reconcileErr})
		}
		if st.shuttingDown && !st.NewsRunning && !st.AlertRunning {
			// Always clear the panel and zero brightness on process exit
			// (SIGTERM/SIGINT), matching scheduled window end.
			err := s.stopDisplay(&st)
			s.stopResult <- err
			return
		}
	}
}

func (s *Supervisor) reconcile(st *internalState) error {
	if st.shuttingDown {
		return nil
	}
	wantDisplay := st.DesiredOn || (st.temporaryAlertDisplay && (st.AlertRunning || len(st.alerts) > 0))
	if !wantDisplay {
		s.cancelAll(st, true)
		if st.NewsRunning || st.AlertRunning {
			return nil
		}
		if st.DisplayRunning || st.forceStopDisplay {
			if err := s.stopDisplay(st); err != nil {
				return err
			}
		}
		st.Lifecycle = StateOff
		return nil
	}
	if !st.DisplayRunning {
		if err := s.startDisplay(st); err != nil {
			return err
		}
	}
	if st.AlertRunning {
		st.Lifecycle = StateAlerting
		return nil
	}
	if len(st.alerts) > 0 {
		if st.NewsRunning {
			if !st.newsStopping {
				s.stopNews(st)
			}
			st.Lifecycle = StatePausing
			return nil
		}
		item := st.alerts[0]
		st.alerts = st.alerts[1:]
		st.QueuedAlerts = len(st.alerts)
		s.startAlert(st, item)
		return nil
	}
	if st.temporaryAlertDisplay {
		st.temporaryAlertDisplay = false
		if err := s.stopDisplay(st); err != nil {
			return err
		}
		st.Lifecycle = StateOff
		return nil
	}
	if st.DesiredOn && !st.NewsRunning {
		s.startNews(st)
	}
	return nil
}

func (s *Supervisor) startDisplay(st *internalState) error {
	st.Lifecycle = StateStarting
	var err error
	for i := 0; i < s.cfg.StartupRetryCount; i++ {
		ctx, cancel := context.WithTimeout(s.appCtx, s.cfg.OperationTimeout)
		err = s.display.Start(ctx)
		cancel()
		if err == nil {
			st.DisplayRunning = true
			st.LastStartedAt = time.Now()
			return nil
		}
		if !wait(s.appCtx, s.cfg.StartupRetryDelay<<i) {
			break
		}
	}
	st.Lifecycle = StateFailed
	st.LastError = fmt.Sprintf("start display: %v", err)
	return errors.New(st.LastError)
}
func (s *Supervisor) stopDisplay(st *internalState) error {
	// Always invoke Stop so clear + brightness 0 run even if our flag is stale
	// (e.g. display was already on when the process started).
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OperationTimeout)
	err := s.display.Stop(ctx)
	cancel()
	if err != nil {
		st.LastError = err.Error()
		st.Lifecycle = StateFailed
		return err
	}
	st.DisplayRunning = false
	st.forceStopDisplay = false
	st.LastStoppedAt = time.Now()
	return nil
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
		s.sendInternal(command{kind: cmdNewsStopped, err: err, run: run})
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
	time.AfterFunc(s.cfg.WorkerStopTimeout, func() { s.sendInternal(command{kind: cmdNewsStopTimedOut, run: run}) })
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
		s.sendInternal(command{kind: cmdAlertFinished, err: err, run: run})
	}()
}

func retryDelay(failures int) time.Duration {
	if failures < 1 {
		return time.Second
	}
	shift := min(failures-1, 6)
	return min(time.Second*time.Duration(1<<shift), time.Minute)
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
