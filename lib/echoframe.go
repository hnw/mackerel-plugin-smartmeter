package mpsm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
)

type ClassCode uint32
type ServiceCode byte
type PropertyCode byte

const (
	HeaderEchonetLite                             = 0x1081   // 0x10=ECHONET Lite, 0x81=電文形式1
	Controller                       ClassCode    = 0x05ff01 // コントローラ
	SmartElectricMeter               ClassCode    = 0x028801 // 低圧スマート電力量メータ
	Get                              ServiceCode  = 0x62
	GetRes                           ServiceCode  = 0x72
	UnitOfCumulativeElectricEnergy   PropertyCode = 0xe1 // 積算電力量単位、作者の自宅では0.1KWh
	PositiveCumulativeElectricEnergy PropertyCode = 0xe0 // 積算電力量（正方向）
	NegativeCumulativeElectricEnergy PropertyCode = 0xe3 // 積算電力量（逆方向）
	InstantaneousElectricPower       PropertyCode = 0xe7 // 瞬時電力計測値
	InstantaneousCurrent             PropertyCode = 0xe8 // 瞬時電流計測値
)

type EchoFrame struct {
	TID  uint16         // トランザクションID
	SEOJ ClassCode      // 送信元ECHONET Liteオブジェクト
	DEOJ ClassCode      // 相手先ECHONET Liteオブジェクト
	ESV  ServiceCode    // ECHONET Liteサービス
	EPC  []PropertyCode // ECHONETプロパティ
	EDT  [][]byte       // プロパティ値データ
}

// NewEchoFrame は echoFrame構造体のコンストラクタ関数
func NewEchoFrame(dstClassCode ClassCode, esv ServiceCode, epc []PropertyCode, edt [][]byte) *EchoFrame {
	fr := new(EchoFrame)
	fr.RegenerateTID()
	fr.SEOJ = Controller
	fr.DEOJ = dstClassCode
	fr.ESV = esv
	opc := len(epc)
	if edt != nil && opc > len(edt) {
		opc = len(edt)
	}
	fr.EPC = make([]PropertyCode, opc)
	fr.EDT = make([][]byte, opc)
	for i := 0; i < opc; i++ {
		fr.EPC[i] = epc[i]
		if edt == nil {
			fr.EDT[i] = nil
		} else {
			fr.EDT[i] = edt[i]
		}
	}
	return fr
}

// ParseEchoFrame は ECHONET Liteフレームのバイト列を受け取り、EchoFrame構造体として返す
func ParseEchoFrame(raw []byte) (*EchoFrame, error) {
	fr := new(EchoFrame)
	if len(raw) < 14 {
		return nil, errors.New("Too short ECHONET Lite frame")
	}
	if binary.BigEndian.Uint16(raw[0:2]) != HeaderEchonetLite {
		return nil, fmt.Errorf("Unknown ECHONET Lite Header: %02X%02X", raw[0], raw[1])
	}
	// トランザクションID
	fr.TID = binary.BigEndian.Uint16(raw[2:4])
	// 送信元ECHONET Liteオブジェクト
	v32 := binary.BigEndian.Uint32(raw[3:7])
	v32 &= 0x00ffffff
	fr.SEOJ = ClassCode(v32)
	// 相手先ECHONET Liteオブジェクト
	v32 = binary.BigEndian.Uint32(raw[6:10])
	v32 &= 0x00ffffff
	fr.DEOJ = ClassCode(v32)
	// ECHONET Liteサービス
	fr.ESV = ServiceCode(raw[10])
	// 処理対象プロパティカウンタ (OPC)
	nProperty := int(raw[11])

	fr.EPC = make([]PropertyCode, nProperty)
	fr.EDT = make([][]byte, nProperty)
	i := 12
	for j := 0; j < nProperty; j++ {
		if len(raw) < i+2 {
			return nil, errors.New("Too short ECHONET Lite frame")
		}
		// ECHONETプロパティ
		fr.EPC[j] = PropertyCode(raw[i])
		// プロパティデータカウンタ (PDC)
		lenEDT := int(raw[i+1])
		if len(raw) < i+2+lenEDT {
			return nil, errors.New("Too short ECHONET Lite frame")
		}
		// プロパティ値データ
		fr.EDT[j] = raw[i+2 : i+2+lenEDT]
		i = i + 2 + lenEDT
	}

	return fr, nil
}

func (f *EchoFrame) Build() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(HeaderEchonetLite))
	// トランザクションID
	binary.Write(buf, binary.BigEndian, f.TID)
	// 送信元ECHONET Liteオブジェクト
	binary.Write(buf, binary.BigEndian, uint8(f.SEOJ>>16&0xff))
	binary.Write(buf, binary.BigEndian, uint16(f.SEOJ&0xffff))
	// 相手先ECHONET Liteオブジェクト
	binary.Write(buf, binary.BigEndian, uint8(f.DEOJ>>16&0xff))
	binary.Write(buf, binary.BigEndian, uint16(f.DEOJ&0xffff))
	// ECHONET Liteサービス
	binary.Write(buf, binary.BigEndian, f.ESV)
	// 処理対象プロパティカウンタ (OPC)
	nProperty := len(f.EPC)
	binary.Write(buf, binary.BigEndian, uint8(nProperty))
	for i := 0; i < nProperty; i++ {
		// ECHONETプロパティ
		binary.Write(buf, binary.BigEndian, f.EPC[i])
		// プロパティデータカウンタ
		binary.Write(buf, binary.BigEndian, uint8(len(f.EDT[i])))
		// プロパティ値データ
		buf.Write(f.EDT[i])
	}
	return buf.Bytes()
}

// CorrespondTo は fとtargetとがリクエスト/レスポンスとして対応しているか確認する
func (f *EchoFrame) CorrespondTo(target *EchoFrame) bool {
	if f.TID != target.TID {
		return false
	}
	if f.SEOJ != target.DEOJ {
		return false
	}
	if f.DEOJ != target.SEOJ {
		return false
	}
	delta := int(f.ESV) - int(target.ESV)
	if delta != -0x10 && delta != 0x10 {
		return false
	}
	if len(f.EPC) == 0 {
		return false
	}
	if len(f.EPC) != len(target.EPC) {
		return false
	}
	opc := len(f.EPC)
	for i := 0; i < opc; i++ {
		if f.EPC[i] != target.EPC[i] {
			return false
		}
	}
	return true
}

// RegenerateTID: TIDを再生成する
func (f *EchoFrame) RegenerateTID() {
	f.TID = uint16(rand.Int31n(0x10000))
}
