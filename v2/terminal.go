package readline

import (
	"bufio"
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/goinsane/readline/v2/runeutil"

	"github.com/goinsane/xcontext"
)

type Terminal struct {
	config              *Config
	stdin               int
	stdout              int
	stderr              int
	screenBrokenPipeCh  chan struct{}
	screenSizeChangedCh chan struct{}
	lineResultCh        chan lineResult
	rb                  *runeutil.RuneBuffer
	stdinReader         io.ReadCloser
	stdinWriter         io.Writer
	ctx                 context.Context
	ctxCancel           context.CancelFunc
	wg                  sync.WaitGroup
	onceClose           sync.Once
	ioErr               atomic.Value
	ioInsMode           bool
	lckr                xcontext.Locker
	oldState            *State
}

func NewTerminal(config Config) (*Terminal, error) {
	var err error
	if config.Stdin == nil {
		config.Stdin = os.Stdin
	}
	if config.Stdout == nil {
		config.Stdout = os.Stdout
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	t := &Terminal{
		config:              &config,
		stdin:               int(config.Stdin.Fd()),
		stdout:              int(config.Stdout.Fd()),
		stderr:              int(config.Stderr.Fd()),
		screenBrokenPipeCh:  make(chan struct{}, 1),
		screenSizeChangedCh: make(chan struct{}, 1),
		lineResultCh:        make(chan lineResult, 1),
	}
	interactive := IsTerminal(t.stdin)
	if config.ForceUseInteractive {
		interactive = true
	}
	t.rb, err = runeutil.NewRuneBuffer(config.Stdout, config.Prompt, config.Mask, interactive, t.GetWidth())
	if err != nil {
		return nil, err
	}
	t.stdinReader, t.stdinWriter = newExtendedStdin(config.Stdin)
	t.ctx, t.ctxCancel = context.WithCancel(context.Background())
	RegisterOnScreenBrokenPipe(t.screenBrokenPipeCh)
	RegisterOnScreenSizeChanged(t.screenSizeChangedCh)
	t.wg.Add(1)
	go t.ioloop()
	return t, nil
}

func (t *Terminal) Close() error {
	var err error
	t.onceClose.Do(func() {
		t.ctxCancel()
		_ = t.stdinReader.Close()
		t.wg.Wait()
		UnregisterOnScreenBrokenPipe(t.screenBrokenPipeCh)
		UnregisterOnScreenSizeChanged(t.screenSizeChangedCh)
		err = t.ExitRawMode()
	})
	return err
}

func (t *Terminal) Stdin() *os.File {
	return t.config.Stdin
}

func (t *Terminal) Stdout() *os.File {
	return t.config.Stdout
}

func (t *Terminal) Stderr() *os.File {
	return t.config.Stderr
}

func (t *Terminal) StdinWriter() io.Writer {
	return t.stdinWriter
}

func (t *Terminal) Write(p []byte) (int, error) {
	return t.config.Stdout.Write(p)
}

// WriteStdin prefill the next Stdin fetch
// Next time you call ReadLine() this value will be writen before the user input
func (t *Terminal) WriteStdin(p []byte) (int, error) {
	return t.stdinWriter.Write(p)
}

func (t *Terminal) EnterRawMode() error {
	t.lckr.Lock()
	defer t.lckr.Unlock()
	return t.enterRawMode()
}

func (t *Terminal) enterRawMode() error {
	var err error
	if t.oldState != nil {
		return ErrAlreadyInRawMode
	}
	t.oldState, err = SetRawMode(t.stdin)
	if err != nil {
		return err
	}
	return nil
}

func (t *Terminal) ExitRawMode() error {
	t.lckr.Lock()
	defer t.lckr.Unlock()
	return t.exitRawMode()
}

func (t *Terminal) exitRawMode() error {
	if t.oldState == nil {
		return ErrNotInRawMode
	}
	if err := RestoreState(t.stdin, t.oldState); err != nil {
		return err
	}
	t.oldState = nil
	return nil
}

func (t *Terminal) GetSize() (int, int, error) {
	cols, rows, err := GetSize(t.stdout)
	if err != nil {
		cols, rows, err = GetSize(t.stderr)
	}
	return cols, rows, err
}

func (t *Terminal) GetWidth() int {
	w := GetWidth(t.stdout)
	if w < 0 {
		w = GetWidth(t.stderr)
	}
	return w
}

func (t *Terminal) GetHeight() int {
	h := GetHeight(t.stdout)
	if h < 0 {
		h = GetHeight(t.stderr)
	}
	return h
}

func (t *Terminal) ReadBytes() ([]byte, error) {
	return t.ReadBytesContext(context.Background())
}

func (t *Terminal) ReadBytesContext(ctx context.Context) (line []byte, err error) {
	err = t.lckr.LockContext(ctx)
	if err != nil {
		return nil, err
	}
	defer t.lckr.Unlock()
	ioErr := t.ioErr.Load()
	if ioErr != nil {
		return nil, ioErr.(error)
	}
	err = t.enterRawMode()
	if err != nil {
		return nil, err
	}
	defer t.exitRawMode()
	t.rb.Refresh(nil)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c := <-t.lineResultCh:
		return c.Line, c.Err
	}
}

func (t *Terminal) ReadString() (string, error) {
	return t.ReadStringContext(context.Background())
}

func (t *Terminal) ReadStringContext(ctx context.Context) (string, error) {
	p, err := t.ReadBytesContext(ctx)
	return string(p), err
}

func (t *Terminal) ReadLine() (string, error) {
	return t.ReadString()
}

func (t *Terminal) ReadLineContext(ctx context.Context) (string, error) {
	return t.ReadStringContext(ctx)
}

func (t *Terminal) ioloop() {
	defer t.wg.Done()

	br := bufio.NewReader(t.stdinReader)
	escaped := false
	escBuf := make([]byte, 0, 16)

	var err error
	for err == nil {
		err = t.ctx.Err()
		if err != nil {
			continue
		}
		var b byte
		var p []byte
		b, err = br.ReadByte()
		if err != nil {
			if isInterruptedSyscall(err) {
				err = nil
				escaped = false
				escBuf = escBuf[:0]
			}
			continue
		}
		if b >= utf8.RuneSelf && !escaped {
			_ = br.UnreadByte()
			var r rune
			r, _, err = br.ReadRune()
			if err != nil {
				continue
			}
			var utf8Array [utf8.UTFMax]byte
			p = utf8Array[:utf8.EncodeRune(utf8Array[:], r)]
		} else {
			p = []byte{b}
		}

		if b == CharEscape || escaped {
			if !escaped {
				escaped = true
				escBuf = escBuf[:0]
			} else {
				escBuf = append(escBuf, p...)
			}
			escKeyPair := decodeEscapeKeyPair(escBuf)
			if escKeyPair != nil && t.escape(escKeyPair) {
				escaped = false
				p = escKeyPair.Remainder
			} else {
				if len(escBuf) < cap(escBuf) {
					continue
				}
				escaped = false
				p = append([]byte{CharEscape}, escBuf...)
			}
		}

		if len(p) <= 0 {
			continue
		}

		switch p[0] {
		case CharLineStart:
			t.opLineStart()

		case CharBackward:
			t.opBackward()

		case CharInterrupt:
			err = ErrInterrupted

		case CharDelete:
			err = io.EOF

		case CharLineEnd:
			t.opLineEnd()

		case CharForward:
			t.opForward()

		case CharBell:
			t.bell()

		case CharBackspace, CharBackspaceEx:
			t.opBackspace()

		case CharTab:
			t.opTab()

		case CharFeed, CharReturn:
			t.opReturn()

		case CharKill:
			t.opKill()

		case CharClear:
			t.opClear()

		case CharNext:
			t.opNext()

		case CharPrev:
			t.opPrev()

		case CharBckSearch:
			t.opBckSearch()

		case CharFwdSearch:
			t.opFwdSearch()

		case CharTranspose:
			t.opTranspose()

		case CharKillFront:
			t.opKillFront()

		case CharKillWordFront:
			t.opKillWordFront()

		case CharYank:
			t.opYank()

		default:
			p = encodeControlChars(p)
			if !t.ioInsMode {
				t.rb.WriteBytes(p)
			} else {
				t.rb.InsertBytes(p)
			}

		}
	}

	if xcontext.IsContextError(err) {
		err = io.EOF
	}
	t.ioErr.Store(err)
	t.sendLineResult(t.rb.Bytes(), err)
}

func (t *Terminal) escape(escKeyPair *escapeKeyPair) bool {
	switch escKeyPair.Char {
	case CharBackspace, CharBackspaceEx:
		t.opKillWordFront()

	case CharTranspose:
		t.opTranspose()

	case CharEscape:

	case 'O', '[':
		return t.escapeEx(escKeyPair)

	case 'b':
		t.opBackwardWord()

	case 'd':
		t.opKillWord()

	case 'f':
		t.opForwardWord()

	default:
		t.bell()

	}

	return true
}

func (t *Terminal) escapeEx(escKeyPair *escapeKeyPair) bool {
	switch escKeyPair.Type {
	case '\x00':
		return false

	case '~':
		t.escapeTilda(escKeyPair)
		return true

	case 'R':
		t.escapeR(escKeyPair)
		return true

	default:
		if escKeyPair.Attribute <= 0 && escKeyPair.Attribute2 < 0 {
			switch escKeyPair.Type {
			case 'A':
				t.opPrev()

			case 'B':
				t.opNext()

			case 'C':
				t.opForward()

			case 'D':
				t.opBackward()

			//case 'E':

			case 'F':
				t.opLineEnd()

			case 'H':
				t.opLineStart()

			default:
				t.bell()

			}
		} else {
			t.bell()
		}

	}

	return true
}

func (t *Terminal) escapeTilda(escKeyPair *escapeKeyPair) {
	if escKeyPair.Attribute2 < 0 {
		switch escKeyPair.Attribute {
		case 1:
			t.opLineStart()

		case 2:
			t.ioInsMode = !t.ioInsMode

		case 3:
			t.opDelete()

		case 4:
			t.opLineEnd()

		case 5:
			// pageup

		case 6:
			// pagedown

		case 7:
			t.opLineStart()

		case 8:
			t.opLineEnd()

		default:
			t.bell()

		}
	} else {
		t.bell()
	}
}

func (t *Terminal) escapeR(escKeyPair *escapeKeyPair) {
	if escKeyPair.Attribute >= 0 && escKeyPair.Attribute2 >= 0 {
		t.screenSizeChanged(escKeyPair.Attribute2, escKeyPair.Attribute)
	} else {
		t.bell()
	}
}

func (t *Terminal) sendLineResult(line []byte, e error) {
	r := lineResult{
		Line: line,
		Err:  e,
	}
	select {
	case t.lineResultCh <- r:
	default:
	}
}

func (t *Terminal) screenSizeChanged(width, height int) {
	_ = t.rb.SetScreenWidth(width)
}

func (t *Terminal) write(p []byte) {
	_, _ = t.Write(p)
}

func (t *Terminal) bell() {
	t.write([]byte{CharBell})
}

func (t *Terminal) opLineStart() {
	if !t.rb.MoveToLineStart() {
		t.bell()
	}
}

func (t *Terminal) opBackward() {
	if !t.rb.MoveBackward() {
		t.bell()
	}
}

func (t *Terminal) opDelete() {
	if !t.rb.Delete() {
		t.bell()
	}
}

func (t *Terminal) opLineEnd() {
	if !t.rb.MoveToLineEnd() {
		t.bell()
	}
}

func (t *Terminal) opForward() {
	if !t.rb.MoveForward() {
		t.bell()
	}
}

func (t *Terminal) opBackspace() {
	if !t.rb.Backspace() {
		t.bell()
	}
}

func (t *Terminal) opTab() {
	t.bell()
}

func (t *Terminal) opReturn() {
	t.rb.MoveToLineEnd()
	t.rb.WriteRune('\n')
	p := t.rb.Bytes()
	if len(p) > 0 {
		p = p[:len(p)-1]
	}
	t.sendLineResult(p, nil)
	t.rb.ResetBuf()
}

func (t *Terminal) opKill() {
	if !t.rb.Kill() {
		t.bell()
	}
}

func (t *Terminal) opClear() {
	t.rb.Clear()
}

func (t *Terminal) opNext() {

}

func (t *Terminal) opPrev() {

}

func (t *Terminal) opBckSearch() {

}

func (t *Terminal) opFwdSearch() {

}

func (t *Terminal) opTranspose() {
	if !t.rb.Transpose() {
		t.bell()
	}
}

func (t *Terminal) opKillFront() {
	if !t.rb.KillFront() {
		t.bell()
	}
}

func (t *Terminal) opKillWord() {
	if !t.rb.KillWord() {
		t.bell()
	}
}

func (t *Terminal) opKillWordFront() {
	if !t.rb.KillWordFront() {
		t.bell()
	}
}

func (t *Terminal) opYank() {
	if !t.rb.Yank() {
		t.bell()
	}
}

func (t *Terminal) opBackwardWord() {
	if !t.rb.MoveToPrevWord() {
		t.bell()
	}
}

func (t *Terminal) opForwardWord() {
	if !t.rb.MoveToNextWord() {
		t.bell()
	}
}
