package runeutil

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

type RuneBuffer struct {
	w           io.Writer
	prompt      []rune
	promptWidth int
	mask        rune
	interactive bool
	screenWidth int

	mu  sync.Mutex
	idx int
	buf []rune

	backup *runeBufferBackup

	hadClean bool

	lastKill []rune
}

func NewRuneBuffer(w io.Writer, prompt string, mask rune, interactive bool, screenWidth int) (*RuneBuffer, error) {
	var err error
	rb := &RuneBuffer{
		w: w,
	}

	rb.setPrompt(prompt)
	rb.setMask(mask)
	rb.setInteractive(interactive)
	err = rb.setScreenWidth(screenWidth)
	if err != nil {
		return nil, err
	}

	return rb, nil
}

func (rb *RuneBuffer) SetPrompt(prompt string) {
	rb.Refresh(func() {
		rb.setPrompt(prompt)
	})
}

func (rb *RuneBuffer) setPrompt(prompt string) {
	rb.prompt = []rune(prompt)
	rb.promptWidth = WidthAll(ColorFilter(rb.prompt))
}

func (rb *RuneBuffer) SetMask(mask rune) {
	rb.Refresh(func() {
		rb.setMask(mask)
	})
}

func (rb *RuneBuffer) setMask(mask rune) {
	rb.mask = mask
}

func (rb *RuneBuffer) SetInteractive(on bool) {
	rb.mu.Lock()
	rb.setInteractive(on)
	rb.mu.Unlock()
	rb.Refresh(nil)
}

func (rb *RuneBuffer) setInteractive(on bool) {
	rb.interactive = on
}

func (rb *RuneBuffer) SetScreenWidth(screenWidth int) error {
	var err error
	rb.Refresh(func() {
		err = rb.setScreenWidth(screenWidth)
	})
	return err
}

func (rb *RuneBuffer) setScreenWidth(screenWidth int) error {
	if screenWidth <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidScreenWidth, screenWidth)
	}
	rb.screenWidth = screenWidth
	return nil
}

func (rb *RuneBuffer) Index() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.idx
}

func (rb *RuneBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return len(rb.buf)
}

func (rb *RuneBuffer) String() string {
	return string(rb.Runes())
}

func (rb *RuneBuffer) Bytes() []byte {
	return []byte(rb.String())
}

func (rb *RuneBuffer) Runes() []rune {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return Copy(rb.buf)
}

func (rb *RuneBuffer) RunesTo(lastIdx int) []rune {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return Copy(rb.bufAt(0, lastIdx))
}

func (rb *RuneBuffer) RunesSub(count int) []rune {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return Copy(rb.bufAt(-1, count))
}

func (rb *RuneBuffer) RunesAt(idx int, count int) []rune {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return Copy(rb.bufAt(idx, count))
}

func (rb *RuneBuffer) bufAt(idx int, count int) []rune {
	if idx < 0 {
		idx = rb.idx
	}
	if idx > len(rb.buf) {
		idx = len(rb.buf)
	}
	var s []rune
	if count >= 0 {
		s = rb.buf[idx:]
		if len(s) > count {
			s = s[:count]
		}
	} else {
		start := idx + count
		if start < 0 {
			start = 0
		}
		s = rb.buf[start:idx]
	}
	return s
}

func (rb *RuneBuffer) Width() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return WidthAll(rb.buf)
}

func (rb *RuneBuffer) WidthTo(lastIdx int) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.widthAt(0, lastIdx)
}

func (rb *RuneBuffer) WidthSub(count int) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.widthAt(-1, count)
}

func (rb *RuneBuffer) WidthAt(idx int, count int) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.widthAt(idx, count)
}

func (rb *RuneBuffer) widthAt(idx int, count int) int {
	return WidthAll(rb.bufAt(idx, count))
}

func (rb *RuneBuffer) Refresh(f func()) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.interactive {
		if f != nil {
			f()
		}
		return
	}

	rb.clean()
	defer rb.print()
	if f != nil {
		f()
	}
}

func (rb *RuneBuffer) Set(idx int, buf []rune) {
	rb.Refresh(func() {
		rb.setBuf(idx, buf)
	})
}

func (rb *RuneBuffer) SetBuf(idx int, buf []rune) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.setBuf(idx, buf)
}

func (rb *RuneBuffer) setBuf(idx int, buf []rune) {
	rb.idx = idx
	rb.buf = CopyAndGrow(buf, cap(buf)-len(buf))
}

func (rb *RuneBuffer) Reset() {
	rb.Refresh(func() {
		rb.resetBuf()
	})
}

func (rb *RuneBuffer) ResetBuf() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.resetBuf()
}

func (rb *RuneBuffer) resetBuf() {
	rb.idx = 0
	rb.buf = rb.buf[:0]
}

func (rb *RuneBuffer) SetRunes(s []rune) {
	rb.Set(len(s), s)
}

func (rb *RuneBuffer) Backup() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.backup = &runeBufferBackup{rb.buf, rb.idx}
}

func (rb *RuneBuffer) Restore() {
	rb.Refresh(func() {
		if rb.backup == nil {
			return
		}
		rb.buf = rb.backup.buf
		rb.idx = rb.backup.idx
	})
}

func (rb *RuneBuffer) write(p []byte) {
	_, _ = rb.w.Write(p)
}

func (rb *RuneBuffer) print() {
	rb.hadClean = false
	rb.write(rb.outputPrint())
}

func (rb *RuneBuffer) outputPrint() []byte {
	buf := bytes.NewBuffer(nil)
	buf.WriteString(string(rb.prompt))
	if rb.mask != 0 && len(rb.buf) > 0 {
		buf.Write([]byte(strings.Repeat(string(rb.mask), len(rb.buf)-1)))
		if rb.buf[len(rb.buf)-1] == '\n' {
			buf.Write([]byte{'\n'})
		} else {
			buf.Write([]byte(string(rb.mask)))
		}
		if len(rb.buf) > rb.idx {
			buf.Write(rb.getBackspaceSequence())
		}
	} else {
		for _, c := range rb.buf {
			if c == '\t' {
				buf.WriteString(strings.Repeat(" ", TabWidth))
			} else {
				buf.WriteRune(c)
			}
		}
		if rb.isInLineEdge() {
			buf.Write([]byte(" \b"))
		}
	}
	// cursor position
	if len(rb.buf) > rb.idx {
		buf.Write(rb.getBackspaceSequence())
	}
	return buf.Bytes()
}

func (rb *RuneBuffer) getBackspaceSequence() []byte {
	var sep = map[int]bool{}

	for idx, size := 0, WidthAll(rb.buf); idx < size; idx++ {
		if idx == 0 {
			idx -= rb.promptWidth
		}
		idx += rb.screenWidth

		sep[idx] = true
	}

	var buf []byte
	for idx := len(rb.buf); idx > rb.idx; idx-- {
		if sep[idx] {
			// up one line, go to the start of the line and move cursor right to the end (rb.screenWidth)
			buf = append(buf, "\033[A"+"\r"+"\033["+strconv.Itoa(rb.screenWidth)+"C"...)
			continue
		}
		// move input to the left of one
		buf = append(buf, '\b')
	}

	return buf
}

func (rb *RuneBuffer) Clean() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.clean()
}

func (rb *RuneBuffer) clean() {
	rb.cleanWithIdxLine(rb.idxLine())
}

func (rb *RuneBuffer) cleanWithIdxLine(idxLine int) {
	if rb.hadClean || !rb.interactive {
		return
	}
	rb.hadClean = true
	rb.write(rb.outputCleanWithIdxLine(idxLine))
}

func (rb *RuneBuffer) outputCleanWithIdxLine(idxLine int) []byte {
	buf := bytes.NewBuffer(nil)
	if rb.screenWidth <= 0 {
		buf.WriteString(strings.Repeat("\r\b", rb.promptWidth+len(rb.buf)))
		buf.Write([]byte("\033[J"))
		return buf.Bytes()
	}
	buf.Write([]byte("\033[J")) // just like ^k :)
	if idxLine == 0 {
		buf.WriteString("\033[2K")
		buf.WriteString("\r")
	} else {
		for i := 0; i < idxLine; i++ {
			buf.WriteString("\033[2K\r\033[A")
		}
		buf.WriteString("\033[2K\r")
	}
	return buf.Bytes()
}

func (rb *RuneBuffer) IdxLine() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.idxLine()
}

func (rb *RuneBuffer) idxLine() int {
	sp := rb.getSplitByLine(rb.buf[:rb.idx])
	return len(sp) - 1
}

func (rb *RuneBuffer) isInLineEdge() bool {
	sp := rb.getSplitByLine(rb.buf)
	return len(sp[len(sp)-1]) == 0
}

func (rb *RuneBuffer) getSplitByLine(s []rune) []string {
	return SplitByLine(rb.promptWidth, rb.screenWidth, s)
}

func (rb *RuneBuffer) IsCursorInEnd() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.isCursorInEnd()
}

func (rb *RuneBuffer) isCursorInEnd() bool {
	return rb.idx == len(rb.buf)
}

func (rb *RuneBuffer) LineCount() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.lineCount()
}

func (rb *RuneBuffer) lineCount() int {
	return LineCount(rb.screenWidth, rb.promptWidth+WidthAll(rb.buf))
}

func (rb *RuneBuffer) CursorLineCount() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.cursorLineCount()
}

func (rb *RuneBuffer) cursorLineCount() int {
	return rb.lineCount() - rb.idxLine()
}

func (rb *RuneBuffer) WriteBytes(p []byte) {
	rb.WriteString(string(p))
}

func (rb *RuneBuffer) WriteString(s string) {
	rb.WriteRunes([]rune(s))
}

func (rb *RuneBuffer) WriteRunes(s []rune) {
	rb.Refresh(func() {
		rem := rb.buf[rb.idx:]
		tail := append(CopyAndGrow(s, len(rem)), rem...)
		rb.buf = append(rb.buf[:rb.idx], tail...)
		rb.idx += len(s)
	})
}

func (rb *RuneBuffer) WriteRune(r rune) {
	rb.WriteRunes([]rune{r})
}

func (rb *RuneBuffer) InsertBytes(p []byte) {
	rb.InsertString(string(p))
}

func (rb *RuneBuffer) InsertString(s string) {
	rb.InsertRunes([]rune(s))
}

func (rb *RuneBuffer) InsertRunes(s []rune) {
	rb.Refresh(func() {
		rb.buf = append(rb.buf, s[copy(rb.buf[rb.idx:], s):]...)
		rb.idx += len(s)
	})
}

func (rb *RuneBuffer) InsertRune(r rune) {
	rb.InsertRunes([]rune{r})
}

func (rb *RuneBuffer) MoveToLineStart() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}
		rb.idx = 0
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveToLineEnd() (success bool) {
	rb.Refresh(func() {
		if rb.idx == len(rb.buf) {
			return
		}
		rb.idx = len(rb.buf)
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveBackward() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}
		rb.idx--
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveForward() (success bool) {
	rb.Refresh(func() {
		if rb.idx == len(rb.buf) {
			return
		}
		rb.idx++
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveToPrevWord() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}

		for i := rb.idx - 1; i > 0; i-- {
			if !IsWordBreak(rb.buf[i]) && IsWordBreak(rb.buf[i-1]) {
				rb.idx = i
				success = true
				return
			}
		}

		rb.idx = 0
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveToNextWord() (success bool) {
	rb.Refresh(func() {
		for i := rb.idx + 1; i < len(rb.buf); i++ {
			if !IsWordBreak(rb.buf[i]) && IsWordBreak(rb.buf[i-1]) {
				rb.idx = i
				success = true
				return
			}
		}

		rb.idx = len(rb.buf)
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveToEndWord() (success bool) {
	rb.Refresh(func() {
		// already at the end, so do nothing
		if rb.idx == len(rb.buf) {
			return
		}
		// if we are at the end of a word already, go to next
		if !IsWordBreak(rb.buf[rb.idx]) && IsWordBreak(rb.buf[rb.idx+1]) {
			rb.idx++
		}

		// keep going until at the end of a word
		for i := rb.idx + 1; i < len(rb.buf); i++ {
			if IsWordBreak(rb.buf[i]) && !IsWordBreak(rb.buf[i-1]) {
				rb.idx = i - 1
				success = true
				return
			}
		}

		rb.idx = len(rb.buf)
		success = true
	})
	return
}

func (rb *RuneBuffer) MoveTo(ch rune, prevChar, reverse bool) (success bool) {
	rb.Refresh(func() {
		if reverse {
			for i := rb.idx - 1; i >= 0; i-- {
				if rb.buf[i] == ch {
					rb.idx = i
					if prevChar {
						rb.idx++
					}
					success = true
					return
				}
			}
			return
		}
		for i := rb.idx + 1; i < len(rb.buf); i++ {
			if rb.buf[i] == ch {
				rb.idx = i
				if prevChar {
					rb.idx--
				}
				success = true
				return
			}
		}
	})
	return
}

func (rb *RuneBuffer) Backspace() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}
		rb.idx--
		rb.buf = append(rb.buf[:rb.idx], rb.buf[rb.idx+1:]...)
		success = true
	})
	return
}

func (rb *RuneBuffer) Transpose() (success bool) {
	rb.Refresh(func() {
		if len(rb.buf) <= 1 {
			return
		}

		if rb.idx == 0 {
			//rb.idx = 1
			return
		} else if rb.idx >= len(rb.buf) {
			rb.idx = len(rb.buf) - 1
		}
		rb.buf[rb.idx], rb.buf[rb.idx-1] = rb.buf[rb.idx-1], rb.buf[rb.idx]
		rb.idx++
		success = true
	})
	return
}

func (rb *RuneBuffer) Erase() (success bool) {
	rb.Refresh(func() {
		if len(rb.buf) == 0 {
			return
		}
		rb.idx = 0
		rb.pushKill(rb.buf[:])
		rb.buf = rb.buf[:0]
		success = true
	})
	return
}

func (rb *RuneBuffer) Delete() (success bool) {
	rb.Refresh(func() {
		if rb.idx == len(rb.buf) {
			return
		}
		rb.pushKill(rb.buf[rb.idx : rb.idx+1])
		rb.buf = append(rb.buf[:rb.idx], rb.buf[rb.idx+1:]...)
		success = true
	})
	return
}

func (rb *RuneBuffer) KillWord() (success bool) {
	rb.Refresh(func() {
		if rb.idx == len(rb.buf) {
			return
		}
		init := rb.idx
		for init < len(rb.buf) && IsWordBreak(rb.buf[init]) {
			init++
		}
		for i := init + 1; i < len(rb.buf); i++ {
			if !IsWordBreak(rb.buf[i]) && IsWordBreak(rb.buf[i-1]) {
				rb.pushKill(rb.buf[rb.idx : i-1])
				rb.buf = append(rb.buf[:rb.idx], rb.buf[i-1:]...)
				success = true
				return
			}
		}
		rb.pushKill(rb.buf[rb.idx:])
		rb.buf = rb.buf[:rb.idx]
		success = true
	})
	return
}

func (rb *RuneBuffer) KillWordFront() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}
		for i := rb.idx - 1; i > 0; i-- {
			if !IsWordBreak(rb.buf[i]) && IsWordBreak(rb.buf[i-1]) {
				rb.pushKill(rb.buf[i:rb.idx])
				rb.buf = append(rb.buf[:i], rb.buf[rb.idx:]...)
				rb.idx = i
				success = true
				return
			}
		}

		rb.buf = rb.buf[:0]
		rb.idx = 0
		success = true
	})
	return
}

func (rb *RuneBuffer) Kill() (success bool) {
	rb.Refresh(func() {
		if rb.idx == len(rb.buf) {
			return
		}
		rb.pushKill(rb.buf[rb.idx:])
		rb.buf = rb.buf[:rb.idx]
		success = true
	})
	return
}

func (rb *RuneBuffer) KillFront() (success bool) {
	rb.Refresh(func() {
		if rb.idx == 0 {
			return
		}
		length := len(rb.buf) - rb.idx
		rb.pushKill(rb.buf[:rb.idx])
		copy(rb.buf[:length], rb.buf[rb.idx:])
		rb.idx = 0
		rb.buf = rb.buf[:length]
		success = true
	})
	return
}

func (rb *RuneBuffer) Yank() (success bool) {
	if len(rb.lastKill) == 0 {
		return
	}
	rb.Refresh(func() {
		buf := make([]rune, 0, len(rb.buf)+len(rb.lastKill))
		buf = append(buf, rb.buf[:rb.idx]...)
		buf = append(buf, rb.lastKill...)
		buf = append(buf, rb.buf[rb.idx:]...)
		rb.buf = buf
		rb.idx += len(rb.lastKill)
		success = true
	})
	return
}

func (rb *RuneBuffer) pushKill(s []rune) {
	rb.lastKill = Copy(s)
}

func (rb *RuneBuffer) Clear() {
	rb.write([]byte("\033[H"))
	rb.Refresh(nil)
}

func (rb *RuneBuffer) SetStyle(start, end int, style string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if end < start {
		panic("end < start")
	}

	// goto start
	move := start - rb.idx
	if move > 0 {
		rb.write([]byte(string(rb.buf[rb.idx : rb.idx+move])))
	} else {
		rb.write(bytes.Repeat([]byte("\b"), rb.widthAt(-1, move)))
	}
	rb.write([]byte("\033[" + style + "m"))
	rb.write([]byte(string(rb.buf[start:end])))
	rb.write([]byte("\033[0m"))
	// TODO: move back
}

type runeBufferBackup struct {
	buf []rune
	idx int
}
