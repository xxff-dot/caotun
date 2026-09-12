package main

import (
	"io"
	"log"
	"os"
)

// ==================== 日志轮转 ====================

// 超过上限自动轮转的日志写入器（保留一份 .old），防日志无限增长
type cappedWriter struct {
	path string
	max  int64
	f    *os.File
	size int64
}

func newCappedWriter(path string, max int64) *cappedWriter {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil
	}
	var size int64
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	if size > max { // 启动时已超限，先轮转
		f.Close()
		os.Remove(path + ".old")
		os.Rename(path, path+".old")
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil
		}
		size = 0
	}
	return &cappedWriter{path: path, max: max, f: f, size: size}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.size+int64(len(p)) > w.max {
		w.f.Close()
		os.Remove(w.path + ".old")
		os.Rename(w.path, w.path+".old")
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 0, err
		}
		w.f = f
		w.size = 0
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// 日志写控制台 + 自动轮转的文件
func setupLogFile(path string, max int64) {
	if w := newCappedWriter(path, max); w != nil {
		log.SetOutput(io.MultiWriter(os.Stderr, w))
	}
}
