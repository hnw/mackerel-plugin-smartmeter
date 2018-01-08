package mpsm

// TODO:
//   SKコマンドの入出力に対応する構造体を切り出したい
//   各種タイムアウト時間が決め打ちになってるのを直したい
//   致命的エラーとリカバリ可能エラーの区別をしたい

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"strconv"
	"strings"
	"time"

	mp "github.com/mackerelio/go-mackerel-plugin"
	"github.com/tarm/serial"
)

// SmartmeterPlugin mackerel plugin
type SmartmeterPlugin struct {
	Prefix         string
	RoutebID       string
	RoutebPassword string
	SerialPort     string
	Channel        string
	PanID          string
	IPAddr         string
	DualStackSK    bool
	Debug          bool
}

// MetricKeyPrefix interface for PluginWithPrefix
func (p SmartmeterPlugin) MetricKeyPrefix() string {
	if p.Prefix == "" {
		p.Prefix = "smartmeter"
	}
	return p.Prefix
}

// GraphDefinition interface for mackerelplugin
func (p SmartmeterPlugin) GraphDefinition() map[string]mp.Graphs {
	return map[string]mp.Graphs{
		"power": {
			Label: "Electric power consumption [W]",
			Unit:  "integer",
			Metrics: []mp.Metrics{
				{Name: "value", Label: "Electric power"},
			},
		},
		"current": {
			Label: "Electric current [A]",
			Unit:  "integer",
			Metrics: []mp.Metrics{
				{Name: "r", Label: "R-phase current", Stacked: true},
				{Name: "t", Label: "T-phase current", Stacked: true},
			},
		},
	}
}

// FetchMetrics interface for mackerelplugin
func (p SmartmeterPlugin) FetchMetrics() (map[string]float64, error) {

	c := &serial.Config{
		Name:     p.SerialPort,
		Baud:     115200,
		Size:     8,
		StopBits: 1,
	}
	s, err := serial.OpenPort(c)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(s)
	writer := bufio.NewWriter(s)
	ch := make(chan string, 4)

	go func() {
		defer close(ch)
		defer s.Close()

		for scanner.Scan() {
			line := scanner.Text()
			ch <- line
		}
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	}()

	// いきなり電力値取得を試みる
	req := NewEchoFrame(SmartElectricMeter, Get, []PropertyCode{InstantaneousElectricPower, InstantaneousCurrent}, nil)
	res, err := p.execEchoRequest(ch, writer, req)
	if err != nil {
		// ここでのエラーはリカバリ可能とみなして処理継続
		// TODO: FAIL ER06 が出た場合はエラーを返したい
		log.Printf("ECHONET request error: %v\n", err)
	} else {
		return echoFrameToMetric(res)
	}

	// 初回で値が取れなかったらPANA認証を行う
	err = p.execPANA(ch, writer)
	if err != nil {
		return nil, err
	}
	// 改めて電力値取得
	for {
		retry := 0
		res, err := p.execEchoRequest(ch, writer, req)
		if err != nil {
			log.Printf("ECHONET request error: %v\n", err)
		} else {
			return echoFrameToMetric(res)
		}
		if retry >= 3 {
			return nil, errors.New("ECHONET request error")
		}
		retry++
	}
}

// Do the plugin
func Do() {
	var (
		optPrefix         = flag.String("metric-key-prefix", "smartmeter", "Metric key prefix")
		optTempfile       = flag.String("tempfile", "", "Temp file name")
		optRoutebID       = flag.String("id", "", "Route B ID")
		optRoutebPassword = flag.String("password", "", "Route B password")
		optSerialPort     = flag.String("device", "", "Path to serial port")
		optChannel        = flag.String("channel", "", "channel")
		optPanID          = flag.String("panid", "", "PAN Id")
		optIPAddr         = flag.String("ipaddr", "", "IP address")
		optDualStackSK    = flag.Bool("dse", false, "Enable Dual Stack Edition (DSE) SK command")
		optDebug          = flag.Bool("debug", false, "debug mode")
		//optScan           = flag.Bool("scan", false, "scan mode")
	)

	flag.Parse()

	logger, err := syslog.New(syslog.LOG_NOTICE|syslog.LOG_USER, "mpsm")
	if err != nil {
		panic(err)
	}
	log.SetOutput(logger)

	p := SmartmeterPlugin{
		Prefix:         *optPrefix,
		RoutebID:       *optRoutebID,
		RoutebPassword: *optRoutebPassword,
		SerialPort:     *optSerialPort,
		Channel:        *optChannel,
		PanID:          *optPanID,
		IPAddr:         *optIPAddr,
		DualStackSK:    *optDualStackSK,
		Debug:          *optDebug,
	}
	plugin := mp.NewMackerelPlugin(p)
	plugin.Tempfile = *optTempfile
	plugin.Run()
}

func (p SmartmeterPlugin) sendSKCommand(input chan string, w *bufio.Writer, cmd string) (string, error) {
	if p.Debug {
		if strings.HasPrefix(cmd, "SKSENDTO ") {
			a := strings.Split(cmd, " ")
			a[len(a)-1] = hex.EncodeToString([]byte(a[len(a)-1]))
			fmt.Println(strings.Join(a, " "))
		} else {
			fmt.Println(cmd)
		}
	}

	_, err := w.WriteString(cmd + "\r\n")
	if err != nil {
		return "", err
	}
	err = w.Flush()
	if err != nil {
		return "", err
	}

	res := ""
	timeout := 10 * time.Second
	tm := time.NewTimer(timeout)

	for {
		select {
		case <-tm.C:
			return "", errors.New("SK command timeout (10sec)")
		case line, ok := <-input:
			if !ok {
				return "", errors.New("SK command read error")
			}
			if strings.HasPrefix(line, "FAIL ") {
				return "", errors.New("SK command response error: " + line)
			}
			if p.Debug {
				fmt.Println(line)
			}
			if line == "OK" {
				return res, nil
			}
			res += line
			if strings.HasPrefix(cmd, "SKLL64 ") {
				// SKLL64コマンドだけはOKを返さない
				return res, nil
			}
			tm.Reset(timeout)
		}
	}
}

// PANAで認証を行う
func (p SmartmeterPlugin) execPANA(input chan string, w *bufio.Writer) error {
	tmTotal := time.NewTimer(20 * time.Second)
	timeout := 10 * time.Second
	tm := time.NewTimer(timeout)

	_, err := p.sendSKCommand(input, w, "SKSETPWD C "+p.RoutebPassword)
	if err != nil {
		return err
	}
	_, err = p.sendSKCommand(input, w, "SKSETRBID "+p.RoutebID)
	if err != nil {
		return err
	}
	_, err = p.sendSKCommand(input, w, "SKSREG S2 "+p.Channel)
	if err != nil {
		return err
	}
	_, err = p.sendSKCommand(input, w, "SKSREG S3 "+p.PanID)
	if err != nil {
		return err
	}

	log.Println("startPANA")
	for {
		_, err = p.sendSKCommand(input, w, "SKJOIN "+p.IPAddr)
		if err != nil {
			return err
		}
		for {
			select {
			case <-tmTotal.C:
				return errors.New("PANA connection timeout (20sec)")
			case <-tm.C:
				return errors.New("PANA connection timeout (10sec)")
			case line, ok := <-input:
				if !ok {
					return errors.New("PANA read error")
				}
				if p.Debug {
					fmt.Printf("%s", line)
				}
				if strings.HasPrefix(line, "EVENT 24 ") {
					log.Println("PANA connection error. retrying...")
				}
				if strings.HasPrefix(line, "EVENT 25 ") {
					log.Println("endPANA")
					return nil
				}
				tm.Reset(timeout)
			}
		}
	}
}

// ECHONET Liteリクエストを出し、対応するECHONET Liteフレームを取得する
func (p SmartmeterPlugin) execEchoRequest(input chan string, w *bufio.Writer, req *echoFrame) (*echoFrame, error) {
	secure := 1
	port := 3610
	side := 0 // 0: B-route, 1: HAN
	retryInterval := 500 * time.Millisecond
	for {
		rawFrame := req.Build()
		var cmd string
		if p.DualStackSK {
			cmd = fmt.Sprintf("SKSENDTO %d %s %04X %d %d %04X %s", secure, p.IPAddr, port, secure, side, len(rawFrame), rawFrame)
		} else {
			cmd = fmt.Sprintf("SKSENDTO %d %s %04X %d %04X %s", secure, p.IPAddr, port, secure, len(rawFrame), rawFrame)
		}
		udpStatus, err := p.sendSKCommand(input, w, cmd)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(udpStatus, " 02") { // UDP送信成功
			return nil, errors.New("PANA unconnected?")
		}
		if strings.HasSuffix(udpStatus, " 00") { // UDP送信成功
			log.Println("readRes")
			return p.readCorrespondingEchonetFrame(input, req)
		}
		// UDP送信失敗？時間を空けて再送する
		time.Sleep(retryInterval)
		retryInterval *= 2
		log.Println("Failed sending UDP. Retrying...")
		req.RegenerateTID()
	}
}

// reqに対応するERXUDPイベント行を受け取ってechoFrameとして返す
// 対応するイベント行を受け取ったか、タイムアウトするか、致命的エラーが発生するかしたら終了
func (p SmartmeterPlugin) readCorrespondingEchonetFrame(input chan string, req *echoFrame) (*echoFrame, error) {
	timeout := 3 * time.Second
	tm := time.NewTimer(timeout)
	for {
		select {
		case <-tm.C:
			return nil, errors.New("Read timeout")
		case line, ok := <-input:
			if !ok {
				return nil, errors.New("Read error")
			}
			if p.Debug {
				fmt.Println(line)
			}
			if !strings.HasPrefix(line, "ERXUDP ") {
				break
			}
			// dev版：
			//   9 or 10 トークン (skcommand,SENDER,DEST,RPORT,LPORT,SENDERLLA,(RSSI),SECURED,DATALEN,DATA)
			// dse版：
			//   10 or 11 トークン (skcommand,SENDER,DEST,RPORT,LPORT,SENDERLLA,(RSSI),SECURED,SIDE,DATALEN,DATA)
			nToken := 9
			if p.DualStackSK {
				nToken = 10
			}
			a := strings.SplitN(line, " ", nToken)
			if len(a) < nToken {
				return nil, errors.New("Unknown ERXUDP format: " + line)
			}
			i, err := strconv.ParseInt(a[nToken-2], 16, 32)
			if err != nil {
				return nil, errors.New("ERXUDP parse error (not a number) : " + line)
			}
			dataLen := int(i)
			data := a[nToken-1]
			if len(data) != dataLen && len(data) != 2*dataLen {
				// RSSIあり（SA2レジスタ=1）を想定して最後のトークンを更に2分割
				b := strings.SplitN(data, " ", 2)
				if len(b) < 2 {
					return nil, errors.New("ERXUDP data length mismatch: " + line)
				}
				j, err := strconv.ParseInt(b[0], 16, 32)
				if err != nil {
					return nil, errors.New("ERXUDP parse error (not a number) : " + line)
				}
				dataLen = int(j)
				data = b[1]
				if len(data) != dataLen && len(data) != 2*dataLen {
					return nil, errors.New("ERXUDP data length mismatch: " + line)
				}
			}
			var rawData []byte
			if int(dataLen) == len(data) {
				// WOPT 0（バイナリ）
				rawData = []byte(data)
			} else {
				// WOPT 1（16進ASCII）
				rawData, err = hex.DecodeString(data)
				if err != nil {
					return nil, errors.New("ERXUDP parse error (not a hexadecimal) : " + line)
				}
			}

			res, err := ParseEchoFrame(rawData)
			if err != nil {
				// PANAのパケットを受け取った場合は先頭2バイトが0x00であるためECHONET Liteヘッダエラーになる
				// リカバリ可能エラーとみなす（この後で正しいフレームが来る可能性がある）
				if p.Debug {
					log.Printf("ECHONET Lite frame parse error: %v\n", err)
				}
				break
			}
			if req.CorrespondTo(res) {
				return res, nil
			}
			tm.Reset(timeout)
		}
	}
}

func echoFrameToMetric(res *echoFrame) (map[string]float64, error) {
	metrics := make(map[string]float64)

	opc := len(res.EPC)
	if opc == 0 {
		return nil, errors.New("No property in response")
	}

	for i := 0; i < opc; i++ {
		if res.EPC[i] == InstantaneousElectricPower {
			metrics["value"] = float64(int32(binary.BigEndian.Uint32(res.EDT[i])))
		} else if res.EPC[i] == InstantaneousCurrent {
			metrics["r"] = float64(int16(binary.BigEndian.Uint16(res.EDT[i][:2]))) / 10.0
			metrics["t"] = float64(int16(binary.BigEndian.Uint16(res.EDT[i][2:]))) / 10.0
		}
	}

	return metrics, nil
}
