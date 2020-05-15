package mpsm

import (
	"encoding/binary"
	"flag"
	"io"
	"log"
	"log/syslog"
	"os"
	"path"
	"time"

	smartmeter "github.com/hnw/go-smartmeter"
	mp "github.com/mackerelio/go-mackerel-plugin"
)

// SmartmeterPlugin mackerel plugin
type SmartmeterPlugin struct {
	Prefix   string
	dev      *smartmeter.Device
	ScanMode bool
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
func (p SmartmeterPlugin) FetchMetrics() (metrics map[string]float64, err error) {

	// scan modeならスキャンして終了
	if p.ScanMode {
		err = p.dev.Scan(smartmeter.Timeout(120*time.Second), smartmeter.Verbosity(3))
		if err != nil {
			//log.Printf("scan error: %+v\n", err)
		}
		return nil, nil
	}

	if p.dev.IPAddr == "" {
		ipAddr, err := p.dev.GetNeibourIP()
		if err == nil {
			p.dev.IPAddr = ipAddr
		}
	}

	request := smartmeter.NewFrame(smartmeter.LvSmartElectricEnergyMeter, smartmeter.Get, []*smartmeter.Property{
		smartmeter.NewProperty(smartmeter.LvSmartElectricEnergyMeter_InstantaneousElectricPower, nil),
		smartmeter.NewProperty(smartmeter.LvSmartElectricEnergyMeter_InstantaneousCurrent, nil),
	})
	// いきなり電力値取得を試みる
	response, err := p.dev.QueryEchonetLite(request, smartmeter.Retry(3))
	if err != nil {
		// 値が取得できなかったので、認証してから再度値を取る
		err = p.dev.Authenticate()
		if err != nil {
			//log.Printf("Fatal error: %v", err)
			return
		}
		response, err = p.dev.QueryEchonetLite(request, smartmeter.Retry(3))
		if err != nil {
			//log.Printf("Fatal error: %v", err)
			return
		}
	}

	if len(response.Properties) == 0 {
		log.Print("Fatal error: No property in response")
		return
	}

	metrics = make(map[string]float64)
	for _, p := range response.Properties {
		switch p.EPC {
		case smartmeter.LvSmartElectricEnergyMeter_InstantaneousElectricPower:
			// 瞬時電力計測値
			metrics["value"] = float64(int32(binary.BigEndian.Uint32(p.EDT)))
		case smartmeter.LvSmartElectricEnergyMeter_InstantaneousCurrent:
			// 瞬時電流計測値
			metrics["r"] = float64(int16(binary.BigEndian.Uint16(p.EDT[:2]))) / 10.0
			metrics["t"] = float64(int16(binary.BigEndian.Uint16(p.EDT[2:]))) / 10.0
		}
	}
	return
}

// Do the plugin
func Do() {
	var (
		optPrefix         = flag.String("metric-key-prefix", "smartmeter", "Metric key prefix")
		optTempfile       = flag.String("tempfile", "", "Temp file name")
		optBRouteID       = flag.String("id", "", "B-route ID")
		optBRoutePassword = flag.String("password", "", "B-route password")
		optSerialPort     = flag.String("device", "", "Path to serial port")
		optChannel        = flag.String("channel", "", "channel")
		optIPAddr         = flag.String("ipaddr", "", "IP address")
		optDualStackSK    = flag.Bool("dse", false, "Enable Dual Stack Edition (DSE) SK command")
		optScanMode       = flag.Bool("scan", false, "scan mode")
		optVerbosity      = flag.Int("verbosity", 1, "Verbosity (0:quiet, 3:debug)")
	)

	flag.Parse()

	var writer io.Writer
	writer = os.Stdout
	if !*optScanMode {
		var err error
		tag := path.Base(os.Args[0])
		writer, err = syslog.New(syslog.LOG_NOTICE|syslog.LOG_USER, tag)
		if err != nil {
			panic(err)
		}
	}

	dev, err := smartmeter.Open(
		*optSerialPort,
		smartmeter.ID(*optBRouteID),
		smartmeter.Password(*optBRoutePassword),
		smartmeter.Channel(*optChannel),
		smartmeter.IPAddr(*optIPAddr),
		smartmeter.DualStackSK(*optDualStackSK),
		smartmeter.Verbosity(*optVerbosity),
		smartmeter.Logger(log.New(writer, "", 0)),
	)
	if err != nil {
		panic(err)
	}

	p := SmartmeterPlugin{
		Prefix:   *optPrefix,
		dev:      dev,
		ScanMode: *optScanMode,
	}
	plugin := mp.NewMackerelPlugin(p)
	plugin.Tempfile = *optTempfile
	plugin.Run()
}
