package dd

import (
	"errors"
	"net/http"
	"sync"
)

type wrapResp struct {
	resp           *http.Response
	usedLength     int64
	usedLengthLock sync.RWMutex
}

func CreateWrapResp(resp *http.Response) *wrapResp {
	w := &wrapResp{
		resp: resp,
	}
	return w
}

func (w *wrapResp) Close() error {
	if w.resp.Body != nil {
		return w.resp.Body.Close()
	} else {
		return errors.New("w.resp.Body nil ")
	}
}

func (w *wrapResp) Read(p []byte) (n int, err error) {
	n, err = w.resp.Body.Read(p)
	if n >= 0 && err == nil {
		w.usedLengthLock.Lock()
		w.usedLength += int64(n)
		w.usedLengthLock.Unlock()
	}
	return
}

// 获取Body总大小
func (w *wrapResp) getContentLength() int64 {
	return w.resp.ContentLength
}

// 获取已读取的Body大小
func (w *wrapResp) getUsedLength() int64 {
	w.usedLengthLock.RLock()
	defer w.usedLengthLock.RUnlock()
	return w.usedLength
}
