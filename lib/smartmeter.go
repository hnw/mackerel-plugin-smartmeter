package mpsm

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/syslog"
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
		log.Printf("ECHONET request error: %v\n", err)
	} else {
		return echoFrameToMetric(res)
	}

	// 値が取れなかったらPANA認証を行う
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
		Debug:          *optDebug,
	}
	plugin := mp.NewMackerelPlugin(p)
	plugin.Tempfile = *optTempfile
	plugin.Run()
}

func (p SmartmeterPlugin) sendSkCommand(input chan string, w *bufio.Writer, cmd string) (string, error) {
	_, err := w.WriteString(cmd + "\r\n")
	if err != nil {
		log.Fatal(err)
	}
	w.Flush()
	if p.Debug {
		if strings.HasPrefix(cmd, "SKSENDTO ") {
			a := strings.Split(cmd, " ")
			a[len(a)-1] = hex.EncodeToString([]byte(a[len(a)-1]))
			fmt.Println(strings.Join(a, " "))
		} else {
			fmt.Println(cmd)
		}
	}

	res := ""
	timeout := 10 * time.Second
	tm := time.NewTimer(timeout)
FOR:
	for {
		select {
		case <-tm.C:
			log.Println("Read timeout (sendSk)")
			break FOR
		case line, ok := <-input:
			if !ok {
				break FOR
			}
			if strings.HasPrefix(line, "FAIL ") {
				log.Fatal(line)
			}
			if p.Debug {
				fmt.Println(line)
			}
			if line == "OK" {
				break FOR
			}
			res += line
			if strings.HasPrefix(cmd, "SKLL64 ") {
				// SKLL64コマンドだけはOKを返さない
				break FOR
			}
			tm.Reset(timeout)
		}
	}
	return res, nil
}

// PANAで認証を行う
func (p SmartmeterPlugin) execPANA(input chan string, w *bufio.Writer) error {
	tmTotal := time.NewTimer(20 * time.Second)
	timeout := 10 * time.Second
	tm := time.NewTimer(timeout)

	p.sendSkCommand(input, w, "SKSETPWD C "+p.RoutebPassword)
	p.sendSkCommand(input, w, "SKSETRBID "+p.RoutebID)
	p.sendSkCommand(input, w, "SKSREG S2 "+p.Channel)
	p.sendSkCommand(input, w, "SKSREG S3 "+p.PanID)

	log.Println("startPANA")
	for {
		p.sendSkCommand(input, w, "SKJOIN "+p.IPAddr)
		for {
			select {
			case <-tmTotal.C:
				return errors.New("PANA connection timeout (20sec)")
			case <-tm.C:
				return errors.New("PANA connection timeout (10sec)")
			case line, ok := <-input:
				if !ok {
					return errors.New("PANA Read error")
				}
				if p.Debug {
					fmt.Println(line)
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
	for {
		req.TID = -1
		rawFrame := req.Build()
		cmd := fmt.Sprintf("SKSENDTO %d %s %04X %d 0 %04X %s", secure, p.IPAddr, port, secure, len(rawFrame), rawFrame)
		udpStatus, _ := p.sendSkCommand(input, w, cmd)
		if strings.HasSuffix(udpStatus, " 02") { // UDP送信成功
			return nil, errors.New("PANA unconnected?")
		} else if strings.HasSuffix(udpStatus, " 00") { // UDP送信成功
			log.Println("readRes")
			res, err := p.readCorrespondingEchonetFrame(input, req)
			if err != nil {
				log.Printf("Error occurred: %v\n", err)
			} else {
				return res, nil
			}
		}
		// UDP送信失敗？時間を空けて再送する
		time.Sleep(500 * time.Millisecond)
	}
}

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
			if strings.HasPrefix(line, "ERXUDP ") {
				res, err := ParseUdpResponse(line)
				if err == nil && req.CorrespondTo(res) {
					return res, nil
				}
			}
			tm.Reset(timeout)
		}
	}
	// should never reach here
	return nil, errors.New("Unknown error")
}

func ParseUdpResponse(line string) (*echoFrame, error) {
	/* PANAのブロードキャスト(?)を受け取ることもある
	 * 例：ERXUDP FE80:0000:0000:0000:021C:6400:03E0:**** FE80:0000:0000:0000:A612:42FF:FE9F:**** 02CC 02CC 001C640003E0**** 0 0 0058 00000058A00000020B6F1805EE840165000700000004000000000000000200000004000003A1000400040000000400000003B401000800000004000000015180000100000010000025718D51339F8699E122922BB6ABD93B
	 * 現状ではECHONET Lite ヘッダエラーとして無視している
	 */
	a := strings.Split(line, " ")
	if len(a) < 10 {
		return nil, fmt.Errorf("Unknown format: %s")
	}
	decoded, err := hex.DecodeString(a[9])
	if err != nil {
		return nil, err
	}
	fr, err := ParseEchoFrame(decoded)
	if err != nil {
		return nil, err
	}
	return fr, nil
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
