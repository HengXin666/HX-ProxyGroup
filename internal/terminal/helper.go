package terminal

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	frameOpen   byte = 1
	frameInput  byte = 2
	frameResize byte = 3
	frameClose  byte = 4
	frameReady  byte = 11
	frameOutput byte = 12
	frameMode   byte = 13
	frameError  byte = 14

	maxHelperFrame = 1 << 20
)

// HelperConfig configures the root-only PTY helper. The helper is intended to
// be started by systemd and reached only through a mode-0660 Unix socket.
type HelperConfig struct {
	SocketPath  string
	SocketGroup string
	AllowedUser string
	Shell       string
	MaxSessions int
}

// RunHelper serves root PTYs until ctx is cancelled. It is deliberately kept
// outside the HTTP process: the control plane remains a non-root service.
func RunHelper(ctx context.Context, config HelperConfig, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	path := strings.TrimSpace(config.SocketPath)
	if path == "" {
		return errors.New("terminal helper socket path is required")
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = defaultMaxSessions
	}
	allowedUID, err := lookupUID(config.AllowedUser)
	if err != nil {
		return err
	}
	if err := prepareSocketPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create terminal helper socket directory: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen on terminal helper socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("set terminal helper socket mode: %w", err)
	}
	if config.SocketGroup != "" {
		group, lookupErr := user.LookupGroup(config.SocketGroup)
		if lookupErr != nil {
			return fmt.Errorf("lookup terminal helper socket group: %w", lookupErr)
		}
		gid, parseErr := strconv.Atoi(group.Gid)
		if parseErr != nil {
			return fmt.Errorf("parse terminal helper socket group id: %w", parseErr)
		}
		if err := os.Chown(path, os.Getuid(), gid); err != nil {
			return fmt.Errorf("set terminal helper socket group: %w", err)
		}
		if err := os.Chown(filepath.Dir(path), os.Getuid(), gid); err != nil {
			return fmt.Errorf("set terminal helper socket directory group: %w", err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("set terminal helper socket directory mode: %w", err)
		}
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	semaphore := make(chan struct{}, config.MaxSessions)
	var waitGroup sync.WaitGroup
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				waitGroup.Wait()
				return nil
			}
			return fmt.Errorf("accept terminal helper connection: %w", acceptErr)
		}
		if allowedUID >= 0 && !peerHasUID(connection, allowedUID) {
			_ = connection.Close()
			continue
		}
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			_ = connection.Close()
			waitGroup.Wait()
			return nil
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			handleHelperConnection(ctx, connection, config.Shell)
		}()
	}
}

func lookupUID(name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return -1, nil
	}
	account, err := user.Lookup(name)
	if err != nil {
		return -1, fmt.Errorf("lookup terminal helper user: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return -1, fmt.Errorf("parse terminal helper user id: %w", err)
	}
	return uid, nil
}

func prepareSocketPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect terminal helper socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("terminal helper socket path is not a Unix socket: %s", path)
	}
	probe, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = probe.Close()
		return fmt.Errorf("terminal helper socket is already active: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale terminal helper socket: %w", err)
	}
	return nil
}

func peerHasUID(connection net.Conn, wanted int) bool {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return false
	}
	var actual int
	var credentialErr error
	rawConn, controlErr := unixConnection.SyscallConn()
	if controlErr != nil {
		return false
	}
	if controlErr = rawConn.Control(func(fd uintptr) {
		credentials, err := unixPeerCredentials(int(fd))
		if err != nil {
			credentialErr = err
			return
		}
		actual = credentials
	}); controlErr != nil {
		return false
	}
	return credentialErr == nil && actual == wanted
}

func unixPeerCredentials(fd int) (int, error) {
	// SO_PEERCRED is Linux-specific, which matches the supported deployment
	// target and avoids trusting a client-provided identity field.
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(credentials.Uid), nil
}

func handleHelperConnection(ctx context.Context, connection net.Conn, shell string) {
	closeOnContext := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-closeOnContext:
		}
	}()
	defer close(closeOnContext)

	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, _, err := readFrame(connection)
	_ = connection.SetReadDeadline(time.Time{})
	if err != nil || kind != frameOpen {
		_ = connection.Close()
		return
	}
	ptyFile, command, err := startShell(shell, os.Environ())
	if err != nil {
		_ = writeFrame(connection, frameError, []byte("start privileged terminal: "+err.Error()))
		_ = connection.Close()
		return
	}
	session := newPTYSession(ptyFile, command)
	defer session.Close("helper connection closed")
	defer connection.Close()
	writer := &helperWriter{connection: connection}
	if err := writer.mode(session); err != nil {
		return
	}
	if err := writer.send(frameReady, nil); err != nil {
		return
	}

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, 16<<10)
		for {
			count, readErr := session.Read(buffer)
			if count > 0 {
				if err := writer.mode(session); err != nil {
					return
				}
				if err := writer.send(frameOutput, buffer[:count]); err != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		kind, payload, readErr := readFrame(connection)
		if readErr != nil {
			break
		}
		switch kind {
		case frameInput:
			if _, writeErr := session.Write(payload); writeErr != nil {
				break
			}
		case frameResize:
			if len(payload) != 8 {
				break
			}
			columns := int(binary.BigEndian.Uint32(payload[:4]))
			rows := int(binary.BigEndian.Uint32(payload[4:]))
			_ = session.Resize(columns, rows)
		case frameClose:
			break
		default:
			break
		}
		if kind == frameClose {
			break
		}
	}
	_ = connection.Close()
	session.Close("helper input closed")
	<-outputDone
}

type helperWriter struct {
	connection net.Conn
	mutex      sync.Mutex
}

func (w *helperWriter) send(kind byte, payload []byte) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return writeFrame(w.connection, kind, payload)
}

func (w *helperWriter) mode(session *ptySession) error {
	mode, err := session.TerminalMode()
	if err != nil {
		return err
	}
	payload := []byte{0, 0}
	if mode.Echo {
		payload[0] = 1
	}
	if mode.Canonical {
		payload[1] = 1
	}
	return w.send(frameMode, payload)
}

func writeFrame(writer io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxHelperFrame-1 {
		return errors.New("terminal helper frame is too large")
	}
	header := make([]byte, 5)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if err := writeAll(writer, header); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func readFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxHelperFrame-1 {
		return 0, nil, errors.New("terminal helper frame is too large")
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}
