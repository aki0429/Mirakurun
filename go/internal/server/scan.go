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

package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// serviceTypes は Node 版 scan.ts の serviceTypes。
var serviceTypes = []int{0x01, 0x02, 0xA1, 0xA4, 0xA5, 0xAD, 0xC0}

// channelOrder は Node 版 scan.ts の channelOrder。
var channelOrder = map[apid.ChannelType]int{apid.ChannelTypeGR: 1, apid.ChannelTypeBS: 2, apid.ChannelTypeCS: 3, apid.ChannelTypeSKY: 4}

const (
	channelNameFormatGR    = "{ch}"
	channelNameFormatBS    = "{ch}"
	channelNameFormatBSSub = "BS{ch00}_{subch}"
	channelNameFormatCS    = "CS{ch}"
	defaultScanType        = apid.ChannelTypeGR
)

// channelScanOption は Node 版 ChannelScanOption。
type channelScanOption struct {
	Type              apid.ChannelType
	StartCh           *int
	EndCh             *int
	StartSubCh        *int
	EndSubCh          *int
	UseSubCh          *bool
	SetDisabledOnAdd  *bool
	DryRun            bool
	Refresh           bool
	ChannelNameFormat string
	ScanModeName      string
}

// scanConfig は Node 版 ScanConfig。
type scanConfig struct {
	Channels         []string
	ScanMode         string
	SetDisabledOnAdd bool
}

// channelScanStatus は Node 版 scanStatus (GET /api/config/channels/scan)。
type channelScanStatus struct {
	IsScanning     bool                      `json:"isScanning"`
	Status         string                    `json:"status"`
	Type           *apid.ChannelType         `json:"type,omitempty"`
	DryRun         *bool                     `json:"dryRun,omitempty"`
	Progress       *int                      `json:"progress,omitempty"`
	CurrentChannel *string                   `json:"currentChannel,omitempty"`
	ScanLog        []string                  `json:"scanLog,omitempty"`
	NewCount       *int                      `json:"newCount,omitempty"`
	TakeoverCount  *int                      `json:"takeoverCount,omitempty"`
	Result         []apid.ConfigChannelsItem `json:"result,omitempty"`
	StartTime      *int64                    `json:"startTime,omitempty"`
	UpdateTime     *int64                    `json:"updateTime,omitempty"`
}

// scanManager は Node 版 scan.ts のモジュールレベル状態を保持する。
type scanManager struct {
	mu              sync.Mutex
	status          channelScanStatus
	cancelRequested bool
}

func newScanManager(s *Server) *scanManager {
	return &scanManager{status: channelScanStatus{IsScanning: false, Status: "not_started"}}
}

// snapshot は現在のステータスのコピーを返す。
func (m *scanManager) snapshot() channelScanStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := m.status
	if m.status.ScanLog != nil {
		cp.ScanLog = append([]string(nil), m.status.ScanLog...)
	}
	if m.status.Result != nil {
		cp.Result = append([]apid.ConfigChannelsItem(nil), m.status.Result...)
	}
	return cp
}

// handleGetChannelScan は GET /api/config/channels/scan。
func (s *Server) handleGetChannelScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.responseJSON(w, s.scan.snapshot())
}

// handlePutChannelScan は PUT /api/config/channels/scan。
func (s *Server) handlePutChannelScan(w http.ResponseWriter, r *http.Request) {
	s.scan.mu.Lock()
	if s.scan.status.IsScanning {
		s.scan.mu.Unlock()
		s.responseError(w, http.StatusConflict, "Already Scanning")
		return
	}
	s.scan.status = channelScanStatus{IsScanning: true, Status: "not_started"}
	s.scan.cancelRequested = false
	s.scan.mu.Unlock()

	q := r.URL.Query()
	_, hasAsync := q["async"]
	_, hasDryRun := q["dryRun"]
	_, hasRefresh := q["refresh"]
	typ := apid.ChannelType(q.Get("type"))

	opt := channelScanOption{
		Type:              typ,
		StartCh:           optIntPtr(q.Get("minCh")),
		EndCh:             optIntPtr(q.Get("maxCh")),
		StartSubCh:        optIntPtr(q.Get("minSubCh")),
		EndSubCh:          optIntPtr(q.Get("maxSubCh")),
		UseSubCh:          optBoolPtr(q, "useSubCh"),
		SetDisabledOnAdd:  optBoolPtr(q, "setDisabledOnAdd"),
		ChannelNameFormat: q.Get("channelNameFormat"),
		ScanModeName:      q.Get("scanMode"),
		DryRun:            hasDryRun,
		Refresh:           hasRefresh,
	}
	skipCh := parseSkipCh(q.Get("skipCh"))

	cfg := generateScanConfig(opt)
	if cfg == nil {
		s.scan.mu.Lock()
		s.scan.status.IsScanning = false
		s.scan.mu.Unlock()
		s.responseError(w, http.StatusBadRequest, "Invalid scan configuration")
		return
	}

	if hasAsync {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","message":"Channel scan started in async mode"}`))
		go func() {
			if _, err := s.runChannelScan(context.Background(), cfg, opt.DryRun, typ, opt.Refresh, skipCh, nil); err != nil {
				s.app.Log.Error("Channel scan error: %s", err)
			}
		}()
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writer := func(text string) {
		_, _ = w.Write([]byte(text))
		if flusher != nil {
			flusher.Flush()
		}
	}
	if _, err := s.runChannelScan(r.Context(), cfg, opt.DryRun, typ, opt.Refresh, skipCh, writer); err != nil {
		s.app.Log.Error("Channel scan error: %s", err)
		_, _ = w.Write([]byte("Error during scan: " + err.Error() + "\n"))
	}
}

// handleDeleteChannelScan は DELETE /api/config/channels/scan。
func (s *Server) handleDeleteChannelScan(w http.ResponseWriter, r *http.Request) {
	s.scan.mu.Lock()
	if !s.scan.status.IsScanning {
		s.scan.mu.Unlock()
		s.responseError(w, http.StatusNotFound, "No scan in progress")
		return
	}
	if s.scan.cancelRequested {
		s.scan.mu.Unlock()
		s.responseError(w, http.StatusConflict, "Already Stopping")
		return
	}
	s.scan.cancelRequested = true
	s.scan.status.Status = "cancelled"
	s.scan.status.ScanLog = append(s.scan.status.ScanLog, "Scan cancellation requested by user.")
	s.scan.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(206)
	_, _ = w.Write([]byte(`{"status":"stopping","message":"Channel scan stop has been requested"}`))
}

// runChannelScan は Node 版 runChannelScan を移植する。
func (s *Server) runChannelScan(ctx context.Context, cfg *scanConfig, dryRun bool, typ apid.ChannelType, refresh bool, skipCh []int, outputWriter func(string)) ([]apid.ConfigChannelsItem, error) {
	var scanLog []string
	appendLog := func(text string) {
		if text != "" {
			scanLog = append(scanLog, strings.TrimSpace(text))
			if outputWriter != nil {
				outputWriter(text)
			}
		}
	}

	oldItems, err := s.loader.LoadChannels()
	if err != nil {
		return nil, err
	}
	if oldItems == nil {
		oldItems = []apid.ConfigChannelsItem{}
	}
	result := make([]apid.ConfigChannelsItem, 0, len(oldItems))
	for _, item := range oldItems {
		if item.Type != typ {
			result = append(result, item)
		}
	}

	now := s.now().UnixMilli()
	s.scan.mu.Lock()
	s.scan.status = channelScanStatus{
		IsScanning:     true,
		Status:         "scanning",
		Type:           &typ,
		DryRun:         &dryRun,
		Progress:       apid.Ptr(0),
		CurrentChannel: apid.Ptr(""),
		ScanLog:        scanLog,
		NewCount:       apid.Ptr(0),
		TakeoverCount:  apid.Ptr(0),
		Result:         []apid.ConfigChannelsItem{},
		StartTime:      &now,
		UpdateTime:     &now,
	}
	s.scan.mu.Unlock()

	defer func() {
		s.scan.mu.Lock()
		s.scan.status.IsScanning = false
		s.scan.cancelRequested = false
		s.scan.mu.Unlock()
	}()

	if dryRun {
		appendLog("-- dry run --\n\n")
	}

	newCount := 0
	takeoverCount := 0
	total := len(cfg.Channels)

	appendLog(fmt.Sprintf("channel scanning... (type: \"%s\")\n\n", typ))

	for i, channel := range cfg.Channels {
		s.scan.mu.Lock()
		cancelled := s.scan.cancelRequested
		s.scan.mu.Unlock()
		if cancelled {
			appendLog("Channel scan was cancelled by user request.\n")
			break
		}
		progress := (i + 1) * 100 / total

		// skipCh によるスキップ (チャンネル名から数字のみ抽出して比較)
		channelNumber := -1
		if n, err := strconv.Atoi(digitsOnly(channel)); err == nil {
			channelNumber = n
		}
		if channelNumber >= 0 && containsInt(skipCh, channelNumber) {
			appendLog(fmt.Sprintf("channel: \"%s\" (%d/%d) [%d%%] - skipped by skipCh parameter\n\n", channel, i+1, total, progress))
			s.scan.mu.Lock()
			s.scan.status.Progress = apid.Ptr(progress)
			s.scan.status.CurrentChannel = apid.Ptr(channel)
			s.scan.mu.Unlock()
			continue
		}

		appendLog(fmt.Sprintf("channel: \"%s\" (%d/%d) [%d%%] ...\n", channel, i+1, total, progress))
		s.scan.mu.Lock()
		s.scan.status.Progress = apid.Ptr(progress)
		s.scan.status.CurrentChannel = apid.Ptr(channel)
		s.scan.status.UpdateTime = apid.Ptr(s.now().UnixMilli())
		s.scan.mu.Unlock()

		if !refresh {
			var existing []apid.ConfigChannelsItem
			for _, item := range oldItems {
				if item.Type == typ && item.Channel == channel && !item.IsDisabled {
					existing = append(existing, item)
				}
			}
			if len(existing) > 0 {
				appendLog(fmt.Sprintf("-> %d existing config found.\n", len(existing)))
				result = append(result, existing...)
				takeoverCount += len(existing)
				appendLog("# scan has skipped due to the \"refresh = false\" option because an existing config was found.\n\n")
				s.scan.mu.Lock()
				s.scan.status.TakeoverCount = apid.Ptr(takeoverCount)
				s.scan.mu.Unlock()
				continue
			}
		}

		services, err := s.app.Tuner.GetServices(ctx, &core.ChannelItem{Name: channel, Type: typ, Channel: channel})
		if err != nil {
			if regexpNoSignal.MatchString(err.Error()) {
				appendLog("-> no signal.\n\n")
			} else {
				appendLog(fmt.Sprintf("-> no signal. [%s]\n\n", err))
			}
			continue
		}
		services = filterServiceTypes(services)
		appendLog(fmt.Sprintf("-> %d services found.\n", len(services)))
		if len(services) == 0 {
			appendLog("\n")
			continue
		}

		items := generateChannelItems(cfg.ScanMode, typ, channel, services, cfg.SetDisabledOnAdd)
		for _, it := range items {
			result = append(result, it)
			newCount++
		}
		appendLog(fmt.Sprintf("Found %d channels for %s\n\n", len(items), channel))
		s.scan.mu.Lock()
		s.scan.status.NewCount = apid.Ptr(newCount)
		s.scan.mu.Unlock()
	}

	sortChannelItems(result)

	appendLog(fmt.Sprintf("-> total %d/%d (%s/Any) channels configured.\n\n", newCount+takeoverCount, len(result), typ))
	appendLog(fmt.Sprintf("-> new %d channels found.\n", newCount))
	appendLog(fmt.Sprintf("-> existing %d channels merged.\n", takeoverCount))

	s.scan.mu.Lock()
	s.scan.status.ScanLog = scanLog
	s.scan.status.NewCount = apid.Ptr(newCount)
	s.scan.status.TakeoverCount = apid.Ptr(takeoverCount)
	s.scan.status.Result = append([]apid.ConfigChannelsItem(nil), result...)
	cancelled := s.scan.cancelRequested
	s.scan.mu.Unlock()

	if !cancelled {
		if !dryRun {
			if err := s.loader.SaveChannels(result); err != nil {
				return nil, err
			}
			appendLog("channel scan has been completed and saved successfully.\n")
			appendLog("**RESTART REQUIRED** to apply changes.\n")
		} else {
			appendLog("channel scan has been completed.\n\n-- dry run --\n")
		}
	}

	appendLog(fmt.Sprintf("Final result: %d channels\n", len(result)))
	return result, nil
}

var regexpNoSignal = regexp.MustCompile(`stream has closed before get network`)

func filterServiceTypes(services []apid.Service) []apid.Service {
	out := []apid.Service{}
	for _, svc := range services {
		if svc.Type == nil {
			continue
		}
		if containsInt(serviceTypes, *svc.Type) {
			out = append(out, svc)
		}
	}
	return out
}

// generateScanConfig は Node 版 generateScanConfig を移植する。
func generateScanConfig(o channelScanOption) *scanConfig {
	switch o.Type {
	case apid.ChannelTypeGR:
		startCh := pickInt(o.StartCh, 13)
		endCh := pickInt(o.EndCh, 62)
		scanMode := pickStr(o.ScanModeName, "Channel")
		setDisabled := pickBool(o.SetDisabledOnAdd, false)
		format := pickStr(o.ChannelNameFormat, channelNameFormatGR)
		return &scanConfig{
			Channels:         mapRange(startCh, endCh, func(ch int) string { return formatChannelName(format, ch, nil) }),
			ScanMode:         scanMode,
			SetDisabledOnAdd: setDisabled,
		}
	case apid.ChannelTypeBS:
		scanMode := pickStr(o.ScanModeName, "Service")
		setDisabled := pickBool(o.SetDisabledOnAdd, true)
		if o.UseSubCh != nil && *o.UseSubCh {
			startCh := pickInt(o.StartCh, 1)
			endCh := pickInt(o.EndCh, 23)
			startSub := pickInt(o.StartSubCh, 0)
			endSub := pickInt(o.EndSubCh, 3)
			format := pickStr(o.ChannelNameFormat, channelNameFormatBSSub)
			channels := []string{}
			for ch := startCh; ch <= endCh; ch++ {
				for sub := startSub; sub <= endSub; sub++ {
					channels = append(channels, formatChannelName(format, ch, &sub))
				}
			}
			return &scanConfig{Channels: channels, ScanMode: scanMode, SetDisabledOnAdd: setDisabled}
		}
		startCh := pickInt(o.StartCh, 101)
		endCh := pickInt(o.EndCh, 256)
		format := pickStr(o.ChannelNameFormat, channelNameFormatBS)
		return &scanConfig{
			Channels:         mapRange(startCh, endCh, func(ch int) string { return formatChannelName(format, ch, nil) }),
			ScanMode:         scanMode,
			SetDisabledOnAdd: setDisabled,
		}
	case apid.ChannelTypeCS:
		startCh := pickInt(o.StartCh, 2)
		endCh := pickInt(o.EndCh, 24)
		scanMode := pickStr(o.ScanModeName, "Service")
		setDisabled := pickBool(o.SetDisabledOnAdd, true)
		format := pickStr(o.ChannelNameFormat, channelNameFormatCS)
		return &scanConfig{
			Channels:         mapRange(startCh, endCh, func(ch int) string { return formatChannelName(format, ch, nil) }),
			ScanMode:         scanMode,
			SetDisabledOnAdd: setDisabled,
		}
	}
	return nil
}

var placeholderRe = regexp.MustCompile(`({[\\_a-zA-Z0-9]+})`)
var chDigitsRe = regexp.MustCompile(`\A\{ch(0*)\}\z`)
var subchDigitsRe = regexp.MustCompile(`\A\{subch(0*)\}\z`)

// formatChannelName は Node 版 formatChannelName を移植する。
func formatChannelName(format string, ch int, subch *int) string {
	type kv struct {
		key, val string
	}
	values := []kv{}
	for _, ph := range placeholderRe.FindAllString(format, -1) {
		if m := chDigitsRe.FindStringSubmatch(ph); m != nil {
			digits := len(m[1])
			if digits < 1 {
				digits = 1
			}
			values = append(values, kv{ph, padStart(strconv.Itoa(ch), digits)})
			continue
		}
		if m := subchDigitsRe.FindStringSubmatch(ph); m != nil {
			digits := len(m[1])
			if digits < 1 {
				digits = 1
			}
			sv := 0
			if subch != nil {
				sv = *subch
			}
			values = append(values, kv{ph, padStart(strconv.Itoa(sv), digits)})
		}
	}
	out := format
	for _, v := range values {
		out = strings.ReplaceAll(out, v.key, v.val)
	}
	return out
}

// generateChannelItems は Node 版 generateChannelItems を移植する。
func generateChannelItems(scanMode string, typ apid.ChannelType, channel string, services []apid.Service, setDisabledOnAdd bool) []apid.ConfigChannelsItem {
	if scanMode == "Service" {
		out := make([]apid.ConfigChannelsItem, 0, len(services))
		for _, svc := range services {
			out = append(out, generateChannelItemForService(typ, channel, svc, setDisabledOnAdd))
		}
		return out
	}
	return []apid.ConfigChannelsItem{generateChannelItemForChannel(typ, channel, services, setDisabledOnAdd)}
}

// generateChannelItemForService は Node 版 generateChannelItemForService を移植する。
func generateChannelItemForService(typ apid.ChannelType, channel string, service apid.Service, setDisabledOnAdd bool) apid.ConfigChannelsItem {
	name := strings.TrimSpace(service.Name)
	if name == "" {
		name = fmt.Sprintf("%s%s:%d", typ, channel, service.ServiceID)
	}
	return apid.ConfigChannelsItem{
		Name:       name,
		Type:       typ,
		Channel:    channel,
		ServiceID:  apid.Ptr(service.ServiceID),
		IsDisabled: setDisabledOnAdd,
	}
}

// generateChannelItemForChannel は Node 版 generateChannelItemForChannel を移植する。
//
// Node の文字列インデックスは UTF-16 コード単位のため、Go でもルーン単位で比較する
// (バイト単位で比較すると全角文字の途中でスライスして不正な UTF-8 になる)。
func generateChannelItemForChannel(typ apid.ChannelType, channel string, services []apid.Service, setDisabledOnAdd bool) apid.ConfigChannelsItem {
	baseName := []rune(services[0].Name)
	matchIndex := len(baseName)
	for i := 1; i < len(services); i++ {
		name := []rune(services[i].Name)
		limit := len(baseName)
		if len(name) < limit {
			limit = len(name)
		}
		for j := 0; j < limit; j++ {
			if baseName[j] != name[j] {
				if j == 0 {
					break
				}
				if j < matchIndex {
					matchIndex = j
				}
				break
			}
			if j+1 >= len(name) && len(name) < matchIndex {
				matchIndex = len(name)
				break
			}
		}
	}
	name := strings.TrimSpace(string(baseName[:minInt(matchIndex, len(baseName))]))
	if name == "" {
		name = string(typ) + channel
	}
	return apid.ConfigChannelsItem{Name: name, Type: typ, Channel: channel, IsDisabled: setDisabledOnAdd}
}

// sortChannelItems は Node 版の result.sort を再現する。
func sortChannelItems(items []apid.ConfigChannelsItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Type == items[j].Type {
			return naturalLess(items[i].Channel, items[j].Channel)
		}
		return channelOrder[items[i].Type] < channelOrder[items[j].Type]
	})
}

// naturalLess は localeCompare({sensitivity:"base",numeric:true}) の近似。
func naturalLess(a, b string) bool {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		ca, cb := a[ia], b[ib]
		if isDigit(ca) && isDigit(cb) {
			na, nb := 0, 0
			for ia < len(a) && isDigit(a[ia]) {
				na = na*10 + int(a[ia]-'0')
				ia++
			}
			for ib < len(b) && isDigit(b[ib]) {
				nb = nb*10 + int(b[ib]-'0')
				ib++
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		la, lb := lowerByte(ca), lowerByte(cb)
		if la != lb {
			return la < lb
		}
		ia++
		ib++
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// ---- 小物ヘルパ ----

func mapRange(start, end int, f func(int) string) []string {
	if end < start {
		return []string{}
	}
	out := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, f(i))
	}
	return out
}

func padStart(s string, width int) string {
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func pickInt(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

func pickStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func pickBool(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func optIntPtr(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

func optBoolPtr(q map[string][]string, key string) *bool {
	vs, ok := q[key]
	if !ok {
		return nil
	}
	v := true
	if len(vs) > 0 && (vs[0] == "false" || vs[0] == "0") {
		v = false
	}
	return &v
}

func parseSkipCh(s string) []int {
	if s == "" {
		return nil
	}
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
