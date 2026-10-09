/*
   Copyright 2016 kanreisa
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tuner

import (
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/aribts"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// packetSize は TS パケット長。
const packetSize = 188

// dsmccBlockSize は DSM-CC のブロック長 (ARIB TR-B15)。
const dsmccBlockSize = 4066

// providePIDsDefault はストリームが常に利用者へ通す PID (Node 版 PROVIDE_PIDS)。
var providePIDsDefault = []int{
	0x0000, // PAT
	0x0001, // CAT
	0x0010, // NIT
	0x0011, // SDT
	0x0012, // EIT
	0x0013, // RST
	0x0014, // TDT
	0x0023, // SDTT
	0x0024, // BIT
	0x0028, // SDTT
	0x0029, // CDT
}

var logoDataNameBS = []byte("LOGO-05")
var logoDataNameCS = []byte("CS_LOGO-05")

// TSFilterOptions は TSFilter の生成オプション (Node 版 TSFilterOptions)。
type TSFilterOptions struct {
	Output    io.Writer
	NetworkID *int
	ServiceID *int
	EventID   *int
	ParseNIT  bool
	ParseSDT  bool
	ParseEIT  bool
	TsmfRelTs int
}

// networkInfo は NIT から取得するネットワーク情報 (Node 版 "network" イベント)。
type networkInfo struct {
	NetworkID          int
	AreaCode           int
	RemoteControlKeyID int
}

// sdtServiceInfo は SDT から取得するサービス情報 (Node 版 "services" イベント)。
type sdtServiceInfo struct {
	NetworkID int
	ServiceID int
	Name      string
	Type      int
	LogoID    int
}

// flagState は EPG 回収進捗の 1 テーブル分のビットフラグ (Node 版 FlagState)。
type flagState struct {
	flag          [32]byte
	ignore        [32]byte
	versionNumber int
}

// flagStateSet は basic / extended いずれかのテーブル群の進捗。
type flagStateSet struct {
	flags       [8]flagState
	lastFlagsID int
}

// basicExtState は 1 サービス分の EPG 回収進捗 (Node 版 BasicExtState)。
type basicExtState struct {
	basic    flagStateSet
	extended flagStateSet
}

// downloadData は DSM-CC DDB のモジュールダウンロード状態 (Node 版 DownloadData)。
type downloadData struct {
	downloadID    int
	moduleID      int
	moduleVersion int
	moduleSize    int
	loadedBytes   int
	data          []byte
}

// TSFilter は TS パケットの中継・PSI/SI 解析・EPG 組み立てを行う (Node 版 TSFilter.ts の移植)。
type TSFilter struct {
	mu     sync.Mutex
	app    *core.App
	output io.Writer

	closed bool
	// ready はサービス/イベント特定前は false。false の間は pid 0x12 などのバッファリングのみ行う。
	ready bool
	// providePids は利用者へ通す PID。nil は全 PID を提供する (provideAll)。
	providePids      map[int]bool
	provideAll       bool
	parsePids        map[int]bool
	parsingPids      map[int]bool
	enableParseCDT   bool
	enableParseDSMCC bool

	tsid            int
	serviceIds      map[int]bool
	parseServiceIds map[int]bool
	pmtPid          int
	streamTime      *int64
	essMap          map[int]int
	essEsPids       map[int]bool
	dlDataMap       map[int]*downloadData

	epg      *EPG
	epgReady bool
	epgState map[int]map[int]*basicExtState

	targetNetworkID  *int
	provideServiceID *int
	provideEventID   *int
	parseNIT         bool
	parseSDT         bool
	parseEIT         bool

	// tsmf
	tsmfEnable               bool
	tsmfSlotCounter          int
	tsmfRelativeStreamNumber []int
	tsmfTsNumber             int

	asm         *aribts.SectionAssembler
	partial     [packetSize]byte
	partialLen  int
	patsec      [183]byte
	patCRC      [4]byte
	buffer      [][]byte
	maxBufferPk int

	eventEndTimeout            time.Duration
	provideEventLastDetectedAt int64
	provideEventTimeout        *time.Timer
	pmtTimer                   *time.Timer
	logoDataTimer              *time.Timer

	streamInfo map[int]*apid.StreamInfoItem

	onClose      []func()
	onEPGReady   []func()
	onNetwork    []func(networkInfo)
	onServices   []func([]sdtServiceInfo)
	lastNetwork  *networkInfo
	lastServices []sdtServiceInfo
}

// NewTSFilter は TSFilter を生成する。
func NewTSFilter(app *core.App, opts TSFilterOptions) *TSFilter {
	tf := &TSFilter{
		app:             app,
		output:          opts.Output,
		providePids:     map[int]bool{},
		parsePids:       map[int]bool{},
		parsingPids:     map[int]bool{},
		serviceIds:      map[int]bool{},
		parseServiceIds: map[int]bool{},
		essMap:          map[int]int{},
		essEsPids:       map[int]bool{},
		dlDataMap:       map[int]*downloadData{},
		streamInfo:      map[int]*apid.StreamInfoItem{},
		pmtPid:          -1,
		tsid:            -1,
		asm:             aribts.NewSectionAssembler(),
		// Node 版と同じく ready の既定値は true (provideServiceID/EventID 指定時のみ false にする)。
		ready: true,
	}
	tf.epgState = map[int]map[int]*basicExtState{}

	// maxBufferBytesBeforeReady / eventEndTimeout (Node 版の既定値と同じ)
	maxBytes := 8 * 1024 * 1024
	if tf.app != nil && tf.app.Config != nil && tf.app.Config.Server != nil && tf.app.Config.Server.MaxBufferBytesBeforeReady != nil {
		maxBytes = *tf.app.Config.Server.MaxBufferBytesBeforeReady
	}
	maxBytes = maxBytes - maxBytes%packetSize
	if maxBytes < packetSize {
		maxBytes = packetSize
	}
	tf.maxBufferPk = maxBytes / packetSize

	eventEnd := int64(1000)
	if tf.app != nil && tf.app.Config != nil && tf.app.Config.Server != nil && tf.app.Config.Server.EventEndTimeout != nil {
		eventEnd = int64(*tf.app.Config.Server.EventEndTimeout)
	}
	tf.eventEndTimeout = time.Duration(eventEnd) * time.Millisecond

	if opts.TsmfRelTs != 0 {
		tf.tsmfEnable = true
		tf.tsmfTsNumber = opts.TsmfRelTs
	}

	tf.targetNetworkID = opts.NetworkID
	tf.provideServiceID = opts.ServiceID
	tf.provideEventID = opts.EventID
	tf.parseNIT = opts.ParseNIT
	tf.parseSDT = opts.ParseSDT
	tf.parseEIT = opts.ParseEIT

	if tf.provideServiceID != nil {
		for _, pid := range providePIDsDefault {
			tf.providePids[pid] = true
		}
		tf.ready = false
	}
	if tf.provideEventID != nil {
		tf.ready = false
		if tf.app != nil && tf.app.Program != nil {
			program := tf.app.Program.Get(core.GetProgramItemID(*tf.targetNetworkID, *tf.provideServiceID, *tf.provideEventID))
			if program != nil {
				timeout := program.StartAt + program.Duration - tf.app.Clock.Now().UnixMilli()
				if program.Duration == 1 {
					timeout += 1000 * 60 * 3
				}
				if timeout < 0 {
					timeout = 1000 * 60 * 3
				}
				tf.provideEventTimeout = time.AfterFunc(time.Duration(timeout)*time.Millisecond, tf.observeProvideEvent)
			}
		}
	}
	if opts.Output != nil {
		tf.output = opts.Output
	} else {
		tf.provideServiceID = nil
		tf.provideEventID = nil
		tf.providePids = map[int]bool{}
		tf.provideAll = false
		tf.ready = false
	}
	if opts.Output != nil && tf.provideServiceID == nil {
		// providePids がまだ空のまま = 全 PID を提供する (Node では null)
		tf.provideAll = true
	}

	if tf.targetNetworkID != nil {
		if *tf.targetNetworkID == 4 { // ARIB TR-B15 (BS/CS)
			tf.enableParseDSMCC = true
		} else {
			tf.enableParseCDT = true
		}
	}

	if tf.app != nil && tf.app.Status != nil {
		tf.app.Status.AddTSFilter(1)
	}
	if tf.app != nil && tf.app.Log != nil {
		tf.app.Log.Debug("TSFilter: created (serviceId=%s, eventId=%s)", intPtrString(tf.provideServiceID), intPtrString(tf.provideEventID))
	}
	return tf
}

// Writer は中継先の io.Writer を返す。
func (tf *TSFilter) Writer() io.Writer { return tf.output }

// Closed は閉じられているか返す。
func (tf *TSFilter) Closed() bool {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return tf.closed
}

// OnClose はクローズ時のコールバックを登録する。
func (tf *TSFilter) OnClose(fn func()) {
	tf.mu.Lock()
	closed := tf.closed
	if !closed {
		tf.onClose = append(tf.onClose, fn)
	}
	tf.mu.Unlock()
	if closed {
		fn()
	}
}

// OnEPGReady は EPG 回収完了時のコールバックを登録する。
func (tf *TSFilter) OnEPGReady(fn func()) {
	tf.mu.Lock()
	ready := tf.epgReady
	if !ready {
		tf.onEPGReady = append(tf.onEPGReady, fn)
	}
	tf.mu.Unlock()
	if ready {
		fn()
	}
}

// OnNetwork は NIT 検出時のコールバックを登録する。
func (tf *TSFilter) OnNetwork(fn func(networkInfo)) {
	tf.mu.Lock()
	last := tf.lastNetwork
	if last == nil {
		tf.onNetwork = append(tf.onNetwork, fn)
	}
	tf.mu.Unlock()
	if last != nil {
		fn(*last)
	}
}

// OnServices は SDT 検出時のコールバックを登録する。
func (tf *TSFilter) OnServices(fn func([]sdtServiceInfo)) {
	tf.mu.Lock()
	last := tf.lastServices
	if last == nil {
		tf.onServices = append(tf.onServices, fn)
	}
	tf.mu.Unlock()
	if last != nil {
		fn(last)
	}
}

// StreamInfo は PID ごとの統計を返す。
func (tf *TSFilter) StreamInfo() apid.StreamInfo {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	out := apid.StreamInfo{}
	for pid, si := range tf.streamInfo {
		out[intToString(pid)] = apid.StreamInfoItem{Packet: si.Packet, Drop: si.Drop}
	}
	return out
}

// Drops は continuity_counter の不連続により破棄した回数を返す (aribts の解析対象 PID の合計)。
func (tf *TSFilter) Drops() int64 {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	var total int64
	for pid := range tf.parsingPids {
		total += int64(tf.asm.Drops(uint16(pid)))
	}
	return total
}

// Write は TS データ (188 バイト単位でない場合もあり得る) を投入する。
func (tf *TSFilter) Write(chunk []byte) (int, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if tf.closed {
		return 0, errors.New("TSFilter has closed already")
	}

	offset := 0
	length := len(chunk)
	var packets [][]byte

	if tf.partialLen > 0 {
		need := packetSize - tf.partialLen
		if length >= need {
			p := make([]byte, packetSize)
			copy(p, tf.partial[:tf.partialLen])
			copy(p[tf.partialLen:], chunk[:need])
			packets = append(packets, p)
			offset = need
			tf.partialLen = 0
		} else {
			copy(tf.partial[tf.partialLen:], chunk)
			tf.partialLen += length
			return length, nil
		}
	}

	for ; offset < length; offset += packetSize {
		if chunk[offset] != 0x47 {
			offset -= packetSize - 1
			continue
		}
		if length-offset >= packetSize {
			packets = append(packets, chunk[offset:offset+packetSize])
		} else {
			copy(tf.partial[:], chunk[offset:])
			tf.partialLen = length - offset
		}
	}

	tf.processPackets(packets)

	if len(tf.buffer) != 0 {
		if tf.ready && tf.output != nil {
			if _, err := tf.output.Write(flatten(tf.buffer)); err != nil {
				return length, err
			}
			tf.buffer = tf.buffer[:0]
		} else {
			head := len(tf.buffer) - tf.maxBufferPk
			if head > 0 {
				if tf.app != nil && tf.app.Status != nil {
					for i := 0; i < head; i++ {
						tf.app.Status.AddError(core.ErrorBufferOverflow)
					}
				}
				tf.buffer = tf.buffer[head:]
			}
		}
	}
	return length, nil
}

// processPackets は 1 バッチ分のパケットを解析・中継する (Node 版 _processPackets)。
func (tf *TSFilter) processPackets(packets [][]byte) {
	var parsingBuffers [][]byte

	for _, packet := range packets {
		pid := int(packet[1]&0x1F)<<8 | int(packet[2])

		if tf.tsmfEnable {
			if pid == 0x002F {
				tsmfFlameSync := int(packet[4]&0x1F)<<8 | int(packet[5])
				if tsmfFlameSync != 0x1A86 && tsmfFlameSync != 0x0579 {
					continue
				}
				tf.tsmfRelativeStreamNumber = make([]int, 0, 52)
				for i := 0; i < 26; i++ {
					tf.tsmfRelativeStreamNumber = append(tf.tsmfRelativeStreamNumber, int(packet[73+i]&0xf0>>4))
					tf.tsmfRelativeStreamNumber = append(tf.tsmfRelativeStreamNumber, int(packet[73+i]&0x0f))
				}
				tf.tsmfSlotCounter = 0
				continue
			}
			if tf.tsmfSlotCounter < 0 || tf.tsmfSlotCounter > 51 {
				continue
			}
			tf.tsmfSlotCounter++
			if tf.tsmfRelativeStreamNumber[tf.tsmfSlotCounter-1] != tf.tsmfTsNumber {
				continue
			}
		}

		if pid == 0x1FFF {
			continue
		}
		if packet[1]&0x80 != 0 { // transport_error_indicator
			if si := tf.streamInfo[pid]; si != nil {
				si.Drop++
			}
			continue
		}

		if pid == 0 {
			targetStart := int(packet[7]) + 4
			if targetStart+4 > 188 {
				if si := tf.streamInfo[pid]; si != nil {
					si.Drop++
				}
				continue
			}
			if !bytes4Equal(packet[targetStart:targetStart+4], tf.patCRC[:]) {
				copy(tf.patCRC[:], packet[targetStart:targetStart+4])
				parsingBuffers = append(parsingBuffers, packet)
			}
		} else if (pid == 0x12 && (tf.parseEIT || tf.provideEventID != nil)) || pid == 0x14 || tf.parsePids[pid] {
			parsingBuffers = append(parsingBuffers, packet)
		}

		if !tf.ready && (pid == 0x12 || tf.provideEventID == nil) {
			continue
		}
		if !tf.provideAll && !tf.providePids[pid] {
			continue
		}

		if pid == 0 && tf.pmtPid != -1 {
			np := make([]byte, packetSize)
			copy(np, packet)
			copy(np[5:], tf.patsec[:])
			packet = np
		}

		if tf.streamInfo[pid] == nil {
			tf.streamInfo[pid] = &apid.StreamInfoItem{}
		}
		tf.streamInfo[pid].Packet++
		tf.buffer = append(tf.buffer, packet)
	}

	if len(parsingBuffers) != 0 {
		tf.feedParsing(parsingBuffers)
	}
}

// feedParsing は解析用パケットを SectionAssembler へ通し、完成したセクションを振り分ける。
func (tf *TSFilter) feedParsing(packets [][]byte) {
	if tf.closed {
		return
	}
	for _, packet := range packets {
		pid, sections := tf.asm.Push(packet)
		tf.parsingPids[int(pid)] = true
		for _, sec := range sections {
			tf.dispatch(int(pid), sec)
		}
	}
}

// dispatch は 1 セクションを table_id に応じて各ハンドラへ振り分ける。
func (tf *TSFilter) dispatch(pid int, sec []byte) {
	if len(sec) < 1 {
		return
	}
	tableID := int(sec[0])
	switch tableID {
	case 0x00:
		if pat, err := aribts.DecodePAT(sec); err == nil {
			tf.onPAT(pat)
		}
	case 0x02:
		if pid == tf.pmtPid {
			if pmt, err := aribts.DecodePMT(sec); err == nil {
				tf.onPMT(pid, pmt)
			}
		}
	case 0x40, 0x41:
		if nit, err := aribts.DecodeNIT(sec); err == nil {
			tf.onNIT(pid, nit)
		}
	case 0x42, 0x46:
		if sdt, err := aribts.DecodeSDT(sec); err == nil {
			tf.onSDT(pid, sdt)
		}
	case 0x73:
		if tot, err := aribts.DecodeTOT(sec); err == nil {
			tf.onTOT(tot)
		}
	case 0xC8:
		if cdt, err := aribts.DecodeCDT(sec); err == nil {
			tf.onCDT(cdt)
		}
	}
	if tableID >= 0x4E && tableID <= 0x6F {
		if eit, err := aribts.DecodeEIT(sec); err == nil {
			tf.onEIT(eit)
		}
	}
	if tableID >= 0x3A && tableID <= 0x3F && tf.essEsPids[pid] {
		if dsmcc, err := aribts.DecodeDSMCC(sec); err == nil {
			tf.onDSMCC(dsmcc)
		}
	}
}

func (tf *TSFilter) onPAT(pat *aribts.PAT) {
	tf.tsid = pat.TransportStreamID
	tf.serviceIds = map[int]bool{}
	tf.parseServiceIds = map[int]bool{}

	for _, program := range pat.Programs {
		serviceID := program.ProgramNumber

		if serviceID == 0 {
			nitPid := program.NetworkPID
			tf.debug("TSFilter#_onPAT: detected NIT PID=%d", nitPid)
			if tf.parseNIT {
				tf.parsePids[nitPid] = true
			}
			continue
		}

		if tf.targetNetworkID != nil && *tf.targetNetworkID == 4 && serviceID == 929 {
			essPmtPid := program.ProgramMapPID
			tf.essMap[serviceID] = essPmtPid
			tf.debug("TSFilter#_onPAT: detected ESS PMT PID=%d as serviceId=%d", essPmtPid, serviceID)
			continue
		}

		tf.serviceIds[serviceID] = true

		var item *core.ServiceItem
		if tf.targetNetworkID != nil && tf.app != nil && tf.app.Service != nil {
			item = tf.app.Service.GetByNetworkID(*tf.targetNetworkID, serviceID)
		}
		tf.debug("TSFilter#_onPAT: detected PMT PID=%d as serviceId=%d", program.ProgramMapPID, serviceID)

		if tf.provideServiceID != nil && serviceID == *tf.provideServiceID {
			if tf.pmtPid != program.ProgramMapPID {
				tf.pmtPid = program.ProgramMapPID
				tf.providePids[tf.pmtPid] = true
				tf.parsePids[tf.pmtPid] = true
				tf.editPATSection(pat.Raw, serviceID)
			}
		}

		if tf.parseEIT && item != nil && tf.targetNetworkID != nil && tf.app != nil {
			for _, service := range tf.app.Service.FindByNetworkID(*tf.targetNetworkID) {
				if !tf.parseServiceIds[service.ServiceID()] {
					tf.parseServiceIds[service.ServiceID()] = true
					tf.debug("TSFilter#_onPAT: parsing serviceId=%d (%s)", service.ServiceID(), service.Name())
				}
			}
		}
	}

	if tf.parseSDT {
		tf.parsePids[0x11] = true
	}
}

// editPATSection はサービス単独配信のための PAT セクションを組み立てる (Node 版 _onPAT の PAT 書き換え)。
func (tf *TSFilter) editPATSection(raw []byte, serviceID int) {
	copy(tf.patsec[0:8], raw[0:8])
	tf.patsec[2] = 17 // section_length = 0x11
	tf.patsec[8] = 0
	tf.patsec[9] = 0
	tf.patsec[10] = 224
	tf.patsec[11] = 16
	tf.patsec[12] = byte(serviceID >> 8)
	tf.patsec[13] = byte(serviceID & 255)
	tf.patsec[14] = byte((tf.pmtPid >> 8) + 224)
	tf.patsec[15] = byte(tf.pmtPid & 255)
	crc := aribts.CRC32(tf.patsec[0:16])
	tf.patsec[16] = byte(crc >> 24)
	tf.patsec[17] = byte(crc >> 16)
	tf.patsec[18] = byte(crc >> 8)
	tf.patsec[19] = byte(crc)
	for i := 20; i < len(tf.patsec); i++ {
		tf.patsec[i] = 0xff
	}
}

func (tf *TSFilter) onPMT(pid int, pmt *aribts.PMT) {
	if _, ok := tf.essMap[pmt.ProgramNumber]; ok {
		for _, stream := range pmt.Streams {
			for _, d := range stream.ESInfo {
				if d.Tag == 0x52 { // stream identifier descriptor
					if v, ok := d.Value.(*aribts.StreamIdentifier); ok &&
						(v.ComponentTag == 0x79 || v.ComponentTag == 0x7A) {
						tf.parsePids[stream.ElementaryPID] = true
						tf.essEsPids[stream.ElementaryPID] = true
						tf.debug("TSFilter#_onPMT: detected ESS ES PID=%d", stream.ElementaryPID)
						break
					}
				}
			}
		}
		delete(tf.parsePids, pid)
		return
	}

	if !tf.ready && tf.provideServiceID != nil && tf.provideEventID == nil {
		tf.ready = true
		tf.info("TSFilter#_onPMT: now ready for serviceId=%d", *tf.provideServiceID)
	}

	if len(pmt.ProgramInfo) > 0 {
		if ca, ok := pmt.ProgramInfo[0].Value.(*aribts.ConditionalAccess); ok {
			tf.providePids[ca.CAPID] = true
		}
	}
	tf.providePids[pmt.PCRPID] = true
	for _, stream := range pmt.Streams {
		tf.providePids[stream.ElementaryPID] = true
	}

	if tf.parsePids[pid] {
		delete(tf.parsePids, pid)
		tf.pmtTimer = time.AfterFunc(time.Second, func() {
			tf.mu.Lock()
			tf.parsePids[pid] = true
			tf.mu.Unlock()
		})
	}
}

func (tf *TSFilter) onNIT(pid int, nit *aribts.NIT) {
	network := networkInfo{NetworkID: nit.NetworkID, AreaCode: -1, RemoteControlKeyID: -1}
	if len(nit.TransportStreams) > 0 {
		for _, d := range nit.TransportStreams[0].Descriptors {
			switch d.Tag {
			case 0xFA:
				if v, ok := d.Value.(*aribts.TerrestrialDeliverySystem); ok {
					network.AreaCode = v.AreaCode
				}
			case 0xCD:
				if v, ok := d.Value.(*aribts.TSInformation); ok {
					network.RemoteControlKeyID = v.RemoteControlKeyID
				}
			}
		}
	}
	tf.emitNetwork(network)
	delete(tf.parsePids, pid)
}

func (tf *TSFilter) onSDT(pid int, sdt *aribts.SDT) {
	if tf.tsid != sdt.TransportStreamID {
		return
	}
	var services []sdtServiceInfo
	for _, service := range sdt.Services {
		if !tf.serviceIds[service.ServiceID] {
			continue
		}
		name := ""
		typ := -1
		logoID := -1
		for _, d := range service.Descriptors {
			if d.Tag == 0x48 {
				if v, ok := d.Value.(*aribts.ServiceDescriptor); ok {
					name = aribts.DecodeString(v.ServiceNameChar)
					typ = v.ServiceType
				}
			}
			if d.Tag == 0xCF {
				if v, ok := d.Value.(*aribts.LogoTransmission); ok && v.HasLogoID {
					logoID = v.LogoID
				}
			}
			if name != "" && logoID != -1 {
				break
			}
		}
		if !containsSDTServiceID(services, service.ServiceID) {
			services = append(services, sdtServiceInfo{
				NetworkID: sdt.OriginalNetworkID,
				ServiceID: service.ServiceID,
				Name:      name,
				Type:      typ,
				LogoID:    logoID,
			})
		}
	}
	tf.emitServices(services)
	delete(tf.parsePids, pid)
}

func (tf *TSFilter) onTOT(tot *aribts.TOT) {
	t := aribts.MJDToUnixMs(tot.JSTTime[:])
	tf.streamTime = &t
}

func (tf *TSFilter) onCDT(cdt *aribts.CDT) {
	if cdt.DataType != 0x01 {
		return
	}
	logoType, logoID, data := aribts.DecodeCDTLogoModule(cdt.DataModuleByte)
	if logoType != 0x05 {
		return
	}
	tf.debug("TSFilter#_onCDT: received logo data (networkId=%d, logoId=%d)", cdt.OriginalNetworkID, logoID)
	if tf.app == nil {
		return
	}
	png := aribts.LogoToPNG(data)
	if err := core.SaveLogoData(tf.app.Config.LogoDataDirPath, cdt.OriginalNetworkID, logoID, png); err != nil {
		tf.warn("TSFilter#_onCDT: failed to save logo data: %s", err)
	}
}

func (tf *TSFilter) onDSMCC(dsmcc *aribts.DSMCC) {
	switch dsmcc.TableID {
	case 0x3C: // DDB
		ddb, ok := dsmcc.Message.(*aribts.DSMCCDownloadDataBlock)
		if !ok {
			return
		}
		dl := tf.dlDataMap[ddb.DownloadID]
		if dl == nil || dl.moduleID != ddb.ModuleID || dl.data == nil {
			return
		}
		if dl.moduleVersion != ddb.ModuleVersion {
			delete(tf.dlDataMap, ddb.DownloadID)
			return
		}
		copy(dl.data[dsmccBlockSize*ddb.BlockNumber:], ddb.BlockDataByte)
		dl.loadedBytes += len(ddb.BlockDataByte)
		tf.debug("TSFilter#_onDSMCC: detected DDB and logo data downloading... (downloadId=%d, %d/%d bytes)",
			ddb.DownloadID, dl.loadedBytes, dl.moduleSize)
		if dl.loadedBytes != dl.moduleSize {
			return
		}
		dlData := dl.data
		dl.data = nil

		for _, logo := range aribts.DecodeLogoModule(dlData) {
			for _, logoService := range logo.Services {
				service := tf.app.Service.GetByNetworkID(logoService.OriginalNetworkID, logoService.ServiceID)
				if service == nil {
					tf.debug("TSFilter#_onDSMCC: service not found (networkId=%d, serviceId=%d)", logoService.OriginalNetworkID, logoService.ServiceID)
					continue
				}
				service.SetLogoID(apid.Ptr(logo.LogoID))
				tf.debug("TSFilter#_onDSMCC: received logo data (networkId=%d, logoId=%d)", service.NetworkID(), logo.LogoID)
				png := aribts.LogoToPNG(logo.Data)
				if err := core.SaveLogoData(tf.app.Config.LogoDataDirPath, service.NetworkID(), logo.LogoID, png); err != nil {
					tf.warn("TSFilter#_onDSMCC: failed to save logo data: %s", err)
				}
				break
			}
		}

	case 0x3B: // DII
		dii, ok := dsmcc.Message.(*aribts.DSMCCDownloadInfoIndication)
		if !ok {
			return
		}
		if _, exists := tf.dlDataMap[dii.DownloadID]; exists {
			return
		}
		for _, module := range dii.Modules {
			for _, descriptor := range module.ModuleInfo {
				if descriptor.Tag != 0x02 {
					continue
				}
				if !bytesEqual(descriptor.TextChar, logoDataNameBS) && !bytesEqual(descriptor.TextChar, logoDataNameCS) {
					continue
				}
				tf.dlDataMap[dii.DownloadID] = &downloadData{
					downloadID:    dii.DownloadID,
					moduleID:      module.ModuleID,
					moduleVersion: module.ModuleVersion,
					moduleSize:    module.ModuleSize,
					loadedBytes:   0,
					data:          make([]byte, module.ModuleSize),
				}
				tf.debug("TSFilter#_onDSMCC: detected DII and buffer allocated for logo data (downloadId=%d, %d bytes)", dii.DownloadID, module.ModuleSize)
				break
			}
		}
	}
}

func (tf *TSFilter) onEIT(eit *aribts.EIT) {
	if tf.pmtPid != -1 && len(eit.Events) != 0 && tf.provideEventID != nil &&
		eit.TableID == 0x4E && eit.SectionNumber == 0 && tf.provideServiceID != nil && *tf.provideServiceID == eit.ServiceID {
		if eit.Events[0].EventID == *tf.provideEventID {
			tf.provideEventLastDetectedAt = tf.app.Clock.Now().UnixMilli()
			if !tf.ready {
				tf.ready = true
				tf.info("TSFilter#_onEIT: now ready for eventId=%d", *tf.provideEventID)
			}
		} else if tf.ready {
			tf.info("TSFilter#_onEIT: closing because eventId=%d has ended...", *tf.provideEventID)
			eventID := *tf.provideEventID
			tf.provideEventID = nil
			time.AfterFunc(tf.eventEndTimeout, func() {
				tf.mu.Lock()
				tf.ready = false
				tf.provideEventID = &eventID
				tf.mu.Unlock()
				tf.Close()
			})
		}
	}

	if tf.parseEIT && tf.parseServiceIds[eit.ServiceID] {
		if tf.epg == nil && !tf.isEPGGathering() {
			if tf.app != nil && tf.app.Status != nil {
				tf.app.Status.SetEPGGathering(*tf.targetNetworkID, true)
			}
			tf.epg = newEPG(tf.app)
			tf.standbyLogoData()
		}
		if tf.epg != nil {
			tf.epg.Write(eit)
			if !tf.epgReady && eit.TableID != 0x4E && eit.TableID != 0x4F {
				tf.updateEpgState(eit)
			}
		}
	}
}

func (tf *TSFilter) observeProvideEvent() {
	if tf.app != nil && tf.app.Clock.Now().UnixMilli()-tf.provideEventLastDetectedAt < 10000 {
		tf.provideEventTimeout = time.AfterFunc(3*time.Second, tf.observeProvideEvent)
		return
	}
	if tf.provideEventID != nil {
		tf.warn("TSFilter#_observeProvideEvent: closing because EIT p/f timed out for eventId=%d...", *tf.provideEventID)
	}
	tf.Close()
}

// standbyLogoData はロゴデータのカルーセル待ち受けを開始する (Node 版 _standbyLogoData)。
func (tf *TSFilter) standbyLogoData() {
	if tf.closed {
		return
	}
	if tf.logoDataTimer != nil {
		return
	}
	if tf.enableParseDSMCC && len(tf.essMap) == 0 {
		return
	}

	var targetServices []*core.ServiceItem
	switch {
	case tf.provideServiceID == nil:
		targetServices = tf.app.Service.FindByNetworkID(*tf.targetNetworkID)
	case tf.enableParseCDT:
		if s := tf.app.Service.GetByNetworkID(*tf.targetNetworkID, *tf.provideServiceID); s != nil {
			targetServices = append(targetServices, s)
		}
	case tf.enableParseDSMCC && *tf.targetNetworkID == 4:
		targetServices = append(targetServices, tf.app.Service.FindByNetworkID(4)...)
		targetServices = append(targetServices, tf.app.Service.FindByNetworkID(6)...)
		targetServices = append(targetServices, tf.app.Service.FindByNetworkID(7)...)
	}

	logoIDNetworkMap := map[int]map[int]bool{}
	for _, service := range targetServices {
		if id := service.LogoID(); id != nil {
			if logoIDNetworkMap[service.NetworkID()] == nil {
				logoIDNetworkMap[service.NetworkID()] = map[int]bool{}
			}
			logoIDNetworkMap[service.NetworkID()][*id] = true
		}
	}

	now := tf.app.Clock.Now().UnixMilli()
	interval := int64(1000 * 60 * 60 * 24 * 7)
	if tf.app.Config.Server != nil && tf.app.Config.Server.LogoDataInterval != nil {
		interval = *tf.app.Config.Server.LogoDataInterval
	}

	for networkID := range logoIDNetworkMap {
		for logoID := range logoIDNetworkMap[networkID] {
			if logoID == -1 && len(logoIDNetworkMap[networkID]) > 1 {
				continue
			}
			if now-core.GetLogoDataMTime(tf.app.Config.LogoDataDirPath, *tf.targetNetworkID, logoID) > interval {
				if tf.closed {
					return
				}
				if tf.enableParseCDT {
					if logoID >= 0 {
						tf.parsePids[0x29] = true
					}
					tf.logoDataTimer = time.AfterFunc(30*time.Minute, func() {
						tf.mu.Lock()
						delete(tf.parsePids, 0x29)
						tf.mu.Unlock()
						tf.info("TSFilter#_standbyLogoData: stopped waiting for logo data (networkId=%d, logoId=%d)", *tf.targetNetworkID, logoID)
					})
					tf.info("TSFilter#_standbyLogoData: waiting for logo data for 30 minutes... (networkId=%d, logoId=%d)", *tf.targetNetworkID, logoID)
				} else if tf.enableParseDSMCC {
					for _, essPmtPid := range tf.essMap {
						tf.parsePids[essPmtPid] = true
					}
					tf.logoDataTimer = time.AfterFunc(30*time.Minute, func() {
						tf.mu.Lock()
						for essEsPid := range tf.essEsPids {
							delete(tf.parsePids, essEsPid)
						}
						tf.mu.Unlock()
						tf.info("TSFilter#_standbyLogoData: stopped waiting for logo data (networkId=[4,6,7])")
					})
					tf.info("TSFilter#_standbyLogoData: waiting for logo data for 30 minutes... (networkId=[4,6,7])")
				}
				return
			}
		}
	}
}

// updateEpgState は EIT の回収進捗を更新し、全テーブルが揃えば epgReady を発火する (Node 版 _updateEpgState)。
func (tf *TSFilter) updateEpgState(eit *aribts.EIT) {
	networkID := eit.OriginalNetworkID
	serviceID := eit.ServiceID
	versionNumber := eit.VersionNumber

	if tf.epgState[networkID] == nil {
		tf.epgState[networkID] = map[int]*basicExtState{}
	}
	if tf.epgState[networkID][serviceID] == nil {
		state := &basicExtState{basic: flagStateSet{lastFlagsID: -1}, extended: flagStateSet{lastFlagsID: -1}}
		for i := 0; i < 0x08; i++ {
			state.basic.flags[i].versionNumber = -1
			state.extended.flags[i].versionNumber = -1
			for j := range state.basic.flags[i].ignore {
				state.basic.flags[i].ignore[j] = 0xFF
				state.extended.flags[i].ignore[j] = 0xFF
			}
		}
		tf.epgState[networkID][serviceID] = state
	}
	state := tf.epgState[networkID][serviceID]

	flagsID := eit.TableID & 0x07
	lastFlagsID := eit.LastTableID & 0x07
	segmentNumber := eit.SectionNumber >> 3
	lastSegmentNumber := eit.LastSectionNumber >> 3
	sectionNumber := eit.SectionNumber & 0x07
	segmentLastSectionNumber := eit.SegmentLastSectionNumber & 0x07

	var targetFlags *flagStateSet
	if eit.TableID&0x0F < 0x08 {
		targetFlags = &state.basic
	} else {
		targetFlags = &state.extended
	}
	targetFlag := &targetFlags.flags[flagsID]

	if targetFlags.lastFlagsID != lastFlagsID ||
		(targetFlag.versionNumber != -1 && targetFlag.versionNumber != versionNumber) {
		if targetFlag.versionNumber != -1 {
			verDiff := versionNumber - targetFlag.versionNumber
			if verDiff == -1 || verDiff > 1 {
				return
			}
		}
		for i := 0; i < 0x08; i++ {
			for j := range targetFlags.flags[i].flag {
				targetFlags.flags[i].flag[j] = 0x00
			}
			ig := byte(0xFF)
			if i <= lastFlagsID {
				ig = 0x00
			}
			for j := range targetFlags.flags[i].ignore {
				targetFlags.flags[i].ignore[j] = ig
			}
		}
	}

	if flagsID == 0 && tf.streamTime != nil {
		segment := (*tf.streamTime + 9*60*60*1000) / (3 * 60 * 60 * 1000) & 0x07
		for i := int64(0); i < segment; i++ {
			targetFlag.ignore[i] = 0xFF
		}
	}
	for i := lastSegmentNumber + 1; i < 0x20; i++ {
		targetFlag.ignore[i] = 0xFF
	}
	for i := segmentLastSectionNumber + 1; i < 8; i++ {
		targetFlag.ignore[segmentNumber] |= 1 << uint(i)
	}
	targetFlag.flag[segmentNumber] |= 1 << uint(sectionNumber)

	targetFlags.lastFlagsID = lastFlagsID
	targetFlag.versionNumber = versionNumber

	ready := true
	for nid := range tf.epgState {
		for sid := range tf.epgState[nid] {
			s := tf.epgState[nid][sid]
			if !flagSetReady(&s.basic) || !flagSetReady(&s.extended) {
				ready = false
				break
			}
		}
		if !ready {
			break
		}
	}

	if ready {
		tf.epgReady = true
		tf.epgState = map[int]map[int]*basicExtState{}
		if tf.targetNetworkID != nil && tf.app != nil && tf.app.Service != nil {
			for _, service := range tf.app.Service.FindByNetworkID(*tf.targetNetworkID) {
				service.SetEPGReady(true)
			}
		}
		for _, fn := range tf.onEPGReady {
			fn()
		}
		tf.onEPGReady = nil
	}
}

func flagSetReady(fs *flagStateSet) bool {
	for i := range fs.flags {
		for j := range fs.flags[i].flag {
			if fs.flags[i].flag[j]|fs.flags[i].ignore[j] != 0xFF {
				return false
			}
		}
	}
	return true
}

func (tf *TSFilter) isEPGGathering() bool {
	if tf.targetNetworkID == nil || tf.app == nil || tf.app.Status == nil {
		return false
	}
	for _, nid := range tf.app.Status.GatheringNetworks() {
		if nid == *tf.targetNetworkID {
			return true
		}
	}
	return false
}

func (tf *TSFilter) emitNetwork(n networkInfo) {
	tf.lastNetwork = &n
	for _, fn := range tf.onNetwork {
		fn(n)
	}
	tf.onNetwork = nil
}

func (tf *TSFilter) emitServices(s []sdtServiceInfo) {
	tf.lastServices = s
	for _, fn := range tf.onServices {
		fn(s)
	}
	tf.onServices = nil
}

// Close は TSFilter を閉じる (Node 版 _close)。
func (tf *TSFilter) Close() error {
	tf.mu.Lock()
	if tf.closed {
		tf.mu.Unlock()
		return nil
	}
	tf.closed = true

	if tf.pmtTimer != nil {
		tf.pmtTimer.Stop()
	}
	if tf.provideEventTimeout != nil {
		tf.provideEventTimeout.Stop()
	}
	if tf.logoDataTimer != nil {
		tf.logoDataTimer.Stop()
	}

	if tf.epg != nil {
		if tf.targetNetworkID != nil && tf.app != nil && tf.app.Status != nil {
			tf.app.Status.SetEPGGathering(*tf.targetNetworkID, false)
		}
		if tf.epgReady && tf.targetNetworkID != nil && tf.app != nil && tf.app.Service != nil {
			now := tf.app.Clock.Now().UnixMilli()
			for _, service := range tf.app.Service.FindByNetworkID(*tf.targetNetworkID) {
				service.SetEPGUpdatedAt(now)
			}
		}
		tf.epg = nil
		tf.epgState = map[int]map[int]*basicExtState{}
	}

	tf.buffer = nil
	tf.streamInfo = map[int]*apid.StreamInfoItem{}
	if tf.app != nil && tf.app.Status != nil {
		tf.app.Status.AddTSFilter(-1)
	}

	output := tf.output
	tf.output = nil
	cb := tf.onClose
	tf.onClose = nil

	if tf.app != nil && tf.app.Log != nil {
		tf.app.Log.Debug("TSFilter#_close: closed (serviceId=%s, eventId=%s)", intPtrString(tf.provideServiceID), intPtrString(tf.provideEventID))
	}
	tf.mu.Unlock()

	if output != nil {
		if c, ok := output.(io.Closer); ok {
			_ = c.Close()
		}
	}
	for _, fn := range cb {
		fn()
	}
	return nil
}

// End は Close の別名。
func (tf *TSFilter) End() { _ = tf.Close() }

func (tf *TSFilter) info(format string, args ...any) {
	if tf.app != nil && tf.app.Log != nil {
		tf.app.Log.Info(format, args...)
	}
}
func (tf *TSFilter) debug(format string, args ...any) {
	if tf.app != nil && tf.app.Log != nil {
		tf.app.Log.Debug(format, args...)
	}
}
func (tf *TSFilter) warn(format string, args ...any) {
	if tf.app != nil && tf.app.Log != nil {
		tf.app.Log.Warn(format, args...)
	}
}

// ---- 小さなヘルパ ----

func flatten(packets [][]byte) []byte {
	n := 0
	for _, p := range packets {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range packets {
		out = append(out, p...)
	}
	return out
}

func bytes4Equal(a, b []byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSDTServiceID(items []sdtServiceInfo, serviceID int) bool {
	for _, it := range items {
		if it.ServiceID == serviceID {
			return true
		}
	}
	return false
}

func intToString(v int) string {
	return strconv.Itoa(v)
}

func intPtrString(p *int) string {
	if p == nil {
		return "null"
	}
	return strconv.Itoa(*p)
}
