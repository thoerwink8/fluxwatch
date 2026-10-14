package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// maxRead 是每轮最多读的字节数，日志一下子灌进来也不卡住一轮。
const maxRead = 4 << 20

// Tailer 跟读一个文件新增的完整行。
//
// 每轮打开、seek 到上次的偏移、读完就关，不长期占着文件：Windows 上开着的文件改不了名、删不掉，
// 会挡住被盯的程序自己轮转日志。截断和轮转靠文件身份（os.SameFile）和大小判断：
//   - 同一个文件变短了 → 截断，从头读；
//   - 路径换成了另一个文件 → 轮转，新文件从头读。旧文件在上一轮之后、改名之前写的最后几行读不到（最多一轮的量）。
type Tailer struct {
	path     string
	backfill int64       // 第一次读到文件时往回读多少字节（0 = 从文件末尾开始，只看新增）
	fi       os.FileInfo // 上一轮读的那个文件；nil = 还没读到过
	off      int64
	partial  []byte
	missing  bool // 上一次打开时文件不存在：之后出现的文件是新建的，从头读
}

// NewTailer 建一个跟读器；backfill 是第一次读到文件时往回读的字节数。
func NewTailer(path string, backfill int64) *Tailer {
	return &Tailer{path: path, backfill: backfill}
}

// Path 是跟读的文件。
func (t *Tailer) Path() string { return t.path }

// Close 保留给调用方；每轮读完已经关了文件，这里不占任何句柄。
func (t *Tailer) Close() {}

// Lines 返回上次以来新增的完整行。文件读不到返回错误（调用方照实报 unknown）。
func (t *Tailer) Lines() ([]string, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.missing = true
		}
		return nil, err
	}
	defer f.Close()
	wasMissing := t.missing
	t.missing = false
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不是日志文件", t.path)
	}
	switch {
	case t.fi == nil && wasMissing:
		// 盯着的时候文件才建出来：里面都是新写的，从头读
		t.off, t.partial = 0, nil
	case t.fi == nil:
		// 第一次：从末尾（或往回 backfill 字节）开始
		t.off = fi.Size() - t.backfill
		if t.off < 0 {
			t.off = 0
		}
		t.partial = nil
		if t.off > 0 {
			t.skipToLineStart(f)
		}
	case !os.SameFile(t.fi, fi):
		// 轮转：路径换成了新文件，从头读
		t.off, t.partial = 0, nil
	case fi.Size() < t.off:
		// 截断：从头读
		t.off, t.partial = 0, nil
	}
	t.fi = fi
	return t.read(f), nil
}

// skipToLineStart 往回读时落在行中间：丢到下一个换行为止。
func (t *Tailer) skipToLineStart(f *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := f.ReadAt(buf, t.off)
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

func (t *Tailer) read(f *os.File) []string {
	var lines []string
	buf := make([]byte, 64*1024)
	total := 0
	for total < maxRead {
		n, err := f.ReadAt(buf, t.off)
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
