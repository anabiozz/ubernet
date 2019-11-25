package ubernet

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// Checker ..
type Checker struct {
	pipePool
	pollerLock sync.Mutex
	resultPipes
	pollerFd int32
	isReady  chan struct{}
}

// NewChecker ..
func NewChecker() *Checker {
	return &Checker{
		pipePool:    newPipePoolSyncPool(),
		resultPipes: newResultPipesSyncMap(),
		pollerFd:    -1,
		isReady:     make(chan struct{}),
	}
}

func (c *Checker) createPoller() (pollerFd int, err error) {
	c.pollerLock.Lock()
	defer c.pollerLock.Unlock()

	if c.pollerFD() > 0 {
		return -1, ErrCheckerAlreadyStarted
	}

	pollerFd, err = createPoller()
	if err != nil {
		return -1, err
	}

	c.setPollerFD(pollerFd)

	return
}

func (c *Checker) pollerFD() int {
	return int(atomic.LoadInt32(&c.pollerFd))
}

func (c *Checker) setPollerFD(fd int) {
	atomic.StoreInt32(&c.pollerFd, int32(fd))
}

// CheckingLoop must be called before anything else.
// NOTE: this function blocks until ctx got canceled.
func (c *Checker) CheckingLoop(ctx context.Context) error {
	pollerFd, err := c.createPoller()
	if err != nil {
		return errors.Wrap(err, "error creating poller")
	}
	defer c.closePoller()

	c.setReady()
	defer c.resetReady()

	return c.pollingLoop(ctx, pollerFd)
}

func (c *Checker) setReady() {
	close(c.isReady)
}

func (c *Checker) resetReady() {
	c.isReady = make(chan struct{})
}

const pollerTimeout = time.Second

func (c *Checker) pollingLoop(ctx context.Context, pollerFd int) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			events, err := pollEvents(pollerFd, pollerTimeout)
			if err != nil {
				return errors.Wrap(err, "error during polling loop")
			}

			c.handlePollerEvents(events)
		}
	}
}

func (c *Checker) handlePollerEvents(events []event) {
	for _, event := range events {
		if pipe, exists := c.resultPipes.popResultPipe(event.Fd); exists {
			pipe <- event.Err
		}
		// error pipe not found
		// in this case, e.Fd should have been handled in the previous event.
	}
}

func (c *Checker) closePoller() error {
	c.pollerLock.Lock()
	defer c.pollerLock.Unlock()

	var err error
	if c.pollerFD() > 0 {
		err = unix.Close(c.pollerFD())
	}
	c.setPollerFD(-1)
	return err
}

// CheckAddr ..
func (c *Checker) CheckAddr(addr string, timeout time.Duration) error {
	// Set deadline
	deadline := time.Now().Add(timeout)

	// Parse address
	rAddr, err := parseSockAddr(addr)
	if err != nil {
		return err
	}

	// Create socket with options set
	fd, err := createSocket()
	if err != nil {
		return err
	}

	// Socket should be closed anyway
	defer unix.Close(fd)

	// Connect to the address
	if success, cErr := connect(fd, rAddr); cErr != nil {
		// If there was an error, return it.
		return &ErrConnect{cErr}
	} else if success {
		// If the connect was successful, we are done.
		return nil
	}

	// Otherwise wait for the result of connect.
	return c.waitConnectResult(fd, deadline.Sub(time.Now()))
}

func (c *Checker) waitConnectResult(fd int, timeout time.Duration) error {
	// get a pipe of connect result
	resultPipe := c.getPipe()
	defer func() {
		c.resultPipes.deregisterResultPipe(fd)
		c.putBackPipe(resultPipe)
	}()

	// this must be done before registerEvents
	c.resultPipes.registerResultPipe(fd, resultPipe)
	// Register to epoll for later error checking
	if err := registerEvents(c.pollerFD(), fd); err != nil {
		return err
	}

	// Wait for connect result
	return c.waitPipeTimeout(resultPipe, timeout)
}

func (c *Checker) waitPipeTimeout(pipe chan error, timeout time.Duration) error {
	select {
	case ret := <-pipe:
		return ret
	case <-time.After(timeout):
		return ErrTimeout
	}
}

// WaitReady returns a chan which is closed when the Checker is ready for use.
func (c *Checker) WaitReady() <-chan struct{} {
	return c.isReady
}
