package terminal

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type remoteSession struct {
	connection    net.Conn
	writeMutex    sync.Mutex
	modeMutex     sync.Mutex
	mode          Mode
	modeReady     bool
	readMutex     sync.Mutex
	readCallMutex sync.Mutex
	readErr       error
	pending       []byte
	output        chan []byte
	stop          chan struct{}
	stopOnce      sync.Once
	closeOnce     sync.Once
	ready         chan error
	readyOnce     sync.Once
}

func openRemoteSession(ctx context.Context, path string) (Session, error) {
	openContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(openContext, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect terminal helper: %w", err)
	}
	session := &remoteSession{
		connection: connection,
		output:     make(chan []byte),
		stop:       make(chan struct{}),
		ready:      make(chan error, 1),
	}
	go session.readLoop()
	if err := session.send(frameOpen, nil); err != nil {
		session.Close("helper open failed")
		return nil, fmt.Errorf("open terminal helper session: %w", err)
	}
	select {
	case err := <-session.ready:
		if err != nil {
			session.Close("helper rejected session")
			return nil, err
		}
		return session, nil
	case <-openContext.Done():
		session.Close("helper open cancelled")
		return nil, openContext.Err()
	}
}

func (s *remoteSession) readLoop() {
	defer close(s.output)
	defer s.signalStop()
	for {
		kind, payload, err := readFrame(s.connection)
		if err != nil {
			s.setReadError(err)
			s.finishReady(err)
			return
		}
		switch kind {
		case frameReady:
			s.finishReady(nil)
		case frameMode:
			if len(payload) != 2 {
				s.setReadError(errors.New("invalid terminal mode frame"))
				s.finishReady(s.currentReadError())
				return
			}
			s.modeMutex.Lock()
			s.mode = Mode{Echo: payload[0] != 0, Canonical: payload[1] != 0}
			s.modeReady = true
			s.modeMutex.Unlock()
		case frameOutput:
			data := append([]byte(nil), payload...)
			select {
			case s.output <- data:
			case <-s.stop:
				return
			}
		case frameError:
			s.setReadError(errors.New(string(payload)))
			s.finishReady(s.currentReadError())
			return
		default:
			s.setReadError(fmt.Errorf("unknown terminal helper frame %d", kind))
			s.finishReady(s.currentReadError())
			return
		}
	}
}

func (s *remoteSession) Read(buffer []byte) (int, error) {
	s.readCallMutex.Lock()
	defer s.readCallMutex.Unlock()
	s.readMutex.Lock()
	if len(s.pending) > 0 {
		count := copy(buffer, s.pending)
		s.pending = s.pending[count:]
		s.readMutex.Unlock()
		return count, nil
	}
	s.readMutex.Unlock()
	data, ok := <-s.output
	if !ok {
		if err := s.currentReadError(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	count := copy(buffer, data)
	if count < len(data) {
		// PTY output frames are bounded, but callers may provide a smaller
		// buffer. Preserve the remainder for the next Read.
		s.readMutex.Lock()
		s.pending = append(s.pending, data[count:]...)
		s.readMutex.Unlock()
	}
	return count, nil
}

func (s *remoteSession) Write(data []byte) (int, error) {
	if err := s.send(frameInput, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *remoteSession) TerminalMode() (Mode, error) {
	s.modeMutex.Lock()
	defer s.modeMutex.Unlock()
	if !s.modeReady {
		return Mode{}, errors.New("terminal mode is not ready")
	}
	return s.mode, nil
}

func (s *remoteSession) Resize(columns, rows int) error {
	if columns < 1 || columns > 1000 || rows < 1 || rows > 1000 {
		return errors.New("terminal size out of range")
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[:4], uint32(columns))
	binary.BigEndian.PutUint32(payload[4:], uint32(rows))
	return s.send(frameResize, payload)
}

func (s *remoteSession) Close(_ string) {
	s.closeOnce.Do(func() {
		s.signalStop()
		_ = s.send(frameClose, nil)
		_ = s.connection.Close()
	})
}

func (s *remoteSession) signalStop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *remoteSession) send(kind byte, payload []byte) error {
	if len(payload) > maxHelperFrame-1 {
		return errors.New("terminal helper frame is too large")
	}
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	return writeFrame(s.connection, kind, payload)
}

func (s *remoteSession) finishReady(err error) {
	s.readyOnce.Do(func() { s.ready <- err; close(s.ready) })
}

func (s *remoteSession) setReadError(err error) {
	s.readMutex.Lock()
	s.readErr = err
	s.readMutex.Unlock()
}

func (s *remoteSession) currentReadError() error {
	s.readMutex.Lock()
	defer s.readMutex.Unlock()
	return s.readErr
}
