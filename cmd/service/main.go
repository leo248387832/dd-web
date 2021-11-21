package main

import (
	"context"
	"flag"
	"github.com/flyqie/dd-web/pkg/dd"
	"github.com/flyqie/dd-web/pkg/http"
	"log"
	"os"
)

// 启动参数
var (
	// DD包地址
	ddUrl string
	// DD到的路径
	ddOf string
	// 遇到错误是否直接退出
	errorExit bool
	// 成功后是否直接退出
	successExit bool
	// DD出错的时候重试几次,0表示不重试
	retryNum int
	// Http监听地址
	httpAddr string
	// Auth Key
	authKey string
)

func InitFlag() {
	flag.IntVar(&retryNum, "retrynum", 0, "DD Retry Number")
	flag.StringVar(&ddUrl, "ddurl", "", "DD Package Url(eg. https://www.example.com/something.gz)")
	flag.StringVar(&ddOf, "ddof", "", "DD Of (eg. /dev/vda)")
	flag.BoolVar(&errorExit, "errorexit", false, "Exit when error")
	flag.BoolVar(&successExit, "successexit", false, "Exit when success")
	flag.StringVar(&httpAddr, "addr", "http://0.0.0.0:2333", "Http Server Addr")
	flag.StringVar(&authKey, "authkey", "flyqie", "Protect your DD information")
}

func main() {
	InitFlag()
	flag.Parse()
	if ddUrl == "" || ddOf == "" {
		flag.Usage()
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ddMsgChan := make(chan map[string]string, 2333)

	log.Println("Start DD Service...")
	ddService := dd.CreateService(ddUrl, ddOf, ctx, cancel, ddMsgChan, errorExit, successExit, retryNum)
	ddService.Start()

	log.Println("Start Http Service...")
	httpService := http.CreateService(httpAddr, ctx, cancel, ddMsgChan, authKey)
	httpService.Start()

	select {
	case <-ctx.Done():
		log.Println("Main Service Exiting...")
		log.Println("Stop DD Service...")
		ddService.Stop(true)
		log.Println("Stop Http Service...")
		httpService.Stop(true)
		break
	}
	log.Print("Main Service Exited")
	os.Exit(0)
}
