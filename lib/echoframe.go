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
	HeaderEchonetLite                       = 0x1081   // 0x10=ECHONET Lite, 0x81=電文形式1
	Controller                 ClassCode    = 0x05ff01 // コントローラ
	SmartElectricMeter         ClassCode    = 0x028801 // 低圧スマート電力量メータ
	Get                        ServiceCode  = 0x62
	GetRes                     ServiceCode  = 0x72
	InstantaneousElectricPower PropertyCode = 0xe7 // 瞬時電力計測値
	InstantaneousCurrents      PropertyCode = 0xe8 // 瞬時電流計測値
)

type echoFrame struct {
	TID  int32          // トランザクションID
	SEOJ ClassCode      // 送信元ECHONET Liteオブジェクト
	DEOJ ClassCode      // 相手先ECHONET Liteオブジェクト
	ESV  ServiceCode    // ECHONET Liteサービス
	EPC  []PropertyCode // ECHONETプロパティ
	EDT  [][]byte       // プロパティ値データ
}

func NewEchoFrame(dstClassCode ClassCode, esv ServiceCode, epc PropertyCode, edt []byte) *echoFrame {
	fr := new(echoFrame)
	fr.TID = -1
	fr.SEOJ = Controller
	fr.DEOJ = dstClassCode
	fr.ESV = esv
	fr.EPC = make([]PropertyCode, 1)
	fr.EDT = make([][]byte, 1)
	fr.EPC[0] = epc
	fr.EDT[0] = edt
	return fr
}

func ParseEchoFrame(raw []byte) (*echoFrame, error) {
	fr := new(echoFrame)
	if len(raw) < 14 {
		return nil, errors.New("Too short ECHONET Lite frame")
	}
	if binary.BigEndian.Uint16(raw[0:2]) != HeaderEchonetLite {
		return nil, fmt.Errorf("Unknown ECHONET Lite Header: %02X%02X", raw[0], raw[1])
	}
	// トランザクションID
	fr.TID = int32(binary.BigEndian.Uint16(raw[2:4]))
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
	}

	return fr, nil
}

func (self *echoFrame) Build() []byte {
	buf := new(bytes.Buffer)
	if self.TID < 0 || self.TID >= 0x10000 {
		self.TID = rand.Int31n(0x10000)
	}
	binary.Write(buf, binary.BigEndian, uint16(HeaderEchonetLite))
	// トランザクションID
	binary.Write(buf, binary.BigEndian, uint16(self.TID))
	// 送信元ECHONET Liteオブジェクト
	binary.Write(buf, binary.BigEndian, uint8(self.SEOJ>>16&0xff))
	binary.Write(buf, binary.BigEndian, uint16(self.SEOJ&0xffff))
	// 相手先ECHONET Liteオブジェクト
	binary.Write(buf, binary.BigEndian, uint8(self.DEOJ>>16&0xff))
	binary.Write(buf, binary.BigEndian, uint16(self.DEOJ&0xffff))
	// ECHONET Liteサービス
	binary.Write(buf, binary.BigEndian, self.ESV)
	// 処理対象プロパティカウンタ (OPC)
	nProperty := len(self.EPC)
	binary.Write(buf, binary.BigEndian, uint8(nProperty))
	for i := 0; i < nProperty; i++ {
		// ECHONETプロパティ
		binary.Write(buf, binary.BigEndian, self.EPC[i])
		// プロパティデータカウンタ
		binary.Write(buf, binary.BigEndian, uint8(len(self.EDT[i])))
		// プロパティ値データ
		buf.Write(self.EDT[i])
	}
	return buf.Bytes()
}

/* selfとfrとがリクエスト/レスポンスとして対応しているかを確認する */
func (self *echoFrame) CorrespondTo(fr *echoFrame) bool {
	if self.TID != fr.TID {
		return false
	}
	if self.SEOJ != fr.DEOJ {
		return false
	}
	if self.DEOJ != fr.SEOJ {
		return false
	}
	delta := int(self.ESV) - int(fr.ESV)
	if delta != -0x10 && delta != 0x10 {
		return false
	}
	if len(self.EPC) >= 1 && len(fr.EPC) >= 1 && self.EPC[0] != fr.EPC[0] {
		return false
	}
	return true
}
