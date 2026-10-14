package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxRead 是每轮最多读的字节数，日志一下子灌进来也不卡住一轮。
const maxRead = 4 << 20

// Tailer 跟读一个文件新增的完整行，处理截断（文件变短从头读）和轮转（路径换成新文件，读完旧的再读新的）。
type Tailer struct {
	path     string
	backfill int64 // 第一次打开时往回读多少字节（0 = 从文件末尾开始，只看新增）
	f        *os.File
	fi       os.FileInfo
	off      int64
	partial  []byte
	opened   bool // 曾经成功打开过：之后出现的新文件从头读
}

// NewTailer 建一个跟读器；backfill 是第一次打开时往回读的字节数。
func NewTailer(path string, backfill int64) *Tailer {
	return &Tailer{path: path, backfill: backfill}
}

// Path 是跟读的文件。
func (t *Tailer) Path() string { return t.path }

// Close 关掉文件。
func (t *Tailer) Close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

// Lines 返回上次以来新增的完整行。文件读不到返回错误（调用方照实报 unknown）。
func (t *Tailer) Lines() ([]string, error) {
	var out []string
	if t.f != nil {
		pfi, err := os.Stat(t.path)
		if err != nil {
			t.Close()
			return nil, err
		}
		if !os.SameFile(t.fi, pfi) {
			// 轮转：把旧文件剩下的读完，再换到新文件从头读
			out = append(out, t.read()...)
			if len(t.partial) > 0 {
				out = append(out, string(t.partial))
				t.partial = nil
			}
			t.Close()
		} else if pfi.Size() < t.off {
			// 截断：从头读
			t.off, t.partial = 0, nil
		}
	}
	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return out, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return out, err
		}
		if fi.IsDir() {
			f.Close()
			return out, fmt.Errorf("%s 是目录，不是日志文件", t.path)
		}
		t.f, t.fi = f, fi
		switch {
		case t.opened:
			t.off = 0
		default:
			t.off = fi.Size() - t.backfill
			if t.off < 0 {
				t.off = 0
			}
			if t.off > 0 {
				t.skipToLineStart()
			}
		}
		t.opened = true
	}
	out = append(out, t.read()...)
	return out, nil
}

// skipToLineStart 往回读时落在行中间：丢到下一个换行为止。
func (t *Tailer) skipToLineStart() {
	buf := make([]byte, 4096)
	for {
		n, err := t.f.ReadAt(buf, t.off)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			t.off += int64(i + 1)
			return
		}
		t.off += int64(n)
		if err != nil {
			return
		}
	}
}

func (t *Tailer) read() []string {
	if t.f == nil {
		return nil
	}
	var lines []string
	buf := make([]byte, 64*1024)
	total := 0
	for total < maxRead {
		n, err := t.f.ReadAt(buf, t.off)
		if n > 0 {
			t.off += int64(n)
			total += n
			data := append(t.partial, buf[:n]...)
			for {
				i := bytes.IndexByte(data, '\n')
				if i < 0 {
					break
				}
				lines = append(lines, string(bytes.TrimRight(data[:i], "\r")))
				data = data[i+1:]
			}
			t.partial = append([]byte(nil), data...)
			if len(t.partial) > 1<<20 { // 一行超过 1MB：不当行看，丢掉
				t.partial = nil
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return lines
			}
			break
		}
	}
	return lines
}
