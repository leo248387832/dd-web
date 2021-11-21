package dd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"syscall"
	"time"
)

type Service struct {
	DDUrl                 string
	DDOf                  string
	DDMsgChan             chan map[string]string
	ErrorExit             bool
	SuccessExit           bool
	Disk                  int
	progressDone          bool
	retryNum              int
	resp                  *http.Response
	wrapResp              *wrapResp
	bufSize               int64
	graceStop             bool
	graceStopLock         sync.RWMutex
	lastStatisticWriteLen int64
	lastStatisticTime     int64
	lastMessageTime       int64
	gzip                  *gzip.Reader
	ctx                   context.Context
	cancel                context.CancelFunc
}

// CreateService 创建DD服务
func CreateService(ddUrl string, ddOf string, ctx context.Context, cancel context.CancelFunc, ddMsgChan chan map[string]string, errorExit bool, successExit bool, retryNum int) *Service {
	s := &Service{
		DDUrl:       ddUrl,
		DDOf:        ddOf,
		ctx:         ctx,
		cancel:      cancel,
		DDMsgChan:   ddMsgChan,
		ErrorExit:   errorExit,
		SuccessExit: successExit,
		retryNum:    retryNum,
		// bufSize 暂时写死32MB
		bufSize: 32 * 1024 * 1024,
	}
	return s
}

// Start 启动DD服务
func (s *Service) Start() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				if err := s.reportMsg("progress", map[string]interface{}{
					"inst_failed":         true,
					"inst_failed_message": "Unknown Error When call s.startDownload: " + fmt.Sprint(err),
					"progress":            0,
					"proc_exit_btn":       s.getProcExitBtnStatus("error"),
					"dd_img_max_size":     "0.00 MB",
					"dd_img_write_speed":  "0.00 MB/S",
					"dd_img_block_size":   "0.00 MB",
					"dd_img_write_size":   "0.00 MB",
					"dd_img_read_size":    "0.00 MB",
				}); err != nil {
					log.Print("Message Report Failed: ", err.Error())
				}
				s.progressDone = true
				if s.ErrorExit {
					s.cancel()
				}
			}
		}()
		s.startDownload()
	}()
}

// startDownload DD主逻辑
func (s *Service) startDownload() {
	var err error
	s.Disk, err = syscall.Open(s.DDOf, syscall.O_RDWR, 0777)
	if err != nil {
		if err := s.reportMsg("progress", map[string]interface{}{
			"inst_failed":         true,
			"inst_failed_message": "s.DiskHandler.StdinPipe(" + s.DDOf + "): " + err.Error(),
			"progress":            0,
			"proc_exit_btn":       s.getProcExitBtnStatus("error"),
			"dd_img_max_size":     "0.00 MB",
			"dd_img_write_speed":  "0.00 MB/S",
			"dd_img_block_size":   "0.00 MB",
			"dd_img_write_size":   "0.00 MB",
			"dd_img_read_size":    "0.00 MB",
		}); err != nil {
			log.Print("Message Report Failed: ", err.Error())
		}
		s.progressDone = true
		if s.ErrorExit {
			s.cancel()
		}
		return
	}

	defer func() {
		_ = syscall.Close(s.Disk)
		if s.wrapResp != nil {
			_ = s.wrapResp.Close()
		}
		if s.gzip != nil {
			_ = s.gzip.Close()
		}
	}()

	for i := 0; i <= s.retryNum; i++ {
		s.lastStatisticWriteLen = 0
		s.lastStatisticTime = 0
		if s.graceStopStatus() {
			break
		}
		s.resp, err = http.Get(s.DDUrl)
		if err != nil {
			log.Println(s.DDUrl, " Http Get[", i, "] Failed: ", err.Error())
			continue
		}
		s.wrapResp = CreateWrapResp(s.resp)
		s.gzip, err = gzip.NewReader(s.wrapResp)
		if err != nil {
			log.Println(s.DDUrl, " Gzip [", i, "] Failed: ", err.Error())
			continue
		}
		ddBuf := make([]byte, s.bufSize)
		var written int64
		var fullyDDMsg bool
		for {
			if s.graceStopStatus() {
				break
			}
			nr, er := s.gzip.Read(ddBuf)
			if nr > 0 {
				nw, ew := syscall.Write(s.Disk, ddBuf[0:nr])
				if nw < 0 || nr < nw {
					nw = 0
					if ew == nil {
						ew = errors.New("Writer Unknown Error: nr < nw || nw < 0 ")
					}
				}
				written += int64(nw)
				if ew != nil {
					err = ew
					break
				}
				if nr != nw {
					err = errors.New("Writer Unknown Error: nr != nw ")
					break
				}
			}
			if er != nil {
				if er != io.EOF {
					err = er
				}
				break
			}
			// 为了避免刷太多更新记录,每5s发送一次进度信息
			nowTime := time.Now().Unix()
			if s.lastMessageTime <= 0 || (nowTime-s.lastMessageTime >= 5) {
				// 更新DD状态
				ddProgress, ddMaxSize, ddWriteSpeed, ddWriteSize, ddReadSize := s.getStatisticsInfo(s.wrapResp.getContentLength(), written, s.wrapResp.getUsedLength())
				var procExitBtn bool
				if ddProgress >= 100 {
					fullyDDMsg = true
					procExitBtn = s.getProcExitBtnStatus("success")
					ddWriteSpeed = "0.00 MB/S"
				} else {
					procExitBtn = false
				}
				if err := s.reportMsg("progress", map[string]interface{}{
					"inst_failed":         false,
					"inst_failed_message": "",
					"progress":            ddProgress,
					"proc_exit_btn":       procExitBtn,
					"dd_img_max_size":     ddMaxSize,
					"dd_img_write_speed":  ddWriteSpeed,
					"dd_img_block_size":   fmt.Sprintf("%.2f MB", float64(s.bufSize/1024/1024)),
					"dd_img_write_size":   ddWriteSize,
					"dd_img_read_size":    ddReadSize,
				}); err != nil {
					log.Print("Message Report Failed: ", err.Error())
				}
				s.lastMessageTime = nowTime
			}
		}
		if s.graceStopStatus() {
			break
		}
		if err != nil {
			log.Println(s.DDUrl, " io.Copy[", i, "] Failed: ", err.Error())
			continue
		} else {
			if !fullyDDMsg {
				// 更新DD状态
				_, ddMaxSize, _, ddWriteSize, ddReadSize := s.getStatisticsInfo(s.wrapResp.getContentLength(), written, s.wrapResp.getUsedLength())
				if err := s.reportMsg("progress", map[string]interface{}{
					"inst_failed":         false,
					"inst_failed_message": "",
					"progress":            100,
					"proc_exit_btn":       s.getProcExitBtnStatus("success"),
					"dd_img_max_size":     ddMaxSize,
					"dd_img_write_speed":  "0.00 MB/S",
					"dd_img_block_size":   fmt.Sprintf("%.2f MB", float64(s.bufSize/1024/1024)),
					"dd_img_write_size":   ddWriteSize,
					"dd_img_read_size":    ddReadSize,
				}); err != nil {
					log.Print("Message Report Failed: ", err.Error())
				}
			}
			break
		}
	}

	if err != nil {
		if err := s.reportMsg("progress", map[string]interface{}{
			"inst_failed":         true,
			"inst_failed_message": "Http Request/io.Copy Failed: " + err.Error(),
			"progress":            0,
			"proc_exit_btn":       s.getProcExitBtnStatus("error"),
			"dd_img_max_size":     "0.00 MB",
			"dd_img_write_speed":  "0.00 MB/S",
			"dd_img_block_size":   "0.00 MB",
			"dd_img_write_size":   "0.00 MB",
			"dd_img_read_size":    "0.00 MB",
		}); err != nil {
			log.Print("Message Report Failed: ", err.Error())
		}
		s.progressDone = true
		if s.ErrorExit {
			s.cancel()
		}
	} else {
		s.progressDone = true
		if s.SuccessExit {
			s.cancel()
		}
	}
}

// Stop 停止DD服务
func (s *Service) Stop(waitProgressDone bool) {
	s.graceStopLock.Lock()
	s.graceStop = true
	s.graceStopLock.Unlock()
	if waitProgressDone {
		for {
			if s.progressDone {
				break
			}
		}
	}
	close(s.DDMsgChan)
}

// getStatisticsInfo 获取统计信息
func (s *Service) getStatisticsInfo(ddImgContentLength int64, writeLen int64, respUsedLength int64) (progress int, maxSize string, writeSpeed string, writeSize string, readSize string) {
	nowTime := time.Now().Unix()
	// 先计算progress、maxSize、writeSize、readSize
	if ddImgContentLength <= 0 || respUsedLength < 0 {
		progress = 50
		maxSize = "未知"
		writeSize = "未知"
		readSize = "未知"
	} else {
		progress = int(math.Floor(((float64(respUsedLength) / float64(ddImgContentLength)) * 100) + 0/5))
		if progress > 100 || ddImgContentLength == respUsedLength {
			progress = 100
		}
		maxSize = fmt.Sprint(ddImgContentLength/1024/1024) + " MB"
		writeSize = fmt.Sprint(writeLen/1024/1024) + " MB"
		readSize = fmt.Sprint(respUsedLength/1024/1024) + " MB"
	}
	// 再计算writeSpeed
	if s.lastStatisticTime == 0 || s.lastStatisticWriteLen == 0 {
		// 俩有一个为0就说明是第一次计算
		writeSpeed = fmt.Sprint(writeLen/1024/1024) + " MB/s"
	} else {
		intervalTime := nowTime - s.lastStatisticTime
		intervalWriteLen := writeLen - s.lastStatisticWriteLen
		if intervalTime <= 0 || intervalWriteLen <= 0 {
			// 统计出错
			writeSpeed = "0 MB/s"
		} else {
			writeSpeed = fmt.Sprintf("%.2f", float64(intervalWriteLen/intervalTime)/1024/1024) + " MB/s"
		}
	}
	s.lastStatisticWriteLen = writeLen
	s.lastStatisticTime = nowTime
	return
}

// graceStopStatus 检查是否需要平滑关闭
func (s *Service) graceStopStatus() (status bool) {
	s.graceStopLock.RLock()
	if s.graceStop {
		status = true
	} else {
		status = false
	}
	s.graceStopLock.RUnlock()
	return
}

// getProcExitBtnStatus 检查是否需要显示进程退出按钮
func (s *Service) getProcExitBtnStatus(resultType string) bool {
	if resultType == "error" {
		if !s.ErrorExit {
			return true
		} else {
			return false
		}
	} else {
		if !s.SuccessExit {
			return true
		} else {
			return false
		}
	}
}

// reportMsg 发送信息
func (s *Service) reportMsg(msgType string, msgSourceData map[string]interface{}) error {
	if s.DDMsgChan == nil {
		return errors.New("[reportProgress: DDMsgChan nil ]")
	}
	msgSourceData["type"] = msgType
	jsonData, err := json.Marshal(msgSourceData)
	if err != nil {
		return errors.New("[reportProgress: Json Encode Failed -> " + err.Error() + " ]")
	}
	msgData := make(map[string]string)
	msgData["type"] = msgType
	msgData["data"] = string(jsonData)
	// 在某些时候可能会阻塞,但是没有办法,我们不能丢下任何一个信息
	s.DDMsgChan <- msgData
	return nil
}
